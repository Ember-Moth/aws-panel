package integration

import (
	"context"
	"github.com/Ember-Moth/aws-panel/internal/auth"
	iv "github.com/Ember-Moth/aws-panel/internal/module/inventory/contract"
	oc "github.com/Ember-Moth/aws-panel/internal/module/operations/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/quotas"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestComputeInventorySnapshotsPreserveOnFailure(t *testing.T) {
	h := newCompute(t, true)
	ctx := context.Background()
	job := h.submit(t, h.target())
	if d := h.op.drive(t, job.ID); d.Job.Status != oc.Succeeded {
		t.Fatal(d.Job.Status)
	}
	scope := h.accountA.AWSAccountID + "/us-east-1"
	h.cloudCompute.mu.Lock()
	h.cloudCompute.ips[scope+"/eipalloc-00000001"] = &computeIP{id: "eipalloc-00000001", scope: scope, ip: "127.0.3.1", labels: map[string]string{"Name": "inventory-ip"}}
	h.cloudCompute.mu.Unlock()
	request := iv.RefreshRequest{Targets: []iv.RefreshTarget{{ConnectionID: h.connectionA.ID, Region: "us-east-1", Kind: iv.EC2Instance}, {ConnectionID: h.connectionA.ID, Region: "us-east-1", Kind: iv.EC2Address}}, IdempotencyKey: uuid.NewString()}
	job, err := h.service.Refresh(ctx, h.op.admin, request, auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if d := h.op.drive(t, job.ID); d.Job.Status != oc.Succeeded {
		t.Fatal(d.Job.Status, d.Items)
	}
	rows, err := h.service.Resources(ctx, h.op.admin, iv.Filter{Page: 1, Kind: iv.EC2Instance})
	if err != nil || len(rows.Items) != 1 || rows.Items[0].Details.EC2 == nil {
		t.Fatal("missing EC2 snapshot", err, rows.Items)
	}
	observed := rows.Items[0].ObservedAt
	addresses, err := h.service.Resources(ctx, h.op.admin, iv.Filter{Page: 1, Kind: iv.EC2Address})
	if err != nil || len(addresses.Items) != 2 || addresses.Items[0].Details.Address == nil {
		t.Fatal("missing address snapshot", err)
	}
	attached := 0
	for _, row := range addresses.Items {
		if row.Details.Address == nil {
			t.Fatal("missing address details")
		}
		if row.Details.Address.Attached {
			attached++
		}
	}
	if attached != 1 {
		t.Fatal("automatic instance IP and spare address not distinguished")
	}
	h.cloudCompute.mu.Lock()
	h.cloudCompute.mode = "read_denied"
	h.cloudCompute.mu.Unlock()
	request.Targets = request.Targets[:1]
	request.IdempotencyKey = uuid.NewString()
	job, err = h.service.Refresh(ctx, h.op.admin, request, auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if d := h.op.drive(t, job.ID); d.Job.Status != oc.Failed {
		t.Fatal("read failure ignored", d.Job.Status)
	}
	rows, err = h.service.Resources(ctx, h.op.admin, iv.Filter{Page: 1, Kind: iv.EC2Instance})
	if err != nil || len(rows.Items) != 1 || !rows.Items[0].Present || !rows.Items[0].ObservedAt.Equal(observed) {
		t.Fatal("failed refresh destroyed snapshot", err)
	}
}
func TestQuotaEC2CapacityReservationsAndBYOIP(t *testing.T) {
	h := newCompute(t, true)
	ctx := context.Background()
	scope := h.accountA.AWSAccountID + "/us-east-1"
	stamp := time.Now().UTC().Format(time.RFC3339)
	h.cloudCompute.mode = "capacity"
	for _, v := range []*computeInstance{{id: "i-00000001", state: "running", reservation: "cr-11111111"}, {id: "i-00000002", state: "running"}, {id: "i-00000003", state: "stopped"}, {id: "i-00000004", state: "pending"}, {id: "i-00000005", state: "running", lifecycle: "spot"}} {
		v.scope = scope
		v.kind = "t3.micro"
		v.image = "ami-11111111"
		v.subnet = "subnet-11111111"
		v.stamp = stamp
		v.labels = map[string]string{}
		h.cloudCompute.instances[scope+"/"+v.id] = v
	}
	h.cloudCompute.ips[scope+"/eipalloc-00000001"] = &computeIP{id: "eipalloc-00000001", scope: scope, ip: "127.0.3.1", pool: "amazon"}
	h.cloudCompute.ips[scope+"/eipalloc-00000002"] = &computeIP{id: "eipalloc-00000002", scope: scope, ip: "127.0.3.2", pool: "ipv4pool-ec2-11111111"}
	cfg, err := h.accounts.ConfigFor(ctx, h.op.admin, h.connectionA.ID, "us-east-1", false)
	if err != nil {
		t.Fatal(err)
	}
	report, err := quotas.NewCloud().Read(ctx, addressConfig(cfg), h.accountA.AWSAccountID, "ec2")
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, q := range report.Entries {
		switch q.Key {
		case "ec2.ondemand.standard":
			found++
			if !q.LimitKnown || !q.UsageKnown || q.Used != 6 {
				t.Fatal("reservation usage incorrect", q)
			}
		case "ec2.elastic_ips":
			found++
			if !q.UsageKnown || q.Used != 1 {
				t.Fatal("BYOIP counted against AWS pool", q)
			}
		}
	}
	if found != 2 {
		t.Fatal("missing quota entries")
	}
	h.cloudCompute.mu.Lock()
	h.cloudCompute.mode = "read_denied"
	h.cloudCompute.mu.Unlock()
	report, err = quotas.NewCloud().Read(ctx, addressConfig(cfg), h.accountA.AWSAccountID, "ec2")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range report.Entries {
		if q.Key == "ec2.ondemand.standard" && q.UsageKnown {
			t.Fatal("usage denied became zero", q)
		}
	}
}
func TestLightsailStaticIPInventory(t *testing.T) {
	h := newInventory(t)
	cloud := newAddressAWS()
	h.cloud.extra = cloud.handle
	job := h.refresh(t, iv.RefreshTarget{ConnectionID: h.connectionA.ID, Region: "us-east-1", Kind: iv.LightsailStaticIP})
	if d := h.op.drive(t, job.ID); d.Job.Status != oc.Succeeded {
		t.Fatal(d.Job.Status, d.Items)
	}
	rows, err := h.service.Resources(context.Background(), h.op.admin, iv.Filter{Page: 1, Kind: iv.LightsailStaticIP})
	if err != nil || len(rows.Items) != 1 || rows.Items[0].Details.Address == nil || !rows.Items[0].Details.Address.Attached {
		t.Fatal("missing Lightsail static IP snapshot", err)
	}
}
