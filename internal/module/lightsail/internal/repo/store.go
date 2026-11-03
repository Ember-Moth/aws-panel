package repo

import (
	"context"
	"errors"
	"time"

	"github.com/herta-network/aws-panel/internal/module/lightsail/contract"
	"github.com/herta-network/aws-panel/internal/module/lightsail/entity"
	"github.com/herta-network/aws-panel/internal/repository/kernel"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Store struct {
	db    *gorm.DB
	guard kernel.ExecutionGuardFactory
}

func New(connection kernel.Connection, guard kernel.ExecutionGuardFactory) *Store {
	return &Store{db: connection.DB, guard: guard}
}
func (s *Store) Creation(ctx context.Context, id string) (*entity.Creation, error) {
	var row entity.Creation
	err := s.db.WithContext(ctx).Table("lightsail_creations").Where("step_id=?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, contract.ErrUnavailable
	}
	return &row, nil
}
func (s *Store) within(ctx context.Context, lease entity.Lease, fn func(*gorm.DB, time.Time) error) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if s.guard == nil {
			return contract.ErrUnavailable
		}
		if err := s.guard(kernel.Connection{DB: tx}).CheckLease(ctx, lease.StepID, lease.Epoch, lease.Owner); err != nil {
			return contract.ErrLease
		}
		var now time.Time
		if tx.Raw("SELECT clock_timestamp()").Scan(&now).Error != nil {
			return contract.ErrUnavailable
		}
		return fn(tx, now)
	})
}
func (s *Store) PrepareCreation(ctx context.Context, row entity.Creation, lease entity.Lease) (entity.Creation, error) {
	if row.StepID != lease.StepID {
		return entity.Creation{}, contract.ErrLease
	}
	err := s.within(ctx, lease, func(tx *gorm.DB, now time.Time) error {
		var existing entity.Creation
		err := tx.Table("lightsail_creations").Clauses(clause.Locking{Strength: "UPDATE"}).Where("step_id=?", row.StepID).Take(&existing).Error
		if err == nil {
			if existing.Token != row.Token || existing.SpecHash != row.SpecHash || existing.AccountID != row.AccountID || existing.Region != row.Region || existing.Name != row.Name {
				return contract.ErrOwnership
			}
			row = existing
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return contract.ErrUnavailable
		}
		row.State = "prepared"
		row.OperationIDs = "[]"
		row.CreatedAt = now
		row.UpdatedAt = now
		if tx.Table("lightsail_creations").Create(&row).Error != nil {
			return contract.ErrUnavailable
		}
		return nil
	})
	return row, err
}
func (s *Store) DispatchCreation(ctx context.Context, id string, lease entity.Lease) (entity.Creation, error) {
	if id != lease.StepID {
		return entity.Creation{}, contract.ErrLease
	}
	var row entity.Creation
	err := s.within(ctx, lease, func(tx *gorm.DB, now time.Time) error {
		if tx.Table("lightsail_creations").Clauses(clause.Locking{Strength: "UPDATE"}).Where("step_id=?", id).Take(&row).Error != nil {
			return contract.ErrUnavailable
		}
		if row.State != "prepared" && row.State != "rejected" {
			return contract.ErrOwnership
		}
		row.State = "dispatched"
		row.SendCount++
		row.Code = ""
		row.OperationIDs = "[]"
		row.LastSentAt = &now
		if row.FirstSentAt == nil {
			row.FirstSentAt = &now
		}
		row.UpdatedAt = now
		if tx.Table("lightsail_creations").Where("step_id=?", id).Select("*").Updates(&row).Error != nil {
			return contract.ErrUnavailable
		}
		return nil
	})
	return row, err
}
func (s *Store) RecordCreation(ctx context.Context, row entity.Creation, lease entity.Lease) error {
	if row.StepID != lease.StepID {
		return contract.ErrLease
	}
	return s.within(ctx, lease, func(tx *gorm.DB, now time.Time) error {
		var current entity.Creation
		if tx.Table("lightsail_creations").Clauses(clause.Locking{Strength: "UPDATE"}).Where("step_id=?", row.StepID).Take(&current).Error != nil {
			return contract.ErrUnavailable
		}
		if current.Token != row.Token || current.SendCount != row.SendCount {
			return contract.ErrOwnership
		}
		if current.ResourceARN != "" && row.ResourceARN != "" && current.ResourceARN != row.ResourceARN {
			return contract.ErrOwnership
		}
		if tx.Table("lightsail_creations").Where("step_id=?", row.StepID).Updates(map[string]any{"state": row.State, "resource_arn": row.ResourceARN, "operation_ids": row.OperationIDs, "aws_request_id": row.AWSRequestID, "code": row.Code, "updated_at": now}).Error != nil {
			return contract.ErrUnavailable
		}
		return nil
	})
}
