package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	awsnet "github.com/Ember-Moth/aws-panel/internal/infra/aws"
	ac "github.com/Ember-Moth/aws-panel/internal/module/accounts/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/addresses/contract"
	oc "github.com/Ember-Moth/aws-panel/internal/module/operations/contract"
	"github.com/aws/aws-sdk-go-v2/aws"
)

type effectProof struct {
	Address    *contract.Address `json:"address,omitempty"`
	Operations []Operation       `json:"operations,omitempty"`
}

func effectLease(call oc.Call) oc.EffectLease {
	return oc.EffectLease{StepID: call.StepID, Owner: call.LeaseOwner, Epoch: call.LeaseEpoch}
}
func parseSpec(call oc.Call) (contract.Spec, error) {
	var spec contract.Spec
	if json.Unmarshal(call.Input, &spec) != nil {
		return spec, contract.ErrInvalid
	}
	if _, err := normalize([]contract.Target{spec.Target}); err != nil {
		return spec, err
	}
	if spec.Target.ConnectionID != call.Target.Binding.ConnectionID || spec.Target.Region != call.Target.Binding.Region || call.Target.Binding.Partition != "aws" {
		return spec, contract.ErrInvalid
	}
	switch call.Kind {
	case "allocate", "attach", "detach", "detach_previous", "release", "release_previous":
	default:
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
func tags(call oc.Call) map[string]string {
	v := map[string]string{"aws-panel:step": call.StepID, "aws-panel:token": call.Token, "aws-panel:spec": hash(string(call.Input))}
	var spec contract.Spec
	if json.Unmarshal(call.Input, &spec) == nil && spec.Managed && spec.Instance != nil {
		v["aws-panel:instance"] = spec.Instance.ID
		v["aws-panel:role"] = "instance-ip"
	}
	return v
}
func expectedEffect(call oc.Call) *oc.Effect {
	return &oc.Effect{StepID: call.StepID, JobID: call.JobID, AccountID: call.Target.Binding.AccountID, Region: call.Target.Binding.Region, Purpose: "addresses." + call.Kind, SpecHash: hash(string(call.Input)), Token: call.Token}
}
func sameEffect(row *oc.Effect, call oc.Call) bool {
	v := expectedEffect(call)
	return row.StepID == v.StepID && row.JobID == v.JobID && row.AccountID == v.AccountID && row.Region == v.Region && row.Purpose == v.Purpose && row.Token == v.Token && row.SpecHash == v.SpecHash
}
func sameAddress(value, expected *contract.Address) bool {
	return value != nil && expected != nil && value.ID == expected.ID && value.ARN == expected.ARN && value.IP == expected.IP && value.BorderGroup == expected.BorderGroup && (expected.Kind == "" || value.Kind == expected.Kind)
}
func sameInstance(value, expected *contract.Instance) bool {
	return value != nil && expected != nil && value.ID == expected.ID && value.ARN == expected.ARN && value.NetworkInterfaceID == expected.NetworkInterfaceID && value.PrivateIP == expected.PrivateIP && value.BorderGroup == expected.BorderGroup && value.WavelengthZoneID == expected.WavelengthZoneID
}
func attachedTo(kind string, address *contract.Address, instance *contract.Instance) bool {
	if address == nil || instance == nil || !address.Attached || address.InstanceID != instance.ID {
		return false
	}
	return kind == "lightsail" || (address.NetworkInterfaceID == instance.NetworkInterfaceID && address.PrivateIP == instance.PrivateIP)
}
func (e *Executor) allocated(ctx context.Context, cfg aws.Config, call oc.Call, spec contract.Spec) (*contract.Address, *contract.Address, error) {
	if spec.Address != nil {
		current, err := e.deps.Cloud.Address(ctx, cfg, spec.Target.Service, call.Target.Binding.AWSAccountID, spec.Address.ID)
		if err != nil {
			return nil, nil, err
		}
		if !sameAddress(current, spec.Address) {
			return nil, nil, contract.ErrConflict
		}
		return current, spec.Address, nil
	}
	for _, step := range call.Completed {
		if step.Kind == "allocate" {
			row, err := e.deps.Effects.Effect(ctx, step.ID)
			if err != nil {
				return nil, nil, err
			}
			if row == nil || row.JobID != call.JobID || row.AccountID != call.Target.Binding.AccountID || row.State != "confirmed" || row.ResourceID == "" {
				return nil, nil, oc.ErrEffect
			}
			var proof effectProof
			if json.Unmarshal([]byte(row.Proof), &proof) != nil || proof.Address == nil {
				return nil, nil, oc.ErrEffect
			}
			current, err := e.deps.Cloud.Address(ctx, cfg, spec.Target.Service, call.Target.Binding.AWSAccountID, row.ResourceID)
			if err != nil {
				return nil, nil, err
			}
			if !sameAddress(current, proof.Address) {
				return nil, nil, contract.ErrConflict
			}
			return current, proof.Address, nil
		}
	}
	return nil, nil, oc.ErrEffect
}

type observation struct {
	address, expected *contract.Address
	instance          *contract.Instance
	done, partial     bool
}

func (e *Executor) observe(ctx context.Context, cfg aws.Config, call oc.Call, spec contract.Spec) (observation, error) {
	value := observation{}
	if spec.Instance != nil {
		current, err := e.deps.Cloud.Instance(ctx, cfg, spec.Target.Service, call.Target.Binding.AWSAccountID, spec.Instance.ID)
		if err != nil {
			return value, err
		}
		if !sameInstance(current, spec.Instance) {
			return value, contract.ErrConflict
		}
		value.instance = current
	}
	switch call.Kind {
	case "attach":
		current, expected, err := e.allocated(ctx, cfg, call, spec)
		if err != nil {
			return value, err
		}
		value.address = current
		value.expected = expected
		value.partial = attachedTo(spec.Target.Service, current, value.instance)
		value.done = value.partial && value.instance.AddressID == current.ID && value.instance.PublicIP == current.IP
	case "detach_previous", "release_previous":
		if spec.Previous == nil {
			return value, contract.ErrInvalid
		}
		value.expected = spec.Previous
		// Do not disconnect the old address until the new allocation still exists.
		replacement, _, err := e.allocated(ctx, cfg, call, spec)
		if err != nil {
			return value, err
		}
		if call.Kind == "release_previous" && (!attachedTo(spec.Target.Service, replacement, value.instance) || value.instance.AddressID != replacement.ID || value.instance.PublicIP != replacement.IP) {
			return value, contract.ErrConflict
		}
		current, err := e.deps.Cloud.Address(ctx, cfg, spec.Target.Service, call.Target.Binding.AWSAccountID, spec.Previous.ID)
		if err != nil {
			return value, err
		}
		value.address = current
		if current != nil && !sameAddress(current, spec.Previous) {
			return value, contract.ErrConflict
		}
		if call.Kind == "release_previous" {
			value.done = current == nil
		} else {
			if current == nil {
				return value, contract.ErrConflict
			}
			value.partial = !current.Attached
			value.done = value.partial && value.instance.AddressID != current.ID
		}
	case "detach", "release":
		if spec.Address == nil {
			return value, contract.ErrInvalid
		}
		value.expected = spec.Address
		current, err := e.deps.Cloud.Address(ctx, cfg, spec.Target.Service, call.Target.Binding.AWSAccountID, spec.Address.ID)
		if err != nil {
			return value, err
		}
		value.address = current
		if current != nil && !sameAddress(current, spec.Address) {
			return value, contract.ErrConflict
		}
		if call.Kind == "release" {
			value.done = current == nil
		} else {
			if current == nil {
				return value, contract.ErrConflict
			}
			value.partial = !current.Attached
			value.done = value.partial && (value.instance == nil || value.instance.AddressID != current.ID)
		}
	default:
		return value, contract.ErrInvalid
	}
	return value, nil
}
func precondition(call oc.Call, spec contract.Spec, value observation) error {
	if value.instance != nil && value.instance.State != "running" && value.instance.State != "stopped" {
		return contract.ErrConflict
	}
	switch call.Kind {
	case "attach":
		if value.instance == nil || value.address == nil {
			return contract.ErrConflict
		}
		if value.address.Attached && !attachedTo(spec.Target.Service, value.address, value.instance) {
			return contract.ErrAttached
		}
		wanted := ""
		if spec.Previous != nil {
			wanted = spec.Previous.ID
		}
		for _, step := range call.Completed {
			if step.Kind == "detach_previous" {
				wanted = ""
			}
		}
		if value.instance.AddressID != wanted {
			return contract.ErrConflict
		}
	case "detach", "detach_previous":
		if value.address == nil || value.expected == nil {
			return contract.ErrConflict
		}
		if value.address.Attached && (value.address.InstanceID != value.expected.InstanceID || value.address.NetworkInterfaceID != value.expected.NetworkInterfaceID || value.address.PrivateIP != value.expected.PrivateIP) {
			return contract.ErrConflict
		}
	case "release", "release_previous":
		if value.address != nil && value.address.Attached {
			return contract.ErrAttached
		}
	}
	return nil
}
func (e *Executor) complete(ctx context.Context, call oc.Call, row *oc.Effect, value observation) oc.Result {
	reference := value.expected
	if value.address != nil {
		reference = value.address
	}
	if reference == nil {
		return attention("RESOURCE_REFERENCE_MISSING", "")
	}
	if row != nil {
		row.State = "confirmed"
		row.ResourceID = reference.ID
		row.Code = "ADDRESS_STATE_CONFIRMED"
		var proof effectProof
		_ = json.Unmarshal([]byte(row.Proof), &proof)
		proof.Address = reference
		row.Proof = string(mustJSON(proof))
		row.ResourceLocks = AddressLocks(callService(call), *reference)
		if err := e.deps.Effects.RecordEffect(ctx, *row, effectLease(call)); err != nil {
			return observeError(err)
		}
	}
	summary := map[string]string{"attach": "地址绑定已核对", "detach": "地址已解绑，仍保留在账号中", "detach_previous": "旧地址已解绑，继续绑定新地址", "release": "地址已释放，不能保证能够找回", "release_previous": "替换地址已确认，旧地址已释放"}[call.Kind]
	return oc.Result{State: oc.Succeeded, Code: "ADDRESS_STATE_CONFIRMED", Output: oc.Output{ResourceID: reference.ARN, Summary: summary}}
}
func callService(call oc.Call) string {
	var spec contract.Spec
	_ = json.Unmarshal(call.Input, &spec)
	return spec.Target.Service
}
func (e *Executor) Execute(ctx context.Context, call oc.Call) oc.Result {
	spec, err := parseSpec(call)
	if err != nil {
		return noSend(err)
	}
	cfg, err := e.config(ctx, call, false)
	if err != nil {
		return noSend(err)
	}
	row, err := e.deps.Effects.Effect(ctx, call.StepID)
	if err != nil {
		return noSend(err)
	}
	if row != nil && !sameEffect(row, call) {
		return attention("EFFECT_MISMATCH", "")
	}
	if row != nil && (row.State == "dispatched" || row.State == "confirmed") {
		return e.reconcile(ctx, cfg, call, spec, row)
	}
	var observed observation
	if call.Kind == "allocate" {
		if spec.Target.Service == "wavelength" {
			cat, err := e.deps.Cloud.Catalogue(ctx, cfg, spec.Target.Service, call.Target.Binding.AWSAccountID)
			if err != nil {
				return noSend(err)
			}
			valid := false
			for _, z := range cat.WavelengthZones {
				valid = valid || (z.BorderGroup == spec.BorderGroup && (spec.Target.WavelengthZoneID == "" || z.ID == spec.Target.WavelengthZoneID))
			}
			if !valid || spec.BorderGroup == "" {
				return noSend(contract.ErrUnsupported)
			}
		}
		found, err := e.deps.Cloud.FindAllocated(ctx, cfg, spec.Target.Service, call.Target.Binding.AWSAccountID, spec.NewName, tags(call))
		if err != nil {
			return noSend(err)
		}
		if len(found) > 0 {
			return noSend(contract.ErrConflict)
		}
		quota, err := e.deps.Cloud.Quota(ctx, cfg, spec.Target.Service, call.Target.Binding.AWSAccountID)
		if err != nil {
			return noSend(err)
		}
		if quota.LimitKnown && quota.UsageKnown && quota.Used+1 > quota.Limit {
			return noSend(contract.ErrQuota)
		}
	} else {
		observed, err = e.observe(ctx, cfg, call, spec)
		if err != nil {
			return noSend(err)
		}
		if observed.done {
			return e.complete(ctx, call, row, observed)
		}
		if observed.partial {
			return waiting("ADDRESS_VISIBILITY_PENDING", observed.expected)
		}
		if err := precondition(call, spec, observed); err != nil {
			return noSend(err)
		}
	}
	prepared, err := e.deps.Effects.PrepareEffect(ctx, *expectedEffect(call), effectLease(call))
	if err != nil {
		return noSend(err)
	}
	if prepared.State != "prepared" && prepared.State != "rejected" {
		return e.reconcile(ctx, cfg, call, spec, &prepared)
	}
	writeCfg, err := e.config(ctx, call, true)
	if err != nil {
		return noSend(err)
	}
	dispatched, err := e.deps.Effects.DispatchEffect(ctx, call.StepID, effectLease(call))
	if err != nil {
		return waiting("DISPATCH_RECORD_UNCONFIRMED", observed.expected)
	}
	var accepted Accepted
	switch call.Kind {
	case "allocate":
		border := spec.Target.Region
		if spec.Target.Service == "wavelength" {
			border = spec.BorderGroup
		}
		if spec.Instance != nil && spec.Instance.BorderGroup != "" {
			border = spec.Instance.BorderGroup
		}
		accepted, err = e.deps.Cloud.Allocate(ctx, writeCfg, spec.Target.Service, spec.NewName, border, tags(call))
	case "attach":
		accepted, err = e.deps.Cloud.Attach(ctx, writeCfg, spec.Target.Service, *observed.address, *observed.instance)
	case "detach", "detach_previous":
		accepted, err = e.deps.Cloud.Detach(ctx, writeCfg, spec.Target.Service, *observed.address)
	case "release", "release_previous":
		accepted, err = e.deps.Cloud.Release(ctx, writeCfg, spec.Target.Service, *observed.address)
	}
	if err != nil {
		info := awsnet.ClassifyError(err)
		dispatched.RequestID = info.RequestID
		dispatched.Code = info.Code
		if info.Code == "AWS_ACCESS_DENIED" || info.Code == "AWS_INVALID_REQUEST" || info.Code == "AWS_QUOTA_EXCEEDED" || info.Code == "AWS_STATE_CONFLICT" || info.Code == "AWS_REGION_UNAVAILABLE" {
			dispatched.State = "rejected"
			if e.deps.Effects.RecordEffect(ctx, dispatched, effectLease(call)) == nil {
				return oc.Result{State: oc.Failed, Code: info.Code, ConfirmedAbsent: true, Output: oc.Output{RequestID: info.RequestID, Summary: "AWS 拒绝本次请求，已有资源保留"}}
			}
		}
		return waiting("ADDRESS_RESULT_UNCONFIRMED", observed.expected)
	}
	dispatched.RequestID = accepted.RequestID
	dispatched.ResourceID = accepted.ResourceID
	dispatched.Proof = string(mustJSON(effectProof{Operations: accepted.Operations}))
	dispatched.Code = "ADDRESS_REQUEST_ACCEPTED"
	if call.Kind != "allocate" && observed.expected != nil {
		dispatched.ResourceID = observed.expected.ID
	}
	if err := e.deps.Effects.RecordEffect(ctx, dispatched, effectLease(call)); err != nil {
		return waiting("ADDRESS_RECEIPT_UNCONFIRMED", observed.expected)
	}
	return waiting("ADDRESS_REQUEST_ACCEPTED", observed.expected)
}
func (e *Executor) Reconcile(ctx context.Context, call oc.Call) oc.Result {
	spec, err := parseSpec(call)
	if err != nil {
		return attention("INVALID_STORED_INPUT", "")
	}
	cfg, err := e.config(ctx, call, false)
	if err != nil {
		return observeError(err)
	}
	row, err := e.deps.Effects.Effect(ctx, call.StepID)
	if err != nil {
		return observeError(err)
	}
	if row != nil && !sameEffect(row, call) {
		return attention("EFFECT_MISMATCH", "")
	}
	return e.reconcile(ctx, cfg, call, spec, row)
}
func (e *Executor) reconcile(ctx context.Context, cfg aws.Config, call oc.Call, spec contract.Spec, row *oc.Effect) oc.Result {
	if call.Kind == "allocate" {
		if row == nil || row.State == "prepared" || row.State == "rejected" {
			return oc.Result{State: oc.Retry, Code: "NO_ACCEPTED_ALLOCATION", ConfirmedAbsent: true, After: time.Second}
		}
		return e.reconcileAllocation(ctx, cfg, call, spec, row)
	}
	value, err := e.observe(ctx, cfg, call, spec)
	if err != nil {
		return observeError(err)
	}
	if spec.Target.Service == "lightsail" && row != nil && row.State == "dispatched" && (call.Kind == "attach" || call.Kind == "detach" || call.Kind == "detach_previous") {
		terminal, failed, err := e.mutationFinished(ctx, cfg, call, spec, row, value)
		if err != nil {
			return observeError(err)
		}
		if failed {
			row.State = "confirmed"
			if !value.done && !value.partial && precondition(call, spec, value) == nil {
				row.State = "rejected"
			}
			row.ResourceID = value.expected.ID
			var proof effectProof
			_ = json.Unmarshal([]byte(row.Proof), &proof)
			proof.Address = value.address
			row.Proof = string(mustJSON(proof))
			if err := e.deps.Effects.RecordEffect(ctx, *row, effectLease(call)); err != nil {
				return observeError(err)
			}
			return oc.Result{State: oc.Failed, Code: "AWS_ADDRESS_OPERATION_FAILED", Output: oc.Output{ResourceID: value.expected.ARN, Summary: "AWS 操作记录失败；当前地址与旧地址引用已保留，请核对后处理"}}
		}
		if !terminal {
			return waiting("ADDRESS_OPERATION_PENDING", value.expected)
		}
	}
	if value.done {
		return e.complete(ctx, call, row, value)
	}

	if value.partial {
		return waiting("ADDRESS_VISIBILITY_PENDING", value.expected)
	}
	if row == nil || row.State == "prepared" || row.State == "rejected" {
		return oc.Result{State: oc.Retry, Code: "NO_ACCEPTED_ADDRESS_WRITE", ConfirmedAbsent: true, After: time.Second}
	}
	if row.State == "confirmed" {
		return attention("CONFIRMED_ADDRESS_CHANGED", value.expected.ARN)
	}
	if err := precondition(call, spec, value); err != nil {
		return observeError(err)
	}
	return waiting("ADDRESS_RESULT_UNCONFIRMED", value.expected)
}
func (e *Executor) reconcileAllocation(ctx context.Context, cfg aws.Config, call oc.Call, spec contract.Spec, row *oc.Effect) oc.Result {
	found, err := e.deps.Cloud.FindAllocated(ctx, cfg, spec.Target.Service, call.Target.Binding.AWSAccountID, spec.NewName, tags(call))
	if err != nil {
		return observeError(err)
	}
	if len(found) > 1 {
		return attention("ALLOCATION_AMBIGUOUS", row.ResourceID)
	}
	if len(found) == 0 && row.State == "confirmed" {
		return attention("ALLOCATED_ADDRESS_UNCONFIRMED", row.ResourceID)
	}
	var current *contract.Address
	if len(found) == 1 {
		current = &found[0]
	}
	if current != nil {
		if spec.Target.Service == "wavelength" && (current.Kind != "wavelength" || current.BorderGroup != spec.BorderGroup) {
			return attention("CARRIER_BORDER_GROUP_CHANGED", current.ARN)
		}
		if row.ResourceID != "" && row.ResourceID != current.ID {
			return attention("ALLOCATION_CHANGED", row.ResourceID)
		}
		if spec.Target.Service != "lightsail" {
			for key, value := range tags(call) {
				if current.Tags[key] != value {
					return attention("ALLOCATION_OWNERSHIP_UNCONFIRMED", current.ARN)
				}
			}
		} else if current.CreatedAt == nil || row.FirstSentAt == nil || current.CreatedAt.Before(row.FirstSentAt.Add(-30*time.Second)) {
			return attention("ALLOCATION_OWNERSHIP_UNCONFIRMED", current.ARN)
		}
		if current.Attached && (spec.Instance == nil || !attachedTo(spec.Target.Service, current, spec.Instance)) {
			return attention("NEW_ADDRESS_ALREADY_ATTACHED", current.ARN)
		}
	}
	if spec.Target.Service == "lightsail" {
		history, err := e.deps.Cloud.History(ctx, cfg, spec.Target.Service, spec.NewName)
		if err != nil {
			return observeError(err)
		}
		var proof effectProof
		_ = json.Unmarshal([]byte(row.Proof), &proof)
		ids := map[string]bool{}
		for _, op := range proof.Operations {
			if op.Type == "AllocateStaticIp" && op.Name == spec.NewName && op.Region == spec.Target.Region {
				ids[op.ID] = true
			}
		}
		matches := []Operation{}
		for _, op := range history {
			if op.Type != "AllocateStaticIp" || op.Name != spec.NewName || op.Region != spec.Target.Region || op.ID == "" || row.FirstSentAt == nil || op.CreatedAt.Before(row.FirstSentAt.Add(-30*time.Second)) || op.CreatedAt.After(time.Now().Add(30*time.Second)) {
				continue
			}
			if len(ids) > 0 && !ids[op.ID] {
				continue
			}
			matches = append(matches, op)
		}
		if len(matches) > 1 {
			return attention("ALLOCATION_HISTORY_AMBIGUOUS", row.ResourceID)
		}
		if len(matches) == 0 {
			return waiting("ALLOCATION_HISTORY_PENDING", current)
		}
		op := matches[0]
		if op.Terminal && op.State == "Failed" && current == nil {
			row.State = "rejected"
			row.Code = "ALLOCATION_REJECTED"
			if err := e.deps.Effects.RecordEffect(ctx, *row, effectLease(call)); err != nil {
				return observeError(err)
			}
			return oc.Result{State: oc.Failed, Code: "ALLOCATION_REJECTED", ConfirmedAbsent: true}
		}
		if current == nil {
			return waiting("ALLOCATION_NOT_VISIBLE", nil)
		}
		row.Proof = string(mustJSON(effectProof{Address: current, Operations: matches}))
		if op.Terminal && op.State == "Failed" {
			row.ResourceID = current.ID
			row.State = "confirmed"
			row.ResourceLocks = AddressLocks(spec.Target.Service, *current)
			if err := e.deps.Effects.RecordEffect(ctx, *row, effectLease(call)); err != nil {
				return observeError(err)
			}
			return oc.Result{State: oc.Failed, Code: "ALLOCATION_FAILED_RESOURCE_PRESENT", Output: oc.Output{ResourceID: current.ARN, Summary: "AWS 分配操作失败，但地址存在，请核对后处理"}}
		}
		if !op.Terminal || (op.State != "Succeeded" && op.State != "Completed") {
			return waiting("ALLOCATION_PENDING", current)
		}
	}
	if current == nil {
		if row.State == "confirmed" {
			return attention("ALLOCATED_ADDRESS_MISSING", row.ResourceID)
		}
		return waiting("ALLOCATION_NOT_VISIBLE", nil)
	}
	row.State = "confirmed"
	row.ResourceID = current.ID
	row.Code = "ALLOCATION_CONFIRMED"
	row.ResourceLocks = AddressLocks(spec.Target.Service, *current)
	if spec.Target.Service != "lightsail" {
		row.Proof = string(mustJSON(effectProof{Address: current}))
	}
	if err := e.deps.Effects.RecordEffect(ctx, *row, effectLease(call)); err != nil {
		return observeError(err)
	}
	return oc.Result{State: oc.Succeeded, Code: "ALLOCATION_CONFIRMED", Output: oc.Output{ResourceID: current.ARN, RequestID: row.RequestID, Summary: "已确认新地址：" + current.IP + "（" + current.ID + "）"}}
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
	info := awsnet.ClassifyError(err)
	return info.Code, info.Transient
}
func noSend(err error) oc.Result {
	code, transient := classify(err)
	state := oc.Failed
	if transient {
		state = oc.Retry
	}
	return oc.Result{State: state, Code: code, ConfirmedAbsent: true, After: 5 * time.Second}
}
func observeError(err error) oc.Result {
	code, transient := classify(err)
	state := oc.Attention
	if transient {
		state = oc.Waiting
	}
	return oc.Result{State: state, Code: code, After: 10 * time.Second}
}
func waiting(code string, address *contract.Address) oc.Result {
	output := oc.Output{Summary: "请求结果正在核对，不会直接重发"}
	if address != nil {
		output.ResourceID = address.ARN
	}
	return oc.Result{State: oc.Waiting, Code: code, After: 10 * time.Second, Output: output}
}
func attention(code, id string) oc.Result {
	return oc.Result{State: oc.Attention, Code: code, Output: oc.Output{ResourceID: id, Summary: "资源状态需要人工核对；现有地址不会自动释放"}}
}

// Positive attachment visibility can precede an asynchronous Lightsail failure.
// Wait for all correlated attach/detach operations before allowing the next step.
func (e *Executor) mutationFinished(ctx context.Context, cfg aws.Config, call oc.Call, spec contract.Spec, row *oc.Effect, value observation) (bool, bool, error) {
	kind := "AttachStaticIp"
	if call.Kind != "attach" {
		kind = "DetachStaticIp"
	}
	names := map[string]bool{value.expected.ID: true}
	if value.instance != nil {
		names[value.instance.ID] = true
	}
	var proof effectProof
	_ = json.Unmarshal([]byte(row.Proof), &proof)
	known := map[string]bool{}
	for _, op := range proof.Operations {
		if op.ID != "" && op.Type == kind && op.Region == spec.Target.Region && names[op.Name] {
			known[op.ID] = true
		}
	}
	matches := map[string]Operation{}
	if len(known) > 0 {
		for id := range known {
			op, err := e.deps.Cloud.Operation(ctx, cfg, spec.Target.Service, id)
			if err != nil {
				return false, false, err
			}
			if op == nil {
				return false, false, nil
			}
			if op.ID != id || op.Type != kind || op.Region != spec.Target.Region || !names[op.Name] {
				return false, false, oc.ErrEffect
			}
			matches[id] = *op
		}
	} else {
		perName := map[string]string{}
		for name := range names {
			rows, err := e.deps.Cloud.History(ctx, cfg, spec.Target.Service, name)
			if err != nil {
				return false, false, err
			}
			for _, op := range rows {
				if op.ID == "" || op.Type != kind || op.Region != spec.Target.Region || !names[op.Name] || row.FirstSentAt == nil || op.CreatedAt.Before(row.FirstSentAt.Add(-30*time.Second)) || op.CreatedAt.After(time.Now().Add(30*time.Second)) {
					continue
				}
				if other := perName[op.Name]; other != "" && other != op.ID {
					return false, false, contract.ErrConflict
				}
				perName[op.Name] = op.ID
				matches[op.ID] = op
			}
		}
	}
	if len(matches) == 0 {
		return false, false, nil
	}
	proof.Operations = nil
	terminal := true
	failed := false
	for _, op := range matches {
		proof.Operations = append(proof.Operations, op)
		if !op.Terminal {
			terminal = false
		}
		if op.Terminal && op.State == "Failed" {
			failed = true
		}
		if op.Terminal && op.State != "Failed" && op.State != "Succeeded" && op.State != "Completed" {
			terminal = false
		}
	}
	row.Proof = string(mustJSON(proof))
	if err := e.deps.Effects.RecordEffect(ctx, *row, effectLease(call)); err != nil {
		return false, false, err
	}
	return terminal, failed, nil
}
