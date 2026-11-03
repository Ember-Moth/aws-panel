package integration

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/herta-network/aws-panel/internal/auth"
	ad "github.com/herta-network/aws-panel/internal/module/addresses/contract"
	cp "github.com/herta-network/aws-panel/internal/module/compute/contract"
	oc "github.com/herta-network/aws-panel/internal/module/operations/contract"
	"strings"
	"testing"
)

func TestComputeCreateLostResponseRootPasswordAndIPv6(t *testing.T) {
	h := newCompute(t, true)
	ctx := context.Background()
	h.cloudCompute.mode = "lost_launch"
	v := h.target()
	v.LoginMethod = "root_password"
	v.KeyPairName = ""
	v.RootPassword = "Compute-root-password-for-test!"
	plan, err := h.serviceCompute.Prepare(ctx, h.op.admin, cp.Request{Targets: []cp.InputTarget{v}})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(plan)
	if strings.Contains(string(body), v.RootPassword) || strings.Contains(string(body), "$6$") {
		t.Fatal("preview leaked secret")
	}
	job, err := h.serviceCompute.Submit(ctx, h.op.admin, plan.ID, uuid.NewString(), auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	detail := h.op.drive(t, job.ID)
	if detail.Job.Status != oc.Succeeded {
		t.Fatal("create failed", detail.Job.Status, detail.Items[0].Steps)
	}
	h.cloudCompute.mu.Lock()
	defer h.cloudCompute.mu.Unlock()
	if h.cloudCompute.calls["RunInstances"] != 1 || len(h.cloudCompute.instances) != 1 {
		t.Fatal("launch repeated")
	}
	for _, instance := range h.cloudCompute.instances {
		if !instance.ipv6 || !strings.Contains(instance.userData, "PermitRootLogin yes") || !strings.Contains(instance.userData, "root:$6$rounds=500000$") || strings.Contains(instance.userData, v.RootPassword) {
			t.Fatal("invalid root/IPv6 launch")
		}
	}
	req := h.cloudCompute.lastLaunch
	for key, want := range map[string]any{"MetadataOptions.HttpTokens": "required", "BlockDeviceMapping.1.Ebs.Encrypted": "true", "BlockDeviceMapping.1.Ebs.VolumeType": "gp3", "NetworkInterface.1.Ipv6AddressCount": "1", "MinCount": "1", "MaxCount": "1"} {
		if req[key] != want {
			t.Fatal("launch policy missing", key, req[key])
		}
	}
	if _, ok := req["KeyName"]; ok {
		t.Fatal("root launch set key")
	}
	var count int64
	if err := h.runtime.DB.Table("secrets").Where("owner_type=?", "instance_launch").Count(&count).Error; err != nil || count != 0 {
		t.Fatal("launch secret not removed", count, err)
	}
	var inputs []string
	if err := h.runtime.DB.Table("job_steps").Where("job_id=?", job.ID).Pluck("input", &inputs).Error; err != nil {
		t.Fatal(err)
	}
	for _, s := range inputs {
		if strings.Contains(s, v.RootPassword) || strings.Contains(s, "$6$") {
			t.Fatal("persisted input leaked secret")
		}
	}
}
func TestComputeQuotaNetworkGuardAndUnknown(t *testing.T) {
	h := newCompute(t, true)
	ctx := context.Background()
	v := h.target()
	h.cloudCompute.quota = 1
	if _, err := h.serviceCompute.Prepare(ctx, h.op.admin, cp.Request{Targets: []cp.InputTarget{v}}); !errors.Is(err, cp.ErrQuota) {
		t.Fatal("known insufficient quota allowed", err)
	}
	h.cloudCompute.quota = 100
	h.cloudCompute.noIPv6 = true
	if _, err := h.serviceCompute.Prepare(ctx, h.op.admin, cp.Request{Targets: []cp.InputTarget{v}}); !errors.Is(err, cp.ErrNetwork) {
		t.Fatal("missing IPv6 subnet accepted", err)
	}
	h.cloudCompute.noIPv6 = false
	h.cloudCompute.mode = "quota_denied"
	p, err := h.serviceCompute.Prepare(ctx, h.op.admin, cp.Request{Targets: []cp.InputTarget{v}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Targets[0].Spec.Quota.LimitKnown || len(p.Targets[0].Spec.Warnings) < 3 {
		t.Fatal("unknown quota not exposed")
	}
}
func TestComputeLifecycleAndUnknownReboot(t *testing.T) {
	h := newCompute(t, true)
	v := h.target()
	v.IPv6 = false
	job := h.submit(t, v)
	if d := h.op.drive(t, job.ID); d.Job.Status != oc.Succeeded {
		t.Fatal(d.Job.Status)
	}
	listing, err := h.serviceCompute.Instances(context.Background(), h.op.admin, h.connectionA.ID, "us-east-1")
	if err != nil || len(listing.Items) != 1 {
		t.Fatal(err)
	}
	target := cp.InputTarget{Target: cp.Target{ConnectionID: h.connectionA.ID, Region: "us-east-1", InstanceID: listing.Items[0].ID}}
	for _, action := range []string{"enable_ipv6", "stop", "start", "reboot"} {
		target.Action = action
		job = h.submit(t, target)
		if d := h.op.drive(t, job.ID); d.Job.Status != oc.Succeeded {
			t.Fatal(action, d.Job.Status, d.Items[0].Steps)
		}
	}
	h.cloudCompute.mu.Lock()
	h.cloudCompute.mode = "lost_manage"
	h.cloudCompute.mu.Unlock()
	target.Action = "reboot"
	job = h.submit(t, target)
	for range 4 {
		h.tick(t, job.ID)
	}
	detail, err := h.op.service.Detail(context.Background(), h.op.admin, job.ID)
	if err != nil || detail.Job.Status != oc.Attention {
		t.Fatal("ambiguous reboot not stopped", detail.Job.Status, err)
	}
	h.cloudCompute.mu.Lock()
	defer h.cloudCompute.mu.Unlock()
	if h.cloudCompute.calls["RebootInstances"] != 2 {
		t.Fatal("ambiguous reboot repeated")
	}
}
func TestComputeUnknownLaunchNoResendAndWriteGate(t *testing.T) {
	h := newCompute(t, false)
	ctx := context.Background()
	plan, err := h.serviceCompute.Prepare(ctx, h.op.admin, cp.Request{Targets: []cp.InputTarget{h.target()}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.CanSubmit {
		t.Fatal("write gate bypassed")
	}
	if _, err := h.serviceCompute.Submit(ctx, h.op.admin, plan.ID, uuid.NewString(), auth.RequestMeta{}); err == nil {
		t.Fatal("disabled launch submitted")
	}
}
func TestComputeElasticIPReplacementLostResponses(t *testing.T) {
	h := newCompute(t, true)
	job := h.submit(t, h.target())
	if d := h.op.drive(t, job.ID); d.Job.Status != oc.Succeeded {
		t.Fatal(d.Job.Status)
	}
	listing, err := h.serviceCompute.Instances(context.Background(), h.op.admin, h.connectionA.ID, "us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	id := listing.Items[0].ID
	scope := h.accountA.AWSAccountID + "/us-east-1"
	h.cloudCompute.mu.Lock()
	h.cloudCompute.ips[scope+"/eipalloc-00000001"] = &computeIP{id: "eipalloc-00000001", scope: scope, ip: "127.0.3.1", instance: id, association: "eipassoc-00000001", labels: map[string]string{"Name": "old-ip"}}
	h.cloudCompute.instances[scope+"/"+id].publicIP = "127.0.3.1"
	h.cloudCompute.mode = "lost_addresses"
	h.cloudCompute.mu.Unlock()
	plan, err := h.serviceAddresses.Prepare(context.Background(), h.op.admin, ad.Request{Targets: []ad.Target{{ConnectionID: h.connectionA.ID, Region: "us-east-1", Service: "ec2", Action: "replace", InstanceID: id, ReleasePrevious: true}}})
	if err != nil {
		t.Fatal(err)
	}
	job, err = h.serviceAddresses.Submit(context.Background(), h.op.admin, plan.ID, uuid.NewString(), auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	detail := h.op.drive(t, job.ID)
	if detail.Job.Status != oc.Succeeded {
		t.Fatal("swap failed", detail.Job.Status, detail.Items[0].Steps)
	}
	h.cloudCompute.mu.Lock()
	defer h.cloudCompute.mu.Unlock()
	for _, action := range []string{"AllocateAddress", "AssociateAddress", "ReleaseAddress"} {
		if h.cloudCompute.calls[action] != 1 {
			t.Fatal("EIP repeated", action, h.cloudCompute.calls[action])
		}
	}
	if h.cloudCompute.ips[scope+"/eipalloc-00000001"] != nil {
		t.Fatal("old EIP retained despite choice")
	}
}

func TestComputeDedicatedNetworkLostResponses(t *testing.T) {
	h := newCompute(t, true)
	h.cloudCompute.mode = "lost_network"
	v := cp.InputTarget{Target: cp.Target{ConnectionID: h.connectionA.ID, Region: "us-east-1", Action: "network", Name: "network-fixture", Zone: "us-east-1a", SSHIPv4CIDR: "10.0.0.0/8", SSHIPv6CIDR: "2600:1f18::/32"}}
	job := h.submit(t, v)
	detail := h.op.drive(t, job.ID)
	if detail.Job.Status != oc.Succeeded {
		t.Fatal("network failed", detail.Job.Status, detail.Items[0].Steps)
	}
	h.cloudCompute.mu.Lock()
	for _, action := range []string{"CreateVpc", "CreateSubnet", "CreateInternetGateway", "AttachInternetGateway", "CreateRouteTable", "AssociateRouteTable", "CreateSecurityGroup", "AuthorizeSecurityGroupIngress", "AuthorizeSecurityGroupEgress"} {
		if h.cloudCompute.calls[action] != 1 {
			t.Fatal("network mutation repeated", action, h.cloudCompute.calls[action])
		}
	}
	if h.cloudCompute.calls["CreateRoute"] != 2 {
		t.Fatal("default route count")
	}
	h.cloudCompute.mu.Unlock()
	cat, err := h.serviceCompute.Catalogue(context.Background(), h.op.admin, h.connectionA.ID, "us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Subnets) != 1 || cat.Subnets[0].ID != "subnet-22222222" || !cat.Subnets[0].PublicIPv4Route || !cat.Subnets[0].PublicIPv6Route || len(cat.SecurityGroups) != 1 || len(cat.SecurityGroups[0].SSHIPv6) != 1 {
		t.Fatal("prepared network not reusable", cat.Subnets, cat.SecurityGroups)
	}
}
func TestComputeNetworkPartialFailurePreservesResourceReferences(t *testing.T) {
	h := newCompute(t, true)
	h.cloudCompute.mode = "reject_network_route6"
	v := cp.InputTarget{Target: cp.Target{ConnectionID: h.connectionA.ID, Region: "us-east-1", Action: "network", Name: "network-partial", Zone: "us-east-1a", SSHIPv4CIDR: "10.0.0.0/8", SSHIPv6CIDR: "2600:1f18::/32"}}
	job := h.submit(t, v)
	detail := h.op.drive(t, job.ID)
	if detail.Job.Status != oc.PartialSuccess && detail.Job.Status != oc.Failed {
		t.Fatal(detail.Job.Status)
	}
	steps := detail.Items[0].Steps
	if steps[0].Output.ResourceID != "vpc-22222222" || steps[1].Output.ResourceID != "subnet-22222222" || steps[2].Output.ResourceID != "igw-22222222" {
		t.Fatal("partial network references missing")
	}
	h.cloudCompute.mu.Lock()
	defer h.cloudCompute.mu.Unlock()
	if h.cloudCompute.calls["CreateSecurityGroup"] != 0 || len(h.cloudCompute.networks) != 1 {
		t.Fatal("continued past network failure")
	}
}
func TestComputeAmbiguousLaunchNeverResends(t *testing.T) {
	h := newCompute(t, true)
	h.cloudCompute.mode = "never_accepted"
	job := h.submit(t, h.target())
	for range 5 {
		h.tick(t, job.ID)
	}
	h.cloudCompute.mu.Lock()
	defer h.cloudCompute.mu.Unlock()
	if h.cloudCompute.calls["RunInstances"] != 1 || len(h.cloudCompute.instances) != 0 {
		t.Fatal("uncertain RunInstances repeated")
	}
}

func TestComputeTerminateLostResponseAfterNetworkRemoval(t *testing.T) {
	h := newCompute(t, true)
	job := h.submit(t, h.target())
	if d := h.op.drive(t, job.ID); d.Job.Status != oc.Succeeded {
		t.Fatal(d.Job.Status)
	}
	rows, err := h.serviceCompute.Instances(context.Background(), h.op.admin, h.connectionA.ID, "us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	h.cloudCompute.mu.Lock()
	h.cloudCompute.mode = "lost_manage"
	h.cloudCompute.mu.Unlock()
	job = h.submit(t, cp.InputTarget{Target: cp.Target{ConnectionID: h.connectionA.ID, Region: "us-east-1", Action: "terminate", InstanceID: rows.Items[0].ID}})
	if d := h.op.drive(t, job.ID); d.Job.Status != oc.Succeeded {
		t.Fatal("termination did not reconcile after NIC fields disappeared", d.Job.Status, d.Items)
	}
	h.cloudCompute.mu.Lock()
	defer h.cloudCompute.mu.Unlock()
	if h.cloudCompute.calls["TerminateInstances"] != 1 {
		t.Fatal("terminate repeated")
	}
}
func TestComputeElasticIPAttachFailureRetainsBothAddresses(t *testing.T) {
	h := newCompute(t, true)
	job := h.submit(t, h.target())
	if d := h.op.drive(t, job.ID); d.Job.Status != oc.Succeeded {
		t.Fatal(d.Job.Status)
	}
	rows, err := h.serviceCompute.Instances(context.Background(), h.op.admin, h.connectionA.ID, "us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	scope := h.accountA.AWSAccountID + "/us-east-1"
	id := rows.Items[0].ID
	h.cloudCompute.mu.Lock()
	h.cloudCompute.ips[scope+"/eipalloc-00000001"] = &computeIP{id: "eipalloc-00000001", scope: scope, ip: "127.0.3.1", instance: id, association: "eipassoc-00000001", labels: map[string]string{"Name": "old-ip"}}
	h.cloudCompute.instances[scope+"/"+id].publicIP = "127.0.3.1"
	h.cloudCompute.mode = "attach_rejected"
	h.cloudCompute.mu.Unlock()
	p, err := h.serviceAddresses.Prepare(context.Background(), h.op.admin, ad.Request{Targets: []ad.Target{{ConnectionID: h.connectionA.ID, Region: "us-east-1", Service: "ec2", Action: "replace", InstanceID: id, ReleasePrevious: true}}})
	if err != nil {
		t.Fatal(err)
	}
	job, err = h.serviceAddresses.Submit(context.Background(), h.op.admin, p.ID, uuid.NewString(), auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	d := h.op.drive(t, job.ID)
	if d.Job.Status != oc.Failed && d.Job.Status != oc.PartialSuccess {
		t.Fatal(d.Job.Status)
	}
	h.cloudCompute.mu.Lock()
	defer h.cloudCompute.mu.Unlock()
	if len(h.cloudCompute.ips) != 2 || h.cloudCompute.ips[scope+"/eipalloc-00000001"].instance != id || h.cloudCompute.calls["ReleaseAddress"] != 0 || d.Items[0].Steps[0].Output.ResourceID == "" {
		t.Fatal("attachment failure lost resource ownership or released old address")
	}
}
