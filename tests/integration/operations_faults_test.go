package integration

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Ember-Moth/aws-panel/internal/auth"
	ic "github.com/Ember-Moth/aws-panel/internal/module/identity/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/operations"
	oc "github.com/Ember-Moth/aws-panel/internal/module/operations/contract"
	"github.com/Ember-Moth/aws-panel/internal/repository"
	"github.com/Ember-Moth/aws-panel/internal/repository/kernel"
	"github.com/google/uuid"
)

type blockedDiagnostic struct {
	oc.Executor
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (e *blockedDiagnostic) Execute(ctx context.Context, call oc.Call) oc.Result {
	result := e.Executor.Execute(ctx, call)
	if call.Kind == "apply" {
		e.once.Do(func() { close(e.started); <-e.release })
		result.Output.Summary = "late-worker-result-must-not-win"
	}
	return result
}
func TestOperationsExpiredExecutorCannotOverwriteTakeover(t *testing.T) {
	h := newOperations(t)
	ctx := context.Background()
	job := h.submit(t, "success", 1)
	blocked := &blockedDiagnostic{Executor: operations.NewDiagnostic(h.store), started: make(chan struct{}), release: make(chan struct{})}
	old, err := operations.New(operations.Deps{Store: h.store, Access: h.members, Executors: []oc.Executor{blocked}, LeaseDuration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.service.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	messages := h.queue.take()
	finished := make(chan error, 1)
	go func() { finished <- old.Run(ctx, messages[0]) }()
	select {
	case <-blocked.started:
	case <-time.After(5 * time.Second):
		t.Fatal("worker never started")
	}
	defer func() {
		select {
		case <-blocked.release:
		default:
			close(blocked.release)
		}
	}()
	if _, err := h.owner.Exec("UPDATE job_steps SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE job_id=$1 AND status='running'", job.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.service.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	detail := h.drive(t, job.ID)
	if detail.Job.Status != oc.Succeeded {
		t.Fatal("takeover failed")
	}
	close(blocked.release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	after, err := h.service.Detail(ctx, h.admin, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Job.Version != detail.Job.Version || after.Items[0].Steps[0].Output.Summary == "late-worker-result-must-not-win" || len(after.Items[0].Steps[0].Attempts) != 2 {
		t.Fatal("expired executor overwrote the new owner's result")
	}
}

type rollbackStore struct {
	repository.OperationsStore
	rollback    atomic.Bool
	rejectAudit bool
}
type rejectOperationsAudit struct{ repository.OperationsTx }

func (rejectOperationsAudit) AppendAudit(context.Context, kernel.AuditEntry) error {
	return oc.ErrUnavailable
}
func (s *rollbackStore) InOperationsTx(ctx context.Context, fn func(repository.OperationsTx) error) error {
	return s.OperationsStore.InOperationsTx(ctx, func(tx repository.OperationsTx) error {
		if s.rejectAudit {
			tx = rejectOperationsAudit{tx}
		}
		err := fn(tx)
		if err == nil && s.rollback.Swap(false) {
			return oc.ErrUnavailable
		}
		return err
	})
}

type rollbackDiagnostic struct {
	oc.Executor
	store *rollbackStore
}

func (e rollbackDiagnostic) Execute(ctx context.Context, call oc.Call) oc.Result {
	result := e.Executor.Execute(ctx, call)
	if call.Kind == "apply" {
		e.store.rollback.Store(true)
	}
	return result
}

type retryDiagnostic struct {
	oc.Executor
	version int
}

func (e retryDiagnostic) Definition() oc.Definition {
	d := e.Executor.Definition()
	d.MaxAttempts = 2
	if e.version > 0 {
		d.Version = e.version
	}
	return d
}
func (e retryDiagnostic) Execute(context.Context, oc.Call) oc.Result {
	return oc.Result{State: oc.Retry, Code: "KNOWN_NOT_ACCEPTED"}
}
func TestOperationsAtomicAuditFailedCompletionAndVersionGate(t *testing.T) {
	h := newOperations(t)
	ctx := context.Background()
	broken := &rollbackStore{OperationsStore: h.store, rejectAudit: true}
	failure, err := operations.New(operations.Deps{Store: broken, Access: h.members, Executors: []oc.Executor{operations.NewDiagnostic(h.store)}})
	if err != nil {
		t.Fatal(err)
	}
	plan := h.plan(t, "success", 1)
	if _, err := failure.Submit(ctx, h.admin, oc.SubmitRequest{PlanID: plan.ID, IdempotencyKey: uuid.NewString()}, auth.RequestMeta{}); err == nil {
		t.Fatal("task creation ignored audit failure")
	}
	var count int
	if err := h.owner.QueryRow("SELECT (SELECT count(*) FROM jobs)+(SELECT count(*) FROM operations_outbox)").Scan(&count); err != nil || count != 0 {
		t.Fatal("audit failure left a partial job or message")
	}
	broken.rejectAudit = false
	failure, err = operations.New(operations.Deps{Store: broken, Access: h.members, Executors: []oc.Executor{rollbackDiagnostic{operations.NewDiagnostic(h.store), broken}}, LeaseDuration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	job := h.submit(t, "success", 1)
	if err := h.service.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	message := h.queue.take()[0]
	if err := failure.Run(ctx, message); err == nil {
		t.Fatal("completion commit failure hidden")
	}
	if _, err := h.owner.Exec("UPDATE job_steps SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE job_id=$1 AND status='running'", job.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.service.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if h.drive(t, job.ID).Job.Status != oc.Succeeded {
		t.Fatal("completion rollback was not reconciled")
	}
	if err := h.owner.QueryRow("SELECT sum(request_count) FROM operations_diagnostic_effects").Scan(&count); err != nil || count != 1 {
		t.Fatal("failed DB commit repeated the accepted effect")
	}
	// An incompatible deployment cannot run a new version against an old plan.
	oldJob := h.submit(t, "success", 1)
	if err := h.service.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	newVersion, err := operations.New(operations.Deps{Store: h.store, Access: h.members, Executors: []oc.Executor{retryDiagnostic{Executor: operations.NewDiagnostic(h.store), version: 2}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range h.queue.take() {
		if err := newVersion.Run(ctx, message); err != nil {
			t.Fatal(err)
		}
	}
	detail, err := h.service.Detail(ctx, h.admin, oldJob.ID)
	if err != nil || detail.Job.Status != oc.Attention || detail.Items[0].Steps[0].Code != "EXECUTOR_UNAVAILABLE" {
		t.Fatal("incompatible executor silently replaced pinned version")
	}
	// Automatic retries have a persisted finite attempt budget.
	bounded, err := operations.New(operations.Deps{Store: h.store, Access: h.members, Publisher: h.queue, Executors: []oc.Executor{retryDiagnostic{Executor: operations.NewDiagnostic(h.store)}}})
	if err != nil {
		t.Fatal(err)
	}
	h.service = bounded
	boundedJob := h.submit(t, "success", 1)
	detail = h.drive(t, boundedJob.ID)
	if detail.Job.Status != oc.Failed || detail.Items[0].Steps[0].AttemptCount != 2 || detail.Items[0].Steps[0].Code != "RETRY_LIMIT" {
		t.Fatal("automatic retries exceeded their budget")
	}
}

type bindingFixture struct {
	mu     sync.Mutex
	values map[string]oc.Binding
}

func (f *bindingFixture) Inspect(_ context.Context, _ int64, id, region string, _ bool) (oc.Binding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	value, ok := f.values[id]
	if !ok {
		return oc.Binding{}, oc.ErrBinding
	}
	value.Chain = append([]oc.ConnectionVersion{}, value.Chain...)
	if region != "" {
		value.Region = region
	}
	return value, nil
}
func (f *bindingFixture) change(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v := f.values[id]
	v.Fingerprint = "configuration-v2"
	v.Chain[0].Version = 2
	f.values[id] = v
}

type cloudFixture struct {
	store repository.OperationsStore
	name  string
}

func (e cloudFixture) Definition() oc.Definition {
	name := e.name
	if name == "" {
		name = "fixture.write"
	}
	return oc.Definition{ID: name, Version: 1, InputVersion: 1, Title: "Fixture cloud operation", WritesCloud: true, MaxAttempts: 5, Deadline: time.Minute, CallTimeout: 5 * time.Second}
}
func (e cloudFixture) Validate(json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}
func (e cloudFixture) Plan(_ context.Context, _ json.RawMessage, targets []oc.Target) ([]oc.ItemPlan, error) {
	items := []oc.ItemPlan{}
	for _, target := range targets {
		items = append(items, oc.ItemPlan{Target: target, Steps: []oc.StepPlan{{Kind: "create", Label: "Create fixture", Input: json.RawMessage(`{}`), WritesCloud: true}, {Kind: "configure", Label: "Configure fixture", Input: json.RawMessage(`{}`), WritesCloud: true}}})
	}
	return items, nil
}
func (e cloudFixture) Execute(ctx context.Context, call oc.Call) oc.Result {
	if call.Check(ctx) != nil {
		return oc.Result{State: oc.Attention, Code: "GUARD_REJECTED"}
	}
	if _, err := e.store.DiagnosticApply(ctx, call.Token, 0); err != nil {
		return oc.Result{State: oc.Waiting, Code: "RESULT_UNCONFIRMED"}
	}
	return oc.Result{State: oc.Succeeded, Code: "CREATED", Output: oc.Output{ResourceID: call.Token}}
}
func (e cloudFixture) Reconcile(ctx context.Context, call oc.Call) oc.Result {
	if call.Check(ctx) != nil {
		return oc.Result{State: oc.Waiting, Code: "GUARD_REJECTED"}
	}
	value, err := e.store.DiagnosticEffect(ctx, call.Token)
	if err != nil {
		return oc.Result{State: oc.Waiting, Code: "RESULT_UNCONFIRMED"}
	}
	if value == nil {
		return oc.Result{State: oc.Retry, Code: "ABSENT_WITH_DURABLE_TOKEN"}
	}
	return oc.Result{State: oc.Succeeded, Code: "RECONCILED", Output: oc.Output{ResourceID: call.Token}}
}
func TestOperationsAccountScopeConfigurationAndTargetLock(t *testing.T) {
	h := newOperations(t)
	ctx := context.Background()
	operator, err := h.members.CreateMember(ctx, h.admin, ic.CreateMemberRequest{Username: "operator", DisplayName: "Operator", Role: ic.RoleOperator, Password: "operations-operator-passphrase"}, auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	account, connection := uuid.NewString(), uuid.NewString()
	if _, err := h.owner.Exec("INSERT INTO aws_accounts(id,partition,aws_account_id,name,created_at,updated_at) VALUES ($1,'aws','444444444444','Fixture',now(),now())", account); err != nil {
		t.Fatal(err)
	}
	if _, err := h.owner.Exec("INSERT INTO account_grants(member_id,account_id,access,updated_at) VALUES ($1,$2,'operate',now())", operator.ID, account); err != nil {
		t.Fatal(err)
	}
	bindings := &bindingFixture{values: map[string]oc.Binding{connection: {AccountID: account, ConnectionID: connection, AWSAccountID: "444444444444", Partition: "aws", Region: "us-east-1", Fingerprint: "configuration-v1", Chain: []oc.ConnectionVersion{{ID: connection, Version: 1}}}}}
	service, err := operations.New(operations.Deps{Store: h.store, Access: h.members, Connections: bindings, Publisher: h.queue, CloudWritesEnabled: true, Executors: []oc.Executor{cloudFixture{store: h.store}, cloudFixture{store: h.store, name: "fixture.other"}}, RedeliveryAfter: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	h.service = service
	prepare := func(name, resource string) oc.Plan {
		t.Helper()
		plan, err := service.Prepare(ctx, operator.ID, oc.PlanRequest{Operation: name, InputVersion: 1, Input: json.RawMessage(`{}`), Targets: []oc.Target{{Key: resource, LockKey: resource, Label: resource, Binding: oc.Binding{ConnectionID: connection, Region: "us-east-1"}}}})
		if err != nil {
			t.Fatal(err)
		}
		return plan
	}
	submit := func(plan oc.Plan) (oc.Job, error) {
		return service.Submit(ctx, operator.ID, oc.SubmitRequest{PlanID: plan.ID, IdempotencyKey: uuid.NewString()}, auth.RequestMeta{})
	}
	job, err := submit(prepare("fixture.write", "resource-one"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := submit(prepare("fixture.other", "resource-one")); !errors.Is(err, oc.ErrConflict) {
		t.Fatal("two active operations acquired the same cloud target")
	}
	if err := service.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	for _, msg := range h.queue.take() {
		if err := service.Run(ctx, msg); err != nil {
			t.Fatal(err)
		}
	}
	// Revoke write access between sequential steps; the queued step must stop.
	if _, err := h.owner.Exec("UPDATE account_grants SET access='read' WHERE member_id=$1", operator.ID); err != nil {
		t.Fatal(err)
	}
	detail := h.drive(t, job.ID)
	if detail.Job.Status != oc.Attention || detail.Items[0].Steps[1].Code != "FORBIDDEN" {
		t.Fatal("revoked account grant allowed the next write")
	}
	var calls int
	if err := h.owner.QueryRow("SELECT sum(request_count) FROM operations_diagnostic_effects").Scan(&calls); err != nil || calls != 1 {
		t.Fatal("revoked grant caused a second side effect")
	}
	if _, err := service.Detail(ctx, operator.ID, job.ID); err != nil {
		t.Fatal("read grant cannot inspect its own result")
	}
	if _, err := h.owner.Exec("DELETE FROM account_grants WHERE member_id=$1", operator.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Events(ctx, operator.ID, job.ID, 0); !errors.Is(err, oc.ErrForbidden) {
		t.Fatal("event stream ignored scope revocation")
	}
	list, err := service.List(ctx, operator.ID, 1)
	if err != nil || len(list.Items) != 0 {
		t.Fatal("revoked account leaked task metadata")
	}
	if _, err := h.owner.Exec("INSERT INTO account_grants(member_id,account_id,access,updated_at) VALUES ($1,$2,'operate',now())", operator.ID, account); err != nil {
		t.Fatal(err)
	}
	bindings.change(connection)
	stepID := detail.Items[0].Steps[1].ID
	if err := service.Retry(ctx, operator.ID, job.ID, oc.RetryRequest{Version: detail.Job.Version, StepIDs: []string{stepID}}, auth.RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	detail = h.drive(t, job.ID)
	if detail.Job.Status != oc.Attention || detail.Items[0].Steps[1].Code != "CONNECTION_CHANGED" {
		t.Fatal("changed configuration was silently adopted")
	}
	if err := service.Retry(ctx, operator.ID, job.ID, oc.RetryRequest{Version: detail.Job.Version, StepIDs: []string{stepID}, RefreshConnections: true}, auth.RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	detail = h.drive(t, job.ID)
	if detail.Job.Status != oc.Succeeded {
		t.Fatal("reviewed configuration could not recover task")
	}
	if detail.Items[0].Steps[0].Attempts[0].Binding.Chain[0].Version != 1 {
		t.Fatal("old attempt configuration was overwritten")
	}
	attempts := detail.Items[0].Steps[1].Attempts
	if attempts[len(attempts)-1].Binding.Chain[0].Version != 2 {
		t.Fatal("actual retry configuration was not recorded")
	}
	other, err := submit(prepare("fixture.other", "resource-one"))
	if err != nil {
		t.Fatal("finished task did not release cloud target")
	}
	if err := service.Cancel(ctx, operator.ID, other.ID, auth.RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	// Refresh only the selected failed target; an already-successful target's
	// retired connection must not block recovery or be executed again.
	secondConnection := uuid.NewString()
	bindings.mu.Lock()
	secondBinding := bindings.values[connection]
	secondBinding.ConnectionID = secondConnection
	secondBinding.Chain = []oc.ConnectionVersion{{ID: secondConnection, Version: 1}}
	bindings.values[secondConnection] = secondBinding
	bindings.mu.Unlock()
	batchPlan, err := service.Prepare(ctx, operator.ID, oc.PlanRequest{Operation: "fixture.write", InputVersion: 1, Input: json.RawMessage(`{}`), Targets: []oc.Target{
		{Key: "resource-two", Label: "Already completed", LockKey: "resource-two", Binding: oc.Binding{ConnectionID: connection, Region: "us-east-1"}},
		{Key: "resource-three", Label: "Needs recovery", LockKey: "resource-three", Binding: oc.Binding{ConnectionID: secondConnection, Region: "us-east-1"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := submit(batchPlan)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	for _, message := range h.queue.take() {
		if err := service.Run(ctx, message); err != nil {
			t.Fatal(err)
		}
	}
	bindings.mu.Lock()
	delete(bindings.values, secondConnection)
	bindings.mu.Unlock()
	detail = h.drive(t, batch.ID)
	if detail.Job.Status != oc.Attention || detail.Items[0].Status != oc.Succeeded {
		t.Fatal("could not isolate the failed target")
	}
	bindings.mu.Lock()
	bindings.values[secondConnection] = secondBinding
	delete(bindings.values, connection)
	bindings.mu.Unlock()
	if err := service.Retry(ctx, operator.ID, batch.ID, oc.RetryRequest{Version: detail.Job.Version, StepIDs: []string{detail.Items[1].Steps[1].ID}, RefreshConnections: true}, auth.RequestMeta{}); err != nil {
		t.Fatal("completed target blocked recovery of another target", err)
	}
	detail = h.drive(t, batch.ID)
	if detail.Job.Status != oc.Succeeded || detail.Items[0].Steps[0].AttemptCount != 1 || detail.Items[0].Steps[1].AttemptCount != 1 {
		t.Fatal("selective recovery repeated completed work")
	}
}

type uncertainDiagnostic struct {
	oc.Executor
	absence atomic.Bool
}

func (e *uncertainDiagnostic) Definition() oc.Definition {
	d := e.Executor.Definition()
	d.MaxAttempts = 2
	return d
}
func (e *uncertainDiagnostic) Execute(context.Context, oc.Call) oc.Result {
	return oc.Result{State: oc.Waiting, Code: "RESPONSE_UNKNOWN"}
}
func (e *uncertainDiagnostic) Reconcile(context.Context, oc.Call) oc.Result {
	return oc.Result{State: oc.Retry, Code: "SAFE_TOKEN_RETRY", ConfirmedAbsent: e.absence.Load()}
}
func TestOperationsUncertaintySurvivesSafeRetryAndCancellation(t *testing.T) {
	h := newOperations(t)
	ctx := context.Background()
	executor := &uncertainDiagnostic{Executor: operations.NewDiagnostic(h.store)}
	service, err := operations.New(operations.Deps{Store: h.store, Access: h.members, Publisher: h.queue, Executors: []oc.Executor{executor}, RedeliveryAfter: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	h.service = service
	job := h.submit(t, "success", 1)
	if err := service.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	for _, msg := range h.queue.take() {
		if err := service.Run(ctx, msg); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.Cancel(ctx, h.admin, job.ID, auth.RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	detail := h.drive(t, job.ID)
	if detail.Job.Status != oc.Attention || detail.Items[0].Steps[0].Code != "RESULT_UNCONFIRMED" {
		t.Fatal("uncertain cancelled operation was marked failed or cancelled")
	}
	executor.absence.Store(true)
	if err := service.Retry(ctx, h.admin, job.ID, oc.RetryRequest{Version: detail.Job.Version, StepIDs: []string{detail.Items[0].Steps[0].ID}}, auth.RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	detail = h.drive(t, job.ID)
	if detail.Job.Status != oc.Cancelled || !detail.Job.CancelRequested || detail.Items[0].Steps[0].AttemptCount != 3 || detail.Items[0].Steps[0].Attempts[2].Mode != "reconcile" {
		t.Fatal("recovery restarted a cancelled write instead of confirming absence")
	}
}

func TestOperationsPendingResourceReferenceSurvivesPolling(t *testing.T) {
	h := newOperations(t)
	ctx := context.Background()
	job := h.submit(t, "wait", 1)
	if err := h.service.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	for _, msg := range h.queue.take() {
		if err := h.service.Run(ctx, msg); err != nil {
			t.Fatal(err)
		}
	}
	before, err := h.service.Detail(ctx, h.admin, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	resource := before.Items[0].Steps[0].Output.ResourceID
	if resource == "" {
		t.Fatal("pending resource ID was not persisted")
	}
	if _, err := h.owner.Exec("UPDATE operations_diagnostic_effects SET ready_at=clock_timestamp()+interval '1 minute'"); err != nil {
		t.Fatal(err)
	}
	h.expedite(t, job.ID)
	if err := h.service.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	for _, msg := range h.queue.take() {
		if err := h.service.Run(ctx, msg); err != nil {
			t.Fatal(err)
		}
	}
	after, err := h.service.Detail(ctx, h.admin, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Items[0].Steps[0].Output.ResourceID != resource || after.Items[0].Steps[0].Attempts[1].Output.ResourceID != "" {
		t.Fatal("polling lost the known resource or attributed it to the wrong attempt")
	}
}
