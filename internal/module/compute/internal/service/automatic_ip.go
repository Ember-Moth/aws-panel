package service

import (
	"context"
	"encoding/json"
	"github.com/Ember-Moth/aws-panel/internal/module/addresses"
	ad "github.com/Ember-Moth/aws-panel/internal/module/addresses/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/compute/contract"
	oc "github.com/Ember-Moth/aws-panel/internal/module/operations/contract"
	"github.com/aws/aws-sdk-go-v2/aws"
)

func (e *Executor) automaticIP(ctx context.Context, cfg aws.Config, c oc.Call, s contract.Spec, reconcile bool) oc.Result {
	if s.AutomaticIP == nil {
		return attention("AUTOMATIC_IP_POLICY_MISSING", "")
	}
	var receipt *oc.Effect
	for _, step := range c.Completed {
		if step.Kind == "create" {
			r, err := e.deps.Effects.Effect(ctx, step.ID)
			if err != nil {
				return observing(err)
			}
			receipt = r
		}
	}
	if receipt == nil || receipt.State != "confirmed" || receipt.JobID != c.JobID || receipt.AccountID != c.Target.Binding.AccountID || receipt.ResourceID == "" {
		return attention("CREATE_RECEIPT_MISSING", "")
	}
	var created contract.Instance
	if json.Unmarshal([]byte(receipt.Proof), &created) != nil {
		return attention("CREATE_RECEIPT_INVALID", receipt.ResourceID)
	}
	instance, err := e.deps.AddressCloud.Instance(ctx, cfg, s.AutomaticIP.Service, c.Target.Binding.AWSAccountID, receipt.ResourceID)
	if err != nil {
		return observing(err)
	}
	if instance == nil || instance.ARN != created.ARN || instance.ID != created.ID || (created.PrimaryENI != "" && instance.NetworkInterfaceID != created.PrimaryENI) || (created.PrivateIP != "" && instance.PrivateIP != created.PrivateIP) {
		return attention("CREATED_INSTANCE_CHANGED", created.ARN)
	}
	if s.AutomaticIP.Service != "lightsail" && (instance.NetworkInterfaceID == "" || instance.PrivateIP == "") {
		return waiting("INSTANCE_NETWORK_PENDING", created.ARN)
	}
	return addresses.RunAutomatic(ctx, addresses.ExecutorDeps{Clients: e.deps.Clients, Cloud: e.deps.AddressCloud, Effects: e.deps.Effects}, c, "ip_", *s.AutomaticIP, *instance, reconcile)
}
func (e *Executor) planAutomaticIP(ctx context.Context, cfg aws.Config, account string, s *contract.Spec) error {
	kind, border, zone := "ec2", cfg.Region, ""
	if s.WavelengthZone != nil {
		kind = "wavelength"
		border = s.WavelengthZone.BorderGroup
		zone = s.WavelengthZone.ID
	}
	p, err := addresses.PlanAutomatic(ctx, e.deps.AddressCloud, cfg, kind, account, zone, border)
	if err != nil {
		return publicError(err)
	}
	s.AutomaticIP = &p
	if err := automaticNetwork(*s); err != nil {
		return err
	}
	s.Warnings = append(s.Warnings, "创建流程自动分配并绑定固定 IP；地址确认前任务不会标为完成。实例与已分配地址会在失败时保留并显示任务引用。")
	return nil
}
func addressQuota(kind string, q ad.Quota) contract.Quota {
	return contract.Quota{Key: kind, Limit: q.Limit, Used: q.Used, LimitKnown: q.LimitKnown, UsageKnown: q.UsageKnown, Note: q.Note}
}

func automaticNetwork(s contract.Spec) error {
	if s.Subnet == nil || !s.Subnet.PublicIPv4Route {
		return contract.ErrNetwork
	}
	if s.Firewall != nil {
		return nil
	}
	allowed := false
	for _, g := range s.SecurityGroups {
		allowed = allowed || len(g.SSHIPv4) > 0
	}
	if !allowed {
		return contract.ErrNetwork
	}
	return nil
}
func (e *Executor) checkAutomaticIP(ctx context.Context, cfg aws.Config, account string, s *contract.Spec) error {
	if err := automaticNetwork(*s); err != nil {
		return err
	}
	kind := "ec2"
	if s.WavelengthZone != nil {
		kind = "wavelength"
	}
	q, err := e.deps.AddressCloud.Quota(ctx, cfg, kind, account)
	if err != nil {
		return err
	}
	if q.LimitKnown && q.UsageKnown && q.Used+1 > q.Limit {
		return contract.ErrQuota
	}
	return nil
}
