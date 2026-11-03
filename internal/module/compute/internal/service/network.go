package service

import (
	"context"
	"encoding/json"
	"fmt"
	awsnet "github.com/Ember-Moth/aws-panel/internal/infra/aws"
	"github.com/Ember-Moth/aws-panel/internal/infra/clock"
	"github.com/Ember-Moth/aws-panel/internal/module/compute/contract"
	oc "github.com/Ember-Moth/aws-panel/internal/module/operations/contract"
	"github.com/aws/aws-sdk-go-v2/aws"
	"slices"
	"strings"
	"time"
)

var networkSteps = []string{"net_vpc", "net_subnet", "net_gateway", "net_attach_gateway", "net_route_table", "net_route_ipv4", "net_route_ipv6", "net_associate_route", "net_security_group", "net_ingress", "net_egress"}

func stepsForSpec(s contract.Spec) []string {
	steps := append([]string{}, stepsForNetwork(s.Target.WavelengthZoneID)...)
	if s.Network != nil && s.Network.ParentZone != "" {
		steps = append(steps, "net_parent_subnet", "net_parent_gateway", "net_parent_attach_gateway", "net_parent_route_table", "net_parent_route_ipv4", "net_parent_associate_route")
	}
	return steps
}
func stepsForNetwork(zone string) []string {
	if zone != "" {
		return []string{"net_vpc", "net_subnet", "net_gateway", "net_route_table", "net_route_ipv4", "net_associate_route", "net_security_group", "net_ingress", "net_egress"}
	}
	return networkSteps
}
func networkStepLabelFor(kind, zone string) string {
	if zone != "" {
		if v := map[string]string{"net_vpc": "创建 IPv4 VPC", "net_subnet": "创建 Wavelength 子网", "net_gateway": "创建 Carrier Gateway", "net_ingress": "放行全部 IPv4 入站", "net_egress": "确认 IPv4 出站规则"}[kind]; v != "" {
			return v
		}
	}
	return networkStepLabel(kind)
}
func networkStepLabel(v string) string {
	if strings.HasPrefix(v, "net_parent_") {
		return "父区域入口 · " + networkStepLabel("net_"+strings.TrimPrefix(v, "net_parent_"))
	}
	return map[string]string{"net_vpc": "创建并等待双栈 VPC", "net_subnet": "创建双栈子网", "net_gateway": "创建互联网网关", "net_attach_gateway": "挂载网关", "net_route_table": "创建独立路由表", "net_route_ipv4": "配置 IPv4 默认路由", "net_route_ipv6": "配置 IPv6 默认路由", "net_associate_route": "关联子网路由", "net_security_group": "创建全放行安全组", "net_ingress": "放行全部入站流量", "net_egress": "配置双栈出站规则"}[v]
}
func (e *Executor) planNetwork(ctx context.Context, cfg aws.Config, b oc.Target, v contract.Target) (contract.Spec, error) {
	cat, err := catalogue(ctx, e.deps.Cloud, cfg, v.WavelengthZoneID)
	if err != nil {
		return contract.Spec{}, publicError(err)
	}
	if cat.WavelengthZone != nil {
		v.Zone = cat.WavelengthZone.Name
	}
	if !slices.Contains(cat.Zones, v.Zone) {
		return contract.Spec{}, contract.ErrNetwork
	}
	qs, err := e.deps.Cloud.NetworkQuotas(ctx, cfg, b.Binding.AWSAccountID)
	if err != nil {
		return contract.Spec{}, publicError(err)
	}
	s := contract.Spec{Target: v, Network: &contract.Network{AllowAll: true, Zone: v.Zone, IPv4CIDR: "10.42.0.0/16", SubnetIPv4CIDR: "10.42.0.0/24", SSHIPv4CIDR: v.SSHIPv4CIDR, SSHIPv6CIDR: v.SSHIPv6CIDR}, NetworkQuotas: qs, CheckedAt: clock.Now(), Warnings: []string{"新建独立 VPC、双栈子网、互联网网关、路由表和安全组；不会修改已有 VPC。", "安全组放行所有 IPv4/IPv6 来源、所有协议和端口的入站与出站；不会创建 NAT 网关或实例。", "失败时保留已创建资源与任务引用，避免误删共享或已投入使用的资源；可在 AWS 控制台按任务标签核对清理。"}}
	if v.WavelengthZoneID != "" {
		s.WavelengthZone = cat.WavelengthZone
		s.Target.Zone = ""
		s.Network.WavelengthZoneID = v.WavelengthZoneID
		if len(cat.ParentZones) == 0 {
			return contract.Spec{}, contract.ErrNetwork
		}
		sortZones := append([]string{}, cat.ParentZones...)
		slices.Sort(sortZones)
		s.Network.ParentZone = sortZones[0]
		s.Warnings = []string{"新建独立 IPv4 VPC、Wavelength 子网及 Carrier Gateway，以及父区域公网子网、互联网网关和独立路由表。", "两类子网使用全放行安全组；实例创建时再分配入口 EIP 和 Carrier IP。", "失败时保留已创建的资源和任务引用，可核对后清理。"}

	}
	for _, q := range s.NetworkQuotas {
		if !q.LimitKnown || !q.UsageKnown {
			s.Warnings = append(s.Warnings, "部分网络配额或用量无法查询，以 AWS 执行结果为准。")
			break
		}
	}
	return s, nil
}
func networkLabels(c oc.Call, s contract.Spec) map[string]string {
	v := labels(c)
	v["aws-panel:network"] = c.ItemID
	v["aws-panel:account"] = c.Target.Binding.AWSAccountID
	v["Name"] = s.Target.Name
	if strings.HasPrefix(c.Kind, "net_parent_") {
		v["aws-panel:network-part"] = "parent"
	}
	if s.Target.WavelengthZoneID != "" {
		v["aws-panel:network-type"] = "wavelength"
		v["aws-panel:zone-id"] = s.Target.WavelengthZoneID
	}
	return v
}
func networkID(kind string, n contract.Network) string {
	switch kind {
	case "net_parent_subnet":
		return n.ParentSubnetID
	case "net_parent_gateway", "net_parent_attach_gateway":
		return n.ParentGatewayID
	case "net_parent_route_table", "net_parent_route_ipv4", "net_parent_associate_route":
		return n.ParentRouteTableID
	case "net_vpc":
		return n.VPCID
	case "net_subnet":
		return n.SubnetID
	case "net_gateway", "net_attach_gateway":
		return n.GatewayID
	case "net_route_table", "net_route_ipv4", "net_route_ipv6", "net_associate_route":
		return n.RouteTableID
	default:
		return n.SecurityGroupID
	}
}
func networkCreates(kind string) bool {
	kind = strings.Replace(kind, "net_parent_", "net_", 1)
	return slices.Contains([]string{"net_vpc", "net_subnet", "net_gateway", "net_route_table", "net_security_group"}, kind)
}
func (e *Executor) networkDependencies(ctx context.Context, c oc.Call, s contract.Spec) (contract.Network, error) {
	n := *s.Network
	steps := stepsForSpec(s)
	index := slices.Index(steps, c.Kind)
	if index < 0 {
		return n, contract.ErrInvalid
	}
	if index == 0 {
		return n, nil
	}
	previous := steps[index-1]
	for _, step := range c.Completed {
		if step.Kind != previous {
			continue
		}
		r, err := e.deps.Effects.Effect(ctx, step.ID)
		if err != nil {
			return n, err
		}
		if r == nil || r.State != "confirmed" || r.JobID != c.JobID || r.AccountID != c.Target.Binding.AccountID || r.Region != c.Target.Binding.Region || r.SpecHash != hash(string(c.Input)) || r.Purpose != OperationID+"."+previous {
			return n, oc.ErrEffect
		}
		if json.Unmarshal([]byte(r.Proof), &n) != nil || n.VPCID == "" {
			return n, oc.ErrEffect
		}
		return n, nil
	}
	return n, oc.ErrEffect
}
func (e *Executor) finishNetwork(ctx context.Context, c oc.Call, row oc.Effect, n contract.Network) oc.Result {
	id := networkID(c.Kind, n)
	if id == "" || row.ResourceID != "" && row.ResourceID != id {
		return attention("NETWORK_RESOURCE_CHANGED", row.ResourceID)
	}
	row.State = "confirmed"
	row.ResourceID = id
	row.Proof = string(raw(n))
	row.Code = "NETWORK_STATE_CONFIRMED"
	row.ResourceLocks = []string{"ec2/network/" + id}
	if err := e.deps.Effects.RecordEffect(ctx, row, lease(c)); err != nil {
		return observing(err)
	}
	summary := networkStepLabelFor(c.Kind, n.WavelengthZoneID) + "已确认"
	if !n.AllowAll {
		if label := map[string]string{"net_security_group": "创建 SSH 安全组", "net_ingress": "配置已预览的 SSH 入站规则"}[c.Kind]; label != "" {
			summary = label + "已确认"
		}
	}

	if c.Kind == "net_egress" && n.ParentZone == "" || c.Kind == "net_parent_associate_route" {
		summary = fmt.Sprintf("网络已就绪：VPC %s · 子网 %s · 安全组 %s；可重新加载 EC2 创建选项使用", n.VPCID, n.SubnetID, n.SecurityGroupID)
		if n.ParentSubnetID != "" {
			summary += " · 父区域入口子网 " + n.ParentSubnetID
		}
	}
	return oc.Result{State: oc.Succeeded, Code: "NETWORK_STATE_CONFIRMED", Output: oc.Output{ResourceID: id, RequestID: row.RequestID, Summary: summary}}
}
func (e *Executor) network(ctx context.Context, cfg aws.Config, c oc.Call, s contract.Spec, reconcile bool) oc.Result {
	dependencies, err := e.networkDependencies(ctx, c, s)
	if err != nil {
		return observing(err)
	}
	row, err := e.deps.Effects.Effect(ctx, c.StepID)
	if err != nil {
		return observing(err)
	}
	if row != nil && !sameEffect(row, c) {
		return attention("EFFECT_MISMATCH", "")
	}
	n, done, err := e.deps.Cloud.Network(ctx, cfg, c.Kind, dependencies, networkLabels(c, s))
	if err != nil {
		if row == nil || row.State == "prepared" || row.State == "rejected" {
			return before(err)
		}
		return observing(err)
	}
	sent := row != nil && (row.State == "dispatched" || row.State == "confirmed")
	if sent {
		if done {
			return e.finishNetwork(ctx, c, *row, n)
		}
		if row.State == "confirmed" {
			return attention("CONFIRMED_NETWORK_CHANGED", row.ResourceID)
		}
		return waiting("NETWORK_STATE_PENDING", row.ResourceID)
	}
	if reconcile {
		return oc.Result{State: oc.Retry, Code: "NETWORK_NOT_DISPATCHED", ConfirmedAbsent: true, After: time.Second}
	}
	if networkCreates(c.Kind) && networkID(c.Kind, n) != "" {
		return attention("NETWORK_OWNERSHIP_UNCONFIRMED", networkID(c.Kind, n))
	}
	if c.Kind == "net_vpc" || c.Kind == "net_gateway" || c.Kind == "net_parent_gateway" {
		qs, err := e.deps.Cloud.NetworkQuotas(ctx, cfg, c.Target.Binding.AWSAccountID)
		if err != nil {
			return before(err)
		}
		key := "vpc.vpcs"
		if c.Kind == "net_gateway" || c.Kind == "net_parent_gateway" {
			key = "vpc.internet_gateways"
			if c.Kind == "net_gateway" && s.Target.WavelengthZoneID != "" {
				key = "vpc.carrier_gateways"
			}
		}

		for _, q := range qs {
			if q.Key == key && q.LimitKnown && q.UsageKnown && q.Used+1 > q.Limit {
				return before(contract.ErrQuota)
			}
		}
	}
	if s.Target.WavelengthZoneID != "" {
		zs, err := wavelengthZones(ctx, e.deps.Cloud, cfg)
		if err != nil {
			return before(err)
		}
		valid := false
		for _, z := range zs {
			valid = valid || (z.ID == s.Target.WavelengthZoneID && z.Name == s.Network.Zone && z.OptInStatus == "opted-in" && z.State == "available")
		}
		if !valid {
			return before(contract.ErrNetwork)
		}
	}
	prepared, err := e.deps.Effects.PrepareEffect(ctx, expected(c), lease(c))
	if err != nil {
		return before(err)
	}
	if prepared.State != "prepared" && prepared.State != "rejected" {
		return waiting("NETWORK_STATE_PENDING", prepared.ResourceID)
	}
	if done {
		return e.finishNetwork(ctx, c, prepared, n)
	}
	cfg, err = e.config(ctx, c, true)
	if err != nil {
		return before(err)
	}
	dispatched, err := e.deps.Effects.DispatchEffect(ctx, c.StepID, lease(c))
	if err != nil {
		return observing(err)
	}
	accepted, err := e.deps.Cloud.Provision(ctx, cfg, c.Kind, n, networkLabels(c, s))
	if err != nil {
		info := awsnet.ClassifyError(err)
		if slices.Contains([]string{"AWS_ACCESS_DENIED", "AWS_INVALID_REQUEST", "AWS_QUOTA_EXCEEDED", "AWS_STATE_CONFLICT"}, info.Code) {
			dispatched.State = "rejected"
			dispatched.Code = info.Code
			dispatched.RequestID = info.RequestID
			if err := e.deps.Effects.RecordEffect(ctx, dispatched, lease(c)); err == nil {
				return oc.Result{State: oc.Failed, Code: info.Code, ConfirmedAbsent: true, Output: oc.Output{Summary: "AWS 拒绝网络步骤，已创建的资源和任务记录保留"}}
			}
		}
		return waiting("NETWORK_RESULT_UNCONFIRMED", networkID(c.Kind, n))
	}
	dispatched.ResourceID = accepted.ResourceID
	dispatched.RequestID = accepted.RequestID
	dispatched.Code = "NETWORK_REQUEST_ACCEPTED"
	dispatched.Proof = string(raw(n))
	if err := e.deps.Effects.RecordEffect(ctx, dispatched, lease(c)); err != nil {
		return waiting("NETWORK_RECEIPT_UNCONFIRMED", accepted.ResourceID)
	}
	return waiting("NETWORK_REQUEST_ACCEPTED", accepted.ResourceID)
}
