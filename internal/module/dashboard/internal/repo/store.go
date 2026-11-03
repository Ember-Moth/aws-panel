package repo

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Ember-Moth/aws-panel/internal/module/dashboard/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/dashboard/entity"
	"github.com/Ember-Moth/aws-panel/internal/repository/kernel"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

type Store struct {
	db    *gorm.DB
	guard kernel.ExecutionGuardFactory
}

func New(c kernel.Connection, guard kernel.ExecutionGuardFactory) *Store {
	return &Store{db: c.DB, guard: guard}
}
func convert(err error) error {
	if err == nil {
		return nil
	}
	var own *contract.Error
	if errors.As(err, &own) {
		return own
	}
	return contract.ErrUnavailable
}
func (s *Store) Snapshots(ctx context.Context, accounts []string) ([]entity.Snapshot, error) {
	rows := []entity.Snapshot{}
	if len(accounts) == 0 {
		return rows, nil
	}
	err := s.db.WithContext(ctx).Table("dashboard_snapshots").Where("account_id IN ?", accounts).Find(&rows).Error
	return rows, convert(err)
}
func (s *Store) Run(ctx context.Context, id string) (*entity.Run, error) {
	var row entity.Run
	err := s.db.WithContext(ctx).Table("dashboard_runs").Where("step_id=?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, contract.ErrUnavailable
	}
	return &row, nil
}
func (s *Store) within(ctx context.Context, lease entity.Lease, fn func(*gorm.DB, time.Time) error) error {
	if s.guard == nil {
		return contract.ErrUnavailable
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.guard(kernel.Connection{DB: tx}).CheckLease(ctx, lease.StepID, lease.Epoch, lease.Owner); err != nil {
			return err
		}
		var now time.Time
		if tx.Raw("SELECT clock_timestamp()").Scan(&now).Error != nil {
			return contract.ErrUnavailable
		}
		return fn(tx, now)
	})
}
func saveSnapshot(tx *gorm.DB, row entity.Snapshot, now time.Time) error {
	if row.AccountID == "" || row.Region == "" || row.Kind == "" || len(row.Code) > 80 || len(row.Data) > 8<<20 || !json.Valid([]byte(row.Data)) {
		return contract.ErrInvalid
	}
	if row.Status != "succeeded" && row.Status != "failed" {
		return contract.ErrInvalid
	}
	var success *time.Time
	if row.Status == "succeeded" {
		success = &now
	}
	return convert(tx.Exec(`INSERT INTO dashboard_snapshots(account_id,region,kind,connection_id,fingerprint,status,code,data,last_attempt_at,last_success_at) VALUES (?,?,?,?,?,?,?,?::jsonb,?,?)
 ON CONFLICT(account_id,region,kind) DO UPDATE SET
 connection_id=CASE WHEN excluded.status='succeeded' THEN excluded.connection_id ELSE dashboard_snapshots.connection_id END,
 fingerprint=CASE WHEN excluded.status='succeeded' THEN excluded.fingerprint ELSE dashboard_snapshots.fingerprint END,
 status=excluded.status,code=excluded.code,last_attempt_at=excluded.last_attempt_at,
 data=CASE WHEN excluded.status='succeeded' THEN excluded.data ELSE dashboard_snapshots.data END,
 last_success_at=CASE WHEN excluded.status='succeeded' THEN excluded.last_success_at ELSE dashboard_snapshots.last_success_at END`, row.AccountID, row.Region, row.Kind, row.ConnectionID, row.Fingerprint, row.Status, row.Code, row.Data, now, success).Error)
}
func (s *Store) Start(ctx context.Context, row entity.Run, catalog entity.Snapshot, lease entity.Lease) (entity.Run, error) {
	if row.StepID != lease.StepID || len(row.SpecHash) != 64 || !json.Valid([]byte(row.Catalog)) {
		return row, contract.ErrInvalid
	}
	err := s.within(ctx, lease, func(tx *gorm.DB, now time.Time) error {
		var existing entity.Run
		err := tx.Table("dashboard_runs").Clauses(clause.Locking{Strength: "UPDATE"}).Where("step_id=?", row.StepID).Take(&existing).Error
		if err == nil {
			if existing.JobID != row.JobID || existing.AccountID != row.AccountID || existing.ConnectionID != row.ConnectionID || existing.SpecHash != row.SpecHash {
				return contract.ErrInvalid
			}
			if existing.Fingerprint == row.Fingerprint {
				row = existing
				return nil
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return contract.ErrUnavailable
		}
		var matches int64
		if tx.Table("job_steps s").Joins("JOIN job_items i ON i.id=s.item_id").Where("s.id=? AND s.job_id=? AND i.account_id=? AND i.connection_id=?", row.StepID, row.JobID, row.AccountID, row.ConnectionID).Count(&matches).Error != nil || matches != 1 {
			return contract.ErrInvalid
		}
		row.NextRegion = 0
		row.StartedAt = now
		row.UpdatedAt = now
		if row.FinishedAt != nil {
			row.FinishedAt = &now
		}
		if err := tx.Table("dashboard_runs").Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "step_id"}}, UpdateAll: true}).Create(&row).Error; err != nil {
			return contract.ErrUnavailable
		}
		return saveSnapshot(tx, catalog, now)
	})
	return row, err
}
func (s *Store) Advance(ctx context.Context, row entity.Run, snapshots []entity.Snapshot, lease entity.Lease) (entity.Run, error) {
	if row.StepID != lease.StepID {
		return row, contract.ErrInvalid
	}
	err := s.within(ctx, lease, func(tx *gorm.DB, now time.Time) error {
		var current entity.Run
		if tx.Table("dashboard_runs").Clauses(clause.Locking{Strength: "UPDATE"}).Where("step_id=?", row.StepID).Take(&current).Error != nil {
			return contract.ErrUnavailable
		}
		if current.JobID != row.JobID || current.AccountID != row.AccountID || current.ConnectionID != row.ConnectionID || current.Fingerprint != row.Fingerprint || current.SpecHash != row.SpecHash || current.NextRegion+1 != row.NextRegion || current.FinishedAt != nil {
			return contract.ErrInvalid
		}
		for _, snapshot := range snapshots {
			if snapshot.AccountID != row.AccountID || snapshot.ConnectionID != row.ConnectionID || snapshot.Fingerprint != row.Fingerprint {
				return contract.ErrInvalid
			}
			if err := saveSnapshot(tx, snapshot, now); err != nil {
				return err
			}
		}
		row.UpdatedAt = now
		if row.FinishedAt != nil {
			row.FinishedAt = &now
		}
		return convert(tx.Table("dashboard_runs").Where("step_id=?", row.StepID).Updates(map[string]any{"next_region": row.NextRegion, "failures": row.Failures, "updated_at": now, "finished_at": row.FinishedAt}).Error)
	})
	return row, err
}

func (s *Store) RecordSnapshot(ctx context.Context, row entity.Snapshot, lease entity.Lease) error {
	return s.within(ctx, lease, func(tx *gorm.DB, now time.Time) error {
		var count int64
		if tx.Table("job_items i").Joins("JOIN job_steps s ON s.item_id=i.id").Where("s.id=? AND i.account_id=? AND i.connection_id=?", lease.StepID, row.AccountID, row.ConnectionID).Count(&count).Error != nil || count != 1 {
			return contract.ErrInvalid
		}
		return saveSnapshot(tx, row, now)
	})
}
