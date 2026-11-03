package integration

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/herta-network/aws-panel/internal/auth"
	ad "github.com/herta-network/aws-panel/internal/module/addresses/contract"
	oc "github.com/herta-network/aws-panel/internal/module/operations/contract"
	"testing"
)

func TestAddressesReplaceLostResponsesPreservesThenReleasesOld(t *testing.T) {
	h := newAddresses(t, true)
	h.cloudAddresses.mode = "lost_responses"
	target := h.target("replace")
	target.InstanceID = "instance-a"
	target.ReleasePrevious = true
	job := h.submit(t, target)
	detail := h.op.drive(t, job.ID)
	if detail.Job.Status != oc.Succeeded {
		t.Fatal("replace did not reconcile", detail.Job.Status, detail.Items[0].Steps)
	}
	h.cloudAddresses.mu.Lock()
	defer h.cloudAddresses.mu.Unlock()
	for _, action := range []string{"AllocateStaticIp", "DetachStaticIp", "AttachStaticIp", "ReleaseStaticIp"} {
		if h.cloudAddresses.calls[action] != 1 {
			t.Fatal("write repeated or missing", action, h.cloudAddresses.calls[action])
		}
	}
	scope := h.accountA.AWSAccountID + "/us-east-1/"
	if h.cloudAddresses.ips[scope+"old-ip"] != nil {
		t.Fatal("old address not released after confirmed replacement")
	}
	if len(detail.Items[0].Steps) != 4 || detail.Items[0].Steps[0].Output.ResourceID == "" || detail.Items[0].Steps[1].Output.ResourceID == "" {
		t.Fatal("replacement lost resource references")
	}
}
func TestAddressesQuotaAndDefaultReleaseOld(t *testing.T) {
	h := newAddresses(t, true)
	ctx := context.Background()
	h.cloudAddresses.quota = 1
	target := h.target("replace")
	target.InstanceID = "instance-a"
	if _, err := h.serviceAddresses.Prepare(ctx, h.op.admin, ad.Request{Targets: []ad.Target{target}}); !errors.Is(err, ad.ErrQuota) {
		t.Fatal("replacement did not require spare quota", err)
	}
	h.cloudAddresses.mu.Lock()
	h.cloudAddresses.quota = 5
	h.cloudAddresses.mu.Unlock()
	job := h.submit(t, target)
	if detail := h.op.drive(t, job.ID); detail.Job.Status != oc.Succeeded {
		t.Fatal("replacement failed", detail.Job.Status)
	}
	h.cloudAddresses.mu.Lock()
	defer h.cloudAddresses.mu.Unlock()
	old := h.cloudAddresses.ips[h.accountA.AWSAccountID+"/us-east-1/old-ip"]
	if old != nil || h.cloudAddresses.calls["ReleaseStaticIp"] != 1 {
		t.Fatal("default replacement did not release the old address")
	}
}
func TestAddressesAsyncAttachFailureDoesNotReleaseOld(t *testing.T) {
	h := newAddresses(t, true)
	h.cloudAddresses.mode = "pending_attach"
	target := h.target("replace")
	target.InstanceID = "instance-a"
	target.ReleasePrevious = true
	job := h.submit(t, target)
	for range 12 {
		h.tick(t, job.ID)
		h.cloudAddresses.mu.Lock()
		started := h.cloudAddresses.calls["AttachStaticIp"] > 0
		h.cloudAddresses.mu.Unlock()
		if started {
			break
		}
	}
	h.tick(t, job.ID)
	h.cloudAddresses.mu.Lock()
	if h.cloudAddresses.calls["ReleaseStaticIp"] != 0 {
		t.Fatal("pending attachment released the previous address")
	}
	for _, op := range h.cloudAddresses.operations {
		if op["operationType"] == "AttachStaticIp" {
			op["isTerminal"] = true
			op["status"] = "Failed"
		}
	}
	h.cloudAddresses.mu.Unlock()
	detail := h.op.drive(t, job.ID)
	if detail.Job.Status != oc.Failed {
		t.Fatal("terminal async failure ignored", detail.Job.Status)
	}
	h.cloudAddresses.mu.Lock()
	defer h.cloudAddresses.mu.Unlock()
	if h.cloudAddresses.ips[h.accountA.AWSAccountID+"/us-east-1/old-ip"] == nil || h.cloudAddresses.calls["ReleaseStaticIp"] != 0 {
		t.Fatal("failure discarded the previous address")
	}
}
func TestAddressesUncertainAllocationNeverResendsAndWriteGate(t *testing.T) {
	h := newAddresses(t, true)
	h.cloudAddresses.mode = "drop_allocate"
	target := h.target("allocate")
	job := h.submit(t, target)
	for range 4 {
		h.tick(t, job.ID)
	}
	h.cloudAddresses.mu.Lock()
	if h.cloudAddresses.calls["AllocateStaticIp"] != 1 {
		t.Fatal("unconfirmed allocation resent")
	}
	h.cloudAddresses.mu.Unlock()
	if _, err := h.op.owner.Exec("UPDATE job_steps SET deadline_at=clock_timestamp()-interval '1 second' WHERE job_id=$1", job.ID); err != nil {
		t.Fatal(err)
	}
	if detail := h.op.drive(t, job.ID); detail.Job.Status != oc.Attention {
		t.Fatal("unconfirmed allocation reported a definite outcome")
	}
}
func TestAddressesReleaseBoundRejectedAndSubmissionGate(t *testing.T) {
	h := newAddresses(t, false)
	ctx := context.Background()
	target := h.target("release")
	target.AddressID = "old-ip"
	if _, err := h.serviceAddresses.Prepare(ctx, h.op.admin, ad.Request{Targets: []ad.Target{target}}); !errors.Is(err, ad.ErrAttached) {
		t.Fatal("bound address could be released", err)
	}
	target = h.target("allocate")
	preview, err := h.serviceAddresses.Prepare(ctx, h.op.admin, ad.Request{Targets: []ad.Target{target}})
	if err != nil {
		t.Fatal(err)
	}
	if preview.CanSubmit {
		t.Fatal("write-disabled preview enabled submission")
	}
	_, err = h.serviceAddresses.Submit(ctx, h.op.admin, preview.ID, uuid.NewString(), auth.RequestMeta{})
	var fault *ad.Error
	if !errors.As(err, &fault) || fault.Code != "CLOUD_WRITES_DISABLED" {
		t.Fatal("address write switch bypassed", err)
	}
}
