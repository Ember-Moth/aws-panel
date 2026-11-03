package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/google/uuid"
	"github.com/herta-network/aws-panel/internal/auth"
	awsnet "github.com/herta-network/aws-panel/internal/infra/aws"
	ac "github.com/herta-network/aws-panel/internal/module/accounts/contract"
	"github.com/herta-network/aws-panel/internal/module/addresses"
	ad "github.com/herta-network/aws-panel/internal/module/addresses/contract"
	"github.com/herta-network/aws-panel/internal/module/lightsail/contract"
	"github.com/herta-network/aws-panel/internal/module/lightsail/entity"
	"github.com/herta-network/aws-panel/internal/module/lightsail/internal/bootstrap"
	"github.com/herta-network/aws-panel/internal/module/lightsail/internal/source"
	oc "github.com/herta-network/aws-panel/internal/module/operations/contract"
	"github.com/herta-network/aws-panel/internal/repository"
)

const Operation = "lightsail.create"

var regionPattern = regexp.MustCompile(`^[a-z]{2}(?:-[a-z0-9]+)+-[0-9]+$`)
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)
var catalogueID = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

type Clients interface {
	ConfigFor(context.Context, int64, string, string, bool) (aws.Config, error)
	ConfigForBound(context.Context, int64, string, string, bool, string, func(context.Context) error) (aws.Config, error)
}
type ExecutorDeps struct {
	AddressCloud  addresses.Cloud
	Effects       repository.EffectStore
	Store         repository.LightsailStore
	Clients       Clients
	Cloud         source.Cloud
	LaunchSecrets LaunchSecrets
}
type Executor struct{ deps ExecutorDeps }

