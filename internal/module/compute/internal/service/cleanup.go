package service

import (
	"context"
	"github.com/Ember-Moth/aws-panel/internal/module/addresses"
	"github.com/Ember-Moth/aws-panel/internal/module/compute/contract"
	oc "github.com/Ember-Moth/aws-panel/internal/module/operations/contract"
	"github.com/aws/aws-sdk-go-v2/aws"
	"strconv"
	"strings"
)

func (e *Executor) planCleanup(ctx context.Context, cfg aws.Config, b oc.Target, s *contract.Spec) ([]string, error) {
	keys := []string{}
	instances := []contract.Instance{*s.Instance}
	rows, err := e.deps.Cloud.Instances(ctx, cfg, b.Binding.AWSAccountID)
	if err != nil {
		return nil, publicError(err)
	}
	rows, err = pairInstances(ctx, e.deps.Effects, b.Binding.AccountID, b.Binding.Region, rows)
	if err != nil {
		return nil, publicError(err)
	}
	for _, v := range rows {
		if v.ID == s.Instance.ID && v.Ingress != nil {
			for _, peer := range rows {
				if peer.ID == v.Ingress.ID {
					copy := peer
					s.CompanionInstance = &copy
					instances = append(instances, peer)
					keys = append(keys, addresses.InstanceLocks("ec2", peer.ID, peer.Name)...)
					s.Warnings = append(s.Warnings, "将同时终止入口实例 "+peer.ID+"；入口专用安全组与 VPC 网络仍保留。")
				}
			}
		}
	}
	for _, v := range instances {
		kind := "ec2"
		if strings.Contains(v.Zone, "-wl") || v.CarrierIP != "" {
			kind = "wavelength"
		}
		cat, err := e.deps.AddressCloud.Catalogue(ctx, cfg, kind, b.Binding.AWSAccountID)
		if err != nil {
			return nil, publicError(err)
		}
		for _, a := range cat.Addresses {
			if a.InstanceID != v.ID {
				continue
			}
			owned, err := addresses.OwnedAllocation(ctx, e.deps.Effects, b.Binding.AccountID, b.Binding.Region, a)
			if err != nil {
				return nil, publicError(err)
			}
			if owned != nil {
				s.CleanupIPs = append(s.CleanupIPs, *owned)
				keys = append(keys, addresses.AddressLocks(kind, a)...)
			} else {
				s.Warnings = append(s.Warnings, "地址 "+a.IP+" 的面板创建归属未确认，将保留。")
			}
		}
	}
	if len(s.CleanupIPs) > 12 {
		return nil, contract.ErrInvalid
	}
	return keys, nil
}
func (e *Executor) cleanup(ctx context.Context, cfg aws.Config, c oc.Call, s contract.Spec, reconcile bool) oc.Result {
	// A cleanup step can run only after the exact primary instance is gone.
	primary, err := e.deps.Cloud.Instance(ctx, cfg, c.Target.Binding.AWSAccountID, s.Instance.ID)
	if err != nil {
		return observing(err)
	}
	if primary != nil && (!stableIdentity(primary, s.Instance) || primary.State != "terminated") {
		return attention("PRIMARY_TERMINATION_UNCONFIRMED", s.Instance.ARN)
	}
	if c.Kind == "companion_terminate" {
		peer := s.CompanionInstance
		if peer == nil {
			return attention("COMPANION_REFERENCE_MISSING", "")
		}
		derived := contract.Spec{Target: contract.Target{ConnectionID: c.Target.Binding.ConnectionID, Region: c.Target.Binding.Region, Action: "terminate", InstanceID: peer.ID}, Instance: peer, CheckedAt: s.CheckedAt}
		c.Kind = "terminate"
		c.Input = raw(derived)
		c.Completed = nil
		if reconcile {
			return e.Reconcile(ctx, c)
		}
		return e.Execute(ctx, c)
	}
	i, err := strconv.Atoi(strings.TrimPrefix(c.Kind, "cleanup_ip_"))
	if err != nil || i < 0 || i >= len(s.CleanupIPs) {
		return attention("INVALID_CLEANUP_STEP", "")
	}
	ip := s.CleanupIPs[i]
	if s.CompanionInstance != nil && ip.InstanceID == s.CompanionInstance.ID {
		peer, err := e.deps.Cloud.Instance(ctx, cfg, c.Target.Binding.AWSAccountID, ip.InstanceID)
		if err != nil {
			return observing(err)
		}
		if peer != nil && (!stableIdentity(peer, s.CompanionInstance) || peer.State != "terminated") {
			return attention("COMPANION_TERMINATION_UNCONFIRMED", s.CompanionInstance.ARN)
		}
	}
	live, err := e.deps.AddressCloud.Address(ctx, cfg, ip.Service, c.Target.Binding.AWSAccountID, ip.Address.ID)
	if err != nil {
		return observing(err)
	}
	if live != nil && live.Attached {
		if live.InstanceID == ip.InstanceID || live.NetworkInterfaceID == ip.Address.NetworkInterfaceID {
			return waiting("TERMINATED_INSTANCE_IP_DETACH_PENDING", live.ARN)
		}
		return attention("MANAGED_IP_REASSIGNED", live.ARN)
	}
	return addresses.ReleaseOwned(ctx, addresses.ExecutorDeps{Clients: e.deps.Clients, Cloud: e.deps.AddressCloud, Effects: e.deps.Effects}, c, ip, reconcile)
}
