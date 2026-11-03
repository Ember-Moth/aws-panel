package integration

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Ember-Moth/aws-panel/internal/auth"
	ac "github.com/Ember-Moth/aws-panel/internal/module/accounts/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/operations"
	oc "github.com/Ember-Moth/aws-panel/internal/module/operations/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/regions"
	rc "github.com/Ember-Moth/aws-panel/internal/module/regions/contract"
	"github.com/google/uuid"
)

func regionServices(t *testing.T, h *inventoryHarness, enabled bool) (operations.Service, regions.Service) {
	t.Helper()
	jobs, err := operations.New(operations.Deps{Store: h.op.store, Access: h.op.members, Connections: inventoryBindings{h.accounts}, Publisher: h.op.queue, CloudWritesEnabled: enabled, RedeliveryAfter: 100 * time.Millisecond, Executors: []oc.Executor{regions.NewExecutor(regions.ExecutorDeps{Clients: h.accounts, Cloud: h.cloud})}})
	if err != nil {
		t.Fatal(err)
	}
	return jobs, regions.New(regions.Deps{Accounts: h.accounts, Access: h.op.members, Jobs: jobs, WritesEnabled: enabled, Cloud: h.cloud})
}
func TestRegionEnablePreviewGateAndAcceptedResponseLoss(t *testing.T) {
	h := newInventory(t)
	ctx := context.Background()
	off, offRegions := regionServices(t, h, false)
	preview, err := offRegions.Prepare(ctx, h.op.admin, rc.EnableRequest{Targets: []rc.Target{{ConnectionID: h.connectionA.ID, Region: "ap-east-1"}, {ConnectionID: h.connectionB.ID, Region: "ap-east-1"}, {ConnectionID: h.connectionA.ID, Region: "us-east-1"}}})
	if err != nil {
		t.Fatal(err)
	}
	if preview.CanSubmit || len(preview.Targets) != 3 {
		t.Fatal("disabled write mode lost preview or enabled submission")
	}
	if _, err := off.Submit(ctx, h.op.admin, oc.SubmitRequest{PlanID: preview.ID, IdempotencyKey: uuid.NewString()}, auth.RequestMeta{}); !errors.Is(err, oc.ErrWritesDisabled) {
		t.Fatal("generic submission bypassed the cloud write switch")
	}
	if h.cloud.enables() != 0 {
		t.Fatal("preview issued a write")
	}
	on, onRegions := regionServices(t, h, true)
	h.op.service = on
	h.cloud.mu.Lock()
	h.cloud.enableModes[h.accountA.AWSAccountID+"/ap-east-1"] = "lost_response"
	h.cloud.mu.Unlock()
	job, err := onRegions.Submit(ctx, h.op.admin, preview.ID, uuid.NewString(), auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.accounts.Delete(ctx, h.op.admin, "connection", h.connectionA.ID, h.connectionA.Version, auth.RequestMeta{}); err == nil {
		t.Fatal("active task connection was deleted")
	}
	if err := on.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	for _, message := range h.op.queue.take() {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if err := on.Run(ctx, message); err != nil {
				t.Error(err)
			}
		}()
	}
	wait.Wait()
	// A restarted instance with writes disabled still reconciles accepted work.
	h.op.service = off
	detail := h.op.drive(t, job.ID)
	if detail.Job.Status != oc.Succeeded || h.cloud.enables() != 2 {
		t.Fatal("region recovery repeated a write or lost success", detail.Job.Status, h.cloud.enables())
	}
	for _, item := range detail.Items {
		if len(item.Steps[0].Attempts) > 1 && item.Steps[0].Attempts[1].Mode != "reconcile" {
			t.Fatal("pending region was not reconciled")
		}
	}
	if err := h.accounts.Delete(ctx, h.op.admin, "connection", h.connectionA.ID, h.connectionA.Version, auth.RequestMeta{}); err != nil {
		t.Fatal("finished task kept a permanent connection reference", err)
	}
}
func TestRegionWriteGateAndReferenceChangeStopUnissuedWork(t *testing.T) {
	h := newInventory(t)
	ctx := context.Background()
	on, service := regionServices(t, h, true)
	h.op.service = on
	plan, err := service.Prepare(ctx, h.op.admin, rc.EnableRequest{Targets: []rc.Target{{ConnectionID: h.connectionA.ID, Region: "ap-east-1"}}})
	if err != nil {
		t.Fatal(err)
	}
	job, err := service.Submit(ctx, h.op.admin, plan.ID, uuid.NewString(), auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	off, _ := regionServices(t, h, false)
	h.op.service = off
	detail := h.op.drive(t, job.ID)
	if detail.Job.Status != oc.Attention || detail.Items[0].Steps[0].Code != "CLOUD_WRITES_DISABLED" || h.cloud.enables() != 0 {
		t.Fatal("queued job bypassed the disabled write gate")
	}
	if err := off.Cancel(ctx, h.op.admin, job.ID, auth.RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	h.op.service = on
	plan, err = service.Prepare(ctx, h.op.admin, rc.EnableRequest{Targets: []rc.Target{{ConnectionID: h.connectionA.ID, Region: "ap-south-2"}}})
	if err != nil {
		t.Fatal(err)
	}
	job, err = service.Submit(ctx, h.op.admin, plan.ID, uuid.NewString(), auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	active := true
	changed, err := h.accounts.SaveConnection(ctx, h.op.admin, h.accountA.ID, h.connectionA.ID, ac.ConnectionRequest{Name: "Route changed", AuthType: "access_key", Region: "us-east-1", RouteMode: "direct", Active: &active, Version: h.connectionA.Version}, auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.accounts.Delete(ctx, h.op.admin, "proxy", h.proxyA.ID, h.proxyA.Version, auth.RequestMeta{}); err == nil {
		t.Fatal("task's former proxy was deleted after rebinding")
	}
	detail = h.op.drive(t, job.ID)
	if detail.Job.Status != oc.Attention || h.cloud.enables() != 0 {
		t.Fatal("changed connection was silently used for a cloud write")
	}
	if err := on.Cancel(ctx, h.op.admin, job.ID, auth.RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	if err := h.accounts.Delete(ctx, h.op.admin, "connection", changed.ID, changed.Version, auth.RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	if err := h.accounts.Delete(ctx, h.op.admin, "proxy", h.proxyA.ID, h.proxyA.Version, auth.RequestMeta{}); err != nil {
		t.Fatal(err)
	}
}
func TestRegionSubmitAndConnectionRemovalAreSerialized(t *testing.T) {
	h := newInventory(t)
	ctx := context.Background()
	_, service := regionServices(t, h, true)
	plan, err := service.Prepare(ctx, h.op.admin, rc.EnableRequest{Targets: []rc.Target{{ConnectionID: h.connectionA.ID, Region: "ap-east-1"}}})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	submitted, removed := make(chan error, 1), make(chan error, 1)
	go func() {
		<-start
		_, err := service.Submit(ctx, h.op.admin, plan.ID, uuid.NewString(), auth.RequestMeta{})
		submitted <- err
	}()
	go func() {
		<-start
		removed <- h.accounts.Delete(ctx, h.op.admin, "connection", h.connectionA.ID, h.connectionA.Version, auth.RequestMeta{})
	}()
	close(start)
	submitErr, removeErr := <-submitted, <-removed
	if submitErr == nil && removeErr == nil {
		t.Fatal("task submission raced past connection deletion")
	}
	if submitErr != nil && removeErr != nil && !strings.Contains(submitErr.Error(), "连接") {
		t.Fatal("unexpected serialized result", submitErr, removeErr)
	}
	if h.cloud.enables() != 0 {
		t.Fatal("reference validation issued a cloud write")
	}
}
