package service

import (
	"context"
	"encoding/json"
	"fmt"
	awsnet "github.com/Ember-Moth/aws-panel/internal/infra/aws"
	"github.com/Ember-Moth/aws-panel/internal/module/compute/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/compute/internal/source"
	oc "github.com/Ember-Moth/aws-panel/internal/module/operations/contract"
	"github.com/Ember-Moth/aws-panel/internal/repository"
	"github.com/aws/aws-sdk-go-v2/aws"
	"net/netip"
	"slices"
	"strings"
	"time"
)

var ingressSteps = []string{"ingress_create", "ingress_ready", "ingress_ip_allocate", "ingress_ip_attach", "relay_forwarding", "relay_group", "relay_ingress", "relay_egress", "relay_attach"}

func ingressStepsFor(s contract.Spec) []string {
	if s.Firewall != nil {
		return ingressSteps[:5]
	}
	return ingressSteps
}

func (e *Executor) planIngress(ctx context.Context, cfg aws.Config, account string, cat contract.Catalogue, s *contract.Spec) error {
	if _, ok := e.deps.Cloud.(source.IngressCloud); !ok {
		return contract.ErrUnavailable
	}
	if s.Subnet == nil || (s.Firewall == nil && len(s.Target.SecurityGroupIDs) > 4) {
		return contract.ErrNetwork
	}
	parent := contract.Catalogue{Images: cat.Images, Types: cat.ParentTypes, Subnets: cat.ParentSubnets, SecurityGroups: cat.SecurityGroups, KeyPairs: cat.KeyPairs, Zones: cat.ParentZones, CheckedAt: cat.CheckedAt}
	target := s.Target
	target.WavelengthZoneID = ""
	target.IngressSubnetID = ""
	target.IngressType = ""
	target.IPv6 = false
	target.PublicIPv4 = false
	target.SubnetID = s.Target.IngressSubnetID
	target.InstanceType = s.Target.IngressType
	// Deterministic defaults are resolved into the immutable preview, never at dispatch.
	if target.SubnetID == "" {
		for _, v := range cat.ParentSubnets {
			if v.VPCID == s.Subnet.VPCID && v.PublicIPv4Route && v.AvailableIPs > 0 {
				target.SubnetID = v.ID
				break
			}
		}
	}
	if target.InstanceType == "" {
		for _, wanted := range []string{"t3.nano", "t4g.nano", "t3.micro", "t4g.micro"} {
			for _, v := range cat.ParentTypes {
				if v.ID == wanted && slices.Contains(v.Architectures, s.Image.Architecture) {
					target.InstanceType = v.ID
					break
				}
			}
			if target.InstanceType != "" {
				break
			}
		}
	}
	name := s.Target.Name
	if len(name) > 40 {
		name = name[:40]
	}
	target.Name = name + "-ingress-" + hash(s.Target.Name)[:8]
	target.DiskGB = max(int32(8), s.Image.MinDiskGB)
	peer, err := selectCreateWithFirewall(target, parent, s.Firewall)
	if err != nil {
		return err
	}
	if peer.Subnet.VPCID != s.Subnet.VPCID {
		return contract.ErrNetwork
	}
	if err = e.planAutomaticIP(ctx, cfg, account, &peer); err != nil {
		return err
	}
	peer.Quota, err = e.deps.Cloud.Quota(ctx, cfg, account, target.InstanceType)
	if err != nil {
		return publicError(err)
	}
	s.Ingress = &contract.IngressPlan{Target: target, Image: *peer.Image, Type: *peer.Type, Subnet: *peer.Subnet, SecurityGroups: peer.SecurityGroups, AutomaticIP: *peer.AutomaticIP, Quota: peer.Quota}
	s.Warnings = append(s.Warnings, "本次创建 2 台 EC2、2 块根盘、1 个弹性 IP 和 1 个 Carrier IP；父区域入口及两者间流量另行计费。入口按实例的全放行安全组转发 IPv4 入站，业务实例看到的入站源地址为入口私网 IP。", "入口系统初始化会安装转发服务；任务核对 AWS 资源配置，实际转发和 SSH 连通性需启动后验证。")
	return nil
}
func (e *Executor) checkIngressPlan(ctx context.Context, cfg aws.Config, account string, cat contract.Catalogue, s contract.Spec) error {
	fresh := s
	fresh.Target.IngressSubnetID = s.Ingress.Subnet.ID
	fresh.Target.IngressType = s.Ingress.Type.ID
	if err := e.planIngress(ctx, cfg, account, cat, &fresh); err != nil {
		return err
	}
	if fresh.Ingress.Subnet.VPCID != s.Ingress.Subnet.VPCID || fresh.Ingress.Subnet.Zone != s.Ingress.Subnet.Zone || fresh.Ingress.Type.VCPU != s.Ingress.Type.VCPU {
		return contract.ErrCatalogue
	}
	q := fresh.Ingress.Quota
	count := float64(fresh.Ingress.Type.VCPU)
	if q.Key == s.Quota.Key {
		count += float64(s.Type.VCPU)
	}
	if q.LimitKnown && q.UsageKnown && q.Used+count > q.Limit {
		return contract.ErrQuota
	}
	return nil
}
func (e *Executor) createdFor(ctx context.Context, cfg aws.Config, c oc.Call, kind string) (*contract.Instance, error) {
	for _, step := range c.Completed {
		if step.Kind != kind {
			continue
		}
		r, err := e.deps.Effects.Effect(ctx, step.ID)
		if err != nil {
			return nil, err
		}
		if r == nil || r.State != "confirmed" || r.JobID != c.JobID || r.AccountID != c.Target.Binding.AccountID || r.Region != c.Target.Binding.Region || r.ResourceID == "" || r.Purpose != OperationID+".create" {
			return nil, oc.ErrEffect
		}
		var old contract.Instance
		if json.Unmarshal([]byte(r.Proof), &old) != nil {
			return nil, oc.ErrEffect
		}
		v, err := e.deps.Cloud.Instance(ctx, cfg, c.Target.Binding.AWSAccountID, r.ResourceID)
		if err != nil {
			return nil, err
		}
		if !stableIdentity(v, &old) || v.SubnetID != old.SubnetID || v.PrivateIP == "" || v.PrimaryENI == "" || (old.PrivateIP != "" && v.PrivateIP != old.PrivateIP) || (old.PrimaryENI != "" && v.PrimaryENI != old.PrimaryENI) {
			return nil, contract.ErrConflict
		}
		return v, nil
	}
	return nil, oc.ErrEffect
}
func (e *Executor) ingress(ctx context.Context, cfg aws.Config, c oc.Call, s contract.Spec, reconcile bool) oc.Result {
	if s.Ingress == nil {
		return attention("INGRESS_PLAN_MISSING", "")
	}
	primary, err := e.createdFor(ctx, cfg, c, "create")
	if err != nil {
		return observing(err)
	}
	if primary.State != "running" {
		return attention("PRIMARY_NOT_RUNNING", primary.ARN)
	}
	if strings.HasPrefix(c.Kind, "ingress_") {
		p := *s.Ingress
		if s.Firewall != nil {
			state, err := e.confirmedFirewall(ctx, cfg, c, s, primary)
			if err != nil {
				return observing(err)
			}
			p.Target.SecurityGroupIDs = []string{state.GroupID}
		}
		derived := contract.Spec{Target: p.Target, Image: &p.Image, Type: &p.Type, Subnet: &p.Subnet, SecurityGroups: p.SecurityGroups, AutomaticIP: &p.AutomaticIP, Quota: p.Quota, CheckedAt: s.CheckedAt, IngressPeer: &contract.IngressPeer{ID: primary.ID, PrivateIP: primary.PrivateIP}}
		translated := c
		translated.Input = raw(derived)
		translated.Kind = strings.TrimPrefix(c.Kind, "ingress_")
		translated.Completed = nil
		for _, step := range c.Completed {
			if strings.HasPrefix(step.Kind, "ingress_") {
				step.Kind = strings.TrimPrefix(step.Kind, "ingress_")
				translated.Completed = append(translated.Completed, step)
			}
		}
		if reconcile {
			return e.Reconcile(ctx, translated)
		}
		return e.Execute(ctx, translated)
	}
	gateway, err := e.createdFor(ctx, cfg, c, "ingress_create")
	if err != nil {
		return observing(err)
	}
	if gateway.State != "running" || gateway.VPCID != primary.VPCID || gateway.SubnetID != s.Ingress.Subnet.ID {
		return attention("INGRESS_INSTANCE_CHANGED", gateway.ARN)
	}
	state := contract.IngressState{PrimaryID: primary.ID, PrimaryENI: primary.PrimaryENI, PrimaryPrivateIP: primary.PrivateIP, GatewayID: gateway.ID, GatewayPrivateIP: gateway.PrivateIP, VPCID: primary.VPCID, SecurityGroupIDs: append([]string{}, s.Target.SecurityGroupIDs...)}
	if c.Kind == "relay_ingress" || c.Kind == "relay_egress" || c.Kind == "relay_attach" {
		for _, step := range c.Completed {
			if step.Kind != "relay_group" {
				continue
			}
			r, err := e.deps.Effects.Effect(ctx, step.ID)
			if err != nil {
				return observing(err)
			}
			if r == nil || r.State != "confirmed" || r.JobID != c.JobID || r.SpecHash != hash(string(c.Input)) || r.Purpose != OperationID+".relay_group" {
				return attention("TRANSIT_GROUP_RECEIPT_MISSING", "")
			}
			state.GroupID = r.ResourceID
		}
		if state.GroupID == "" {
			return attention("TRANSIT_GROUP_RECEIPT_MISSING", "")
		}
	}
	cloud, ok := e.deps.Cloud.(source.IngressCloud)
	if !ok {
		return before(contract.ErrUnavailable)
	}
	tags := labels(c)
	tags["aws-panel:primary"] = primary.ID
	tags["aws-panel:role"] = "ingress-transit"
	tags["aws-panel:bundle"] = c.ItemID
	tags["aws-panel:account"] = c.Target.Binding.AWSAccountID
	row, err := e.deps.Effects.Effect(ctx, c.StepID)
	if err != nil {
		return observing(err)
	}
	if row != nil && !sameEffect(row, c) {
		return attention("EFFECT_MISMATCH", "")
	}
	observed, done, err := cloud.ObserveIngress(ctx, cfg, c.Kind, state, tags)
	if err != nil {
		return observing(err)
	}
	finish := func(r oc.Effect) oc.Result {
		r.State = "confirmed"
		r.Proof = string(raw(observed))
		r.ResourceID = gateway.ID
		if c.Kind != "relay_forwarding" {
			r.ResourceID = observed.GroupID
		}
		r.Code = "INGRESS_CONFIG_CONFIRMED"
		if err := e.deps.Effects.RecordEffect(ctx, r, lease(c)); err != nil {
			return observing(err)
		}
		summary := "入口转发配置已确认"
		if c.Kind == "relay_attach" {
			summary = "入口 EC2 与 EIP、Carrier IP 和转发安全组已配置；系统初始化与实际转发连通性待验证"
		}
		return oc.Result{State: oc.Succeeded, Code: r.Code, Output: oc.Output{ResourceID: r.ResourceID, Summary: summary}}
	}
	if row != nil && (row.State == "dispatched" || row.State == "confirmed") {
		if done {
			return finish(*row)
		}
		if row.State == "confirmed" {
			return attention("INGRESS_CONFIG_CHANGED", row.ResourceID)
		}
		return waiting("INGRESS_CONFIG_PENDING", row.ResourceID)
	}
	if reconcile {
		return oc.Result{State: oc.Retry, Code: "INGRESS_NOT_DISPATCHED", ConfirmedAbsent: true, After: time.Second}
	}
	if c.Kind == "relay_group" && observed.GroupID != "" {
		return attention("TRANSIT_GROUP_OWNERSHIP_UNCONFIRMED", observed.GroupID)
	}
	prepared, err := e.deps.Effects.PrepareEffect(ctx, expected(c), lease(c))
	if err != nil {
		return before(err)
	}
	if prepared.State != "prepared" && prepared.State != "rejected" {
		return waiting("INGRESS_CONFIG_PENDING", prepared.ResourceID)
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
	accepted, err := cloud.ConfigureIngress(ctx, cfg, c.Kind, state, tags)
	if err != nil {
		info := awsnet.ClassifyError(err)
		if slices.Contains([]string{"AWS_ACCESS_DENIED", "AWS_INVALID_REQUEST", "AWS_QUOTA_EXCEEDED", "AWS_STATE_CONFLICT"}, info.Code) {
			sent.State = "rejected"
			sent.Code = info.Code
			if e.deps.Effects.RecordEffect(ctx, sent, lease(c)) == nil {
				return before(err)
			}
		}
		return waiting("INGRESS_RESULT_UNCONFIRMED", "")
	}
	sent.ResourceID = accepted.ResourceID
	sent.RequestID = accepted.RequestID
	sent.Proof = string(raw(state))
	sent.Code = "INGRESS_REQUEST_ACCEPTED"
	if err = e.deps.Effects.RecordEffect(ctx, sent, lease(c)); err != nil {
		return observing(err)
	}
	return waiting(sent.Code, sent.ResourceID)
}

func ingressUserData(target string) (string, error) {
	ip, err := netip.ParseAddr(target)
	if err != nil || !ip.Is4() || !ip.IsPrivate() {
		return "", contract.ErrNetwork
	}
	return fmt.Sprintf(`#!/bin/bash
set -eu
install -d -m 0755 /usr/local/libexec
cat >/usr/local/libexec/aws-panel-ingress <<'SCRIPT'
#!/bin/bash
set -euo pipefail
if ! command -v nft >/dev/null; then
 if command -v apt-get >/dev/null; then export DEBIAN_FRONTEND=noninteractive; apt-get -o Acquire::Retries=3 update; apt-get -o Acquire::Retries=3 install -y nftables; else dnf -y install nftables; fi
fi
relay_ip=$(ip -4 route get %s | awk '{for(i=1;i<=NF;i++) if($i=="src") {print $(i+1); exit}}')
[[ "$relay_ip" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]
printf 'net.ipv4.ip_forward=1\nnet.ipv4.conf.all.rp_filter=0\nnet.ipv4.conf.default.rp_filter=0\n' >/etc/sysctl.d/90-aws-panel-ingress.conf
sysctl --system
if nft list table ip aws_panel_ingress >/dev/null 2>&1; then nft delete table ip aws_panel_ingress; fi
nft -f - <<NFT
table ip aws_panel_ingress {
 chain incoming { type nat hook prerouting priority dstnat; policy accept; ip daddr $relay_ip dnat to %s; }
 chain outgoing { type nat hook postrouting priority srcnat; policy accept; ip daddr %s snat to $relay_ip; }
}
NFT
SCRIPT
chmod 0755 /usr/local/libexec/aws-panel-ingress
cat >/etc/systemd/system/aws-panel-ingress.service <<'UNIT'
[Unit]
Description=AWS Panel Wavelength ingress forwarding
Wants=network-online.target
After=network-online.target
StartLimitIntervalSec=0
[Service]
Type=oneshot
ExecStart=/usr/local/libexec/aws-panel-ingress
RemainAfterExit=yes
Restart=on-failure
RestartSec=15
[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable --now --no-block aws-panel-ingress.service
`, ip.String(), ip.String(), ip.String()), nil
}

func creationOwned(ctx context.Context, store repository.EffectStore, account, region string, v contract.Instance) (bool, error) {
	step := v.Tags["aws-panel:step"]
	if step == "" || v.Tags["aws-panel:token"] == "" || v.Tags["aws-panel:spec"] == "" {
		return false, nil
	}
	r, err := store.Effect(ctx, step)
	if err != nil {
		return false, err
	}
	if r == nil || r.State != "confirmed" || r.AccountID != account || r.Region != region || r.Purpose != OperationID+".create" || r.ResourceID != v.ID || r.SpecHash != v.Tags["aws-panel:spec"] || r.Token != v.Tags["aws-panel:token"] {
		return false, nil
	}
	var old contract.Instance
	if json.Unmarshal([]byte(r.Proof), &old) != nil {
		return false, nil
	}
	if v.Tags["aws-panel:role"] == "ingress" && (old.IngressFor == "" || old.IngressFor != v.Tags["aws-panel:primary"]) {
		return false, nil
	}
	return stableIdentity(&old, &v), nil
}
func pairInstances(ctx context.Context, store repository.EffectStore, account, region string, rows []contract.Instance) ([]contract.Instance, error) {
	indices := map[string]int{}
	for i, v := range rows {
		indices[v.ID] = i
	}
	for i, v := range rows {
		if v.Tags["aws-panel:role"] != "ingress" {
			continue
		}
		index, ok := indices[v.Tags["aws-panel:primary"]]
		if !ok || rows[index].Tags["aws-panel:bundle"] != v.Tags["aws-panel:bundle"] || rows[index].VPCID != v.VPCID {
			continue
		}
		owned, err := creationOwned(ctx, store, account, region, v)
		if err != nil {
			return nil, err
		}
		if !owned {
			continue
		}
		owned, err = creationOwned(ctx, store, account, region, rows[index])
		if err != nil {
			return nil, err
		}
		if !owned {
			continue
		}
		if rows[index].Ingress != nil {
			return nil, contract.ErrConflict
		}
		rows[index].Ingress = &contract.Companion{ID: v.ID, PublicIP: v.PublicIP, State: v.State}
		rows[i].IngressFor = rows[index].ID
	}
	return rows, nil
}
