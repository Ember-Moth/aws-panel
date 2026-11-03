package service

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsnet "github.com/herta-network/aws-panel/internal/infra/aws"
	"github.com/herta-network/aws-panel/internal/instanceinit"
	ac "github.com/herta-network/aws-panel/internal/module/accounts/contract"
	"github.com/herta-network/aws-panel/internal/module/addresses"
	"github.com/herta-network/aws-panel/internal/module/compute/contract"
	"github.com/herta-network/aws-panel/internal/module/compute/internal/source"
	oc "github.com/herta-network/aws-panel/internal/module/operations/contract"
	"slices"
	"strings"
	"time"
)

func lease(c oc.Call) oc.EffectLease {
	return oc.EffectLease{StepID: c.StepID, Owner: c.LeaseOwner, Epoch: c.LeaseEpoch}
}
func expected(c oc.Call) oc.Effect {
	return oc.Effect{StepID: c.StepID, JobID: c.JobID, AccountID: c.Target.Binding.AccountID, Region: c.Target.Binding.Region, Purpose: OperationID + "." + c.Kind, SpecHash: hash(string(c.Input)), Token: c.Token}
}
func sameEffect(v *oc.Effect, c oc.Call) bool {
	e := expected(c)
	return v.StepID == e.StepID && v.JobID == e.JobID && v.AccountID == e.AccountID && v.Region == e.Region && v.Purpose == e.Purpose && v.SpecHash == e.SpecHash && v.Token == e.Token
}
func labels(c oc.Call) map[string]string {
	return map[string]string{"aws-panel:step": c.StepID, "aws-panel:token": c.Token, "aws-panel:spec": hash(string(c.Input))}
}
func parse(c oc.Call) (contract.Spec, error) {
	var s contract.Spec
	if json.Unmarshal(c.Input, &s) != nil {
		return s, contract.ErrInvalid
	}
	if _, err := normalize([]contract.Target{s.Target}); err != nil {
		return s, err
	}
	if s.Target.Action == "network" {
		if s.Network == nil || !slices.Contains(networkSteps, c.Kind) || s.Target.ConnectionID != c.Target.Binding.ConnectionID || s.Target.Region != c.Target.Binding.Region || c.Target.Binding.Partition != "aws" {
			return s, contract.ErrInvalid
		}
		return s, nil
	}
	if s.Target.ConnectionID != c.Target.Binding.ConnectionID || s.Target.Region != c.Target.Binding.Region || c.Target.Binding.Partition != "aws" || (c.Kind != s.Target.Action && !(s.Target.Action == "create" && c.Kind == "ready")) {
		return s, contract.ErrInvalid
	}
	if s.Target.Action == "create" && (s.Image == nil || s.Type == nil || s.Subnet == nil) || s.Target.Action != "create" && s.Instance == nil {
		return s, contract.ErrInvalid
	}
	return s, nil
}
func (e *Executor) config(ctx context.Context, c oc.Call, write bool) (aws.Config, error) {
	if c.Check == nil {
		return aws.Config{}, oc.ErrLease
	}
	if err := c.Check(ctx); err != nil {
		return aws.Config{}, err
	}
	return e.deps.Clients.ConfigForBound(ctx, c.ActorID, c.Target.Binding.ConnectionID, c.Target.Binding.Region, write, c.Target.Binding.Fingerprint, c.Check)
}
func classify(err error) (string, bool) {
	var own *contract.Error
	if errors.As(err, &own) {
		return own.Code, own.Code == contract.ErrUnavailable.Code
	}
	var op *oc.Error
	if errors.As(err, &op) {
		return op.Code, op.Code == oc.ErrUnavailable.Code || op.Code == oc.ErrLease.Code || op.Code == oc.ErrTargetBusy.Code
	}
	var account *ac.Error
	if errors.As(err, &account) {
		return account.Code, account.Code == ac.ErrUnavailable.Code
	}
	v := awsnet.ClassifyError(err)
	return v.Code, v.Transient
}
func before(err error) oc.Result {
	code, temporary := classify(err)
	state := oc.Failed
	if temporary {
		state = oc.Retry
	}
	return oc.Result{State: state, Code: code, ConfirmedAbsent: true, After: 5 * time.Second}
}
func observing(err error) oc.Result {
	code, temporary := classify(err)
	state := oc.Attention
	if temporary {
		state = oc.Waiting
	}
	return oc.Result{State: state, Code: code, After: 10 * time.Second}
}
func waiting(code, id string) oc.Result {
	return oc.Result{State: oc.Waiting, Code: code, After: 10 * time.Second, Output: oc.Output{ResourceID: id, Summary: "正在查询 AWS 确认请求结果"}}
}
func attention(code, id string) oc.Result {
	return oc.Result{State: oc.Attention, Code: code, Output: oc.Output{ResourceID: id, Summary: "请求结果或实例状态需要核对；不会自动重建或销毁资源"}}
}
func stableIdentity(a, b *contract.Instance) bool {
	return a != nil && b != nil && a.ID == b.ID && a.ARN == b.ARN && a.LaunchTime.Equal(b.LaunchTime) && a.ImageID == b.ImageID
}
func identity(a, b *contract.Instance) bool {
	return stableIdentity(a, b) && a.SubnetID == b.SubnetID && a.PrimaryENI == b.PrimaryENI
}
func done(kind string, v *contract.Instance) bool {
	if v == nil {
		return kind == "terminate"
	}
	switch kind {
	case "start":
		return v.State == "running"
	case "stop":
		return v.State == "stopped"
	case "terminate":
		return v.State == "terminated"
	case "enable_ipv6":
		return len(v.IPv6) > 0
	}
	return false
}
func (e *Executor) complete(ctx context.Context, c oc.Call, row *oc.Effect, v *contract.Instance) oc.Result {
	id := ""
	if v != nil {
		id = v.ARN
	}
	if row != nil {
		row.State = "confirmed"
		if v != nil {
			row.ResourceID = v.ID
			row.ResourceLocks = addresses.InstanceLocks("ec2", v.ID, v.Name)
			row.Proof = string(raw(v))
		}
		row.Code = "EC2_STATE_CONFIRMED"
		if err := e.deps.Effects.RecordEffect(ctx, *row, lease(c)); err != nil {
			return observing(err)
		}
	}
	summary := "已确认实例状态"
	if c.Kind == "reboot" {
		summary = "AWS 已接受重启请求；系统恢复情况需另行核对"
	}
	if c.Kind == "enable_ipv6" {
		summary = "已分配 IPv6，实际网络可达性与系统配置需核对"
	}
	return oc.Result{State: oc.Succeeded, Code: "EC2_STATE_CONFIRMED", Output: oc.Output{ResourceID: id, Summary: summary}}
}
func (e *Executor) Execute(ctx context.Context, c oc.Call) oc.Result {
	s, err := parse(c)
	if err != nil {
		return attention("INVALID_STORED_INPUT", "")
	}
	cfg, err := e.config(ctx, c, false)
	if err != nil {
		return before(err)
	}
	if strings.HasPrefix(c.Kind, "net_") {
		return e.network(ctx, cfg, c, s, false)
	}
	if c.Kind == "ready" {
		return e.ready(ctx, cfg, c, s)
	}
	row, err := e.deps.Effects.Effect(ctx, c.StepID)
	if err != nil {
		return before(err)
	}
	if row != nil && !sameEffect(row, c) {
		return attention("EFFECT_MISMATCH", "")
	}
	if row != nil && (row.State == "dispatched" || row.State == "confirmed") {
		return e.reconcile(ctx, cfg, c, s, row)
	}
	var current *contract.Instance
	userData := ""
	if c.Kind == "create" {
		cat, err := e.deps.Cloud.Catalogue(ctx, cfg)
		if err != nil {
			return before(err)
		}
		fresh, err := selectCreate(s.Target, cat)
		if err != nil {
			return before(err)
		}
		if fresh.Image.ID != s.Image.ID || fresh.Image.RootDevice != s.Image.RootDevice || fresh.Image.Owner != s.Image.Owner || fresh.Type.VCPU != s.Type.VCPU || fresh.Subnet.VPCID != s.Subnet.VPCID || fresh.Subnet.Zone != s.Subnet.Zone {
			return before(contract.ErrCatalogue)
		}
		instances, err := e.deps.Cloud.Instances(ctx, cfg, c.Target.Binding.AWSAccountID)
		if err != nil {
			return before(err)
		}
		for _, v := range instances {
			if v.Name == s.Target.Name && v.State != "terminated" {
				return before(contract.ErrConflict)
			}
		}
		quota, err := e.deps.Cloud.Quota(ctx, cfg, c.Target.Binding.AWSAccountID, s.Target.InstanceType)
		if err != nil {
			return before(err)
		}
		if quota.LimitKnown && quota.UsageKnown && quota.Used+float64(s.Type.VCPU) > quota.Limit {
			return before(contract.ErrQuota)
		}
		if s.Target.LoginMethod == "root_password" {
			if e.deps.LaunchSecrets == nil {
				return before(contract.ErrUnavailable)
			}
			secret, err := e.deps.LaunchSecrets.LaunchSecret(ctx, c.ActorID, s.Target.LaunchSecretID, launchBinding(s.Target, c.Target.Binding.AccountID))
			if err != nil {
				return before(err)
			}
			userData, err = instanceinit.RootPassword(secret)
			if err != nil {
				return before(contract.ErrLogin)
			}
		}
	} else {
		current, err = e.deps.Cloud.Instance(ctx, cfg, c.Target.Binding.AWSAccountID, s.Target.InstanceID)
		if err != nil {
			return before(err)
		}
		if c.Kind == "terminate" && current != nil && current.State == "terminated" && stableIdentity(current, s.Instance) {
			return e.complete(ctx, c, nil, current)
		}
		if !identity(current, s.Instance) {
			return before(contract.ErrConflict)
		}
		if done(c.Kind, current) {
			return e.complete(ctx, c, nil, current)
		}
		if !validAction(c.Kind, *current) || current.State != s.Instance.State || !slices.Equal(current.SecurityGroupIDs, s.Instance.SecurityGroupIDs) {
			return before(contract.ErrConflict)
		}
		if c.Kind == "enable_ipv6" || c.Kind == "start" {
			cat, err := e.deps.Cloud.Catalogue(ctx, cfg)
			if err != nil {
				return before(err)
			}
			if c.Kind == "enable_ipv6" && !ipv6Ready(*current, cat) {
				return before(contract.ErrNetwork)
			}
			if c.Kind == "start" {
				var count int32
				for _, t := range cat.Types {
					if t.ID == current.Type {
						count = t.VCPU
					}
				}
				if count == 0 {
					return before(contract.ErrCatalogue)
				}
				q, err := e.deps.Cloud.Quota(ctx, cfg, c.Target.Binding.AWSAccountID, current.Type)
				if err != nil {
					return before(err)
				}
				if q.LimitKnown && q.UsageKnown && q.Used+float64(count) > q.Limit {
					return before(contract.ErrQuota)
				}
			}
		}
	}
	prepared, err := e.deps.Effects.PrepareEffect(ctx, expected(c), lease(c))
	if err != nil {
		return before(err)
	}
	if prepared.State != "prepared" && prepared.State != "rejected" {
		return e.reconcile(ctx, cfg, c, s, &prepared)
	}
	cfg, err = e.config(ctx, c, true)
	if err != nil {
		return before(err)
	}
	dispatched, err := e.deps.Effects.DispatchEffect(ctx, c.StepID, lease(c))
	if err != nil {
		return observing(err)
	}
	var accepted source.Accepted
	if c.Kind == "create" {
		accepted, err = e.deps.Cloud.Launch(ctx, cfg, s, c.Token, userData, labels(c))
	} else {
		accepted, err = e.deps.Cloud.Manage(ctx, cfg, c.Kind, *current)
	}
	if err != nil {
		info := awsnet.ClassifyError(err)
		if info.Code == "AWS_ACCESS_DENIED" || info.Code == "AWS_INVALID_REQUEST" || info.Code == "AWS_QUOTA_EXCEEDED" || info.Code == "AWS_STATE_CONFLICT" {
			dispatched.State = "rejected"
			dispatched.Code = info.Code
			dispatched.RequestID = info.RequestID
			if save := e.deps.Effects.RecordEffect(ctx, dispatched, lease(c)); save == nil {
				return oc.Result{State: oc.Failed, Code: info.Code, ConfirmedAbsent: true, Output: oc.Output{RequestID: info.RequestID, Summary: "AWS 拒绝请求，已有资源保留"}}
			}
		}
		return waiting("EC2_RESULT_UNCONFIRMED", dispatched.ResourceID)
	}
	dispatched.ResourceID = accepted.ResourceID
	dispatched.RequestID = accepted.RequestID
	dispatched.Code = "EC2_REQUEST_ACCEPTED"
	if err := e.deps.Effects.RecordEffect(ctx, dispatched, lease(c)); err != nil {
		return waiting("EC2_RECEIPT_UNCONFIRMED", accepted.ResourceID)
	}
	if c.Kind == "reboot" {
		return e.complete(ctx, c, &dispatched, current)
	}
	return waiting("EC2_REQUEST_ACCEPTED", accepted.ResourceID)
}
func (e *Executor) Reconcile(ctx context.Context, c oc.Call) oc.Result {
	s, err := parse(c)
	if err != nil {
		return attention("INVALID_STORED_INPUT", "")
	}
	cfg, err := e.config(ctx, c, false)
	if err != nil {
		return observing(err)
	}
	if strings.HasPrefix(c.Kind, "net_") {
		return e.network(ctx, cfg, c, s, true)
	}
	if c.Kind == "ready" {
		return e.ready(ctx, cfg, c, s)
	}
	row, err := e.deps.Effects.Effect(ctx, c.StepID)
	if err != nil {
		return observing(err)
	}
	if row != nil && !sameEffect(row, c) {
		return attention("EFFECT_MISMATCH", "")
	}
	return e.reconcile(ctx, cfg, c, s, row)
}
func (e *Executor) reconcile(ctx context.Context, cfg aws.Config, c oc.Call, s contract.Spec, row *oc.Effect) oc.Result {
	if row == nil || row.State == "prepared" || row.State == "rejected" {
		return oc.Result{State: oc.Retry, Code: "EC2_NOT_DISPATCHED", ConfirmedAbsent: true, After: time.Second}
	}
	if c.Kind == "create" {
		rows, err := e.deps.Cloud.FindCreated(ctx, cfg, c.Target.Binding.AWSAccountID, c.Token)
		if err != nil {
			return observing(err)
		}
		if len(rows) == 0 {
			return waiting("EC2_INSTANCE_NOT_VISIBLE", row.ResourceID)
		}
		if len(rows) != 1 {
			return attention("EC2_OWNERSHIP_AMBIGUOUS", row.ResourceID)
		}
		v := rows[0]
		if row.ResourceID != "" && v.ID != row.ResourceID {
			return attention("EC2_INSTANCE_CHANGED", row.ResourceID)
		}
		for k, want := range labels(c) {
			if v.Tags[k] != want {
				return attention("EC2_OWNERSHIP_UNCONFIRMED", v.ARN)
			}
		}
		if v.ClientToken != c.Token || v.Name != s.Target.Name || v.ImageID != s.Target.ImageID || v.Type != s.Target.InstanceType || v.SubnetID != s.Target.SubnetID || !slices.Equal(v.SecurityGroupIDs, s.Target.SecurityGroupIDs) || v.RootDevice != s.Image.RootDevice || row.FirstSentAt == nil || v.LaunchTime.Before(row.FirstSentAt.Add(-30*time.Second)) {
			return attention("EC2_SPEC_UNCONFIRMED", v.ARN)
		}
		row.State = "confirmed"
		row.ResourceID = v.ID
		row.ResourceLocks = addresses.InstanceLocks("ec2", v.ID, v.Name)
		row.Proof = string(raw(v))
		row.Code = "EC2_CREATED"
		if err := e.deps.Effects.RecordEffect(ctx, *row, lease(c)); err != nil {
			return observing(err)
		}
		if s.Target.LaunchSecretID != "" {
			if err := e.deps.LaunchSecrets.DeleteLaunchSecret(ctx, c.ActorID, s.Target.LaunchSecretID, launchBinding(s.Target, c.Target.Binding.AccountID)); err != nil {
				return observing(err)
			}
		}
		if v.State == "terminated" || v.State == "shutting-down" {
			return oc.Result{State: oc.Failed, Code: "EC2_TERMINATED_DURING_LAUNCH", Output: oc.Output{ResourceID: v.ARN, Summary: "创建记录已确认，但实例已终止；请核对 AWS 状态原因"}}
		}
		return oc.Result{State: oc.Succeeded, Code: "EC2_CREATED", Output: oc.Output{ResourceID: v.ARN, RequestID: row.RequestID, Summary: "已确认实例归属，等待运行状态"}}
	}
	current, err := e.deps.Cloud.Instance(ctx, cfg, c.Target.Binding.AWSAccountID, s.Target.InstanceID)
	if err != nil {
		return observing(err)
	}
	if current == nil && c.Kind == "terminate" {
		return e.complete(ctx, c, row, s.Instance)
	}
	if c.Kind == "terminate" && current != nil && current.State == "terminated" && stableIdentity(current, s.Instance) {
		return e.complete(ctx, c, row, current)
	}
	if !identity(current, s.Instance) {
		return attention("EC2_INSTANCE_CHANGED", s.Instance.ARN)
	}
	if done(c.Kind, current) {
		return e.complete(ctx, c, row, current)
	}
	if c.Kind == "reboot" {
		if row.Code == "EC2_REQUEST_ACCEPTED" || row.State == "confirmed" {
			return e.complete(ctx, c, row, current)
		}
		return attention("REBOOT_RESULT_UNKNOWN", current.ARN)
	}
	if row.State == "confirmed" {
		return attention("EC2_CONFIRMED_STATE_CHANGED", current.ARN)
	}
	if current.State == "terminated" || current.State == "shutting-down" {
		return attention("EC2_INSTANCE_TERMINATING", current.ARN)
	}
	return waiting("EC2_STATE_PENDING", current.ARN)
}
func (e *Executor) ready(ctx context.Context, cfg aws.Config, c oc.Call, s contract.Spec) oc.Result {
	var receipt *oc.Effect
	var err error
	for _, step := range c.Completed {
		if step.Kind == "create" {
			receipt, err = e.deps.Effects.Effect(ctx, step.ID)
			if err != nil {
				return observing(err)
			}
		}
	}
	if receipt == nil || receipt.JobID != c.JobID || receipt.AccountID != c.Target.Binding.AccountID || receipt.State != "confirmed" || receipt.ResourceID == "" {
		return attention("EC2_CREATE_RECEIPT_MISSING", "")
	}
	var previous contract.Instance
	if json.Unmarshal([]byte(receipt.Proof), &previous) != nil {
		return attention("EC2_CREATE_RECEIPT_INVALID", receipt.ResourceID)
	}
	v, err := e.deps.Cloud.Instance(ctx, cfg, c.Target.Binding.AWSAccountID, receipt.ResourceID)
	if err != nil {
		return observing(err)
	}
	if v == nil {
		return waiting("EC2_NOT_VISIBLE", receipt.ResourceID)
	}
	if v.ID != previous.ID || !v.LaunchTime.Equal(previous.LaunchTime) || v.ImageID != s.Target.ImageID || v.SubnetID != s.Target.SubnetID {
		return attention("EC2_INSTANCE_CHANGED", v.ARN)
	}
	if v.State == "terminated" || v.State == "shutting-down" || v.State == "stopped" {
		return oc.Result{State: oc.Failed, Code: "EC2_NOT_RUNNING", Output: oc.Output{ResourceID: v.ARN}}
	}
	if v.State != "running" || s.Target.IPv6 && len(v.IPv6) == 0 {
		return waiting("EC2_STARTING", v.ARN)
	}
	return oc.Result{State: oc.Succeeded, Code: "EC2_RUNNING", Output: oc.Output{ResourceID: v.ARN, Summary: "AWS 显示实例运行中；cloud-init、SSH 与 IPv6 连通性需另行验证"}}
}
