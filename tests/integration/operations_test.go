package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/herta-network/aws-panel/internal/auth"
	"github.com/herta-network/aws-panel/internal/config"
	"github.com/herta-network/aws-panel/internal/infra/postgres"
	"github.com/herta-network/aws-panel/internal/module/identity"
	ic "github.com/herta-network/aws-panel/internal/module/identity/contract"
	"github.com/herta-network/aws-panel/internal/module/operations"
	oc "github.com/herta-network/aws-panel/internal/module/operations/contract"
	"github.com/herta-network/aws-panel/internal/module/platform"
	"github.com/herta-network/aws-panel/internal/repository"
	"github.com/herta-network/aws-panel/internal/repository/kernel"
)

type capturedPublisher struct {
	mu       sync.Mutex
	messages []oc.Message
	fail     atomic.Bool
}

func (p *capturedPublisher) Publish(_ context.Context, _ string, msg oc.Message) error {
	if p.fail.Load() {
		return errors.New("queue offline")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.messages = append(p.messages, msg)
	return nil
}
func (p *capturedPublisher) take() []oc.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	messages := p.messages
	p.messages = nil
	return messages
}

type failedPublishMark struct {
	repository.OperationsStore
	fail atomic.Bool
}

func (s *failedPublishMark) FinishOutbox(ctx context.Context, id, owner string, success bool) error {
	if success && s.fail.Swap(false) {
		return oc.ErrUnavailable
	}
	return s.OperationsStore.FinishOutbox(ctx, id, owner, success)
}

type operationHarness struct {
	store   repository.OperationsStore
	service operations.Service
	members identity.Service
	admin   int64
	queue   *capturedPublisher
	owner   *sql.DB
}

