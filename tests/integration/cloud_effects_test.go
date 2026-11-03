package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Ember-Moth/aws-panel/internal/auth"
	"github.com/Ember-Moth/aws-panel/internal/module/operations"
	oc "github.com/Ember-Moth/aws-panel/internal/module/operations/contract"
	"github.com/Ember-Moth/aws-panel/internal/repository"
	"github.com/google/uuid"
)

type networkLockInput struct{ Instance, Address string }
type networkLockExecutor struct{}

func (networkLockExecutor) Definition() oc.Definition {
	return oc.Definition{ID: "fixture.network", Version: 1, InputVersion: 1, Title: "network locks", WritesCloud: true, MaxAttempts: 4, Deadline: time.Minute, CallTimeout: 2 * time.Second}
}
func (networkLockExecutor) Validate(raw json.RawMessage) (json.RawMessage, error) {
	var v networkLockInput
	if json.Unmarshal(raw, &v) != nil || v.Instance == "" || v.Address == "" {
		return nil, oc.ErrInvalid
	}
	return json.Marshal(v)
}
func (networkLockExecutor) Plan(_ context.Context, raw json.RawMessage, targets []oc.Target) ([]oc.ItemPlan, error) {
	var v networkLockInput
	_ = json.Unmarshal(raw, &v)
	target := targets[0]
	target.LockKey = "instance/" + v.Instance
	target.LockKeys = []string{"address/" + v.Address}
	return []oc.ItemPlan{{Target: target, Steps: []oc.StepPlan{{Kind: "write", Label: "write", Input: raw, WritesCloud: true}}}}, nil
}
func (networkLockExecutor) Execute(context.Context, oc.Call) oc.Result {
	return oc.Result{State: oc.Succeeded}
}
func (networkLockExecutor) Reconcile(context.Context, oc.Call) oc.Result {
	return oc.Result{State: oc.Succeeded}
}
func TestOperationsMultipleResourceLocks(t *testing.T) {
	h := newInventory(t)
	ctx := context.Background()
	jobs, err := operations.New(operations.Deps{Store: h.op.store, Access: h.op.members, Connections: inventoryBindings{h.accounts}, Publisher: h.op.queue, CloudWritesEnabled: true, Executors: []oc.Executor{networkLockExecutor{}}})
	if err != nil {
		t.Fatal(err)
	}
	plan := func(connection, instance, address string) oc.Plan {
		raw, _ := json.Marshal(networkLockInput{instance, address})
		value, err := jobs.Prepare(ctx, h.op.admin, oc.PlanRequest{Operation: "fixture.network", InputVersion: 1, Input: raw, Targets: []oc.Target{{Key: instance, Label: instance, Binding: oc.Binding{ConnectionID: connection, Region: "us-east-1"}}}})
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	submit := func(value oc.Plan) (oc.Job, error) {
		return jobs.Submit(ctx, h.op.admin, oc.SubmitRequest{PlanID: value.ID, IdempotencyKey: uuid.NewString()}, auth.RequestMeta{})
	}
	a := plan(h.connectionA.ID, "one", "shared-ip")
	b := plan(h.connectionA.ID, "two", "shared-ip")
	first, err := submit(a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := submit(b); !errors.Is(err, oc.ErrTargetBusy) {
		t.Fatal("secondary address lock did not prevent conflicting attachment", err)
	}
	if _, err := submit(plan(h.connectionA.ID, "one", "other-ip")); err == nil {
		t.Fatal("instance lock did not prevent two simultaneous switches")
	}
	other, err := submit(plan(h.connectionB.ID, "one", "shared-ip"))
	if err != nil {
		t.Fatal("resource locks crossed account boundaries", err)
	}
	if err := jobs.Cancel(ctx, h.op.admin, other.ID, auth.RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	if err := jobs.Cancel(ctx, h.op.admin, first.ID, auth.RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	if _, err := submit(b); err != nil {
		t.Fatal("cancelled item retained its secondary resource lock", err)
	}
}

type effectFixtureExecutor struct {
	networkLockExecutor
	store repository.EffectStore
	t     *testing.T
	sends atomic.Int32
	last  oc.EffectLease
}

func (e *effectFixtureExecutor) Execute(ctx context.Context, call oc.Call) oc.Result {
	sum := sha256.Sum256(call.Input)
	lease := oc.EffectLease{StepID: call.StepID, Owner: call.LeaseOwner, Epoch: call.LeaseEpoch}
	e.last = lease
	row, err := e.store.PrepareEffect(ctx, oc.Effect{StepID: call.StepID, JobID: call.JobID, AccountID: call.Target.Binding.AccountID, Region: call.Target.Binding.Region, Purpose: "fixture.network", SpecHash: hex.EncodeToString(sum[:]), Token: call.Token}, lease)
	if err != nil {
		e.t.Error(err)
		return oc.Result{State: oc.Attention}
	}
	row, err = e.store.DispatchEffect(ctx, row.StepID, lease)
	if err != nil {
		e.t.Error(err)
		return oc.Result{State: oc.Attention}
	}
	if _, err := e.store.DispatchEffect(ctx, row.StepID, lease); !errors.Is(err, oc.ErrEffect) {
		e.t.Error("dispatched effect allowed another send", err)
	}
	e.sends.Add(1)
	return oc.Result{State: oc.Waiting, After: time.Second}
}
func (e *effectFixtureExecutor) Reconcile(ctx context.Context, call oc.Call) oc.Result {
	row, err := e.store.Effect(ctx, call.StepID)
	if err != nil || row == nil {
		e.t.Error("receipt missing", err)
		return oc.Result{State: oc.Attention}
	}
	row.State = "confirmed"
	row.ResourceID = "fixture/" + row.Token
	row.Proof = `{"observed":true}`
	row.ResourceLocks = []string{"address-created/" + row.Token}
	lease := oc.EffectLease{StepID: call.StepID, Owner: call.LeaseOwner, Epoch: call.LeaseEpoch}
	if err := e.store.RecordEffect(ctx, *row, lease); err != nil {
		e.t.Error(err)
		return oc.Result{State: oc.Attention}
	}
	if _, err := e.store.DispatchEffect(ctx, row.StepID, lease); !errors.Is(err, oc.ErrEffect) {
		e.t.Error("confirmed effect allowed another send", err)
	}
	return oc.Result{State: oc.Succeeded, Output: oc.Output{ResourceID: row.ResourceID}}
}
func TestOperationsEffectReceiptRecoveryAndLeaseFence(t *testing.T) {
	h := newInventory(t)
	ctx := context.Background()
	store := operations.NewEffectStore(h.runtime)
	executor := &effectFixtureExecutor{store: store, t: t}
	jobs, err := operations.New(operations.Deps{Store: h.op.store, Access: h.op.members, Connections: inventoryBindings{h.accounts}, Publisher: h.op.queue, CloudWritesEnabled: true, Executors: []oc.Executor{executor}})
	if err != nil {
		t.Fatal(err)
	}
	h.op.service = jobs
	plan, err := jobs.Prepare(ctx, h.op.admin, oc.PlanRequest{Operation: "fixture.network", InputVersion: 1, Input: json.RawMessage(`{"Instance":"instance-a","Address":"address-a"}`), Targets: []oc.Target{{Key: "target", Label: "target", Binding: oc.Binding{ConnectionID: h.connectionA.ID, Region: "us-east-1"}}}})
	if err != nil {
		t.Fatal(err)
	}
	job, err := jobs.Submit(ctx, h.op.admin, oc.SubmitRequest{PlanID: plan.ID, IdempotencyKey: uuid.NewString()}, auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	detail := h.op.drive(t, job.ID)
	if detail.Job.Status != oc.Succeeded || executor.sends.Load() != 1 {
		t.Fatal("effect was resent instead of reconciled", detail.Job.Status)
	}
	row, err := store.Effect(ctx, detail.Items[0].Steps[0].ID)
	if err != nil || row == nil || row.State != "confirmed" || row.SendCount != 1 {
		t.Fatal("receipt outcome missing", err)
	}
	var locks int
	if err := h.op.owner.QueryRow("SELECT jsonb_array_length(lock_keys) FROM job_items WHERE id=$1", detail.Items[0].ID).Scan(&locks); err != nil || locks != 3 {
		t.Fatal("newly identified resource was not included in item locks", locks, err)
	}
	if err := store.RecordEffect(ctx, *row, executor.last); !errors.Is(err, oc.ErrLease) {
		t.Fatal("expired executor altered effect receipt", err)
	}
}
