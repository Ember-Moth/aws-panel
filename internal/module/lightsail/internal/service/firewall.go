package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	awsnet "github.com/Ember-Moth/aws-panel/internal/infra/aws"
	"github.com/Ember-Moth/aws-panel/internal/module/lightsail/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/lightsail/internal/source"
	oc "github.com/Ember-Moth/aws-panel/internal/module/operations/contract"
	"slices"
	"time"
)

type firewallProof struct {
	Operations []source.Operation `json:"operations,omitempty"`
}

func (e *Executor) firewall(ctx context.Context, c oc.Call, reconcile bool) oc.Result {
	s, err := decodeSpec(c)
	if err != nil {
		return noSend(err)
	}
	if !s.AllowAllFirewall || e.deps.Effects == nil {
		return noSend(contract.ErrInvalid)
	}
	cloud, ok := e.deps.Cloud.(source.FirewallCloud)
	if !ok {
		return noSend(contract.ErrUnavailable)
	}
	cfg, err := e.config(ctx, c, false)
	if err != nil {
		return observeFailure(err)
	}
	createID := ""
	for _, step := range c.Completed {
		if step.Kind == "create" {
			createID = step.ID
		}
	}
	if createID == "" {
		return oc.Result{State: oc.Attention, Code: "CREATE_REFERENCE_MISSING"}
	}
	receipt, err := e.deps.Store.Creation(ctx, createID)
	if err != nil {
		return observeFailure(err)
	}
	if receipt == nil || receipt.State != "identified" || receipt.JobID != c.JobID || receipt.AccountID != c.Target.Binding.AccountID || receipt.ResourceARN == "" {
		return oc.Result{State: oc.Attention, Code: "CREATE_REFERENCE_MISSING"}
	}
	instance, err := e.deps.Cloud.Instance(ctx, cfg, s.Target.Name)
	if err != nil {
		return observeFailure(err)
	}
	if instance == nil || instance.ARN != receipt.ResourceARN || !owned(*instance, s, *receipt, c.Target.Binding.AWSAccountID) || instance.State != "running" {
		return oc.Result{State: oc.Attention, Code: "FIREWALL_INSTANCE_CHANGED"}
	}
	sum := sha256.Sum256(c.Input)
	expected := oc.Effect{StepID: c.StepID, JobID: c.JobID, AccountID: c.Target.Binding.AccountID, Region: c.Target.Binding.Region, Purpose: "lightsail.firewall", SpecHash: hex.EncodeToString(sum[:]), Token: c.Token}
	lease := oc.EffectLease{StepID: c.StepID, Owner: c.LeaseOwner, Epoch: c.LeaseEpoch}
	row, err := e.deps.Effects.Effect(ctx, c.StepID)
	if err != nil {
		return observeFailure(err)
	}
	if row != nil && (row.JobID != expected.JobID || row.AccountID != expected.AccountID || row.Region != expected.Region || row.SpecHash != expected.SpecHash || row.Token != expected.Token || row.Purpose != expected.Purpose) {
		return oc.Result{State: oc.Attention, Code: "EFFECT_MISMATCH"}
	}
	open, err := cloud.FirewallOpen(ctx, cfg, s.Target.Name, s.Target.IPType)
	if err != nil {
		return observeFailure(err)
	}
	finish := func(r oc.Effect) oc.Result {
		r.State = "confirmed"
		r.ResourceID = instance.ARN
		r.Code = "FIREWALL_STATE_CONFIRMED"
		if err := e.deps.Effects.RecordEffect(ctx, r, lease); err != nil {
			return observeFailure(err)
		}
		return oc.Result{State: oc.Succeeded, Code: r.Code, Output: oc.Output{ResourceID: instance.ARN, Summary: "全部公网入站已放行；Lightsail 云端出站默认全部允许"}}
	}
	wait := func(code string) oc.Result {
		return oc.Result{State: oc.Waiting, Code: code, After: 10 * time.Second, Output: oc.Output{ResourceID: instance.ARN, Summary: "正在核对防火墙规则与 AWS 操作结果"}}
	}
	if row != nil && (row.State == "dispatched" || row.State == "confirmed") {
		if row.State == "confirmed" {
			if open {
				return finish(*row)
			}
			return oc.Result{State: oc.Attention, Code: "CONFIRMED_FIREWALL_CHANGED"}
		}
		var proof firewallProof
		if row.Proof != "" && json.Unmarshal([]byte(row.Proof), &proof) != nil {
			return oc.Result{State: oc.Attention, Code: "FIREWALL_RECEIPT_INVALID"}
		}
		history, err := e.deps.Cloud.Operations(ctx, cfg, s.Target.Name)
		if err != nil {
			return observeFailure(err)
		}
		ids := map[string]bool{}
		for _, op := range proof.Operations {
			ids[op.ID] = true
		}
		matches := []source.Operation{}
		for _, op := range history {
			if op.Name != s.Target.Name || op.Region != s.Target.Region || op.Type != "PutInstancePublicPorts" || op.ID == "" || row.FirstSentAt == nil || op.CreatedAt.Before(row.FirstSentAt.Add(-30*time.Second)) || op.CreatedAt.After(time.Now().Add(30*time.Second)) || len(ids) > 0 && !ids[op.ID] {
				continue
			}
			matches = append(matches, op)
		}
		if len(matches) > 1 {
			return oc.Result{State: oc.Attention, Code: "FIREWALL_HISTORY_AMBIGUOUS"}
		}
		if len(matches) == 0 {
			return wait("FIREWALL_OPERATION_NOT_VISIBLE")
		}
		op := matches[0]
		row.Proof = encode(firewallProof{Operations: matches})
		if err := e.deps.Effects.RecordEffect(ctx, *row, lease); err != nil {
			return observeFailure(err)
		}
		if op.Terminal && op.State != "Succeeded" && op.State != "Completed" {
			return oc.Result{State: oc.Failed, Code: "FIREWALL_OPERATION_FAILED", Output: oc.Output{ResourceID: instance.ARN}}
		}
		if !op.Terminal || !open {
			return wait("FIREWALL_STATE_PENDING")
		}
		return finish(*row)
	}
	if reconcile {
		return oc.Result{State: oc.Retry, Code: "FIREWALL_NOT_DISPATCHED", ConfirmedAbsent: true, After: time.Second}
	}
	prepared, err := e.deps.Effects.PrepareEffect(ctx, expected, lease)
	if err != nil {
		return noSend(err)
	}
	if prepared.State != "prepared" && prepared.State != "rejected" {
		return wait("FIREWALL_STATE_PENDING")
	}
	if open {
		return finish(prepared)
	}
	cfg, err = e.config(ctx, c, true)
	if err != nil {
		return noSend(err)
	}
	sent, err := e.deps.Effects.DispatchEffect(ctx, c.StepID, lease)
	if err != nil {
		return observeFailure(err)
	}
	accepted, err := cloud.OpenFirewall(ctx, cfg, s.Target.Name, s.Target.IPType)
	if err != nil {
		info := awsnet.ClassifyError(err)
		if slices.Contains([]string{"AWS_ACCESS_DENIED", "AWS_INVALID_REQUEST", "AWS_QUOTA_EXCEEDED", "AWS_STATE_CONFLICT"}, info.Code) {
			sent.State = "rejected"
			sent.Code = info.Code
			if e.deps.Effects.RecordEffect(ctx, sent, lease) == nil {
				return noSend(err)
			}
		}
		return wait("FIREWALL_RESULT_UNCONFIRMED")
	}
	for _, op := range accepted.Operations {
		if op.ID == "" || op.Name != s.Target.Name || op.Region != s.Target.Region || op.Type != "PutInstancePublicPorts" {
			return wait("FIREWALL_RESPONSE_INVALID")
		}
	}
	sent.RequestID = accepted.RequestID
	sent.ResourceID = instance.ARN
	sent.Proof = encode(firewallProof{Operations: accepted.Operations})
	sent.Code = "FIREWALL_REQUEST_ACCEPTED"
	if err := e.deps.Effects.RecordEffect(ctx, sent, lease); err != nil {
		return observeFailure(err)
	}
	return wait(sent.Code)
}
