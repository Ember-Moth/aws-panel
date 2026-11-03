package service

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsnet "github.com/herta-network/aws-panel/internal/infra/aws"
	"github.com/herta-network/aws-panel/internal/infra/clock"
	"github.com/herta-network/aws-panel/internal/module/compute/contract"
	oc "github.com/herta-network/aws-panel/internal/module/operations/contract"
	"slices"
	"time"
)

var networkSteps = []string{"net_vpc", "net_subnet", "net_gateway", "net_attach_gateway", "net_route_table", "net_route_ipv4", "net_route_ipv6", "net_associate_route", "net_security_group", "net_ingress", "net_egress"}

func networkStepLabel(v string) string {
	return map[string]string{"net_vpc": "创建并等待双栈 VPC", "net_subnet": "创建双栈子网", "net_gateway": "创建互联网网关", "net_attach_gateway": "挂载网关", "net_route_table": "创建独立路由表", "net_route_ipv4": "配置 IPv4 默认路由", "net_route_ipv6": "配置 IPv6 默认路由", "net_associate_route": "关联子网路由", "net_security_group": "创建 SSH 安全组", "net_ingress": "配置 SSH 与 ICMPv6 入站规则", "net_egress": "配置双栈出站规则"}[v]
}
func (e *Executor) planNetwork(ctx context.Context, cfg aws.Config, b oc.Target, v contract.Target) (contract.Spec, error) {
	cat, err := e.deps.Cloud.Catalogue(ctx, cfg)
	if err != nil {
		return contract.Spec{}, publicError(err)
	}
	if !slices.Contains(cat.Zones, v.Zone) {
		return contract.Spec{}, contract.ErrNetwork
	}
	qs, err := e.deps.Cloud.NetworkQuotas(ctx, cfg, b.Binding.AWSAccountID)
	if err != nil {
		return contract.Spec{}, publicError(err)
	}
	s := contract.Spec{Target: v, Network: &contract.Network{Zone: v.Zone, IPv4CIDR: "10.42.0.0/16", SubnetIPv4CIDR: "10.42.0.0/24", SSHIPv4CIDR: v.SSHIPv4CIDR, SSHIPv6CIDR: v.SSHIPv6CIDR}, NetworkQuotas: qs, CheckedAt: clock.Now(), Warnings: []string{"新建独立 VPC、双栈子网、互联网网关、路由表和安全组；不会修改已有 VPC。", "SSH 仅按所填 IPv4/IPv6 来源放行，ICMPv6 入站和双栈出站放行；不会创建 NAT 网关或实例。", "失败时保留已创建资源与任务引用，避免误删共享或已投入使用的资源；可在 AWS 控制台按任务标签核对清理。"}}
	for _, q := range qs {
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
	return v
}
func networkID(kind string, n contract.Network) string {
	switch kind {
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
	return slices.Contains([]string{"net_vpc", "net_subnet", "net_gateway", "net_route_table", "net_security_group"}, kind)
}
func (e *Executor) networkDependencies(ctx context.Context, c oc.Call, s contract.Spec) (contract.Network, error) {
	n := *s.Network
	index := slices.Index(networkSteps, c.Kind)
	if index < 0 {
		return n, contract.ErrInvalid
	}
	if index == 0 {
		return n, nil
	}
	previous := networkSteps[index-1]
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
	summary := networkStepLabel(c.Kind) + "已确认"
	if c.Kind == "net_egress" {
		summary = fmt.Sprintf("双栈网络已就绪：VPC %s · 子网 %s · 安全组 %s；可重新加载 EC2 创建选项使用", n.VPCID, n.SubnetID, n.SecurityGroupID)
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
	if c.Kind == "net_vpc" || c.Kind == "net_gateway" {
		qs, err := e.deps.Cloud.NetworkQuotas(ctx, cfg, c.Target.Binding.AWSAccountID)
		if err != nil {
			return before(err)
		}
		key := "vpc.vpcs"
		if c.Kind == "net_gateway" {
			key = "vpc.internet_gateways"
		}
		for _, q := range qs {
			if q.Key == key && q.LimitKnown && q.UsageKnown && q.Used+1 > q.Limit {
				return before(contract.ErrQuota)
			}
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
