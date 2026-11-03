package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Ember-Moth/aws-panel/internal/auth"
	awsnet "github.com/Ember-Moth/aws-panel/internal/infra/aws"
	"github.com/Ember-Moth/aws-panel/internal/infra/clock"
	ac "github.com/Ember-Moth/aws-panel/internal/module/accounts/contract"
	oc "github.com/Ember-Moth/aws-panel/internal/module/operations/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/regions/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/regions/internal/source"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/google/uuid"
)

const Operation = "regions.enable"

var regionPattern = regexp.MustCompile(`^[a-z]{2}(?:-[a-z0-9]+)+-[0-9]+$`)

type Clients interface {
	ConfigFor(context.Context, int64, string, string, bool) (aws.Config, error)
	ConfigForBound(context.Context, int64, string, string, bool, string, func(context.Context) error) (aws.Config, error)
}
type ExecutorDeps struct {
	Clients Clients
	Cloud   source.Cloud
}
type Executor struct{ deps ExecutorDeps }

func NewExecutor(deps ExecutorDeps) *Executor { return &Executor{deps: deps} }
func (*Executor) Definition() oc.Definition {
	return oc.Definition{ID: Operation, Version: 1, InputVersion: 1, Title: "批量启用 AWS 区域", WritesCloud: true, MaxAttempts: 100, Deadline: 8 * time.Hour, CallTimeout: 30 * time.Second}
}
func targetKey(value contract.Target) string { return value.ConnectionID + "/" + value.Region }
func normalize(targets []contract.Target) ([]contract.Target, error) {
	if len(targets) == 0 || len(targets) > 50 {
		return nil, contract.ErrInvalid
	}
	values := append([]contract.Target{}, targets...)
	seen := map[string]bool{}
	for i := range values {
		value := &values[i]
		id, err := uuid.Parse(value.ConnectionID)
		if err != nil || len(value.Region) > 40 || !regionPattern.MatchString(value.Region) || strings.HasPrefix(value.Region, "cn-") || strings.HasPrefix(value.Region, "us-gov-") {
			return nil, contract.ErrInvalid
		}
		value.ConnectionID = id.String()
		key := targetKey(*value)
		if seen[key] {
			return nil, contract.ErrInvalid
		}
		seen[key] = true
	}
	sort.Slice(values, func(i, j int) bool { return targetKey(values[i]) < targetKey(values[j]) })
	return values, nil
}
func (*Executor) Validate(raw json.RawMessage) (json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	var input contract.EnableRequest
	if d.Decode(&input) != nil {
		return nil, contract.ErrInvalid
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return nil, contract.ErrInvalid
	}
	targets, err := normalize(input.Targets)
	if err != nil {
		return nil, err
	}
	return json.Marshal(contract.EnableRequest{Targets: targets})
}
func (e *Executor) Plan(ctx context.Context, raw json.RawMessage, targets []oc.Target) ([]oc.ItemPlan, error) {
	actor, ok := auth.Current(ctx)
	if !ok {
		return nil, contract.ErrForbidden
	}
	var input contract.EnableRequest
	if json.Unmarshal(raw, &input) != nil || len(input.Targets) != len(targets) {
		return nil, contract.ErrInvalid
	}
	byKey := map[string]oc.Target{}
	for _, target := range targets {
		byKey[target.Key] = target
	}
	items := []oc.ItemPlan{}
	seen := map[string]bool{}
	for _, value := range input.Targets {
		target, ok := byKey[targetKey(value)]
		if !ok || target.Binding.ConnectionID != value.ConnectionID || target.Binding.Partition != "aws" || target.Binding.Region != "us-east-1" {
			return nil, contract.ErrInvalid
		}
		key := target.Binding.AccountID + "/" + value.Region
		if seen[key] {
			return nil, contract.ErrInvalid
		}
		seen[key] = true
		cfg, err := e.deps.Clients.ConfigFor(ctx, actor.ID, value.ConnectionID, "us-east-1", false)
		if err != nil {
			return nil, err
		}
		state, err := e.deps.Cloud.Status(ctx, cfg, value.Region)
		if err != nil {
			return nil, publicError(err)
		}
		if state.Status == "DISABLING" {
			return nil, contract.ErrState
		}
		target.Key = key
		target.LockKey = "account/region/" + value.Region
		target.Label = target.Binding.AWSAccountID + " · " + value.Region
		body, _ := json.Marshal(contract.StepSpec{Region: value.Region, ObservedState: state.Status, CheckedAt: clock.Now()})
		items = append(items, oc.ItemPlan{Target: target, Steps: []oc.StepPlan{{Kind: "enable", Label: "启用区域并等待就绪", WritesCloud: true, Input: body}}})
	}
	return items, nil
}
func after(attempt int) time.Duration {
	return min(5*time.Minute, time.Duration(1<<min(attempt, 5))*15*time.Second)
}
func stateResult(value source.State, attempt int) oc.Result {
	output := oc.Output{ResourceID: "region/" + value.Region, RequestID: value.RequestID}
	switch value.Status {
	case "ENABLED", "ENABLED_BY_DEFAULT":
		output.Summary = "区域已启用"
		return oc.Result{State: oc.Succeeded, Code: "REGION_ENABLED", Output: output}
	case "ENABLING":
		output.Summary = "区域正在启用，等待 AWS 完成"
		return oc.Result{State: oc.Waiting, Code: "REGION_ENABLING", Output: output, After: after(attempt)}
	case "DISABLING":
		output.Summary = "区域正在停用，状态与本任务冲突"
		return oc.Result{State: oc.Failed, Code: "REGION_DISABLING", Output: output}
	case "DISABLED":
		return oc.Result{State: oc.Retry, Code: "REGION_DISABLED", Output: output, After: after(attempt)}
	default:
		return oc.Result{State: oc.Attention, Code: "INVALID_AWS_RESPONSE"}
	}
}
func (e *Executor) read(ctx context.Context, call oc.Call) (source.State, contract.StepSpec, error) {
	var spec contract.StepSpec
	if json.Unmarshal(call.Input, &spec) != nil {
		return source.State{}, spec, contract.ErrInvalid
	}
	if err := call.Check(ctx); err != nil {
		return source.State{}, spec, err
	}
	cfg, err := e.deps.Clients.ConfigForBound(ctx, call.ActorID, call.Target.Binding.ConnectionID, "us-east-1", false, call.Target.Binding.Fingerprint, call.Check)
	if err != nil {
		return source.State{}, spec, err
	}
	state, err := e.deps.Cloud.Status(ctx, cfg, spec.Region)
	return state, spec, err
}
func (e *Executor) Execute(ctx context.Context, call oc.Call) oc.Result {
	state, spec, err := e.read(ctx, call)
	if err != nil {
		return readFailure(err, call.Uncertain)
	}
	if state.Status != "DISABLED" {
		return stateResult(state, call.Attempt)
	}
	cfg, err := e.deps.Clients.ConfigForBound(ctx, call.ActorID, call.Target.Binding.ConnectionID, "us-east-1", true, call.Target.Binding.Fingerprint, call.Check)
	if err != nil {
		return readFailure(err, call.Uncertain)
	}
	id, err := e.deps.Cloud.Enable(ctx, cfg, spec.Region)
	if err == nil {
		return oc.Result{State: oc.Waiting, Code: "REGION_ENABLE_SUBMITTED", After: after(call.Attempt), Output: oc.Output{ResourceID: "region/" + spec.Region, RequestID: id, Summary: "区域启用请求已提交，正在核对状态"}}
	}
	info := awsnet.ClassifyError(err)
	if !call.Uncertain && (info.Code == "AWS_ACCESS_DENIED" || info.Code == "AWS_INVALID_REQUEST") {
		return oc.Result{State: oc.Failed, Code: info.Code, Output: oc.Output{RequestID: info.RequestID}, ConfirmedAbsent: true}
	}
	return oc.Result{State: oc.Waiting, Code: "REGION_ENABLE_UNCONFIRMED", After: after(call.Attempt), Output: oc.Output{ResourceID: "region/" + spec.Region, RequestID: info.RequestID, Summary: "启用请求结果待确认，将先查询区域状态"}}
}
func (e *Executor) Reconcile(ctx context.Context, call oc.Call) oc.Result {
	state, _, err := e.read(ctx, call)
	if err != nil {
		return readFailure(err, true)
	}
	return stateResult(state, call.Attempt)
}
func readFailure(err error, uncertain bool) oc.Result {
	info := awsnet.ClassifyError(err)
	code := info.Code
	var own *contract.Error
	if errors.As(err, &own) {
		code = own.Code
		info.Transient = false
	}
	var operation *oc.Error
	if errors.As(err, &operation) {
		code = operation.Code
		info.Transient = false
	}
	var account *ac.Error
	if errors.As(err, &account) {
		code = account.Code
		info.Transient = false
	}
	state := oc.Failed
	if uncertain {
		state = oc.Waiting
		if !info.Transient {
			state = oc.Attention
		}
	} else if info.Transient {
		state = oc.Retry
	}
	return oc.Result{State: state, Code: code, After: 30 * time.Second, ConfirmedAbsent: !uncertain, Output: oc.Output{RequestID: info.RequestID}}
}
func publicError(err error) error {
	var own *contract.Error
	if errors.As(err, &own) {
		return own
	}
	info := awsnet.ClassifyError(err)
	message := "无法读取区域状态，请检查连接、区域和 AWS 权限"
	if info.Code == "AWS_ACCESS_DENIED" {
		message = "AWS 拒绝读取区域状态，请检查 account:GetRegionOptStatus 权限"
	}
	return &contract.Error{Code: info.Code, Message: message}
}
