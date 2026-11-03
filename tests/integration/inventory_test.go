package integration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/herta-network/aws-panel/internal/auth"
	ac "github.com/herta-network/aws-panel/internal/module/accounts/contract"
	ic "github.com/herta-network/aws-panel/internal/module/identity/contract"
	"github.com/herta-network/aws-panel/internal/module/inventory"
	iv "github.com/herta-network/aws-panel/internal/module/inventory/contract"
	ie "github.com/herta-network/aws-panel/internal/module/inventory/entity"
	"github.com/herta-network/aws-panel/internal/module/operations"
	oc "github.com/herta-network/aws-panel/internal/module/operations/contract"
	"github.com/herta-network/aws-panel/internal/repository"
)

func TestInventorySDKPaginationIsolationAndStaleSnapshots(t *testing.T) {
	h := newInventory(t)
	ctx := context.Background()
	h.cloud.set(h.accountA.AWSAccountID, "us-west-2", "many")
	request := iv.RefreshRequest{IdempotencyKey: uuid.NewString(), Targets: []iv.RefreshTarget{
		{ConnectionID: h.connectionA.ID, Kind: iv.AccountRegion}, {ConnectionID: h.connectionA.ID, Kind: iv.LightsailRegion},
		{ConnectionID: h.connectionA.ID, Kind: iv.LightsailInstance, Region: "us-east-1"}, {ConnectionID: h.connectionA.ID, Kind: iv.LightsailInstance, Region: "us-west-2"},
		{ConnectionID: h.connectionB.ID, Kind: iv.LightsailInstance, Region: "us-east-1"},
	}}
	job, err := h.service.Refresh(ctx, h.op.admin, request, auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := h.service.Refresh(ctx, h.op.admin, request, auth.RequestMeta{}); err != nil || replay.ID != job.ID {
		t.Fatal("refresh idempotency did not survive normalized JSON")
	}
	changed := request
	changed.Targets = request.Targets[:1]
	if _, err := h.service.Refresh(ctx, h.op.admin, changed, auth.RequestMeta{}); !errors.Is(err, iv.ErrConflict) {
		t.Fatal("refresh idempotency accepted another scope")
	}
	if err := h.op.service.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	var concurrent sync.WaitGroup
	for _, message := range h.op.queue.take() {
		concurrent.Add(1)
		go func() {
			defer concurrent.Done()
			if err := h.op.service.Run(ctx, message); err != nil {
				t.Error(err)
			}
		}()
	}
	concurrent.Wait()
	if detail := h.op.drive(t, job.ID); detail.Job.Status != oc.Succeeded {
		t.Fatal("inventory batch failed", detail.Job.Status)
	}
	first, err := h.service.Resources(ctx, h.op.admin, iv.Filter{Kind: iv.LightsailInstance, Page: 1})
	if err != nil || len(first.Items) != 25 || first.Next != 2 {
		t.Fatal("resource page one incomplete", err)
	}
	second, err := h.service.Resources(ctx, h.op.admin, iv.Filter{Kind: iv.LightsailInstance, Page: 2})
	if err != nil || len(second.Items) != 5 || second.Next != 0 {
		t.Fatal("resource page two incomplete", err)
	}
	seen := map[string]bool{}
	for _, row := range append(first.Items, second.Items...) {
		if seen[row.ID] {
			t.Fatal("duplicate snapshot across pages")
		}
		seen[row.ID] = true
		if row.Details.Instance == nil || row.Details.Instance.CPU != 2 {
			t.Fatal("instance details absent")
		}
	}
	regions, err := h.service.Resources(ctx, h.op.admin, iv.Filter{Kind: iv.AccountRegion, Page: 1})
	if err != nil || len(regions.Items) != 2 {
		t.Fatal("Account API pagination incomplete", err)
	}
	for _, row := range regions.Items {
		if row.Details.Region == nil || row.Details.Region.OptStatus == "" {
			t.Fatal("region opt-in state missing")
		}
	}
	data, _ := json.Marshal(first)
	if strings.Contains(string(data), testSecretA) || strings.Contains(string(data), inventorySecretB) || !strings.Contains(string(data), "2026-11-01T08:00:00+08:00") {
		t.Fatal("snapshot leaked private tags or lost Beijing timestamps")
	}
	filter := iv.Filter{AccountID: h.accountA.ID, Kind: iv.LightsailInstance, Region: "us-east-1", Page: 1}
	before, err := h.service.Resources(ctx, h.op.admin, filter)
	if err != nil || len(before.Items) != 2 || len(before.Scopes) != 1 || before.Scopes[0].LastSuccessAt == nil {
		t.Fatal("scope snapshot absent", err)
	}
	success := *before.Scopes[0].LastSuccessAt
	for _, mode := range []string{"denied_second", "repeat_token", "wrong_account"} {
		h.cloud.set(h.accountA.AWSAccountID, "us-east-1", mode)
		failed := h.refresh(t, iv.RefreshTarget{ConnectionID: h.connectionA.ID, Kind: iv.LightsailInstance, Region: "us-east-1"})
		detail := h.op.drive(t, failed.ID)
		if detail.Job.Status != oc.Failed {
			t.Fatal("incomplete AWS response was not reported", mode, detail.Job.Status)
		}
		after, err := h.service.Resources(ctx, h.op.admin, filter)
		if err != nil || len(after.Items) != 2 || !after.Scopes[0].LastSuccessAt.Equal(success) || after.Scopes[0].Status != "failed" || !after.Scopes[0].Stale {
			t.Fatal("failed pagination replaced a valid snapshot", mode, err)
		}
		for _, row := range after.Items {
			if !row.Present || strings.Contains(row.Name, "partial-page") {
				t.Fatal("partial page escaped the snapshot transaction")
			}
		}
	}
	h.cloud.set(h.accountA.AWSAccountID, "us-east-1", "empty")
	empty := h.refresh(t, iv.RefreshTarget{ConnectionID: h.connectionA.ID, Kind: iv.LightsailInstance, Region: "us-east-1"})
	if h.op.drive(t, empty.ID).Job.Status != oc.Succeeded {
		t.Fatal("complete empty snapshot failed")
	}
	current, err := h.service.Resources(ctx, h.op.admin, filter)
	if err != nil || len(current.Items) != 0 {
		t.Fatal("complete empty result was not applied")
	}
	filter.IncludeMissing = true
	missing, err := h.service.Resources(ctx, h.op.admin, filter)
	if err != nil || len(missing.Items) != 2 || missing.Items[0].Present || missing.Items[0].MissingSince == nil {
		t.Fatal("last seen records were not retained")
	}
	other, err := h.service.Resources(ctx, h.op.admin, iv.Filter{AccountID: h.accountB.ID, Kind: iv.LightsailInstance, Page: 1})
	if err != nil || len(other.Items) != 2 {
		t.Fatal("one account's empty result removed another account")
	}
	h.cloud.set(h.accountA.AWSAccountID, "us-east-1", "normal")
	restored := h.refresh(t, iv.RefreshTarget{ConnectionID: h.connectionA.ID, Kind: iv.LightsailInstance, Region: "us-east-1"})
	if h.op.drive(t, restored.ID).Job.Status != oc.Succeeded {
		t.Fatal("resource reappearance failed")
	}
	filter.IncludeMissing = false
	again, err := h.service.Resources(ctx, h.op.admin, filter)
	if err != nil || again.Items[0].ID != before.Items[0].ID || again.Items[0].MissingSince != nil {
		t.Fatal("stable resource identity did not survive reappearance")
	}
	reader, err := h.op.members.CreateMember(ctx, h.op.admin, ic.CreateMemberRequest{Username: "inventory-reader", DisplayName: "Reader", Role: ic.RoleViewer, Password: "inventory-reader-passphrase"}, auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.op.members.SetAccountGrant(ctx, h.op.admin, reader.ID, ic.GrantRequest{AccountID: h.accountA.ID, Access: "read"}, auth.RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	visible, err := h.service.Resources(ctx, reader.ID, iv.Filter{Kind: iv.LightsailInstance, Page: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range visible.Items {
		if row.AccountID != h.accountA.ID {
			t.Fatal("resource list escaped account grant")
		}
	}
	if _, err := h.service.Resource(ctx, reader.ID, other.Items[0].ID); !errors.Is(err, iv.ErrForbidden) {
		t.Fatal("resource details escaped account grant")
	}
	if _, err := h.service.Resources(ctx, reader.ID, iv.Filter{AccountID: h.accountB.ID, Kind: iv.LightsailInstance, Page: 1}); !errors.Is(err, iv.ErrForbidden) {
		t.Fatal("account filter bypassed authorization")
	}
	if _, err := h.service.Refresh(ctx, reader.ID, iv.RefreshRequest{Targets: []iv.RefreshTarget{{ConnectionID: h.connectionB.ID, Kind: iv.AccountRegion}}, IdempotencyKey: uuid.NewString()}, auth.RequestMeta{}); !errors.Is(err, iv.ErrForbidden) {
		t.Fatal("reader refreshed an ungranted account")
	}
	readJob, err := h.service.Refresh(ctx, reader.ID, iv.RefreshRequest{Targets: []iv.RefreshTarget{{ConnectionID: h.connectionA.ID, Kind: iv.AccountRegion}}, IdempotencyKey: uuid.NewString()}, auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if h.op.drive(t, readJob.ID).Job.Status != oc.Succeeded {
		t.Fatal("read grant could not refresh inventory")
	}
	// Changing the configured connection must not duplicate resource ownership.
	alt, err := h.accounts.SaveConnection(ctx, h.op.admin, h.accountA.ID, "", ac.ConnectionRequest{Name: "Alternative read path", AuthType: "access_key", Region: "us-east-1", RouteMode: "socks5", ProxyID: &h.proxyA.ID, Credentials: &ac.Credentials{AccessKeyID: testKeyA, SecretAccessKey: testSecretA}}, auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := h.accounts.Verify(ctx, h.op.admin, alt.ID, auth.RequestMeta{}); err != nil || !result.OK {
		t.Fatal("alternative connection verification failed")
	}
	alternate := h.refresh(t, iv.RefreshTarget{ConnectionID: alt.ID, Kind: iv.LightsailInstance, Region: "us-east-1"})
	if h.op.drive(t, alternate.ID).Job.Status != oc.Succeeded {
		t.Fatal("alternative connection sync failed")
	}
	final, err := h.service.Resources(ctx, h.op.admin, filter)
	if err != nil || len(final.Items) != 2 || final.Items[0].ID != before.Items[0].ID {
		t.Fatal("resource ownership was duplicated by another connection")
	}
}

type failedScheduleMark struct {
	repository.InventoryStore
	fail atomic.Bool
}

func (s *failedScheduleMark) FinishSchedule(ctx context.Context, id string, version int64, owner string, jobID *string, code string) error {
	if jobID != nil && s.fail.Swap(false) {
		return iv.ErrUnavailable
	}
	return s.InventoryStore.FinishSchedule(ctx, id, version, owner, jobID, code)
}
func TestInventorySchedulesIdempotencyScopeLocksAndRevocation(t *testing.T) {
	h := newInventory(t)
	ctx := context.Background()
	target := iv.RefreshTarget{ConnectionID: h.connectionA.ID, Kind: iv.LightsailInstance, Region: "us-east-1"}
	preset, err := h.service.SaveSchedule(ctx, h.op.admin, "", iv.ScheduleRequest{Name: "Recurring inventory", Targets: []iv.RefreshTarget{target}, IntervalMinutes: 5, Active: true}, auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	manual := h.refresh(t, target)
	if _, err := h.db.Exec("UPDATE inventory_schedules SET next_run_at=clock_timestamp()-interval '1 second' WHERE id=$1", preset.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.service.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	busy, err := h.service.Schedule(ctx, h.op.admin, preset.ID)
	if err != nil || busy.Code != "CONFLICT" || busy.LastJobID != nil {
		t.Fatal("scheduled sync bypassed a manual scope lock")
	}
	if h.op.drive(t, manual.ID).Job.Status != oc.Succeeded {
		t.Fatal("manual sync failed")
	}
	mark := &failedScheduleMark{InventoryStore: h.store}
	mark.fail.Store(true)
	restarted := inventory.New(inventory.Deps{Store: mark, Accounts: h.accounts, Access: h.op.members, Jobs: h.op.service, Observer: operations.NewObserver(h.op.store)})
	if _, err := h.db.Exec("UPDATE inventory_schedules SET next_run_at=clock_timestamp()-interval '1 second' WHERE id=$1", preset.ID); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Tick(ctx); err == nil {
		t.Fatal("scheduled-job marker failure not injected")
	}
	var countBefore int
	if err := h.db.QueryRow("SELECT count(*) FROM jobs").Scan(&countBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec("UPDATE inventory_schedules SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", preset.ID); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	var countAfter int
	if err := h.db.QueryRow("SELECT count(*) FROM jobs").Scan(&countAfter); err != nil || countBefore != countAfter {
		t.Fatal("recovered schedule submitted a duplicate task")
	}
	saved, err := h.service.Schedule(ctx, h.op.admin, preset.ID)
	if err != nil || saved.LastJobID == nil || saved.Code != "" {
		t.Fatal("scheduled task reference was not recovered")
	}
	if h.op.drive(t, *saved.LastJobID).Job.Status != oc.Succeeded {
		t.Fatal("scheduled SDK sync failed")
	}
	reader, err := h.op.members.CreateMember(ctx, h.op.admin, ic.CreateMemberRequest{Username: "scheduler-reader", DisplayName: "Reader", Role: ic.RoleViewer, Password: "scheduler-reader-passphrase"}, auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.op.members.SetAccountGrant(ctx, h.op.admin, reader.ID, ic.GrantRequest{AccountID: h.accountA.ID, Access: "read"}, auth.RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	owned, err := h.service.SaveSchedule(ctx, reader.ID, "", iv.ScheduleRequest{Name: "Reader owned", Targets: []iv.RefreshTarget{target}, IntervalMinutes: 5, Active: true}, auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.Schedule(ctx, reader.ID, preset.ID); !errors.Is(err, iv.ErrForbidden) {
		t.Fatal("schedule ownership was not enforced")
	}
	if err := h.op.members.SetAccountGrant(ctx, h.op.admin, reader.ID, ic.GrantRequest{AccountID: h.accountA.ID, Access: "none"}, auth.RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec("UPDATE inventory_schedules SET next_run_at=clock_timestamp()-interval '1 second' WHERE id=$1", owned.ID); err != nil {
		t.Fatal(err)
	}
	calls := h.cloud.count()
	if err := h.service.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	revoked, err := h.service.Schedule(ctx, h.op.admin, owned.ID)
	if err != nil || revoked.Code != "FORBIDDEN" || h.cloud.count() != calls {
		t.Fatal("scheduled job ignored revoked account access")
	}
	stopped, err := h.service.SaveSchedule(ctx, h.op.admin, owned.ID, iv.ScheduleRequest{Name: revoked.Name, Targets: revoked.Targets, IntervalMinutes: 5, Active: false, Version: revoked.Version}, auth.RequestMeta{})
	if err != nil || stopped.ActorID != reader.ID {
		t.Fatal("administrator could not disable a broken schedule without adopting its identity")
	}
	if err := h.service.DeleteSchedule(ctx, reader.ID, stopped.ID, stopped.Version, auth.RequestMeta{}); err != nil {
		t.Fatal(err)
	}
}

func TestInventoryLateSnapshotIsFencedByTaskLease(t *testing.T) {
	h := newInventory(t)
	ctx := context.Background()
	target := iv.RefreshTarget{ConnectionID: h.connectionA.ID, Kind: iv.LightsailInstance, Region: "us-east-1"}
	h.cloud.pageTwoEntered = make(chan struct{})
	h.cloud.pageTwoRelease = make(chan struct{})
	h.cloud.set(h.accountA.AWSAccountID, "us-east-1", "slow_second")
	defer func() {
		select {
		case <-h.cloud.pageTwoRelease:
		default:
			close(h.cloud.pageTwoRelease)
		}
	}()
	job := h.refresh(t, target)
	if err := h.op.service.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	message := h.op.queue.take()[0]
	finished := make(chan error, 1)
	go func() { finished <- h.op.service.Run(ctx, message) }()
	select {
	case <-h.cloud.pageTwoEntered:
	case <-time.After(10 * time.Second):
		t.Fatal("SDK did not reach the blocked page")
	}
	var run ie.Run
	var owner string
	if err := h.db.QueryRow("SELECT r.id,r.scope_id,r.step_id,r.generation,r.lease_epoch,s.lease_owner FROM inventory_runs r JOIN job_steps s ON s.id=r.step_id WHERE r.job_id=$1", job.ID).Scan(&run.ID, &run.ScopeID, &run.StepID, &run.Generation, &run.LeaseEpoch, &owner); err != nil {
		t.Fatal(err)
	}
	run.JobID = job.ID
	scopeRow, err := h.store.Scope(ctx, run.ScopeID)
	if err != nil {
		t.Fatal(err)
	}
	ticket := ie.Ticket{Run: run, Scope: scopeRow, Lease: ie.Lease{JobID: job.ID, StepID: run.StepID, Epoch: run.LeaseEpoch, Owner: owner}}
	if _, err := h.db.Exec("UPDATE job_steps SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", run.StepID); err != nil {
		t.Fatal(err)
	}
	if err := h.op.service.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	h.cloud.set(h.accountA.AWSAccountID, "us-east-1", "normal")
	close(h.cloud.pageTwoRelease)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if h.op.drive(t, job.ID).Job.Status != oc.Succeeded {
		t.Fatal("snapshot takeover failed")
	}
	if err := h.store.CompleteRefresh(ctx, ticket, nil, 1, "late-worker"); !errors.Is(err, iv.ErrStale) {
		t.Fatal("expired worker published a stale empty snapshot")
	}
	rows, err := h.service.Resources(ctx, h.op.admin, iv.Filter{AccountID: h.accountA.ID, Kind: iv.LightsailInstance, Region: "us-east-1", Page: 1})
	if err != nil || len(rows.Items) != 2 {
		t.Fatal("stale commit erased the new snapshot")
	}
}

func TestInventoryProxyOutageRetainsLastSuccessfulData(t *testing.T) {
	h := newInventory(t)
	ctx := context.Background()
	target := iv.RefreshTarget{ConnectionID: h.connectionA.ID, Kind: iv.LightsailInstance, Region: "us-east-1"}
	initial := h.refresh(t, target)
	if h.op.drive(t, initial.ID).Job.Status != oc.Succeeded {
		t.Fatal("initial sync failed")
	}
	filter := iv.Filter{AccountID: h.accountA.ID, Kind: iv.LightsailInstance, Region: "us-east-1", Page: 1}
	before, err := h.service.Resources(ctx, h.op.admin, filter)
	if err != nil {
		t.Fatal(err)
	}
	h.stopProxyA()
	calls := h.cloud.count()
	failed := h.refresh(t, target)
	if err := h.op.service.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	for _, message := range h.op.queue.take() {
		work, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		err := h.op.service.Run(work, message)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
	}
	after, err := h.service.Resources(ctx, h.op.admin, filter)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Items) != 2 || after.Items[0].ID != before.Items[0].ID || !after.Scopes[0].LastSuccessAt.Equal(*before.Scopes[0].LastSuccessAt) || after.Scopes[0].Code != "NETWORK_UNAVAILABLE" || after.Scopes[0].Message == "" {
		t.Fatal("proxy outage hid its error or replaced the valid snapshot")
	}
	if h.cloud.count() != calls {
		t.Fatal("broken proxy request escaped its route")
	}
	if err := h.op.service.Cancel(ctx, h.op.admin, failed.ID, auth.RequestMeta{}); err != nil {
		t.Fatal(err)
	}
}
