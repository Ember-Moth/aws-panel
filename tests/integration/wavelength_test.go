package integration

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Ember-Moth/aws-panel/internal/auth"
	ad "github.com/Ember-Moth/aws-panel/internal/module/addresses/contract"
	cp "github.com/Ember-Moth/aws-panel/internal/module/compute/contract"
	iv "github.com/Ember-Moth/aws-panel/internal/module/inventory/contract"
	oc "github.com/Ember-Moth/aws-panel/internal/module/operations/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/quotas"
	"github.com/google/uuid"
	"testing"
)

func wavelengthTarget(h *computeHarness) cp.InputTarget {
	v := h.target()
	v.WavelengthZoneID = wavelengthZoneID
	v.IPv6 = false
	v.PublicIPv4 = false
	return v
}
func TestWavelengthOptInLostResponseAndGroupScope(t *testing.T) {
	h := newCompute(t, true)
	ctx := context.Background()
	h.cloudCompute.mode = "lost_wavelength"
	zs, err := h.serviceCompute.WavelengthZones(ctx, h.op.admin, h.connectionA.ID, "us-east-1")
	if err != nil || len(zs.Zones) != 2 || zs.Zones[0].OptInStatus != "not-opted-in" {
		t.Fatal(zs, err)
	}
	target := cp.InputTarget{Target: cp.Target{ConnectionID: h.connectionA.ID, Region: "us-east-1", Action: "wavelength_opt_in", WavelengthZoneID: wavelengthZoneID}}
	p, err := h.serviceCompute.Prepare(ctx, h.op.admin, cp.Request{Targets: []cp.InputTarget{target}})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Targets[0].Spec.GroupZones) != 2 || p.Targets[0].Spec.WavelengthZone.Group != "us-east-1-wl1" {
		t.Fatal("group effects omitted", p)
	}
	job, err := h.serviceCompute.Submit(ctx, h.op.admin, p.ID, uuid.NewString(), auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if d := h.op.drive(t, job.ID); d.Job.Status != oc.Succeeded {
		t.Fatal(d.Job.Status, d.Items)
	}
	// A repeated explicit request observes the already enabled group without another write.
	if d := h.op.drive(t, h.submit(t, target).ID); d.Job.Status != oc.Succeeded {
		t.Fatal(d.Job.Status)
	}
	h.cloudCompute.mu.Lock()
	defer h.cloudCompute.mu.Unlock()
	if h.cloudCompute.calls["ModifyAvailabilityZoneGroup"] != 1 || len(h.cloudCompute.instances) != 0 || len(h.cloudCompute.networks) != 0 {
		t.Fatal("opt-in repeated or created resources", h.cloudCompute.calls)
	}
}
func TestWavelengthUnknownOptInNeverResends(t *testing.T) {
	h := newCompute(t, true)
	h.cloudCompute.mode = "wavelength_never_accepted"
	job := h.submit(t, cp.InputTarget{Target: cp.Target{ConnectionID: h.connectionA.ID, Region: "us-east-1", Action: "wavelength_opt_in", WavelengthZoneID: wavelengthZoneID}})
	for range 5 {
		h.tick(t, job.ID)
	}
	h.cloudCompute.mu.Lock()
	defer h.cloudCompute.mu.Unlock()
	if h.cloudCompute.calls["ModifyAvailabilityZoneGroup"] != 1 {
		t.Fatal("unknown opt-in resent")
	}
}
func TestWavelengthCreateGuardsGp2AndLifecycle(t *testing.T) {
	h := newCompute(t, true)
	h.cloudCompute.wavelength = true
	ctx := context.Background()
	v := wavelengthTarget(h)
	if _, err := h.serviceCompute.Prepare(ctx, h.op.admin, cp.Request{Targets: []cp.InputTarget{v}}); err == nil {
		t.Fatal("unenabled zone accepted")
	}
	h.cloudCompute.wavelengthEnabled = true
	for _, kind := range []string{"ipv6", "public", "bad-zone"} {
		bad := v
		switch kind {
		case "ipv6":
			bad.IPv6 = true
		case "public":
			bad.PublicIPv4 = true
		case "bad-zone":
			bad.WavelengthZoneID = "use1-unknown-wlz1"
		}
		if _, err := h.serviceCompute.Prepare(ctx, h.op.admin, cp.Request{Targets: []cp.InputTarget{bad}}); err == nil {
			t.Fatal("invalid wavelength accepted", kind)
		}
	}
	cat, err := h.serviceCompute.WavelengthCatalogue(ctx, h.op.admin, h.connectionA.ID, "us-east-1", wavelengthZoneID)
	if err != nil || len(cat.Subnets) != 1 || len(cat.Types) != 1 || cat.WavelengthZone.ID != wavelengthZoneID {
		t.Fatal(cat, err)
	}
	h.cloudCompute.mode = "lost_launch"
	p, err := h.serviceCompute.Prepare(ctx, h.op.admin, cp.Request{Targets: []cp.InputTarget{v}})
	if err != nil || p.Targets[0].Spec.VolumeType != "gp2" {
		t.Fatal(p, err)
	}
	job, err := h.serviceCompute.Submit(ctx, h.op.admin, p.ID, uuid.NewString(), auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if d := h.op.drive(t, job.ID); d.Job.Status != oc.Succeeded {
		t.Fatal(d.Job.Status, d.Items)
	}
	h.cloudCompute.mu.Lock()
	req := h.cloudCompute.launches[0]
	relay := h.cloudCompute.launches[1]
	calls := h.cloudCompute.calls["RunInstances"]
	h.cloudCompute.mode = ""
	h.cloudCompute.mu.Unlock()
	if calls != 2 || relay["BlockDeviceMapping.1.Ebs.VolumeType"] != "gp3" || relay["NetworkInterface.1.SubnetId"] != "subnet-33333333" || req["BlockDeviceMapping.1.Ebs.VolumeType"] != "gp2" || req["NetworkInterface.1.AssociatePublicIpAddress"] != "false" || req["NetworkInterface.1.Ipv6AddressCount"] != nil {
		t.Fatal("Wavelength launch policy", req)
	}
	listing, err := h.serviceCompute.Instances(ctx, h.op.admin, h.connectionA.ID, "us-east-1")
	if err != nil || len(listing.Items) != 2 || listing.Items[0].Ingress == nil || listing.Items[0].CarrierIP == "" || listing.Items[0].Ingress.PublicIP == "" {
		t.Fatal(err)
	}
	target := cp.InputTarget{Target: cp.Target{ConnectionID: h.connectionA.ID, Region: "us-east-1", WavelengthZoneID: wavelengthZoneID, InstanceID: listing.Items[0].ID}}
	target.Action = "enable_ipv6"
	if _, err := h.serviceCompute.Prepare(ctx, h.op.admin, cp.Request{Targets: []cp.InputTarget{target}}); err == nil {
		t.Fatal("IPv6 enabled in Wavelength")
	}
	for _, action := range []string{"stop", "start", "reboot", "terminate"} {
		target.Action = action
		if d := h.op.drive(t, h.submit(t, target).ID); d.Job.Status != oc.Succeeded {
			t.Fatal(action, d.Job.Status, d.Items)
		}
	}
}
func TestWavelengthNetworkLostResponsesIPv4Only(t *testing.T) {
	h := newCompute(t, true)
	h.cloudCompute.wavelength = true
	h.cloudCompute.wavelengthEnabled = true
	h.cloudCompute.mode = "lost_network"
	v := cp.InputTarget{Target: cp.Target{ConnectionID: h.connectionA.ID, Region: "us-east-1", Action: "network", WavelengthZoneID: wavelengthZoneID, Name: "edge-network", SSHIPv4CIDR: "10.0.0.0/8"}}
	d := h.op.drive(t, h.submit(t, v).ID)
	if d.Job.Status != oc.Succeeded {
		t.Fatal(d.Job.Status, d.Items)
	}
	if len(d.Items[0].Steps) != 15 {
		t.Fatal("unexpected steps", len(d.Items[0].Steps))
	}
	h.cloudCompute.mu.Lock()
	defer h.cloudCompute.mu.Unlock()
	for action, want := range map[string]int{"CreateVpc": 1, "CreateSubnet": 2, "CreateCarrierGateway": 1, "CreateRouteTable": 2, "CreateRoute": 2, "AssociateRouteTable": 2, "CreateSecurityGroup": 1, "AuthorizeSecurityGroupIngress": 1} {
		if h.cloudCompute.calls[action] != want {
			t.Fatal("write repeated or missing", action, h.cloudCompute.calls)
		}
	}
	if h.cloudCompute.calls["CreateInternetGateway"] != 1 || h.cloudCompute.calls["AttachInternetGateway"] != 1 {
		t.Fatal("parent internet gateway missing or repeated")
	}
	for _, n := range h.cloudCompute.networks {
		if n.network.IPv6CIDR != "" || n.network.SubnetIPv6CIDR != "" || !n.route4 || n.route6 || (!n.network.IPv4Only && n.network.GatewayID != "cagw-22222222") || (n.network.IPv4Only && n.network.GatewayID != "igw-22222222") {
			t.Fatal("wrong network", n)
		}
	}
}
func submitCarrier(t *testing.T, h *computeHarness, v ad.Target) oc.Detail {
	t.Helper()
	p, err := h.serviceAddresses.Prepare(context.Background(), h.op.admin, ad.Request{Targets: []ad.Target{v}})
	if err != nil {
		t.Fatal(err)
	}
	job, err := h.serviceAddresses.Submit(context.Background(), h.op.admin, p.ID, uuid.NewString(), auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	d := h.op.drive(t, job.ID)
	if d.Job.Status != oc.Succeeded {
		t.Fatal(d.Job.Status, d.Items)
	}
	return d
}
func TestWavelengthCarrierReplacementInventoryAndQuota(t *testing.T) {
	h := newCompute(t, true)
	h.cloudCompute.wavelength = true
	h.cloudCompute.wavelengthEnabled = true
	ctx := context.Background()
	if d := h.op.drive(t, h.submit(t, wavelengthTarget(h)).ID); d.Job.Status != oc.Succeeded {
		t.Fatal(d.Job.Status)
	}
	ls, err := h.serviceCompute.Instances(ctx, h.op.admin, h.connectionA.ID, "us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	id := ls.Items[0].ID
	scope := h.accountA.AWSAccountID + "/us-east-1"
	base := ad.Target{ConnectionID: h.connectionA.ID, Region: "us-east-1", Service: "wavelength", Action: "allocate", WavelengthZoneID: wavelengthZoneID, InstanceID: id}
	h.cloudCompute.mode = "lost_addresses"
	cat, err := h.serviceAddresses.Catalogue(ctx, h.op.admin, h.connectionA.ID, "us-east-1", "wavelength")
	if err != nil || len(cat.Addresses) != 1 || !cat.Addresses[0].Attached || cat.Addresses[0].BorderGroup != wavelengthZoneName {
		t.Fatal(cat, err)
	}
	old := cat.Addresses[0].ID
	base.Action = "replace"
	base.ReleasePrevious = false
	submitCarrier(t, h, base)
	h.cloudCompute.mu.Lock()
	if len(h.cloudCompute.ips) != 2 || h.cloudCompute.ips[scope+"/"+old] != nil || h.cloudCompute.calls["AllocateAddress"] != 3 || h.cloudCompute.calls["AssociateAddress"] != 3 || h.cloudCompute.calls["ReleaseAddress"] != 1 {
		t.Fatal("carrier replacement did not automatically release old IP", h.cloudCompute.calls)
	}
	h.cloudCompute.mode = ""
	h.cloudCompute.mu.Unlock()
	normal, err := h.serviceAddresses.Catalogue(ctx, h.op.admin, h.connectionA.ID, "us-east-1", "ec2")
	if err != nil || len(normal.Addresses) != 1 || len(normal.Instances) != 1 {
		t.Fatal("carrier leaked into ordinary EIP", normal, err)
	}
	cfg, err := h.accounts.ConfigFor(ctx, h.op.admin, h.connectionA.ID, "us-east-1", false)
	if err != nil {
		t.Fatal(err)
	}
	cfg = addressConfig(cfg)
	report, err := quotas.NewCloud().Read(ctx, cfg, h.accountA.AWSAccountID, "ec2")
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, q := range report.Entries {
		if q.Key == "ec2.elastic_ips" {
			found++
			if !q.UsageKnown || q.Used != 1 {
				t.Fatal("carrier consumed regular EIP quota", q)
			}
		}
		if q.Key == "ec2.carrier_ips" {
			found++
			if !q.UsageKnown || q.Used != 1 || q.LimitKnown {
				t.Fatal("carrier quota wrongly inferred", q)
			}
		}
	}
	if found != 2 {
		t.Fatal("quota missing")
	}
	refresh, err := h.service.Refresh(ctx, h.op.admin, iv.RefreshRequest{Targets: []iv.RefreshTarget{{ConnectionID: h.connectionA.ID, Region: "us-east-1", Kind: iv.EC2Address}}, IdempotencyKey: uuid.NewString()}, auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if d := h.op.drive(t, refresh.ID); d.Job.Status != oc.Succeeded {
		t.Fatal(d.Job.Status)
	}
	rows, err := h.service.Resources(ctx, h.op.admin, iv.Filter{Kind: iv.EC2Address, Page: 1})
	if err != nil || len(rows.Items) != 2 {
		t.Fatal("carrier inventory missing", rows, err)
	}
	observations, err := h.service.Overview(ctx, h.op.admin)
	if err != nil {
		t.Fatal(err)
	}
	foundCarrier := false
	for _, o := range observations {
		if o.Kind == iv.EC2Address && o.Region == "us-east-1" {
			foundCarrier = true
			if o.Count != 2 || o.CarrierCount != 1 || !o.CarrierKnown || len(o.IPv4) != 1 {
				t.Fatal("inventory overview conflated carriers", o)
			}
		}
	}
	if !foundCarrier {
		t.Fatal("inventory carrier overview missing")
	}
	base.Action = "release"
	base.InstanceID = ""
	base.AddressID = ""
	base.NamePrefix = ""
	base.WavelengthZoneID = ""
	cat, err = h.serviceAddresses.Catalogue(ctx, h.op.admin, h.connectionA.ID, "us-east-1", "wavelength")
	if err != nil || len(cat.Addresses) != 1 {
		t.Fatal(err, cat)
	}
	base.AddressID = cat.Addresses[0].ID
	if _, err = h.serviceAddresses.Prepare(ctx, h.op.admin, ad.Request{Targets: []ad.Target{base}}); !errors.Is(err, ad.ErrAttached) {
		t.Fatal("released attached carrier", err)
	}
	base.Action = "detach"
	submitCarrier(t, h, base)
	base.Action = "release"
	submitCarrier(t, h, base)
}
func TestWavelengthWrongBorderAndUnboundAllocation(t *testing.T) {
	h := newCompute(t, true)
	h.cloudCompute.wavelength = true
	h.cloudCompute.wavelengthEnabled = true
	ctx := context.Background()
	base := ad.Target{ConnectionID: h.connectionA.ID, Region: "us-east-1", Service: "wavelength", Action: "allocate"}
	if _, err := h.serviceAddresses.Prepare(ctx, h.op.admin, ad.Request{Targets: []ad.Target{base}}); err == nil {
		t.Fatal("unbound carrier without zone allowed")
	}
	base.WavelengthZoneID = "use1-unknown-wlz1"
	if _, err := h.serviceAddresses.Prepare(ctx, h.op.admin, ad.Request{Targets: []ad.Target{base}}); err == nil {
		t.Fatal("unknown zone allowed")
	}
	base.WavelengthZoneID = "use1-wl1-nyc-wlz1"
	submitCarrier(t, h, base)
	if d := h.op.drive(t, h.submit(t, wavelengthTarget(h)).ID); d.Job.Status != oc.Succeeded {
		t.Fatal(d.Job.Status)
	}
	cat, err := h.serviceAddresses.Catalogue(ctx, h.op.admin, h.connectionA.ID, "us-east-1", "wavelength")
	if err != nil {
		t.Fatal(err)
	}
	base.Action = "attach"
	base.WavelengthZoneID = ""
	base.InstanceID = cat.Instances[0].ID
	for _, ip := range cat.Addresses {
		if ip.BorderGroup == "us-east-1-wl1-nyc-wlz-1" {
			base.AddressID = ip.ID
		}
	}
	if _, err := h.serviceAddresses.Prepare(ctx, h.op.admin, ad.Request{Targets: []ad.Target{base}}); !errors.Is(err, ad.ErrUnsupported) {
		t.Fatal("cross-border carrier binding allowed", err)
	}
	// Empty optional fields preserve the persisted normal EC2 launch-secret binding.
	raw, _ := json.Marshal(h.target().Target)
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	if _, ok := fields["wavelength_zone_id"]; ok {
		t.Fatal("normal EC2 serialization changed")
	}
}

func TestWavelengthGroupChangesAfterUnknownSendNeedAttention(t *testing.T) {
	h := newCompute(t, true)
	h.cloudCompute.mode = "wavelength_never_accepted"
	job := h.submit(t, cp.InputTarget{Target: cp.Target{ConnectionID: h.connectionA.ID, Region: "us-east-1", Action: "wavelength_opt_in", WavelengthZoneID: wavelengthZoneID}})
	h.tick(t, job.ID)
	h.cloudCompute.mu.Lock()
	h.cloudCompute.mode = "wavelength_group_changed"
	h.cloudCompute.mu.Unlock()
	d := h.op.drive(t, job.ID)
	if d.Job.Status != oc.Attention {
		t.Fatal("unknown write declared absent after group drift", d.Job.Status, d.Items)
	}
	h.cloudCompute.mu.Lock()
	defer h.cloudCompute.mu.Unlock()
	if h.cloudCompute.calls["ModifyAvailabilityZoneGroup"] != 1 {
		t.Fatal("group drift retried write")
	}
}
