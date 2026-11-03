package integration

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/herta-network/aws-panel/internal/auth"
	cp "github.com/herta-network/aws-panel/internal/module/compute/contract"
	lc "github.com/herta-network/aws-panel/internal/module/lightsail/contract"
	oc "github.com/herta-network/aws-panel/internal/module/operations/contract"
	"testing"
)

func TestComputeAutomaticAllTrafficWithRestrictedBootstrap(t *testing.T) {
	h := newCompute(t, true)
	h.cloudCompute.restrictiveBootstrap = true
	h.cloudCompute.mode = "lost_firewall"
	v := h.target()
	v.SecurityGroupIDs = nil
	v.LoginMethod = "root_password"
	v.KeyPairName = ""
	v.RootPassword = "Automatic-open-firewall-test!"
	p, err := h.serviceCompute.Prepare(context.Background(), h.op.admin, cp.Request{Targets: []cp.InputTarget{v}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Targets[0].Spec.Firewall == nil || len(p.Targets[0].Spec.BootstrapGroups) != 1 || len(p.Targets[0].Spec.Target.SecurityGroupIDs) != 0 {
		t.Fatal("automatic group preview missing", p)
	}
	j, err := h.serviceCompute.Submit(context.Background(), h.op.admin, p.ID, uuid.NewString(), auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	d := h.op.drive(t, j.ID)
	if d.Job.Status != oc.Succeeded || len(d.Items[0].Steps) != 8 {
		t.Fatal(d.Job.Status, d.Items)
	}
	h.cloudCompute.mu.Lock()
	defer h.cloudCompute.mu.Unlock()
	for action, want := range map[string]int{"RunInstances": 1, "CreateSecurityGroup": 1, "AuthorizeSecurityGroupIngress": 1, "AuthorizeSecurityGroupEgress": 1, "ModifyNetworkInterfaceAttribute": 1, "AllocateAddress": 1} {
		if h.cloudCompute.calls[action] != want {
			t.Errorf("%s=%d want %d", action, h.cloudCompute.calls[action], want)
		}
	}
	for _, g := range h.cloudCompute.firewalls {
		if !g.in4 || !g.in6 || !g.out4 || !g.out6 {
			t.Fatal("not all traffic", g)
		}
		for _, i := range h.cloudCompute.instances {
			if len(i.groups) != 1 || i.groups[0] != g.id {
				t.Fatal("dedicated group not attached")
			}
		}
	}
}
func TestComputeFirewallFailureDoesNotAllocateIP(t *testing.T) {
	h := newCompute(t, true)
	h.cloudCompute.mode = "reject_firewall"
	d := h.op.drive(t, h.submit(t, h.target()).ID)
	if d.Job.Status != oc.Failed && d.Job.Status != oc.PartialSuccess {
		t.Fatal(d.Job.Status, d.Items)
	}
	h.cloudCompute.mu.Lock()
	defer h.cloudCompute.mu.Unlock()
	if len(h.cloudCompute.instances) != 1 || len(h.cloudCompute.ips) != 0 || h.cloudCompute.calls["CreateSecurityGroup"] != 1 {
		t.Fatal("firewall failure lost instance or continued to IP")
	}
}
func TestLightsailAllTrafficFirewallFamiliesAndLostResponse(t *testing.T) {
	h := newLaunch(t, true)
	ctx := context.Background()
	targets := []lc.CreateTarget{}
	for _, kind := range []string{"dualstack", "ipv4", "ipv6"} {
		v := h.target("firewall-" + kind)
		v.IPType = kind
		if kind == "ipv6" {
			v.BundleID = "small_ipv6_3_0"
		}
		targets = append(targets, v)
		h.mode(v.Name, "lost_firewall")
	}
	p, err := h.serviceLaunch.Prepare(ctx, h.op.admin, lc.CreateRequest{Targets: targets})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range p.Targets {
		if !v.Spec.AllowAllFirewall {
			t.Fatal("firewall policy omitted")
		}
	}
	j, err := h.serviceLaunch.Submit(ctx, h.op.admin, p.ID, uuid.NewString(), auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	d := h.op.drive(t, j.ID)
	if d.Job.Status != oc.Succeeded {
		t.Fatal(d.Job.Status, d.Items)
	}
	h.cloudLaunch.mu.Lock()
	defer h.cloudLaunch.mu.Unlock()
	for _, v := range targets {
		if h.cloudLaunch.firewallCalls[v.Name] != 1 {
			t.Fatal("firewall write repeated", v.Name)
		}
		raw := h.cloudLaunch.firewallInputs[v.Name]
		ports := raw["portInfos"].([]any)
		if len(ports) != 1 {
			t.Fatal(ports)
		}
		p := ports[0].(map[string]any)
		if p["protocol"] != "all" || p["toPort"] != float64(65535) {
			t.Fatal("not all ports/protocols", p)
		}
		v4, _ := p["cidrs"].([]any)
		v6, _ := p["ipv6Cidrs"].([]any)
		if (v.IPType != "ipv6") != (len(v4) == 1) || (v.IPType != "ipv4") != (len(v6) == 1) {
			t.Fatal("wrong address families", v.IPType, p)
		}
		if len(v4) > 0 && v4[0] != "0.0.0.0/0" || len(v6) > 0 && v6[0] != "::/0" {
			t.Fatal("not all sources", p)
		}
	}
}
func TestLightsailFirewallWaitsForOperationBeforeIP(t *testing.T) {
	h := newLaunch(t, true)
	name := "pending-firewall"
	h.mode(name, "pending_firewall")
	j := h.submit(t, h.target(name))
	for range 12 {
		h.tick(t, j.ID)
		h.cloudLaunch.mu.Lock()
		sent := h.cloudLaunch.firewallCalls[name] > 0
		h.cloudLaunch.mu.Unlock()
		if sent {
			break
		}
	}
	h.tick(t, j.ID)
	h.cloudLaunch.mu.Lock()
	if h.cloudLaunch.addresses.calls["AllocateStaticIp"] != 0 {
		t.Fatal("IP allocated before firewall result confirmed")
	}
	for _, ops := range h.cloudLaunch.firewallOps {
		for _, v := range ops {
			op := v.(map[string]any)
			op["status"] = "Succeeded"
			op["isTerminal"] = true
		}
	}
	h.cloudLaunch.mu.Unlock()
	if d := h.op.drive(t, j.ID); d.Job.Status != oc.Succeeded {
		t.Fatal(d.Job.Status, d.Items)
	}
	var effect struct{ Proof string }
	if err := h.runtime.DB.Table("operation_effects").Select("proof").Where("job_id=? AND purpose=?", j.ID, "lightsail.firewall").First(&effect).Error; err != nil {
		t.Fatal(err)
	}
	var proof map[string]any
	if json.Unmarshal([]byte(effect.Proof), &proof) != nil || len(proof["operations"].([]any)) != 1 {
		t.Fatal("missing durable firewall operation proof")
	}
}