func newOperations(t *testing.T) *operationHarness {
	t.Helper()
	db, _, owner := identityDatabase(t)
	members := identity.New(identity.Deps{Store: identity.NewRepository(kernel.Connection{DB: db}, platform.NewAuditAppender), Limiter: allowLogin{}, SessionTTL: time.Hour})
	admin, err := members.Bootstrap(context.Background(), ic.CreateMemberRequest{Username: "admin", DisplayName: "Admin", Password: "operations-test-admin-passphrase"}, auth.RequestMeta{RequestID: "operations-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	store := operations.NewRepository(kernel.Connection{DB: db}, platform.NewAuditAppender)
	queue := &capturedPublisher{}
	service, err := operations.New(operations.Deps{Store: store, Access: members, Publisher: queue, Executors: []oc.Executor{operations.NewDiagnostic(store)}, LeaseDuration: time.Second, RedeliveryAfter: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return &operationHarness{store: store, service: service, members: members, admin: admin.ID, queue: queue, owner: owner}
}
func (h *operationHarness) plan(t *testing.T, behavior string, count int) oc.Plan {
	t.Helper()
	raw, _ := json.Marshal(oc.DiagnosticRequest{Count: count, Behavior: behavior})
	plan, err := h.service.Prepare(context.Background(), h.admin, oc.PlanRequest{Operation: "system.diagnostic", InputVersion: 1, Input: raw})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}
func (h *operationHarness) submit(t *testing.T, behavior string, count int) oc.Job {
	t.Helper()
	plan := h.plan(t, behavior, count)
	job, err := h.service.Submit(context.Background(), h.admin, oc.SubmitRequest{PlanID: plan.ID, IdempotencyKey: uuid.NewString()}, auth.RequestMeta{RequestID: "submit-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	return job
}
func (h *operationHarness) expedite(t *testing.T, id string) {
	t.Helper()
	for _, query := range []string{"UPDATE job_steps SET next_run_at=clock_timestamp(),scheduled_at=clock_timestamp()-interval '2 minutes' WHERE job_id=$1 AND status IN ('queued','waiting') AND next_run_at IS NOT NULL", "UPDATE operations_outbox SET available_at=clock_timestamp(),publish_until=NULL WHERE job_id=$1 AND published_at IS NULL"} {
		if _, err := h.owner.Exec(query, id); err != nil {
			t.Fatal(err)
		}
	}
}
func (h *operationHarness) drive(t *testing.T, id string) oc.Detail {
	t.Helper()
	ctx := context.Background()
	// A full 16-step cloud workflow may need a send and observation tick per step.
	for range 64 {
		detail, err := h.service.Detail(ctx, h.admin, id)
		if err != nil {
			t.Fatal(err)
		}
		switch detail.Job.Status {
		case oc.Succeeded, oc.Failed, oc.Cancelled, oc.PartialSuccess, oc.Attention:
			return detail
		}
		h.expedite(t, id)
		if err := h.service.Recover(ctx); err != nil {
			t.Fatal(err)
		}
		if err := h.service.Publish(ctx); err != nil {
			t.Fatal(err)
		}
		for _, message := range h.queue.take() {
			if err := h.service.Run(ctx, message); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Fatal("task never settled")
	return oc.Detail{}
}

func TestOperationsTransactionalDeliveryAndRecovery(t *testing.T) {
	h := newOperations(t)
	ctx := context.Background()
	plan := h.plan(t, "response_lost", 1)
	idempotency := uuid.NewString()
	var wg sync.WaitGroup
	results := make(chan oc.Job, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			job, err := h.service.Submit(ctx, h.admin, oc.SubmitRequest{PlanID: plan.ID, IdempotencyKey: idempotency}, auth.RequestMeta{})
			if err != nil {
				t.Error(err)
				return
			}
			results <- job
		}()
	}
	wg.Wait()
	close(results)
	var job oc.Job
	for value := range results {
		if job.ID != "" && job.ID != value.ID {
			t.Fatal("duplicate submission created different jobs")
		}
		job = value
	}
	if job.ID == "" {
		t.Fatal("submission missing")
	}
	different := h.plan(t, "success", 2)
	if _, err := h.service.Submit(ctx, h.admin, oc.SubmitRequest{PlanID: different.ID, IdempotencyKey: idempotency}, auth.RequestMeta{}); !errors.Is(err, oc.ErrConflict) {
		t.Fatal("same key accepted different parameters")
	}
	var count int
	if err := h.owner.QueryRow("SELECT count(*) FROM jobs").Scan(&count); err != nil || count != 1 {
		t.Fatal("transactional submission duplicated")
	}
	// A committed job remains runnable while Redis is unavailable.
	h.queue.fail.Store(true)
	if h.service.Publish(ctx) == nil {
		t.Fatal("queue outage hidden")
	}
	h.queue.fail.Store(false)
	h.expedite(t, job.ID)
	// Crash after enqueue, before the outbox's published marker is committed.
	failure := &failedPublishMark{OperationsStore: h.store}
	failure.fail.Store(true)
	restarted, err := operations.New(operations.Deps{Store: failure, Access: h.members, Publisher: h.queue, Executors: []oc.Executor{operations.NewDiagnostic(h.store)}})
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Publish(ctx) == nil {
		t.Fatal("published-marker fault ignored")
	}
	first := h.queue.take()
	if len(first) != 1 {
		t.Fatal("message was not enqueued before marker failure")
	}
	h.expedite(t, job.ID)
	if err := restarted.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	second := h.queue.take()
	if len(second) != 1 {
		t.Fatal("outbox was not redelivered")
	}
	if err := h.service.Run(ctx, first[0]); err != nil {
		t.Fatal(err)
	}
	if err := h.service.Run(ctx, second[0]); err != nil {
		t.Fatal(err)
	}
	detail := h.drive(t, job.ID)
	if detail.Job.Status != oc.Succeeded {
		t.Fatalf("response-loss recovery: %s", detail.Job.Status)
	}
	if err := h.owner.QueryRow("SELECT sum(request_count) FROM operations_diagnostic_effects").Scan(&count); err != nil || count != 1 {
		t.Fatal("accepted operation was blindly repeated")
	}
	if len(detail.Items[0].Steps[0].Attempts) != 2 || detail.Items[0].Steps[0].Attempts[1].Mode != "reconcile" {
		t.Fatal("ambiguous attempt skipped reconciliation")
	}
	if err := h.service.Run(ctx, first[0]); err != nil {
		t.Fatal(err)
	}
	// Redis can lose an already-published message; database state recreates it.
	lost := h.submit(t, "success", 1)
	if err := h.service.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	h.queue.take()
	h.expedite(t, lost.ID)
	if err := h.service.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if h.drive(t, lost.ID).Job.Status != oc.Succeeded {
		t.Fatal("lost queue message was not recovered")
	}
	page, err := h.service.Events(ctx, h.admin, job.ID, 0)
	if err != nil || len(page.Events) == 0 {
		t.Fatal("events absent")
	}
	for i, event := range page.Events {
		if event.Seq != int64(i+1) {
			t.Fatal("event sequence gap")
		}
	}
	last := page.Events[len(page.Events)/2].Seq
	resumed, err := h.service.Events(ctx, h.admin, job.ID, last)
	if err != nil || resumed.Events[0].Seq != last+1 {
		t.Fatal("event replay cursor failed")
	}
}

func TestOperationsCancellationRetryDeadlineAndAccess(t *testing.T) {
	h := newOperations(t)
	ctx := context.Background()
	partial := h.submit(t, "partial", 2)
	detail := h.drive(t, partial.ID)
	if detail.Job.Status != oc.PartialSuccess {
		t.Fatal("partial success was lost")
	}
	failed := detail.Items[1].Steps[0]
	if err := h.service.Retry(ctx, h.admin, partial.ID, oc.RetryRequest{Version: detail.Job.Version, StepIDs: []string{failed.ID}}, auth.RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	detail = h.drive(t, partial.ID)
	if detail.Job.Status != oc.PartialSuccess || len(detail.Items[0].Steps[0].Attempts) != 1 || len(detail.Items[1].Steps[0].Attempts) != 3 {
		t.Fatal("manual retry overwrote successful work or history")
	}
	if detail.Items[1].Steps[0].Attempts[1].Mode != "reconcile" {
		t.Fatal("manual retry did not reconcile first")
	}
	cancelled := h.submit(t, "success", 2)
	if err := h.service.Cancel(ctx, h.admin, cancelled.ID, auth.RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	if got := h.drive(t, cancelled.ID); got.Job.Status != oc.Cancelled || got.Items[0].Steps[0].AttemptCount != 0 {
		t.Fatal("cancelled work was executed")
	}
	pending := h.submit(t, "response_lost", 1)
	if err := h.service.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	for _, msg := range h.queue.take() {
		if err := h.service.Run(ctx, msg); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.service.Cancel(ctx, h.admin, pending.ID, auth.RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	detail = h.drive(t, pending.ID)
	if detail.Items[0].Steps[0].Status != oc.Succeeded || detail.Items[0].Steps[1].Status != oc.Cancelled {
		t.Fatal("cancellation abandoned an already-issued result")
	}
	deadline := h.submit(t, "success", 1)
	if _, err := h.owner.Exec("UPDATE job_steps SET deadline_at=clock_timestamp()-interval '1 second' WHERE job_id=$1", deadline.ID); err != nil {
		t.Fatal(err)
	}
	detail = h.drive(t, deadline.ID)
	if detail.Job.Status != oc.Failed || detail.Items[0].Steps[0].AttemptCount != 0 {
		t.Fatal("deadline allowed new writes")
	}
	expired := h.plan(t, "success", 1)
	if _, err := h.owner.Exec("UPDATE plans SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", expired.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.Submit(ctx, h.admin, oc.SubmitRequest{PlanID: expired.ID, IdempotencyKey: uuid.NewString()}, auth.RequestMeta{}); !errors.Is(err, oc.ErrExpired) {
		t.Fatal("expired plan accepted")
	}
	viewer, err := h.members.CreateMember(ctx, h.admin, ic.CreateMemberRequest{Username: "reader", DisplayName: "Reader", Role: ic.RoleViewer, Password: "operations-test-reader-passphrase"}, auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.Detail(ctx, viewer.ID, partial.ID); !errors.Is(err, oc.ErrForbidden) {
		t.Fatal("diagnostic details escaped administrator scope")
	}
	if _, err := h.service.Events(ctx, viewer.ID, partial.ID, 0); !errors.Is(err, oc.ErrForbidden) {
		t.Fatal("events escaped task scope")
	}
	list, err := h.service.List(ctx, viewer.ID, 1)
	if err != nil || len(list.Items) != 0 {
		t.Fatal("hidden tasks leaked in list")
	}
}

type crashDiagnostic struct{ oc.Executor }

func (e crashDiagnostic) Execute(ctx context.Context, call oc.Call) oc.Result {
	result := e.Executor.Execute(ctx, call)
	if result.State == oc.Succeeded && call.Kind == "apply" {
		os.Exit(0)
	}
	return result
}

func TestOperationsWorkerCrashHelper(t *testing.T) {
	raw := os.Getenv("AWS_PANEL_CRASH_TASK")
	if raw == "" {
		t.Skip("worker crash helper")
	}
	cfg := config.Defaults().Database
	port, err := strconv.Atoi(os.Getenv("AWS_PANEL_TEST_DB_PORT"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Port = port
	cfg.Name = "aws_panel_test"
	cfg.Password = os.Getenv("AWS_PANEL_TEST_DB_PASSWORD")
	if name := os.Getenv("AWS_PANEL_TEST_DB_NAME"); name != "" {
		cfg.Name = name
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db, pool, err := postgres.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	members := identity.New(identity.Deps{Store: identity.NewRepository(kernel.Connection{DB: db}, platform.NewAuditAppender), Limiter: allowLogin{}, SessionTTL: time.Hour})
	store := operations.NewRepository(kernel.Connection{DB: db}, platform.NewAuditAppender)
	service, err := operations.New(operations.Deps{Store: store, Access: members, Executors: []oc.Executor{crashDiagnostic{operations.NewDiagnostic(store)}}, LeaseDuration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	var msg oc.Message
	if json.Unmarshal([]byte(raw), &msg) != nil {
		t.Fatal("invalid helper message")
	}
	if err := service.Run(ctx, msg); err != nil {
		t.Fatal(err)
	}
	t.Fatal("worker did not exit at the injected crash window")
}
func TestOperationsProcessDeathReconcilesBeforeExecute(t *testing.T) {
	h := newOperations(t)
	ctx := context.Background()
	job := h.submit(t, "success", 1)
	if err := h.service.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	messages := h.queue.take()
	if len(messages) != 1 {
		t.Fatal("expected one wakeup")
	}
	raw, _ := json.Marshal(messages[0])
	command := exec.Command(os.Args[0], "-test.run=^TestOperationsWorkerCrashHelper$")
	command.Env = append(os.Environ(), "AWS_PANEL_CRASH_TASK="+string(raw))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("worker helper: %v %s", err, output)
	}
	detail, err := h.service.Detail(ctx, h.admin, job.ID)
	if err != nil || detail.Items[0].Steps[0].Status != oc.Running {
		t.Fatal("worker did not leave an in-flight attempt")
	}
	// The worker is confirmed dead; expire its durable lease to exercise takeover.
	if _, err := h.owner.Exec("UPDATE job_steps SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE job_id=$1 AND status='running'", job.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.service.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	detail = h.drive(t, job.ID)
	if detail.Job.Status != oc.Succeeded {
		t.Fatal("dead worker was not recovered")
	}
	attempts := detail.Items[0].Steps[0].Attempts
	if len(attempts) != 2 || attempts[0].Outcome != "lease_lost" || attempts[1].Mode != "reconcile" {
		t.Fatal("takeover did not preserve and reconcile the expired attempt")
	}
	var calls int
	if err := h.owner.QueryRow("SELECT sum(request_count) FROM operations_diagnostic_effects").Scan(&calls); err != nil || calls != 1 {
		t.Fatal("worker death duplicated a side effect")
	}
}
