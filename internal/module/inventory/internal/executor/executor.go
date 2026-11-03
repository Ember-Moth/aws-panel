package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsnet "github.com/herta-network/aws-panel/internal/infra/aws"
	ac "github.com/herta-network/aws-panel/internal/module/accounts/contract"
	"github.com/herta-network/aws-panel/internal/module/inventory/contract"
	"github.com/herta-network/aws-panel/internal/module/inventory/entity"
	"github.com/herta-network/aws-panel/internal/module/inventory/internal/scope"
	"github.com/herta-network/aws-panel/internal/module/inventory/internal/source"
	oc "github.com/herta-network/aws-panel/internal/module/operations/contract"
	"github.com/herta-network/aws-panel/internal/repository"
)

type Clients interface {
	ConfigForBound(context.Context, int64, string, string, bool, string, func(context.Context) error) (aws.Config, error)
}
type Deps struct {
	Store   repository.InventoryStore
	Clients Clients
	Cloud   source.Cloud
}
type Executor struct{ deps Deps }

func New(deps Deps) *Executor { return &Executor{deps: deps} }
func (*Executor) Definition() oc.Definition {
	return oc.Definition{ID: scope.Operation, Version: 1, InputVersion: 1, Title: "同步 AWS 资源", MaxAttempts: 6, Deadline: 30 * time.Minute, CallTimeout: 2 * time.Minute}
}
func (*Executor) Validate(raw json.RawMessage) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var input contract.RefreshInput
	if decoder.Decode(&input) != nil {
		return nil, contract.ErrInvalid
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, contract.ErrInvalid
	}
	normalized, err := scope.Normalize(input.Targets)
	if err != nil {
		return nil, err
	}
	input.Targets = normalized
	return json.Marshal(input)
}
func (*Executor) Plan(ctx context.Context, raw json.RawMessage, targets []oc.Target) ([]oc.ItemPlan, error) {
	var input contract.RefreshInput
	if json.Unmarshal(raw, &input) != nil || len(input.Targets) != len(targets) {
		return nil, contract.ErrInvalid
	}
	byKey := map[string]oc.Target{}
	for _, target := range targets {
		byKey[target.Key] = target
	}
	seen := map[string]bool{}
	items := []oc.ItemPlan{}
	for _, request := range input.Targets {
		target, ok := byKey[scope.InputKey(request)]
		if !ok || target.Binding.ConnectionID != request.ConnectionID || target.Binding.Region != request.Region {
			return nil, contract.ErrInvalid
		}
		if target.Binding.Partition != "aws" {
			return nil, contract.ErrPartition
		}
		target.Key = scope.Key(target.Binding.AccountID, request.Kind, request.Region)
		if seen[target.Key] {
			return nil, &contract.Error{Code: "INVALID_INPUT", Message: "同一账号的一个同步范围只能选择一条连接"}
		}
		seen[target.Key] = true
		target.LockKey = "inventory/" + request.Kind + "/" + scope.Region(request.Kind, request.Region)
		target.Label = target.Binding.AWSAccountID + " · " + scope.Label(request.Kind)
		if scope.Regional(request.Kind) {
			target.Label += " · " + request.Region
		}
		body, _ := json.Marshal(request)
		items = append(items, oc.ItemPlan{Target: target, Steps: []oc.StepPlan{{Kind: "fetch", Label: "读取完整分页并更新快照", Input: body}}})
	}
	return items, nil
}
func (e *Executor) Execute(ctx context.Context, call oc.Call) oc.Result {
	if err := call.Check(ctx); err != nil {
		return failed(err)
	}
	var request contract.RefreshTarget
	if json.Unmarshal(call.Input, &request) != nil {
		return oc.Result{State: oc.Failed, Code: "INVALID_INPUT", ConfirmedAbsent: true}
	}
	if call.Target.Binding.Partition != "aws" {
		return failed(contract.ErrPartition)
	}
	cached, err := e.deps.Store.CompletedRun(ctx, call.StepID, call.Target.Binding.Fingerprint)
	if err != nil {
		return failed(err)
	}
	if cached != nil {
		return completed(*cached)
	}
	lease := entity.Lease{JobID: call.JobID, StepID: call.StepID, Epoch: call.LeaseEpoch, Owner: call.LeaseOwner}
	ticket, err := e.deps.Store.BeginRefresh(ctx, entity.Scope{AccountID: call.Target.Binding.AccountID, Kind: request.Kind, Region: scope.Region(request.Kind, request.Region)}, lease, call.Target.Binding.ConnectionID, call.Target.Binding.Fingerprint)
	if err != nil {
		return failed(err)
	}
	cfg, err := e.deps.Clients.ConfigForBound(ctx, call.ActorID, call.Target.Binding.ConnectionID, call.Target.Binding.Region, false, call.Target.Binding.Fingerprint, call.Check)
	var data source.Snapshot
	if err == nil {
		data, err = e.deps.Cloud.Fetch(ctx, cfg, source.Request{Kind: request.Kind, Region: request.Region, AWSAccountID: call.Target.Binding.AWSAccountID, Partition: call.Target.Binding.Partition})
	}
	var snapshots []entity.Snapshot
	if err == nil {
		snapshots, err = validate(data, request)
	}
	if err == nil {
		err = call.Check(ctx)
	}
	if err != nil {
		result := failed(err)
		persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = e.deps.Store.FailRefresh(persist, ticket, result.Code)
		return result
	}
	if err := e.deps.Store.CompleteRefresh(ctx, ticket, snapshots, data.Pages, data.RequestID); err != nil {
		return oc.Result{State: oc.Waiting, Code: "SNAPSHOT_COMMIT_UNCONFIRMED", After: 2 * time.Second}
	}
	return oc.Result{State: oc.Succeeded, Code: "SNAPSHOT_UPDATED", Output: oc.Output{ResourceID: "inventory/" + ticket.Scope.ID, RequestID: data.RequestID, Summary: fmt.Sprintf("已同步 %d 项，覆盖 %d 页", len(snapshots), data.Pages)}}
}
func (e *Executor) Reconcile(ctx context.Context, call oc.Call) oc.Result {
	if err := call.Check(ctx); err != nil {
		return failed(err)
	}
	row, err := e.deps.Store.CompletedRun(ctx, call.StepID, call.Target.Binding.Fingerprint)
	if err != nil {
		return oc.Result{State: oc.Waiting, Code: "SNAPSHOT_RECONCILE_UNAVAILABLE", After: 2 * time.Second}
	}
	if row != nil {
		return completed(*row)
	}
	// No cloud mutation is performed by this operation. A missing committed
	// snapshot can be read again; cancellation will stop before another read.
	return oc.Result{State: oc.Retry, Code: "SNAPSHOT_NOT_COMMITTED", After: time.Second, ConfirmedAbsent: true}
}
func completed(row entity.Run) oc.Result {
	return oc.Result{State: oc.Succeeded, Code: "SNAPSHOT_RECONCILED", Output: oc.Output{ResourceID: "inventory/" + row.ScopeID, RequestID: row.AWSRequestID, Summary: fmt.Sprintf("已核对同步结果：%d 项，%d 页", row.ResourceCount, row.PageCount)}}
}
func validText(value string, limit int) bool {
	return value != "" && len(value) <= limit && utf8.ValidString(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}
func validate(data source.Snapshot, request contract.RefreshTarget) ([]entity.Snapshot, error) {
	if data.Pages < 1 || data.Pages > 256 || len(data.Resources) > 20000 || len(data.RequestID) > 256 {
		return nil, contract.ErrResponse
	}
	rows := make([]entity.Snapshot, 0, len(data.Resources))
	seen := map[string]bool{}
	for _, resource := range data.Resources {
		if !validText(resource.ID, 2048) || !validText(resource.Name, 255) || !validText(resource.State, 80) || !scope.RegionOK(resource.Region) || resource.ObservedAt.IsZero() {
			return nil, contract.ErrResponse
		}
		if seen[resource.ID] {
			return nil, contract.ErrIncomplete
		}
		seen[resource.ID] = true
		if scope.Regional(request.Kind) && resource.Region != request.Region {
			return nil, contract.ErrResponse
		}
		valid := false
		switch request.Kind {
		case contract.LightsailInstance:
			valid = resource.Details.Instance != nil
		case contract.EC2Instance:
			valid = resource.Details.EC2 != nil
		case contract.LightsailStaticIP, contract.EC2Address:
			valid = resource.Details.Address != nil
		default:
			valid = resource.Details.Region != nil
		}
		if !valid {
			return nil, contract.ErrResponse
		}

		details, err := json.Marshal(resource.Details)
		if err != nil || len(details) > 64<<10 {
			return nil, contract.ErrResponse
		}
		rows = append(rows, entity.Snapshot{ResourceID: resource.ID, Name: resource.Name, Region: resource.Region, State: resource.State, Details: string(details), ObservedAt: resource.ObservedAt})
	}
	return rows, nil
}
func failed(err error) oc.Result {
	code, retry := failureCode(err)
	state := oc.Failed
	if retry {
		state = oc.Retry
	}
	result := oc.Result{State: state, Code: code, After: 3 * time.Second, ConfirmedAbsent: true}
	result.Output.RequestID = awsnet.ClassifyError(err).RequestID
	return result
}
func failureCode(err error) (string, bool) {
	var own *contract.Error
	if errors.As(err, &own) {
		return own.Code, own.Code == contract.ErrUnavailable.Code || own.Code == contract.ErrStale.Code
	}
	var operation *oc.Error
	if errors.As(err, &operation) {
		return operation.Code, operation.Code == oc.ErrUnavailable.Code || operation.Code == oc.ErrLease.Code
	}
	var connection *ac.Error
	if errors.As(err, &connection) {
		return connection.Code, connection.Code == ac.ErrUnavailable.Code
	}
	info := awsnet.ClassifyError(err)
	return info.Code, info.Transient
}
func Message(code string) string {
	switch code {
	case "":
		return ""
	case "AWS_ACCESS_DENIED", "FORBIDDEN":
		return "没有读取该范围的权限，请检查成员授权和 AWS IAM 权限"
	case "AWS_REGION_UNAVAILABLE":
		return "账号或区域尚不可用，已有快照保持不变"
	case "AWS_INVALID_REQUEST", "PARTITION_UNSUPPORTED":
		return "该服务或区域暂不可用，请检查同步范围"
	case "AWS_THROTTLED":
		return "AWS 请求受到限流，等待后重试"
	case "NETWORK_UNAVAILABLE":
		return "网络或代理链路不可用，已有快照保留，任务将按预算重试"
	case "PAGINATION_INCOMPLETE":
		return "分页不完整，已有快照保持不变"
	case "INVALID_AWS_RESPONSE":
		return "响应校验失败，已有快照保持不变"
	case "CONNECTION_CHANGED", "CONNECTION_UNVERIFIED", "CONNECTION_DISABLED", "CREDENTIALS_EXPIRED", "VAULT_UNAVAILABLE":
		return "访问连接已变化或不可用，请检查并重新验证"
	case "CONFLICT":
		return "已有任务正在处理相同范围，请查看任务中心"
	case "CANCEL_REQUESTED":
		return "同步已取消，已保存的快照保留"
	default:
		return "本次同步未完成，请查看对应任务；已有快照保留"
	}
}
