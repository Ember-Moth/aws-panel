package integration

import (
	"context"
	"encoding/json"
	"github.com/Ember-Moth/aws-panel/internal/auth"
	"github.com/Ember-Moth/aws-panel/internal/module/compute"
	cp "github.com/Ember-Moth/aws-panel/internal/module/compute/contract"
	lc "github.com/Ember-Moth/aws-panel/internal/module/lightsail/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/operations"
	oc "github.com/Ember-Moth/aws-panel/internal/module/operations/contract"
	"github.com/google/uuid"
	"strings"
	"testing"
)

func TestLightsailAutomaticIPAndIPv6Only(t *testing.T) {
	h := newLaunch(t, true)
	ctx := context.Background()
	v := h.target("ipv6-only")
	v.IPType = "ipv6"
	v.BundleID = "small_ipv6_3_0"
	p, err := h.serviceLaunch.Prepare(ctx, h.op.admin, lc.CreateRequest{Targets: []lc.CreateTarget{v}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Targets[0].Spec.AutomaticIP != nil {
		t.Fatal("IPv6-only instance allocated IPv4")
	}
	j, err := h.serviceLaunch.Submit(ctx, h.op.admin, p.ID, uuid.NewString(), auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if d := h.op.drive(t, j.ID); d.Job.Status != oc.Succeeded || len(d.Items[0].Steps) != 3 {
		t.Fatal(d.Job.Status, d.Items)
	}
	h.cloudLaunch.mu.Lock()
	defer h.cloudLaunch.mu.Unlock()
	if len(h.cloudLaunch.addresses.ips) != 0 {
		t.Fatal("IPv6-only created an address")
	}
}

func TestWavelengthIngressResponseLossAndOwnedCleanup(t *testing.T) {
	h := newCompute(t, true)
	ctx := context.Background()
	h.cloudCompute.wavelength = true
	h.cloudCompute.wavelengthEnabled = true
	h.cloudCompute.mode = "lost_ingress"
	v := wavelengthTarget(h)
	v.LoginMethod = "root_password"
	v.KeyPairName = ""
	v.RootPassword = "Wavelength-root-password-test!"
	p, err := h.serviceCompute.Prepare(ctx, h.op.admin, cp.Request{Targets: []cp.InputTarget{v}})
	if err != nil {
		t.Fatal(err)
	}
	spec := p.Targets[0].Spec
	if spec.Ingress == nil || spec.AutomaticIP == nil || spec.Ingress.AutomaticIP.Service != "ec2" || spec.AutomaticIP.Service != "wavelength" {
		t.Fatal("pair missing in preview")
	}
	j, err := h.serviceCompute.Submit(ctx, h.op.admin, p.ID, uuid.NewString(), auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	d := h.op.drive(t, j.ID)
	if d.Job.Status != oc.Succeeded || len(d.Items[0].Steps) != 13 {
		t.Fatal(d.Job.Status, d.Items)
	}
	listing, err := h.serviceCompute.Instances(ctx, h.op.admin, h.connectionA.ID, "us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	primary := listing.Items[0]
	if primary.Ingress == nil || primary.CarrierIP == "" || primary.Ingress.PublicIP == "" || primary.CarrierIP == primary.Ingress.PublicIP {
		t.Fatal("paired address roles missing", listing)
	}
	h.cloudCompute.mu.Lock()
	for action, want := range map[string]int{"RunInstances": 2, "AllocateAddress": 2, "AssociateAddress": 2, "ModifyInstanceAttribute": 1, "CreateSecurityGroup": 1, "AuthorizeSecurityGroupIngress": 1, "RevokeSecurityGroupEgress": 0, "ModifyNetworkInterfaceAttribute": 1} {
		if h.cloudCompute.calls[action] != want {
			t.Errorf("%s count=%d want=%d", action, h.cloudCompute.calls[action], want)
		}
	}
	if len(h.cloudCompute.transit) != 0 || len(h.cloudCompute.firewalls) != 1 {
		t.Error("permissive creation used legacy transit group")
	}
	for _, g := range h.cloudCompute.firewalls {
		if !g.in4 || !g.out4 {
			t.Error("Wavelength traffic not fully allowed")
		}
	}

	for _, i := range h.cloudCompute.instances {
		if i.labels["aws-panel:role"] == "ingress" {
			if !i.forwarding || !strings.Contains(i.userData, "dnat to 10.0.0.10") || !strings.Contains(i.userData, "snat to $relay_ip") || strings.Contains(i.userData, "root:$6$") {
				t.Error("forwarder initialization missing or reused root secret")
			}
		}
	}
	h.cloudCompute.mode = "lost_manage"
	h.cloudCompute.mu.Unlock()
	term := cp.InputTarget{Target: cp.Target{ConnectionID: h.connectionA.ID, Region: "us-east-1", Action: "terminate", InstanceID: primary.ID, WavelengthZoneID: wavelengthZoneID}}
	p, err = h.serviceCompute.Prepare(ctx, h.op.admin, cp.Request{Targets: []cp.InputTarget{term}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Targets[0].Spec.CompanionInstance == nil || len(p.Targets[0].Spec.CleanupIPs) != 2 {
		t.Fatal("cleanup preview missing owned resources", p)
	}
	j, err = h.serviceCompute.Submit(ctx, h.op.admin, p.ID, uuid.NewString(), auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if d = h.op.drive(t, j.ID); d.Job.Status != oc.Succeeded {
		t.Fatal(d.Job.Status, d.Items)
	}
	h.cloudCompute.mu.Lock()
	defer h.cloudCompute.mu.Unlock()
	if len(h.cloudCompute.ips) != 0 || h.cloudCompute.calls["TerminateInstances"] != 2 || h.cloudCompute.calls["ReleaseAddress"] != 2 {
		t.Fatal("owned cleanup incomplete or repeated", h.cloudCompute.calls)
	}
}

// Recreate a persisted pre-automatic-IP plan: only create/ready and three
// ownership tags. The AWS fixture drops any newly invented bundle tag.
type legacyComputePlanner struct{ oc.Executor }

func (e legacyComputePlanner) Plan(ctx context.Context, input json.RawMessage, targets []oc.Target) ([]oc.ItemPlan, error) {
	items, err := e.Executor.Plan(ctx, input, targets)
	if err != nil {
		return nil, err
	}
	for i := range items {
		var s cp.Spec
		if err := json.Unmarshal(items[i].Steps[0].Input, &s); err != nil {
			return nil, err
		}
		s.AutomaticIP = nil
		s.Firewall = nil
		s.Ingress = nil
		body, _ := json.Marshal(s)
		items[i].Steps = items[i].Steps[:2]
		for j := range items[i].Steps {
			items[i].Steps[j].Input = body
		}
	}
	return items, nil
}
func TestComputeLegacyCreatePlanRemainsReadable(t *testing.T) {
	h := newCompute(t, true)
	h.cloudCompute.mode = "legacy_launch"
	effects := operations.NewEffectStore(h.runtime)
	executor := compute.NewExecutor(compute.ExecutorDeps{Clients: h.accounts, LaunchSecrets: h.accounts, Cloud: h.cloudCompute, AddressCloud: newAddressAWS(), Effects: effects})
	jobs, err := operations.New(operations.Deps{Store: h.op.store, Access: h.op.members, Connections: inventoryBindings{h.accounts}, Publisher: h.op.queue, CloudWritesEnabled: true, Executors: []oc.Executor{legacyComputePlanner{executor}}})
	if err != nil {
		t.Fatal(err)
	}
	h.op.service = jobs
	h.serviceCompute = compute.New(compute.Deps{Accounts: h.accounts, Access: h.op.members, Jobs: jobs, Cloud: h.cloudCompute, WritesEnabled: true, Effects: effects})
	if d := h.op.drive(t, h.submit(t, h.target()).ID); d.Job.Status != oc.Succeeded || len(d.Items[0].Steps) != 2 {
		t.Fatal("legacy plan failed", d.Job.Status, d.Items)
	}
	h.cloudCompute.mu.Lock()
	defer h.cloudCompute.mu.Unlock()
	if h.cloudCompute.calls["RunInstances"] != 1 || h.cloudCompute.calls["AllocateAddress"] != 0 {
		t.Fatal("legacy plan gained unexpected effects", h.cloudCompute.calls)
	}
}
func TestComputeTerminationPreservesUnprovenIP(t *testing.T) {
	h := newCompute(t, true)
	if d := h.op.drive(t, h.submit(t, h.target()).ID); d.Job.Status != oc.Succeeded {
		t.Fatal(d.Job.Status)
	}
	h.cloudCompute.mu.Lock()
	id := ""
	for _, ip := range h.cloudCompute.ips {
		id = ip.instance
		delete(ip.labels, "aws-panel:step")
	}
	h.cloudCompute.mu.Unlock()
	v := cp.InputTarget{Target: cp.Target{ConnectionID: h.connectionA.ID, Region: "us-east-1", Action: "terminate", InstanceID: id}}
	p, err := h.serviceCompute.Prepare(context.Background(), h.op.admin, cp.Request{Targets: []cp.InputTarget{v}})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Targets[0].Spec.CleanupIPs) != 0 {
		t.Fatal("unproven IP included in cleanup")
	}
	j, err := h.serviceCompute.Submit(context.Background(), h.op.admin, p.ID, uuid.NewString(), auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if d := h.op.drive(t, j.ID); d.Job.Status != oc.Succeeded {
		t.Fatal(d.Job.Status, d.Items)
	}
	h.cloudCompute.mu.Lock()
	defer h.cloudCompute.mu.Unlock()
	if h.cloudCompute.calls["ReleaseAddress"] != 0 || len(h.cloudCompute.ips) != 1 {
		t.Fatal("unproven IP deleted")
	}
}
