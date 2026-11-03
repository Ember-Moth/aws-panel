package service

import (
	"context"
	"encoding/json"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsnet "github.com/herta-network/aws-panel/internal/infra/aws"
	"github.com/herta-network/aws-panel/internal/module/compute/contract"
	"github.com/herta-network/aws-panel/internal/module/compute/internal/source"
	oc "github.com/herta-network/aws-panel/internal/module/operations/contract"
	"slices"
	"time"
)

var firewallSteps = []string{"firewall_group", "firewall_ingress", "firewall_egress", "firewall_attach"}

func firewallLabel(kind string) string {
	return map[string]string{"firewall_group": "创建实例全放行安全组", "firewall_ingress": "放行所有入站流量", "firewall_egress": "放行所有出站流量", "firewall_attach": "绑定全放行安全组"}[kind]
}
func launchGroups(s contract.Spec) []string {
	if len(s.Target.SecurityGroupIDs) > 0 {
		return s.Target.SecurityGroupIDs
	}
	return s.BootstrapGroups
}
func firewallLabels(c oc.Call, s contract.Spec, n contract.FirewallState) map[string]string {
	v := labels(c)
	v["aws-panel:firewall"] = c.ItemID
	v["aws-panel:primary"] = n.InstanceID
	v["aws-panel:role"] = "instance-open"
	v["aws-panel:account"] = c.Target.Binding.AWSAccountID
	v["Name"] = s.Firewall.Name
	return v
}
func (e *Executor) firewallDependencies(ctx context.Context, c oc.Call, s contract.Spec, instance *contract.Instance) (contract.FirewallState, error) {
	if s.Firewall == nil || instance == nil {
		return contract.FirewallState{}, contract.ErrInvalid
	}
	n := contract.FirewallState{InstanceID: instance.ID, ENI: instance.PrimaryENI, PrivateIP: instance.PrivateIP, VPCID: instance.VPCID, Name: s.Firewall.Name, IPv6: s.Firewall.IPv6}
	found := false
	for _, step := range c.Completed {
		if step.Kind != "create" && step.Kind != "firewall_group" {
			continue
		}
		row, err := e.deps.Effects.Effect(ctx, step.ID)
		if err != nil {
			return n, err
		}
		if row == nil || row.State != "confirmed" || row.JobID != c.JobID || row.AccountID != c.Target.Binding.AccountID || row.Region != c.Target.Binding.Region || row.SpecHash != hash(string(c.Input)) || row.Purpose != OperationID+"."+step.Kind {
			return n, oc.ErrEffect
		}
		if step.Kind == "create" {
			var old contract.Instance
			if json.Unmarshal([]byte(row.Proof), &old) != nil || !stableIdentity(&old, instance) {
				return n, oc.ErrEffect
			}
			n.OriginalGroups = append([]string{}, old.SecurityGroupIDs...)
			found = true
		} else {
			n.GroupID = row.ResourceID
		}
	}
	if !found || len(n.OriginalGroups) == 0 {
		return n, oc.ErrEffect
	}
	return n, nil
}
func (e *Executor) confirmedFirewall(ctx context.Context, cfg aws.Config, c oc.Call, s contract.Spec, instance *contract.Instance) (contract.FirewallState, error) {
	n, err := e.firewallDependencies(ctx, c, s, instance)
	if err != nil {
		return n, err
	}
	found := false
	for _, step := range c.Completed {
		if step.Kind != "firewall_attach" {
			continue
		}
		r, err := e.deps.Effects.Effect(ctx, step.ID)
		if err != nil {
			return n, err
		}
		found = r != nil && r.State == "confirmed" && r.JobID == c.JobID && r.AccountID == c.Target.Binding.AccountID && r.Region == c.Target.Binding.Region && r.SpecHash == hash(string(c.Input)) && r.ResourceID == n.GroupID && r.Purpose == OperationID+".firewall_attach"
	}
	if !found {
		return n, oc.ErrEffect
	}
	cloud, ok := e.deps.Cloud.(source.FirewallCloud)
	if !ok {
		return n, contract.ErrUnavailable
	}
	v, done, err := cloud.ObserveFirewall(ctx, cfg, "firewall_attach", n, firewallLabels(c, s, n))
	if err != nil {
		return n, err
	}
	if !done {
		return n, contract.ErrConflict
	}
	return v, nil
}
func (e *Executor) firewall(ctx context.Context, cfg aws.Config, c oc.Call, s contract.Spec, reconcile bool) oc.Result {
	if s.Firewall == nil {
		return attention("FIREWALL_POLICY_MISSING", "")
	}
	cloud, ok := e.deps.Cloud.(source.FirewallCloud)
	if !ok {
		return before(contract.ErrUnavailable)
	}
	instance, err := e.createdFor(ctx, cfg, c, "create")
	if err != nil {
		return observing(err)
	}
	if instance.State != "running" && instance.State != "stopped" {
		return attention("FIREWALL_INSTANCE_STATE_CHANGED", instance.ARN)
	}
	n, err := e.firewallDependencies(ctx, c, s, instance)
	if err != nil {
		return observing(err)
	}
	if c.Kind != "firewall_group" && n.GroupID == "" {
		return attention("FIREWALL_GROUP_RECEIPT_MISSING", instance.ARN)
	}
	tags := firewallLabels(c, s, n)
	row, err := e.deps.Effects.Effect(ctx, c.StepID)
	if err != nil {
		return observing(err)
	}
	if row != nil && !sameEffect(row, c) {
		return attention("EFFECT_MISMATCH", "")
	}
	current, done, err := cloud.ObserveFirewall(ctx, cfg, c.Kind, n, tags)
	if err != nil {
		return observing(err)
	}
	finish := func(r oc.Effect) oc.Result {
		if current.GroupID == "" || r.ResourceID != "" && r.ResourceID != current.GroupID {
			return attention("FIREWALL_GROUP_CHANGED", r.ResourceID)
		}
		r.State = "confirmed"
		r.ResourceID = current.GroupID
		r.Proof = string(raw(current))
		r.ResourceLocks = []string{"ec2/security-group/" + current.GroupID}
		r.Code = "FIREWALL_STATE_CONFIRMED"
		if err := e.deps.Effects.RecordEffect(ctx, r, lease(c)); err != nil {
			return observing(err)
		}
		return oc.Result{State: oc.Succeeded, Code: r.Code, Output: oc.Output{ResourceID: current.GroupID, Summary: firewallLabel(c.Kind) + "已确认"}}
	}
	if row != nil && (row.State == "dispatched" || row.State == "confirmed") {
		if done {
			return finish(*row)
		}
		if row.State == "confirmed" {
			return attention("CONFIRMED_FIREWALL_CHANGED", row.ResourceID)
		}
		return waiting("FIREWALL_STATE_PENDING", row.ResourceID)
	}
	if reconcile {
		return oc.Result{State: oc.Retry, Code: "FIREWALL_NOT_DISPATCHED", ConfirmedAbsent: true, After: time.Second}
	}
	if c.Kind == "firewall_group" && current.GroupID != "" {
		return attention("FIREWALL_OWNERSHIP_UNCONFIRMED", current.GroupID)
	}
	prepared, err := e.deps.Effects.PrepareEffect(ctx, expected(c), lease(c))
	if err != nil {
		return before(err)
	}
	if prepared.State != "prepared" && prepared.State != "rejected" {
		return waiting("FIREWALL_STATE_PENDING", prepared.ResourceID)
	}
	if done {
		return finish(prepared)
	}
	cfg, err = e.config(ctx, c, true)
	if err != nil {
		return before(err)
	}
	sent, err := e.deps.Effects.DispatchEffect(ctx, c.StepID, lease(c))
	if err != nil {
		return observing(err)
	}
	accepted, err := cloud.ConfigureFirewall(ctx, cfg, c.Kind, n, tags)
	if err != nil {
		info := awsnet.ClassifyError(err)
		if slices.Contains([]string{"AWS_ACCESS_DENIED", "AWS_INVALID_REQUEST", "AWS_QUOTA_EXCEEDED", "AWS_STATE_CONFLICT"}, info.Code) {
			sent.State = "rejected"
			sent.Code = info.Code
			if e.deps.Effects.RecordEffect(ctx, sent, lease(c)) == nil {
				return before(err)
			}
		}
		return waiting("FIREWALL_RESULT_UNCONFIRMED", sent.ResourceID)
	}
	sent.ResourceID = accepted.ResourceID
	sent.RequestID = accepted.RequestID
	sent.Code = "FIREWALL_REQUEST_ACCEPTED"
	if err := e.deps.Effects.RecordEffect(ctx, sent, lease(c)); err != nil {
		return observing(err)
	}
	return waiting(sent.Code, sent.ResourceID)
}