func NewExecutor(deps ExecutorDeps) *Executor { return &Executor{deps: deps} }
func (*Executor) Definition() oc.Definition {
	return oc.Definition{ID: Operation, Version: 1, InputVersion: 1, Title: "批量创建 Lightsail 实例", WritesCloud: true, MaxAttempts: 60, Deadline: 2 * time.Hour, CallTimeout: 2 * time.Minute}
}
func key(value contract.Target) string {
	return value.ConnectionID + "/" + value.Region + "/" + value.Name
}
func normalize(values []contract.Target) ([]contract.Target, error) {
	if len(values) == 0 || len(values) > 50 {
		return nil, contract.ErrInvalid
	}
	result := append([]contract.Target{}, values...)
	seen := map[string]bool{}
	for i := range result {
		value := &result[i]
		id, err := uuid.Parse(value.ConnectionID)
		if err != nil {
			return nil, contract.ErrInvalid
		}
		value.ConnectionID = id.String()
		if value.LoginMethod == "" {
			value.LoginMethod = "ssh_key"
		}
		if value.LoginMethod == "root_password" {
			if _, err := uuid.Parse(value.LaunchSecretID); err != nil {
				return nil, contract.ErrLogin
			}
			if value.KeyPairName != "" {
				return nil, contract.ErrLogin
			}
		} else if value.LoginMethod != "ssh_key" || value.LaunchSecretID != "" {
			return nil, contract.ErrLogin
		}
		if !namePattern.MatchString(value.Name) || len(value.Region) > 30 || !regionPattern.MatchString(value.Region) || strings.HasPrefix(value.Region, "cn-") || strings.HasPrefix(value.Region, "us-gov-") || len(value.Zone) > 40 || !strings.HasPrefix(value.Zone, value.Region) || !catalogueID.MatchString(value.BlueprintID) || !catalogueID.MatchString(value.BundleID) || value.KeyPairName != "" && !catalogueID.MatchString(value.KeyPairName) {
			return nil, contract.ErrInvalid
		}
		if value.IPType != "ipv4" && value.IPType != "ipv6" && value.IPType != "dualstack" {
			return nil, contract.ErrInvalid
		}
		k := key(*value)
		if seen[k] {
			return nil, contract.ErrInvalid
		}
		seen[k] = true
	}
	sort.Slice(result, func(i, j int) bool { return key(result[i]) < key(result[j]) })
	return result, nil
}
func (*Executor) Validate(raw json.RawMessage) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var input contract.PlanInput
	if decoder.Decode(&input) != nil {
		return nil, contract.ErrInvalid
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, contract.ErrInvalid
	}
	values, err := normalize(input.Targets)
	if err != nil {
		return nil, err
	}
	return json.Marshal(contract.PlanInput{Targets: values})
}
func selectSpec(value contract.Target, catalogue contract.Catalogue) (contract.StepSpec, error) {
	result := contract.StepSpec{Target: value, Quota: catalogue.Quota, CheckedAt: catalogue.CheckedAt}
	zone := false
	for _, candidate := range catalogue.Zones {
		zone = zone || candidate == value.Zone
	}
	if !zone {
		return result, contract.ErrCatalogue
	}
	for _, candidate := range catalogue.Blueprints {
		if candidate.ID == value.BlueprintID {
			result.Blueprint = candidate
		}
	}
	for _, candidate := range catalogue.Bundles {
		if candidate.ID == value.BundleID {
			result.Bundle = candidate
		}
	}
	if value.LoginMethod == "root_password" && !result.Blueprint.RootPasswordSupported {
		return result, contract.ErrPasswordImage
	}
	if result.Blueprint.ID == "" || result.Bundle.ID == "" || result.Bundle.CPU < 1 || result.Bundle.Power < result.Blueprint.MinPower || result.Bundle.PublicIPv4 == (value.IPType == "ipv6") {
		return result, contract.ErrCatalogue
	}
	if value.KeyPairName != "" {
		found := false
		for _, name := range catalogue.KeyPairs {
			found = found || name == value.KeyPairName
		}
		if !found {
			return result, contract.ErrCatalogue
		}
	}
	return result, nil
}
func (e *Executor) Plan(ctx context.Context, raw json.RawMessage, targets []oc.Target) ([]oc.ItemPlan, error) {
	actor, ok := auth.Current(ctx)
	if !ok {
		return nil, contract.ErrForbidden
	}
	var input contract.PlanInput
	if json.Unmarshal(raw, &input) != nil || len(input.Targets) != len(targets) {
		return nil, contract.ErrInvalid
	}
	if _, ok := e.deps.Cloud.(source.FirewallCloud); !ok || e.deps.Effects == nil {
		return nil, contract.ErrUnavailable
	}
	byKey := map[string]oc.Target{}
	for _, target := range targets {
		byKey[target.Key] = target
	}
	catalogues := map[string]contract.Catalogue{}
	configs := map[string]aws.Config{}
	cpu := map[string]int64{}
	ips := map[string]float64{}
	seen := map[string]bool{}
	result := []oc.ItemPlan{}
	for _, value := range input.Targets {
		target, ok := byKey[key(value)]
		if !ok || target.Binding.Partition != "aws" || target.Binding.ConnectionID != value.ConnectionID || target.Binding.Region != value.Region {
			return nil, contract.ErrInvalid
		}
		identity := target.Binding.AccountID + "/" + value.Region + "/" + value.Name
		if seen[identity] {
			return nil, contract.ErrConflict
		}
		seen[identity] = true
		group := value.ConnectionID + "/" + value.Region
		cfg, ok := configs[group]
		if !ok {
			var err error
			cfg, err = e.deps.Clients.ConfigFor(ctx, actor.ID, value.ConnectionID, value.Region, true)
			if err != nil {
				return nil, publicError(err)
			}
			configs[group] = cfg
			catalogue, err := e.deps.Cloud.Catalogue(ctx, cfg)
			if err != nil {
				return nil, publicError(err)
			}
			catalogues[group] = catalogue
		}
		spec, err := selectSpec(value, catalogues[group])
		if err != nil {
			return nil, err
		}
		existing, err := e.deps.Cloud.Instance(ctx, cfg, value.Name)
		if err != nil {
			return nil, publicError(err)
		}
		if existing != nil {
			return nil, &contract.Error{Code: contract.ErrConflict.Code, Message: fmt.Sprintf("账号 %s 的 %s / %s 已存在，请更换名称", target.Binding.AWSAccountID, value.Region, value.Name)}
		}
		if value.LoginMethod == "root_password" {
			if e.deps.LaunchSecrets == nil {
				return nil, contract.ErrUnavailable
			}
			if _, err := e.deps.LaunchSecrets.LaunchSecret(ctx, actor.ID, value.LaunchSecretID, launchBinding(value, target.Binding.AccountID)); err != nil {
				return nil, publicError(err)
			}
		}
		quotaGroup := target.Binding.AccountID + "/" + value.Region
		cpu[quotaGroup] += int64(spec.Bundle.CPU)
		if spec.Quota.Known && spec.Quota.UsedVCPU+cpu[quotaGroup] > spec.Quota.LimitVCPU {
			return nil, contract.ErrQuota
		}
		if value.IPType != "ipv6" {
			policy, err := addresses.PlanAutomatic(ctx, e.deps.AddressCloud, cfg, "lightsail", target.Binding.AWSAccountID, "", "")
			if err != nil {
				return nil, err
			}
			ips[quotaGroup]++
			if policy.Quota.LimitKnown && policy.Quota.UsageKnown && policy.Quota.Used+ips[quotaGroup] > policy.Quota.Limit {
				return nil, contract.ErrQuota
			}
			spec.AutomaticIP = &policy
		}
		spec.AllowAllFirewall = true
		body, _ := json.Marshal(spec)
		target.Key = identity
		target.Label = target.Binding.AWSAccountID + " · " + value.Region + " · " + value.Name
		target.LockKey = "lightsail/instance/" + value.Name
		steps := []oc.StepPlan{{Kind: "create", Label: "创建并确认实例归属", Input: body, WritesCloud: true}, {Kind: "ready", Label: "等待实例运行", Input: body}, {Kind: "firewall", Label: "放行全部公网入站", Input: body, WritesCloud: true}}
		if spec.AutomaticIP != nil {
			steps = append(steps, oc.StepPlan{Kind: "ip_allocate", Label: "自动分配静态 IP", Input: body, WritesCloud: true}, oc.StepPlan{Kind: "ip_attach", Label: "自动绑定静态 IP", Input: body, WritesCloud: true})
		}
		result = append(result, oc.ItemPlan{Target: target, Steps: steps})
	}
	return result, nil
}
func specHash(spec contract.StepSpec, account string) string {
	raw, _ := json.Marshal(struct{ Account, Region, Name, Zone, Blueprint, Bundle, IP, Key string }{account, spec.Target.Region, spec.Target.Name, spec.Target.Zone, spec.Target.BlueprintID, spec.Target.BundleID, spec.Target.IPType, spec.Target.KeyPairName})
	if spec.Target.LoginMethod == "root_password" {
		raw = append(raw, []byte("/root-password-v1/"+spec.Target.LaunchSecretID)...)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func lease(call oc.Call) entity.Lease {
	return entity.Lease{StepID: call.StepID, Epoch: call.LeaseEpoch, Owner: call.LeaseOwner}
}
func decodeSpec(call oc.Call) (contract.StepSpec, error) {
	var spec contract.StepSpec
	if json.Unmarshal(call.Input, &spec) != nil {
		return spec, contract.ErrInvalid
	}
	if _, err := normalize([]contract.Target{spec.Target}); err != nil {
		return spec, err
	}
	if spec.Target.ConnectionID != call.Target.Binding.ConnectionID || spec.Target.Region != call.Target.Binding.Region || call.Target.Binding.Partition != "aws" {
		return spec, contract.ErrInvalid
	}
	return spec, nil
}
func (e *Executor) config(ctx context.Context, call oc.Call, write bool) (aws.Config, error) {
	if err := call.Check(ctx); err != nil {
		return aws.Config{}, err
	}
	return e.deps.Clients.ConfigForBound(ctx, call.ActorID, call.Target.Binding.ConnectionID, call.Target.Binding.Region, write, call.Target.Binding.Fingerprint, call.Check)
}
func (e *Executor) Execute(ctx context.Context, call oc.Call) oc.Result {
	if call.Kind == "firewall" {
		return e.firewall(ctx, call, false)
	}
	if isIPStep(call.Kind) {
		return e.automaticIP(ctx, call, false)
	}
	if call.Kind == "ready" {
		return e.ready(ctx, call)
	}
	spec, err := decodeSpec(call)
	if err != nil {
		return noSend(err)
	}
	cfg, err := e.config(ctx, call, false)
	if err != nil {
		return noSend(err)
	}
	row, err := e.deps.Store.Creation(ctx, call.StepID)
	if err != nil {
		return noSend(err)
	}
	if row != nil && (row.State == "dispatched" || row.State == "identified") {
		return e.reconcile(ctx, call, spec, cfg, *row)
	}
	existing, err := e.deps.Cloud.Instance(ctx, cfg, spec.Target.Name)
	if err != nil {
		return noSend(err)
	}
	if existing != nil {
		return oc.Result{State: oc.Failed, Code: "NAME_CONFLICT", ConfirmedAbsent: row == nil || row.State != "dispatched", Output: oc.Output{Summary: "同名实例已存在，本任务未发送新的创建请求"}}
	}
	catalogue, err := e.deps.Cloud.Catalogue(ctx, cfg)
	if err != nil {
		return noSend(err)
	}
	current, err := selectSpec(spec.Target, catalogue)
	if err != nil {
		return noSend(err)
	}
	if current.Bundle.MonthlyCents > spec.Bundle.MonthlyCents || current.Blueprint.Version != spec.Blueprint.Version || current.Bundle.CPU != spec.Bundle.CPU || current.Bundle.RAMGB != spec.Bundle.RAMGB || current.Bundle.DiskGB != spec.Bundle.DiskGB {
		return noSend(contract.ErrCatalogue)
	}
	if current.Quota.Known && current.Quota.UsedVCPU+int64(current.Bundle.CPU) > current.Quota.LimitVCPU {
		return noSend(contract.ErrQuota)
	}
	body, _ := json.Marshal(spec)
	value, err := e.deps.Store.PrepareCreation(ctx, entity.Creation{StepID: call.StepID, JobID: call.JobID, AccountID: call.Target.Binding.AccountID, Region: spec.Target.Region, Name: spec.Target.Name, Token: call.Token, SpecHash: specHash(spec, call.Target.Binding.AWSAccountID), Spec: string(body)}, lease(call))
	if err != nil {
		return noSend(err)
	}
	if value.State != "prepared" && value.State != "rejected" {
		return e.reconcile(ctx, call, spec, cfg, value)
	}
	writeCfg, err := e.config(ctx, call, true)
	if err != nil {
		return noSend(err)
	}
	userData := ""
	if spec.Target.LoginMethod == "root_password" {
		if e.deps.LaunchSecrets == nil {
			return noSend(contract.ErrUnavailable)
		}
		hash, err := e.deps.LaunchSecrets.LaunchSecret(ctx, call.ActorID, spec.Target.LaunchSecretID, launchBinding(spec.Target, call.Target.Binding.AccountID))
		if err != nil {
			return noSend(err)
		}
		userData, err = bootstrap.Script(spec.Target.BlueprintID, hash)
		if err != nil {
			return noSend(err)
		}
	}
	value, err = e.deps.Store.DispatchCreation(ctx, call.StepID, lease(call))
	if err != nil {
		return oc.Result{State: oc.Waiting, Code: "DISPATCH_RECORD_UNCONFIRMED", After: 5 * time.Second}
	}
	created, callErr := e.deps.Cloud.Create(ctx, writeCfg, spec.Target, map[string]string{"aws-panel:step": value.StepID, "aws-panel:token": value.Token, "aws-panel:spec": value.SpecHash}, userData)
	if callErr != nil {
		info := awsnet.ClassifyError(callErr)
		value.AWSRequestID = info.RequestID
		value.Code = info.Code
		if info.Code == "AWS_ACCESS_DENIED" || info.Code == "AWS_INVALID_REQUEST" {
			value.State = "rejected"
			if e.deps.Store.RecordCreation(ctx, value, lease(call)) == nil {
				return oc.Result{State: oc.Failed, Code: info.Code, ConfirmedAbsent: true, Output: oc.Output{RequestID: info.RequestID, Summary: "AWS 拒绝创建请求；保留记录以便核对或重试"}}
			}
		}
		return oc.Result{State: oc.Waiting, Code: "CREATE_RESULT_UNCONFIRMED", After: 10 * time.Second, Output: oc.Output{RequestID: info.RequestID, Summary: "创建请求结果待确认，将先检查同名实例和操作记录"}}
	}
	ids := []string{}
	for _, operation := range created.Operations {
		if operation.ID == "" || operation.Name != spec.Target.Name || operation.Region != spec.Target.Region || operation.Type != "CreateInstance" {
			continue
		}
		ids = append(ids, operation.ID)
	}
	value.OperationIDs = encode(ids)
	value.AWSRequestID = created.RequestID
	value.Code = "CREATE_ACCEPTED"
	if e.deps.Store.RecordCreation(ctx, value, lease(call)) != nil {
		return oc.Result{State: oc.Waiting, Code: "CREATE_RECORD_UNCONFIRMED", After: 10 * time.Second}
	}
	return oc.Result{State: oc.Waiting, Code: "CREATE_ACCEPTED", After: 10 * time.Second, Output: oc.Output{RequestID: created.RequestID, Summary: "AWS 已接受创建请求，等待实例及操作记录可查询"}}
}
func encode(value any) string { raw, _ := json.Marshal(value); return string(raw) }
func (e *Executor) Reconcile(ctx context.Context, call oc.Call) oc.Result {
	if call.Kind == "firewall" {
		return e.firewall(ctx, call, true)
	}
	if isIPStep(call.Kind) {
		return e.automaticIP(ctx, call, true)
	}
	if call.Kind == "ready" {
		return e.ready(ctx, call)
	}
	spec, err := decodeSpec(call)
	if err != nil {
		return noSend(err)
	}
	cfg, err := e.config(ctx, call, false)
	if err != nil {
		return observeFailure(err)
	}
	row, err := e.deps.Store.Creation(ctx, call.StepID)
	if err != nil {
		return observeFailure(err)
	}
	if row == nil {
		return oc.Result{State: oc.Retry, Code: "NO_CREATE_DISPATCH", ConfirmedAbsent: true, After: time.Second}
	}
	return e.reconcile(ctx, call, spec, cfg, *row)
}
func owned(instance source.Instance, spec contract.StepSpec, row entity.Creation, account string) bool {
	parsed, err := arn.Parse(instance.ARN)
	if err != nil || parsed.Partition != "aws" || parsed.AccountID != account || parsed.Region != spec.Target.Region || instance.Name != spec.Target.Name || instance.Region != spec.Target.Region {
		return false
	}
	if instance.Tags["aws-panel:step"] != row.StepID || instance.Tags["aws-panel:token"] != row.Token || instance.Tags["aws-panel:spec"] != row.SpecHash || instance.BlueprintID != spec.Target.BlueprintID || instance.BundleID != spec.Target.BundleID || instance.Zone != spec.Target.Zone || instance.IPType != spec.Target.IPType || spec.Target.KeyPairName != "" && instance.KeyPairName != spec.Target.KeyPairName {
		return false
	}
	if row.FirstSentAt == nil || instance.CreatedAt.Before(row.FirstSentAt.Add(-30*time.Second)) || instance.CreatedAt.After(time.Now().Add(30*time.Second)) {
		return false
	}
	return row.ResourceARN == "" || row.ResourceARN == instance.ARN
}
func relevant(values []source.Operation, row entity.Creation) []source.Operation {
	var known []string
	_ = json.Unmarshal([]byte(row.OperationIDs), &known)
	ids := map[string]bool{}
	for _, id := range known {
		ids[id] = true
	}
	result := []source.Operation{}
	for _, value := range values {
		if value.Name != row.Name || value.Region != row.Region || value.Type != "CreateInstance" || value.ID == "" || value.CreatedAt.IsZero() || row.FirstSentAt == nil || value.CreatedAt.Before(row.FirstSentAt.Add(-30*time.Second)) {
			continue
		}
		if len(ids) > 0 && !ids[value.ID] {
			continue
		}
		result = append(result, value)
	}
	return result
}
func (e *Executor) reconcile(ctx context.Context, call oc.Call, spec contract.StepSpec, cfg aws.Config, row entity.Creation) oc.Result {
	if row.JobID != call.JobID || row.AccountID != call.Target.Binding.AccountID || row.SpecHash != specHash(spec, call.Target.Binding.AWSAccountID) {
		return oc.Result{State: oc.Attention, Code: "OWNERSHIP_UNCONFIRMED"}
	}
	instance, err := e.deps.Cloud.Instance(ctx, cfg, spec.Target.Name)
	if err != nil {
		return observeFailure(err)
	}
	if row.State == "prepared" || row.State == "rejected" {
		if instance == nil {
			return oc.Result{State: oc.Retry, Code: "CREATE_NOT_ACCEPTED", ConfirmedAbsent: true, After: time.Second}
		}
		if row.State == "rejected" && owned(*instance, spec, row, call.Target.Binding.AWSAccountID) {
			row.State = "dispatched"
		} else {
			return oc.Result{State: oc.Failed, Code: "NAME_CONFLICT", ConfirmedAbsent: true}
		}
	}
	if instance == nil && row.State == "identified" {
		return oc.Result{State: oc.Attention, Code: "CREATED_RESOURCE_MISSING", Output: oc.Output{ResourceID: row.ResourceARN, Summary: "已确认的实例目前无法查询；不会自动重新创建"}}
	}
	if instance != nil && !owned(*instance, spec, row, call.Target.Binding.AWSAccountID) {
		return oc.Result{State: oc.Attention, Code: "OWNERSHIP_UNCONFIRMED", Output: oc.Output{Summary: "同名实例的标签、参数或身份不匹配，未认领该资源"}}
	}
	operations, err := e.deps.Cloud.Operations(ctx, cfg, spec.Target.Name)
	if err != nil {
		return observeFailure(err)
	}
	matches := relevant(operations, row)
	if len(matches) > 1 {
		return oc.Result{State: oc.Attention, Code: "CREATE_HISTORY_AMBIGUOUS"}
	}
	if len(matches) == 0 {
		return oc.Result{State: oc.Waiting, Code: "CREATE_HISTORY_PENDING", After: 20 * time.Second}
	}
	operation := matches[0]
	if instance == nil {
		if operation.Terminal && operation.State == "Failed" {
			row.State = "rejected"
			row.Code = "AWS_CREATE_FAILED"
			if err := e.deps.Store.RecordCreation(ctx, row, lease(call)); err != nil {
				return observeFailure(err)
			}
			return oc.Result{State: oc.Failed, Code: "AWS_CREATE_FAILED", ConfirmedAbsent: true}
		}
		return oc.Result{State: oc.Waiting, Code: "INSTANCE_NOT_VISIBLE", After: 20 * time.Second, Output: oc.Output{Summary: "请求已记录，实例尚不可查询；继续核对，不重新创建"}}
	}
	row.State = "identified"
	row.ResourceARN = instance.ARN
	row.OperationIDs = encode([]string{operation.ID})
	row.Code = "INSTANCE_IDENTIFIED"
	if err := e.deps.Store.RecordCreation(ctx, row, lease(call)); err != nil {
		return observeFailure(err)
	}
	if spec.Target.LoginMethod == "root_password" && e.deps.LaunchSecrets != nil {
		_ = e.deps.LaunchSecrets.DeleteLaunchSecret(ctx, call.ActorID, spec.Target.LaunchSecretID, launchBinding(spec.Target, call.Target.Binding.AccountID))
	}
	if operation.Terminal && operation.State == "Failed" {
		return oc.Result{State: oc.Failed, Code: "CREATE_FAILED_RESOURCE_PRESENT", Output: oc.Output{ResourceID: instance.ARN, RequestID: instance.RequestID, Summary: "AWS 创建记录失败，但实例已出现；请检查该资源，避免重复创建"}}
	}
	if !operation.Terminal || (operation.State != "Succeeded" && operation.State != "Completed") {
		return oc.Result{State: oc.Waiting, Code: "CREATE_OPERATION_PENDING", After: 20 * time.Second, Output: oc.Output{ResourceID: instance.ARN, Summary: "实例已确认归属，等待 AWS 创建操作完成"}}
	}
	return oc.Result{State: oc.Succeeded, Code: "INSTANCE_IDENTIFIED", Output: oc.Output{ResourceID: instance.ARN, RequestID: instance.RequestID, Summary: "实例已出现并确认归属，继续检查运行状态"}}
}
func (e *Executor) ready(ctx context.Context, call oc.Call) oc.Result {
	spec, err := decodeSpec(call)
	if err != nil {
		return noSend(err)
	}
	createID := ""
	for _, step := range call.Completed {
		if step.Kind == "create" {
			createID = step.Output.ResourceID
			break
		}
	}
	// The first successful step exposes its ARN; the receipt is found using
	// that step's id, carried separately in its completed-step descriptor.
	stepID := ""
	for _, step := range call.Completed {
		if step.Kind == "create" {
			stepID = step.ID
		}
	}
	if createID == "" || stepID == "" {
		return oc.Result{State: oc.Attention, Code: "CREATE_REFERENCE_MISSING"}
	}
	row, err := e.deps.Store.Creation(ctx, stepID)
	if err != nil {
		return observeFailure(err)
	}
	if row == nil || row.JobID != call.JobID || row.ResourceARN != createID {
		return oc.Result{State: oc.Attention, Code: "CREATE_REFERENCE_MISSING"}
	}
	cfg, err := e.config(ctx, call, false)
	if err != nil {
		return observeFailure(err)
	}
	instance, err := e.deps.Cloud.Instance(ctx, cfg, spec.Target.Name)
	if err != nil {
		return observeFailure(err)
	}
	if instance == nil || !owned(*instance, spec, *row, call.Target.Binding.AWSAccountID) {
		return oc.Result{State: oc.Attention, Code: "CREATED_RESOURCE_CHANGED", Output: oc.Output{ResourceID: createID}}
	}
	output := oc.Output{ResourceID: instance.ARN, RequestID: instance.RequestID}
	if instance.State == "running" {
		output.Summary = "实例已进入 running 状态；应用与 SSH 可用性需另行验证"
		if spec.Target.LoginMethod == "root_password" {
			output.Summary = "实例已运行；root 密码初始化已安排，实际密码登录仍需验证"
		}
		return oc.Result{State: oc.Succeeded, Code: "INSTANCE_RUNNING", Output: output}
	}
	if instance.State == "stopped" || instance.State == "terminated" || instance.State == "shutting-down" {
		output.Summary = "实例已创建，但未达到运行状态；资源引用已保留"
		return oc.Result{State: oc.Failed, Code: "INSTANCE_NOT_RUNNING", Output: output}
	}
	output.Summary = "实例已创建，等待运行状态"
	return oc.Result{State: oc.Waiting, Code: "INSTANCE_STARTING", After: 20 * time.Second, Output: output}
}
func noSend(err error) oc.Result {
	code, transient := classify(err)
	state := oc.Failed
	if transient {
		state = oc.Retry
	}
	return oc.Result{State: state, Code: code, After: 5 * time.Second, ConfirmedAbsent: true}
}
func observeFailure(err error) oc.Result {
	code, transient := classify(err)
	state := oc.Attention
	if transient {
		state = oc.Waiting
	}
	return oc.Result{State: state, Code: code, After: 20 * time.Second}
}
func classify(err error) (string, bool) {
	var own *contract.Error
	if errors.As(err, &own) {
		return own.Code, own.Code == contract.ErrUnavailable.Code || own.Code == contract.ErrLease.Code
	}
	var op *oc.Error
	if errors.As(err, &op) {
		return op.Code, op.Code == oc.ErrUnavailable.Code
	}
	var account *ac.Error
	if errors.As(err, &account) {
		return account.Code, account.Code == ac.ErrUnavailable.Code
	}
	info := awsnet.ClassifyError(err)
	return info.Code, info.Transient
}
func publicError(err error) error {
	if err == nil {
		return nil
	}
	var own *contract.Error
	if errors.As(err, &own) {
		return own
	}
	var op *oc.Error
	if errors.As(err, &op) {
		if op.Code == "CONNECTION_CHANGED" {
			return &contract.Error{Code: op.Code, Message: "连接不可用或配置已变化，请检查连接并重新预览"}
		}
		return &contract.Error{Code: op.Code, Message: op.Message}
	}
	var address *ad.Error
	if errors.As(err, &address) {
		return &contract.Error{Code: address.Code, Message: address.Message}
	}
	var account *ac.Error
	if errors.As(err, &account) {
		return &contract.Error{Code: account.Code, Message: account.Message}
	}
	info := awsnet.ClassifyError(err)
	return &contract.Error{Code: info.Code, Message: "无法完成 Lightsail 读取检查，请检查连接、区域与 IAM 权限"}
}
