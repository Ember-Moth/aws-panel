package repo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/Ember-Moth/aws-panel/internal/module/operations/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/operations/entity"
	"github.com/Ember-Moth/aws-panel/internal/repository"
	"github.com/Ember-Moth/aws-panel/internal/repository/kernel"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Store struct {
	db           *gorm.DB
	audit        kernel.AuditFactory
	bindingGuard kernel.BindingGuardFactory
}

func New(c kernel.Connection, audit kernel.AuditFactory, guards ...kernel.BindingGuardFactory) *Store {
	s := &Store{db: c.DB, audit: audit}
	if len(guards) > 0 {
		s.bindingGuard = guards[0]
	}
	return s
}
func convert(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return contract.ErrNotFound
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && (pg.Code == "23505" || pg.Code == "23503") {
		return contract.ErrConflict
	}
	return contract.ErrUnavailable
}
func (s *Store) Plan(ctx context.Context, id string) (entity.Plan, error) {
	var row entity.Plan
	err := s.db.WithContext(ctx).Table("plans").Where("id = ?", id).Take(&row).Error
	return row, convert(err)
}
func (s *Store) Job(ctx context.Context, id string) (entity.Job, error) {
	var row entity.Job
	err := s.db.WithContext(ctx).Table("jobs").Where("id = ?", id).Take(&row).Error
	return row, convert(err)
}
func (s *Store) JobByKey(ctx context.Context, actor int64, operation, key string) (entity.Job, error) {
	var row entity.Job
	err := s.db.WithContext(ctx).Table("jobs").Where("actor_id = ? AND operation = ? AND idempotency_key = ?", actor, operation, key).Take(&row).Error
	return row, convert(err)
}
func (s *Store) Jobs(ctx context.Context, scope entity.Scope, page, size int) ([]entity.Job, error) {
	rows := []entity.Job{}
	q := s.db.WithContext(ctx).Table("jobs")
	if !scope.Admin {
		if len(scope.AccountIDs) == 0 {
			return rows, nil
		}
		q = q.Where("diagnostic = false AND NOT EXISTS (SELECT 1 FROM job_items i WHERE i.job_id=jobs.id AND (i.account_id IS NULL OR i.account_id NOT IN ?))", scope.AccountIDs)
	}
	err := q.Order("created_at DESC, id").Offset((page - 1) * size).Limit(size + 1).Find(&rows).Error
	return rows, convert(err)
}
func (s *Store) Items(ctx context.Context, id string) ([]entity.Item, error) {
	rows := []entity.Item{}
	err := s.db.WithContext(ctx).Table("job_items").Where("job_id = ?", id).Order("ordinal").Find(&rows).Error
	return rows, convert(err)
}
func (s *Store) Steps(ctx context.Context, id string) ([]entity.Step, error) {
	rows := []entity.Step{}
	err := s.db.WithContext(ctx).Table("job_steps").Where("job_id = ?", id).Order("item_id, ordinal").Find(&rows).Error
	return rows, convert(err)
}
func (s *Store) Attempts(ctx context.Context, id string) ([]entity.Attempt, error) {
	rows := []entity.Attempt{}
	err := s.db.WithContext(ctx).Table("job_attempts").Where("job_id = ?", id).Order("step_id, number").Find(&rows).Error
	return rows, convert(err)
}
func (s *Store) Events(ctx context.Context, id string, after int64, limit int) ([]entity.Event, error) {
	rows := []entity.Event{}
	err := s.db.WithContext(ctx).Table("job_events").Where("job_id = ? AND seq > ?", id, after).Order("seq").Limit(limit).Find(&rows).Error
	return rows, convert(err)
}
func (s *Store) LatestTargets(ctx context.Context, operation, refs string) ([]entity.TargetState, error) {
	rows := []entity.TargetState{}
	err := s.db.WithContext(ctx).Raw(`SELECT DISTINCT ON(i.account_id,i.target_key) i.account_id,i.target_key,j.id AS job_id,s.id AS step_id,i.status,s.code,j.created_at,s.updated_at,s.next_run_at
	FROM job_items i JOIN jobs j ON j.id=i.job_id JOIN job_steps s ON s.item_id=i.id AND s.ordinal=0
	JOIN jsonb_to_recordset(?::jsonb) AS r(account_id uuid,target_key text) ON i.account_id=r.account_id AND i.target_key=r.target_key
	WHERE j.operation=? ORDER BY i.account_id,i.target_key,s.updated_at DESC,j.created_at DESC,j.id DESC`, refs, operation).Scan(&rows).Error
	return rows, convert(err)
}
func (s *Store) InOperationsTx(ctx context.Context, fn func(repository.OperationsTx) error) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return fn(&Store{db: tx, audit: s.audit, bindingGuard: s.bindingGuard}) })
}
func (s *Store) LockCloudReferences(ctx context.Context) error {
	return convert(s.db.WithContext(ctx).Exec("SELECT pg_advisory_xact_lock(?)", kernel.CloudConfigurationLock).Error)
}
func (s *Store) CheckBinding(ctx context.Context, id, fingerprint string) error {
	if s.bindingGuard == nil {
		return nil
	}
	if err := s.bindingGuard(kernel.Connection{DB: s.db}).CheckBinding(ctx, id, fingerprint); err != nil {
		return contract.ErrBinding
	}
	return nil
}
func (s *Store) InUse(ctx context.Context, kind, id string) (bool, error) {
	query := s.db.WithContext(ctx).Table("job_items").Where("status IN ?", []string{contract.Queued, contract.Running, contract.Waiting, contract.Attention})
	switch kind {
	case "aws_account":
		query = query.Where("account_id=?", id)
	case "connection":
		query = query.Where("(connection_id=? OR EXISTS(SELECT 1 FROM jsonb_array_elements(COALESCE(binding->'chain','[]'::jsonb)) n WHERE n->>'id'=?))", id, id)
	case "proxy":
		query = query.Where("EXISTS(SELECT 1 FROM jsonb_array_elements(COALESCE(binding->'chain','[]'::jsonb)) n WHERE n->>'proxy_id'=?)", id)
	default:
		return false, contract.ErrInvalid
	}
	var count int64
	err := query.Count(&count).Error
	return count > 0, convert(err)
}
func (s *Store) Now(ctx context.Context) (time.Time, error) {
	var now time.Time
	err := s.db.WithContext(ctx).Raw("SELECT clock_timestamp()").Scan(&now).Error
	return now, convert(err)
}
func (s *Store) LockSubmit(ctx context.Context, actor int64, operation, key string) error {
	value := fmt.Sprintf("aws-panel/job/%d/%s/%s", actor, operation, key)
	return convert(s.db.WithContext(ctx).Exec("SELECT pg_advisory_xact_lock(hashtextextended(?,0))", value).Error)
}
func (s *Store) JobForUpdate(ctx context.Context, id string) (entity.Job, error) {
	var row entity.Job
	err := s.db.WithContext(ctx).Table("jobs").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&row).Error
	return row, convert(err)
}
func (s *Store) StepsForUpdate(ctx context.Context, id string) ([]entity.Step, error) {
	rows := []entity.Step{}
	err := s.db.WithContext(ctx).Table("job_steps").Clauses(clause.Locking{Strength: "UPDATE"}).Where("job_id=?", id).Order("item_id, ordinal").Find(&rows).Error
	return rows, convert(err)
}
func (s *Store) CreatePlan(ctx context.Context, row entity.Plan) error {
	return convert(s.db.WithContext(ctx).Table("plans").Create(&row).Error)
}
func (s *Store) CreateJob(ctx context.Context, row entity.Job) error {
	return convert(s.db.WithContext(ctx).Table("jobs").Create(&row).Error)
}
func (s *Store) CreateItem(ctx context.Context, row entity.Item) error {
	if row.LockKeys == "" {
		row.LockKeys = "[]"
	}
	if err := convert(s.db.WithContext(ctx).Table("job_items").Create(&row).Error); err != nil {
		return err
	}
	return s.syncTargetLocks(ctx, row)
}
func (s *Store) CreateStep(ctx context.Context, row entity.Step) error {
	return convert(s.db.WithContext(ctx).Table("job_steps").Create(&row).Error)
}
func (s *Store) save(ctx context.Context, table, id string, row any) error {
	q := s.db.WithContext(ctx).Table(table).Where("id = ?", id).Select("*").Updates(row)
	if q.Error != nil {
		return convert(q.Error)
	}
	if q.RowsAffected != 1 {
		return contract.ErrConflict
	}
	return nil
}
func (s *Store) SaveJob(ctx context.Context, row entity.Job) error {
	return s.save(ctx, "jobs", row.ID, &row)
}
func (s *Store) SaveItem(ctx context.Context, row entity.Item) error {
	if row.LockKeys == "" {
		row.LockKeys = "[]"
	}
	if err := s.save(ctx, "job_items", row.ID, &row); err != nil {
		return err
	}
	return s.syncTargetLocks(ctx, row)
}

