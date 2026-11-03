package integration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/herta-network/aws-panel/internal/auth"
	lc "github.com/herta-network/aws-panel/internal/module/lightsail/contract"
	le "github.com/herta-network/aws-panel/internal/module/lightsail/entity"
	oc "github.com/herta-network/aws-panel/internal/module/operations/contract"
	"github.com/herta-network/aws-panel/internal/repository"
)

func (h *launchHarness) tick(t *testing.T, id string) {
	t.Helper()
	h.op.expedite(t, id)
	ctx := context.Background()
	if err := h.op.service.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if err := h.op.service.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	for _, message := range h.op.queue.take() {
		if err := h.op.service.Run(ctx, message); err != nil {
			t.Fatal(err)
		}
	}
}
func (h *launchHarness) count(name string) int {
	h.cloudLaunch.mu.Lock()
	defer h.cloudLaunch.mu.Unlock()
	return h.cloudLaunch.creates[name]
}
func (h *launchHarness) mode(name, mode string) {
	h.cloudLaunch.mu.Lock()
	defer h.cloudLaunch.mu.Unlock()
	h.cloudLaunch.modes[name] = mode
}
func TestLightsailCatalogueCompatibilityQuotaAndWriteGate(t *testing.T) {
	h := newLaunch(t, false)
	ctx := context.Background()
	catalogue, err := h.serviceLaunch.Catalogue(ctx, h.op.admin, h.connectionA.ID, "us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(catalogue.Blueprints) != 1 || len(catalogue.Bundles) != 2 || len(catalogue.Zones) != 1 || !catalogue.Quota.Known || catalogue.Bundles[0].MonthlyCents != 350 || catalogue.Bundles[1].MonthlyCents != 500 {
		t.Fatal("catalogue filtering, paging or price mismatch", catalogue)
	}
	preview, err := h.serviceLaunch.Prepare(ctx, h.op.admin, lc.CreateRequest{Targets: []lc.CreateTarget{h.target("preview-one"), h.target("preview-two")}})
	if err != nil {
		t.Fatal(err)
	}
	if preview.CanSubmit || preview.MonthlyCents != 1000 {
		t.Fatal("preview quote or write gate mismatch")
	}
	_, err = h.serviceLaunch.Submit(ctx, h.op.admin, preview.ID, uuid.NewString(), auth.RequestMeta{})
	var fault *lc.Error
	if !errors.As(err, &fault) || fault.Code != "CLOUD_WRITES_DISABLED" {
		t.Fatal("write gate bypass", err)
	}
	invalid := h.target("invalid-ip")
	invalid.IPType = "ipv6"
	if _, err = h.serviceLaunch.Prepare(ctx, h.op.admin, lc.CreateRequest{Targets: []lc.CreateTarget{invalid}}); !errors.Is(err, lc.ErrCatalogue) {
		t.Fatal("incompatible IP type accepted", err)
	}
	h.cloudLaunch.mu.Lock()
	h.cloudLaunch.quota = 3
	h.cloudLaunch.mu.Unlock()
	if _, err = h.serviceLaunch.Prepare(ctx, h.op.admin, lc.CreateRequest{Targets: []lc.CreateTarget{h.target("quota-one"), h.target("quota-two")}}); !errors.Is(err, lc.ErrQuota) {
		t.Fatal("batch quota not checked", err)
	}
	h.cloudLaunch.mu.Lock()
	h.cloudLaunch.quotaDenied = true
	h.cloudLaunch.mu.Unlock()
	catalogue, err = h.serviceLaunch.Catalogue(ctx, h.op.admin, h.connectionA.ID, "us-east-1")
	if err != nil || catalogue.Quota.Known || catalogue.Quota.Code != "QUOTA_UNAVAILABLE" {
		t.Fatal("unknown quota misrepresented", err)
	}
	if h.count("preview-one") != 0 {
		t.Fatal("preview sent a write")
	}
}
func TestLightsailCreateReconcilesLostResponsesAndSubmissionDuplicates(t *testing.T) {
	h := newLaunch(t, true)
	ctx := context.Background()
	h.mode("lost-response", "lost_response")
	h.cloud.mu.Lock()
	h.cloud.optStates[h.accountB.AWSAccountID+"/us-west-2"] = "ENABLED_BY_DEFAULT"
	h.cloud.mu.Unlock()
	second := h.target("second-account")
	second.ConnectionID = h.connectionB.ID
	second.Region = "us-west-2"
	second.Zone = "us-west-2a"
	plan, err := h.serviceLaunch.Prepare(ctx, h.op.admin, lc.CreateRequest{Targets: []lc.CreateTarget{h.target("normal-one"), h.target("lost-response"), second}})
	if err != nil {
		t.Fatal(err)
	}
	key := uuid.NewString()
	job, err := h.serviceLaunch.Submit(ctx, h.op.admin, plan.ID, key, auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := h.serviceLaunch.Submit(ctx, h.op.admin, plan.ID, key, auth.RequestMeta{})
	if err != nil || repeated.ID != job.ID {
		t.Fatal("duplicate submission", err)
	}
	detail := h.op.drive(t, job.ID)
	if detail.Job.Status != oc.Succeeded {
		raw, _ := json.Marshal(detail)
		t.Fatal(string(raw))
	}
	for _, item := range detail.Items {
		if len(item.Steps) != 2 || item.Steps[0].Output.ResourceID == "" || item.Steps[1].Code != "INSTANCE_RUNNING" || item.Steps[1].Output.ResourceID != item.Steps[0].Output.ResourceID {
			t.Fatal("existence and running states not preserved")
		}
		row, err := h.creationStore.Creation(ctx, item.Steps[0].ID)
		if err != nil || row.State != "identified" || row.SendCount != 1 || h.count(row.Name) != 1 {
			t.Fatal("reconciliation repeated create", row, err)
		}
		if len(item.Steps[0].Attempts) < 2 || item.Steps[0].Attempts[1].Mode != "reconcile" {
			t.Fatal("create not reconciled")
		}
	}
	if _, err := h.serviceLaunch.Prepare(ctx, h.op.admin, lc.CreateRequest{Targets: []lc.CreateTarget{h.target("normal-one")}}); !errors.Is(err, lc.ErrConflict) {
		t.Fatal("existing name adopted", err)
	}
}
func TestLightsailUncertainDispatchNeverAutomaticallyResends(t *testing.T) {
	h := newLaunch(t, true)
	h.mode("unknown-result", "drop_before_accept")
	job := h.submit(t, h.target("unknown-result"))
	for range 4 {
		h.tick(t, job.ID)
	}
	if h.count("unknown-result") != 1 {
		t.Fatal("unknown outcome resent create")
	}
	if _, err := h.op.owner.Exec("UPDATE job_steps SET deadline_at=clock_timestamp()-interval '1 second' WHERE job_id=$1", job.ID); err != nil {
		t.Fatal(err)
	}
	detail := h.op.drive(t, job.ID)
	if detail.Job.Status != oc.Attention || detail.Items[0].Steps[0].Code != "RESULT_UNCONFIRMED" {
		t.Fatal("unconfirmed creation reported final failure", detail.Job.Status)
	}
	err := h.op.service.Retry(context.Background(), h.op.admin, job.ID, oc.RetryRequest{Version: detail.Job.Version, StepIDs: []string{detail.Items[0].Steps[0].ID}}, auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		h.tick(t, job.ID)
	}
	if h.count("unknown-result") != 1 {
		t.Fatal("manual resume blindly resent create")
	}
}
func TestLightsailNameRaceOwnershipAndPriceChange(t *testing.T) {
	h := newLaunch(t, true)
	ctx := context.Background()
	job := h.submit(t, h.target("price-changed"))
	h.cloudLaunch.mu.Lock()
	h.cloudLaunch.price = 6
	h.cloudLaunch.mu.Unlock()
	detail := h.op.drive(t, job.ID)
	if detail.Job.Status != oc.Failed || detail.Items[0].Steps[0].Code != "CATALOGUE_CHANGED" || h.count("price-changed") != 0 {
		t.Fatal("higher price silently accepted", detail.Job.Status)
	}
	h.cloudLaunch.mu.Lock()
	h.cloudLaunch.price = 5
	h.cloudLaunch.mu.Unlock()
	h.mode("wrong-instance", "wrong_spec")
	wrong := h.submit(t, h.target("wrong-instance"))
	detail = h.op.drive(t, wrong.ID)
	if detail.Job.Status != oc.Attention || detail.Items[0].Steps[0].Code != "OWNERSHIP_UNCONFIRMED" || detail.Items[0].Steps[0].Output.ResourceID != "" {
		t.Fatal("foreign parameters adopted")
	}
	// A second plan created before the first resource exists must still check its name at execution.
	plan, err := h.serviceLaunch.Prepare(ctx, h.op.admin, lc.CreateRequest{Targets: []lc.CreateTarget{h.target("race-name")}})
	if err != nil {
		t.Fatal(err)
	}
	first := h.submit(t, h.target("race-name"))
	if h.op.drive(t, first.ID).Job.Status != oc.Succeeded {
		t.Fatal("setup creation failed")
	}
	second, err := h.serviceLaunch.Submit(ctx, h.op.admin, plan.ID, uuid.NewString(), auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	detail = h.op.drive(t, second.ID)
	if detail.Job.Status != oc.Failed || detail.Items[0].Steps[0].Code != "NAME_CONFLICT" || h.count("race-name") != 1 {
		t.Fatal("name race adopted existing instance")
	}
}
func TestLightsailPartialFailureSelectiveRetryAndResourceRetention(t *testing.T) {
	h := newLaunch(t, true)
	ctx := context.Background()
	h.mode("retry-target", "reject")
	job := h.submit(t, h.target("good-target"), h.target("retry-target"))
	detail := h.op.drive(t, job.ID)
	if detail.Job.Status != oc.PartialSuccess {
		t.Fatal("expected partial success", detail.Job.Status)
	}
	var failedID string
	for _, item := range detail.Items {
		if item.Steps[0].Status == oc.Failed {
			failedID = item.Steps[0].ID
		}
	}
	raw, _ := json.Marshal(detail)
	if strings.Contains(string(raw), testSecretA) {
		t.Fatal("raw AWS error leaked")
	}
	h.mode("retry-target", "")
	if err := h.op.service.Retry(ctx, h.op.admin, job.ID, oc.RetryRequest{Version: detail.Job.Version, StepIDs: []string{failedID}}, auth.RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	detail = h.op.drive(t, job.ID)
	if detail.Job.Status != oc.Succeeded || h.count("good-target") != 1 || h.count("retry-target") != 2 {
		t.Fatal("retry repeated successful target or failed to recover", detail.Job.Status)
	}
	h.mode("failed-resource", "failed_with_resource")
	failed := h.submit(t, h.target("failed-resource"))
	detail = h.op.drive(t, failed.ID)
	if detail.Job.Status != oc.Failed || detail.Items[0].Steps[0].Code != "CREATE_FAILED_RESOURCE_PRESENT" || detail.Items[0].Steps[0].Output.ResourceID == "" {
		t.Fatal("failure lost resource reference")
	}
	if err := h.op.service.Retry(ctx, h.op.admin, failed.ID, oc.RetryRequest{Version: detail.Job.Version, StepIDs: []string{detail.Items[0].Steps[0].ID}}, auth.RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	_ = h.op.drive(t, failed.ID)
	if h.count("failed-resource") != 1 {
		t.Fatal("failed existing resource recreated")
	}
}

type receiptFault struct {
	repository.LightsailStore
	dispatch atomic.Bool
	record   atomic.Bool
}

func (f *receiptFault) DispatchCreation(ctx context.Context, id string, lease le.Lease) (le.Creation, error) {
	row, err := f.LightsailStore.DispatchCreation(ctx, id, lease)
	if err == nil && f.dispatch.Swap(false) {
		return le.Creation{}, lc.ErrUnavailable
	}
	return row, err
}
func (f *receiptFault) RecordCreation(ctx context.Context, row le.Creation, lease le.Lease) error {
	if f.record.Swap(false) {
		return lc.ErrUnavailable
	}
	return f.LightsailStore.RecordCreation(ctx, row, lease)
}
func TestLightsailDurableDispatchAndReceiptRecoveryWithWritesOff(t *testing.T) {
	h := newLaunch(t, true)
	ctx := context.Background()
	fault := &receiptFault{LightsailStore: h.creationStore}
	fault.record.Store(true)
	h.services(t, fault, true)
	job := h.submit(t, h.target("receipt-lost"))
	h.tick(t, job.ID)
	if h.count("receipt-lost") != 1 {
		t.Fatal("initial dispatch missing")
	}
	// The process restarts without the ability to send new writes. Tags and
	// operation history must suffice when saving the accepted response failed.
	h.services(t, h.creationStore, false)
	detail := h.op.drive(t, job.ID)
	if detail.Job.Status != oc.Succeeded || h.count("receipt-lost") != 1 {
		t.Fatal("read-only receipt recovery failed", detail.Job.Status)
	}
	row, err := h.creationStore.Creation(ctx, detail.Items[0].Steps[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.creationStore.RecordCreation(ctx, *row, le.Lease{StepID: row.StepID, Owner: "expired-worker", Epoch: 1}); !errors.Is(err, lc.ErrLease) {
		t.Fatal("stale worker changed receipt", err)
	}
	fault = &receiptFault{LightsailStore: h.creationStore}
	fault.dispatch.Store(true)
	h.services(t, fault, true)
	unsent := h.submit(t, h.target("dispatch-commit-lost"))
	for range 4 {
		h.tick(t, unsent.ID)
	}
	if h.count("dispatch-commit-lost") != 0 {
		t.Fatal("unconfirmed dispatch commit allowed another send")
	}
	var state string
	if err := h.op.owner.QueryRow("SELECT state FROM lightsail_creations WHERE job_id=$1", unsent.ID).Scan(&state); err != nil || state != "dispatched" {
		t.Fatal("dispatch was not durable", state, err)
	}
}

func TestLightsailOperationCompletionAndUnclearOwnership(t *testing.T) {
	h := newLaunch(t, true)
	ctx := context.Background()
	for _, mode := range []string{"wrong_tags", "ambiguous_history"} {
		name := strings.ReplaceAll(mode, "_", "-")
		h.mode(name, mode)
		job := h.submit(t, h.target(name))
		detail := h.op.drive(t, job.ID)
		expected := "OWNERSHIP_UNCONFIRMED"
		if mode == "ambiguous_history" {
			expected = "CREATE_HISTORY_AMBIGUOUS"
		}
		if detail.Job.Status != oc.Attention || detail.Items[0].Steps[0].Code != expected || h.count(name) != 1 {
			t.Fatal("ambiguous ownership was adopted", mode, detail.Job.Status)
		}
	}
	h.mode("operation-pending", "pending_operation")
	job := h.submit(t, h.target("operation-pending"))
	h.tick(t, job.ID)
	h.tick(t, job.ID)
	detail, err := h.op.service.Detail(ctx, h.op.admin, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Job.Status != oc.Waiting || detail.Items[0].Steps[0].Code != "CREATE_OPERATION_PENDING" || detail.Items[0].Steps[0].Output.ResourceID == "" {
		t.Fatal("instance presence was mistaken for operation completion", detail.Job.Status)
	}
	h.cloudLaunch.mu.Lock()
	op := h.cloudLaunch.operations[h.accountA.AWSAccountID+"/us-east-1/operation-pending"]
	op["status"] = "Failed"
	op["isTerminal"] = true
	h.cloudLaunch.mu.Unlock()
	detail = h.op.drive(t, job.ID)
	if detail.Job.Status != oc.Failed || detail.Items[0].Steps[0].Output.ResourceID == "" || h.count("operation-pending") != 1 {
		t.Fatal("later operation failure lost its resource")
	}
}
