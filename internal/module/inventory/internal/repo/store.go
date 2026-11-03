package repo

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/herta-network/aws-panel/internal/module/inventory/contract"
	"github.com/herta-network/aws-panel/internal/module/inventory/entity"
	"github.com/herta-network/aws-panel/internal/repository"
	"github.com/herta-network/aws-panel/internal/repository/kernel"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Store struct {
	db    *gorm.DB
	audit kernel.AuditFactory
	guard kernel.ExecutionGuardFactory
}

func New(c kernel.Connection, audit kernel.AuditFactory, guard kernel.ExecutionGuardFactory) *Store {
	return &Store{db: c.DB, audit: audit, guard: guard}
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
func (s *Store) Now(ctx context.Context) (time.Time, error) {
	var now time.Time
	err := s.db.WithContext(ctx).Raw("SELECT clock_timestamp()").Scan(&now).Error
	return now, convert(err)
}
func (s *Store) InInventoryTx(ctx context.Context, fn func(repository.InventoryTx) error) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return fn(&Store{db: tx, audit: s.audit, guard: s.guard}) })
}
func (s *Store) EnsureScopes(ctx context.Context, values []entity.Scope) error {
	return s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		for _, value := range values {
			if value.ID == "" {
				value.ID = uuid.NewString()
			}
			value.Status = "never"
			value.CreatedAt = time.Now()
			if err := db.WithContext(ctx).Table("inventory_scopes").Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "account_id"}, {Name: "kind"}, {Name: "region"}}, DoNothing: true}).Create(&value).Error; err != nil {
				return convert(err)
			}
		}
		return nil
	})
}
func (s *Store) Scope(ctx context.Context, id string) (entity.Scope, error) {
	var row entity.Scope
	err := s.db.WithContext(ctx).Table("inventory_scopes").Where("id=?", id).Take(&row).Error
	return row, convert(err)
}
func (s *Store) Scopes(ctx context.Context, q entity.Query) ([]entity.Scope, error) {
	rows := []entity.Scope{}
	if len(q.AccountIDs) == 0 {
		return rows, nil
	}
	db := s.db.WithContext(ctx).Table("inventory_scopes").Where("account_id IN ?", q.AccountIDs)
	if q.Kind != "" {
		db = db.Where("kind=?", q.Kind)
	}
	if q.Region != "" {
		db = db.Where("(kind IN ('account_region','lightsail_region') OR region=?)", q.Region)
	}
	err := db.Order("account_id,kind,region").Offset((q.Page - 1) * q.Limit).Limit(q.Limit + 1).Find(&rows).Error
	return rows, convert(err)
}
func (s *Store) Resources(ctx context.Context, q entity.Query) ([]entity.Resource, error) {
	rows := []entity.Resource{}
	if len(q.AccountIDs) == 0 {
		return rows, nil
	}
	db := s.db.WithContext(ctx).Table("resource_snapshots r").Select("r.*,s.account_id,s.kind,s.region AS scope_region").Joins("JOIN inventory_scopes s ON s.id=r.scope_id").Where("s.account_id IN ?", q.AccountIDs)
	if q.Kind != "" {
		db = db.Where("s.kind=?", q.Kind)
	}
	if q.Region != "" {
		db = db.Where("r.region=?", q.Region)
	}
	if q.State != "" {
		db = db.Where("r.state=?", q.State)
	}
	if !q.IncludeMissing {
		db = db.Where("r.present=true")
	}
	if q.Text != "" {
		value := strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(q.Text)
		db = db.Where("(r.name ILIKE ? OR r.resource_id ILIKE ?)", "%"+value+"%", "%"+value+"%")
	}
	err := db.Order("r.name,r.id").Offset((q.Page - 1) * q.Limit).Limit(q.Limit + 1).Find(&rows).Error
	return rows, convert(err)
}
func (s *Store) Resource(ctx context.Context, id string) (entity.Resource, error) {
	var row entity.Resource
	err := s.db.WithContext(ctx).Table("resource_snapshots r").Select("r.*,s.account_id,s.kind,s.region AS scope_region").Joins("JOIN inventory_scopes s ON s.id=r.scope_id").Where("r.id=?", id).Take(&row).Error
	return row, convert(err)
}
func (s *Store) KnownRegions(ctx context.Context, ids []string) ([]string, error) {
	values := []string{}
	if len(ids) == 0 {
		return values, nil
	}
	err := s.db.WithContext(ctx).Raw("SELECT DISTINCT r.region FROM resource_snapshots r JOIN inventory_scopes s ON s.id=r.scope_id WHERE s.account_id IN ? AND r.present=true ORDER BY r.region LIMIT 200", ids).Scan(&values).Error
	return values, convert(err)
}
func (s *Store) CompletedRun(ctx context.Context, stepID, fingerprint string) (*entity.Run, error) {
	var row entity.Run
	err := s.db.WithContext(ctx).Table("inventory_runs").Where("step_id=? AND fingerprint=? AND status='complete'", stepID, fingerprint).Order("started_at DESC").Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &row, convert(err)
}
func (s *Store) check(ctx context.Context, lease entity.Lease) error {
	if s.guard == nil {
		return contract.ErrUnavailable
	}
	if err := s.guard(kernel.Connection{DB: s.db}).CheckLease(ctx, lease.StepID, lease.Epoch, lease.Owner); err != nil {
		return contract.ErrStale
	}
	return nil
}
func (s *Store) BeginRefresh(ctx context.Context, scope entity.Scope, lease entity.Lease, connectionID, fingerprint string) (entity.Ticket, error) {
	var ticket entity.Ticket
	err := s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		tx := &Store{db: db, audit: s.audit, guard: s.guard}
		if err := tx.check(ctx, lease); err != nil {
			return err
		}
		if err := tx.EnsureScopes(ctx, []entity.Scope{scope}); err != nil {
			return err
		}
		if err := db.WithContext(ctx).Table("inventory_scopes").Clauses(clause.Locking{Strength: "UPDATE"}).Where("account_id=? AND kind=? AND region=?", scope.AccountID, scope.Kind, scope.Region).Take(&scope).Error; err != nil {
			return convert(err)
		}
		now, err := tx.Now(ctx)
		if err != nil {
			return err
		}
		scope.Generation++
		scope.Status = "syncing"
		scope.LastAttemptAt = &now
		scope.LastErrorCode = ""
		scope.LastConnectionID = connectionID
		run := entity.Run{ID: uuid.NewString(), ScopeID: scope.ID, JobID: lease.JobID, StepID: lease.StepID, LeaseEpoch: lease.Epoch, Generation: scope.Generation, ConnectionID: connectionID, Fingerprint: fingerprint, Status: "running", StartedAt: now}
		scope.ActiveRunID = &run.ID
		if err := db.WithContext(ctx).Table("inventory_runs").Create(&run).Error; err != nil {
			return convert(err)
		}
		if err := db.WithContext(ctx).Table("inventory_scopes").Where("id=?", scope.ID).Select("*").Updates(&scope).Error; err != nil {
			return convert(err)
		}
		ticket = entity.Ticket{Scope: scope, Run: run, Lease: lease}
		return nil
	})
	return ticket, err
}
func (s *Store) CompleteRefresh(ctx context.Context, ticket entity.Ticket, values []entity.Snapshot, pages int, requestID string) error {
	return s.finish(ctx, ticket, func(db *gorm.DB, scope *entity.Scope, now time.Time) error {
		for start := 0; start < len(values); start += 100 {
			end := min(start+100, len(values))
			batch := append([]entity.Snapshot{}, values[start:end]...)
			for i := range batch {
				batch[i].ID = uuid.NewString()
				batch[i].ScopeID = scope.ID
				batch[i].Generation = ticket.Run.Generation
				batch[i].Present = true
				batch[i].MissingSince = nil
			}
			if err := db.WithContext(ctx).Table("resource_snapshots").Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "scope_id"}, {Name: "resource_id"}}, DoUpdates: clause.AssignmentColumns([]string{"name", "region", "state", "details", "observed_at", "present", "missing_since", "generation"})}).Create(&batch).Error; err != nil {
				return convert(err)
			}
		}
		if err := db.WithContext(ctx).Table("resource_snapshots").Where("scope_id=? AND generation<>? AND present=true", scope.ID, ticket.Run.Generation).Updates(map[string]any{"present": false, "missing_since": now}).Error; err != nil {
			return convert(err)
		}
		scope.Status = "ready"
		scope.LastSuccessAt = &now
		scope.LastErrorCode = ""
		scope.ResourceCount = len(values)
		return convert(db.WithContext(ctx).Table("inventory_runs").Where("id=?", ticket.Run.ID).Updates(map[string]any{"status": "complete", "finished_at": now, "page_count": pages, "resource_count": len(values), "aws_request_id": requestID}).Error)
	})
}
func (s *Store) FailRefresh(ctx context.Context, ticket entity.Ticket, code string) error {
	return s.finish(ctx, ticket, func(db *gorm.DB, scope *entity.Scope, now time.Time) error {
		scope.Status = "failed"
		scope.LastErrorCode = code
		return convert(db.WithContext(ctx).Table("inventory_runs").Where("id=?", ticket.Run.ID).Updates(map[string]any{"status": "failed", "code": code, "finished_at": now}).Error)
	})
}
func (s *Store) finish(ctx context.Context, ticket entity.Ticket, fn func(*gorm.DB, *entity.Scope, time.Time) error) error {
	return s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		tx := &Store{db: db, audit: s.audit, guard: s.guard}
		if err := tx.check(ctx, ticket.Lease); err != nil {
			return err
		}
		var scope entity.Scope
		if err := db.WithContext(ctx).Table("inventory_scopes").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", ticket.Scope.ID).Take(&scope).Error; err != nil {
			return convert(err)
		}
		if scope.ActiveRunID == nil || *scope.ActiveRunID != ticket.Run.ID || scope.Generation != ticket.Run.Generation {
			return contract.ErrStale
		}
		now, err := tx.Now(ctx)
		if err != nil {
			return err
		}
		if err := fn(db, &scope, now); err != nil {
			return err
		}
		return convert(db.WithContext(ctx).Table("inventory_scopes").Where("id=?", scope.ID).Select("*").Updates(&scope).Error)
	})
}