// Call only inside the surrounding job transaction. Sorted acquisition avoids
// lock-order inversions when two operations share more than one resource.
func (s *Store) syncTargetLocks(ctx context.Context, row entity.Item) error {
	if row.Status != contract.Queued && row.Status != contract.Running && row.Status != contract.Waiting && row.Status != contract.Attention {
		return convert(s.db.WithContext(ctx).Exec("DELETE FROM operation_target_locks WHERE item_id=?", row.ID).Error)
	}
	var keys []string
	if json.Unmarshal([]byte(row.LockKeys), &keys) != nil {
		return contract.ErrUnavailable
	}
	if row.LockKey != "" {
		keys = append(keys, row.LockKey)
	}
	sort.Strings(keys)
	seen := map[string]bool{}
	for _, key := range keys {
		if seen[key] {
			continue
		}
		seen[key] = true
		if err := s.db.WithContext(ctx).Exec("INSERT INTO operation_target_locks(lock_key,item_id) VALUES(?,?) ON CONFLICT(lock_key) DO NOTHING", key, row.ID).Error; err != nil {
			return convert(err)
		}
		var owner string
		if err := s.db.WithContext(ctx).Raw("SELECT item_id::text FROM operation_target_locks WHERE lock_key=?", key).Scan(&owner).Error; err != nil {
			return convert(err)
		}
		if owner != row.ID {
			return contract.ErrTargetBusy
		}
	}
	return nil
}
func (s *Store) SaveStep(ctx context.Context, row entity.Step) error {
	return s.save(ctx, "job_steps", row.ID, &row)
}
func (s *Store) CreateAttempt(ctx context.Context, row entity.Attempt) error {
	return convert(s.db.WithContext(ctx).Table("job_attempts").Create(&row).Error)
}
func (s *Store) FinishAttempt(ctx context.Context, id string, epoch int64, outcome, code, result string, at time.Time) error {
	return convert(s.db.WithContext(ctx).Table("job_attempts").Where("step_id = ? AND lease_epoch = ? AND finished_at IS NULL", id, epoch).Updates(map[string]any{"finished_at": at, "outcome": outcome, "code": code, "result": result}).Error)
}
func (s *Store) AppendEvent(ctx context.Context, row entity.Event) error {
	return convert(s.db.WithContext(ctx).Table("job_events").Create(&row).Error)
}
func (s *Store) AddOutbox(ctx context.Context, row entity.Outbox) error {
	return convert(s.db.WithContext(ctx).Table("operations_outbox").Create(&row).Error)
}
func (s *Store) AppendAudit(ctx context.Context, event kernel.AuditEntry) error {
	if s.audit == nil {
		return contract.ErrUnavailable
	}
	return convert(s.audit(kernel.Connection{DB: s.db}).AppendAudit(ctx, event))
}
func (s *Store) RenewLease(ctx context.Context, id string, epoch int64, owner string, duration time.Duration) error {
	q := s.db.WithContext(ctx).Exec(`UPDATE job_steps SET lease_expires_at=clock_timestamp()+(? * interval '1 millisecond')
	WHERE id=? AND status='running' AND lease_epoch=? AND lease_owner=? AND lease_expires_at>clock_timestamp()`, duration.Milliseconds(), id, epoch, owner)
	if q.Error != nil {
		return convert(q.Error)
	}
	if q.RowsAffected != 1 {
		return contract.ErrLease
	}
	return nil
}
func (s *Store) LeaseState(ctx context.Context, id string, epoch int64, owner string) (bool, error) {
	var row struct{ CancelRequested bool }
	err := s.db.WithContext(ctx).Table("job_steps s").Select("j.cancel_requested").Joins("JOIN jobs j ON j.id=s.job_id").Where("s.id=? AND s.status='running' AND s.lease_epoch=? AND s.lease_owner=? AND s.lease_expires_at>clock_timestamp() AND s.deadline_at>clock_timestamp()", id, epoch, owner).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, contract.ErrLease
	}
	return row.CancelRequested, convert(err)
}
func (s *Store) CheckLease(ctx context.Context, id string, epoch int64, owner string) error {
	var row entity.Step
	err := s.db.WithContext(ctx).Table("job_steps").Clauses(clause.Locking{Strength: "SHARE"}).Where("id=? AND status='running' AND lease_epoch=? AND lease_owner=? AND lease_expires_at>clock_timestamp() AND deadline_at>clock_timestamp()", id, epoch, owner).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return contract.ErrLease
	}
	if err != nil {
		return convert(err)
	}
	now, err := s.Now(ctx)
	if err != nil {
		return err
	}
	if row.LeaseExpiresAt == nil || !row.LeaseExpiresAt.After(now) || !row.DeadlineAt.After(now) {
		return contract.ErrLease
	}
	return nil
}
func (s *Store) DueJobs(ctx context.Context, limit int, recoverAfter time.Duration) ([]string, error) {
	var ids []string
	err := s.db.WithContext(ctx).Raw(`SELECT DISTINCT job_id FROM job_steps WHERE
	(status='running' AND lease_expires_at<=clock_timestamp()) OR
	(status IN ('queued','waiting') AND next_run_at<=clock_timestamp() AND scheduled_at<=clock_timestamp()-(? * interval '1 millisecond'))
	ORDER BY job_id LIMIT ?`, recoverAfter.Milliseconds(), limit).Scan(&ids).Error
	return ids, convert(err)
}
func (s *Store) RecordAttemptBinding(ctx context.Context, id string, epoch int64, owner, binding string) error {
	q := s.db.WithContext(ctx).Exec(`UPDATE job_attempts a SET binding=? FROM job_steps s WHERE a.step_id=s.id AND a.lease_epoch=s.lease_epoch
	AND s.id=? AND s.lease_epoch=? AND s.lease_owner=? AND s.status='running' AND s.lease_expires_at>clock_timestamp() AND a.finished_at IS NULL`, binding, id, epoch, owner)
	if q.Error != nil {
		return convert(q.Error)
	}
	if q.RowsAffected != 1 {
		return contract.ErrLease
	}
	return nil
}
func (s *Store) ClaimOutbox(ctx context.Context, owner string, limit int, duration time.Duration) ([]entity.Outbox, error) {
	rows := []entity.Outbox{}
	err := s.db.WithContext(ctx).Raw(`WITH due AS (
	 SELECT id FROM operations_outbox WHERE published_at IS NULL AND available_at<=clock_timestamp()
	 AND (publish_until IS NULL OR publish_until<=clock_timestamp()) ORDER BY available_at,id FOR UPDATE SKIP LOCKED LIMIT ?
	) UPDATE operations_outbox o SET publish_owner=?,publish_until=clock_timestamp()+(? * interval '1 millisecond'),attempts=o.attempts+1
	FROM due WHERE o.id=due.id RETURNING o.*`, limit, owner, duration.Milliseconds()).Scan(&rows).Error
	return rows, convert(err)
}
func (s *Store) FinishOutbox(ctx context.Context, id, owner string, success bool) error {
	q := s.db.WithContext(ctx).Table("operations_outbox").Where("id=? AND publish_owner=? AND published_at IS NULL", id, owner)
	values := map[string]any{"publish_owner": "", "publish_until": nil, "last_error": ""}
	if success {
		values["published_at"] = gorm.Expr("clock_timestamp()")
	} else {
		values["last_error"] = "QUEUE_UNAVAILABLE"
		values["available_at"] = gorm.Expr("clock_timestamp()+interval '3 seconds'")
	}
	return convert(q.Updates(values).Error)
}
func (s *Store) DiagnosticApply(ctx context.Context, token string, delay time.Duration) (time.Time, error) {
	var ready time.Time
	err := s.db.WithContext(ctx).Raw(`INSERT INTO operations_diagnostic_effects(token,request_count,ready_at) VALUES (?,1,clock_timestamp()+(? * interval '1 millisecond'))
	ON CONFLICT(token) DO UPDATE SET request_count=operations_diagnostic_effects.request_count+1 RETURNING ready_at`, token, delay.Milliseconds()).Scan(&ready).Error
	return ready, convert(err)
}
func (s *Store) DiagnosticEffect(ctx context.Context, token string) (*time.Time, error) {
	var row struct{ ReadyAt time.Time }
	err := s.db.WithContext(ctx).Table("operations_diagnostic_effects").Where("token=?", token).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &row.ReadyAt, convert(err)
}
