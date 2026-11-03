package repo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"
	"unicode"

	oc "github.com/herta-network/aws-panel/internal/module/operations/contract"
	"github.com/herta-network/aws-panel/internal/module/operations/entity"
	"github.com/herta-network/aws-panel/internal/repository/kernel"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Effects struct{ db *gorm.DB }

func NewEffects(connection kernel.Connection) *Effects { return &Effects{db: connection.DB} }
func (s *Effects) Effect(ctx context.Context, id string) (*oc.Effect, error) {
	var row oc.Effect
	err := s.db.WithContext(ctx).Table("operation_effects").Where("step_id=?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, oc.ErrUnavailable
	}
	return &row, nil
}
func (s *Effects) within(ctx context.Context, lease oc.EffectLease, fn func(*gorm.DB, time.Time) error) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		guard := New(kernel.Connection{DB: tx}, nil)
		if err := guard.CheckLease(ctx, lease.StepID, lease.Epoch, lease.Owner); err != nil {
			return err
		}
		now, err := guard.Now(ctx)
		if err != nil {
			return err
		}
		return fn(tx, now)
	})
}
func (s *Effects) PrepareEffect(ctx context.Context, row oc.Effect, lease oc.EffectLease) (oc.Effect, error) {
	if row.StepID != lease.StepID || row.Purpose == "" || len(row.Purpose) > 80 || len(row.SpecHash) != 64 {
		return oc.Effect{}, oc.ErrEffect
	}
	err := s.within(ctx, lease, func(tx *gorm.DB, now time.Time) error {
		var existing oc.Effect
		err := tx.Table("operation_effects").Clauses(clause.Locking{Strength: "UPDATE"}).Where("step_id=?", row.StepID).Take(&existing).Error
		if err == nil {
			if existing.JobID != row.JobID || existing.AccountID != row.AccountID || existing.Region != row.Region || existing.Purpose != row.Purpose || existing.SpecHash != row.SpecHash || existing.Token != row.Token {
				return oc.ErrEffect
			}
			row = existing
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return oc.ErrUnavailable
		}
		var matched int64
		if tx.Table("job_steps s").Joins("JOIN job_items i ON i.id=s.item_id").Where("s.id=? AND s.job_id=? AND i.account_id=? AND i.region=? AND s.idempotency_token=?", row.StepID, row.JobID, row.AccountID, row.Region, row.Token).Count(&matched).Error != nil {
			return oc.ErrUnavailable
		}
		if matched != 1 {
			return oc.ErrEffect
		}
		row.SendCount = 0
		row.FirstSentAt = nil
		row.LastSentAt = nil
		row.RequestID = ""
		row.Code = ""
		row.State = "prepared"
		row.Proof = "{}"
		row.ResourceID = ""
		row.CreatedAt = now
		row.UpdatedAt = now
		return convert(tx.Table("operation_effects").Create(&row).Error)
	})
	return row, err
}
func (s *Effects) DispatchEffect(ctx context.Context, id string, lease oc.EffectLease) (oc.Effect, error) {
	if id != lease.StepID {
		return oc.Effect{}, oc.ErrLease
	}
	var row oc.Effect
	err := s.within(ctx, lease, func(tx *gorm.DB, now time.Time) error {
		if tx.Table("operation_effects").Clauses(clause.Locking{Strength: "UPDATE"}).Where("step_id=?", id).Take(&row).Error != nil {
			return oc.ErrUnavailable
		}
		if row.State != "prepared" && row.State != "rejected" {
			return oc.ErrEffect
		}
		row.State = "dispatched"
		row.SendCount++
		row.LastSentAt = &now
		if row.FirstSentAt == nil {
			row.FirstSentAt = &now
		}
		row.UpdatedAt = now
		row.Code = ""
		row.RequestID = ""
		row.Proof = "{}"
		return convert(tx.Table("operation_effects").Where("step_id=?", id).Select("*").Updates(&row).Error)
	})
	return row, err
}
func (s *Effects) RecordEffect(ctx context.Context, row oc.Effect, lease oc.EffectLease) error {
	if row.StepID != lease.StepID {
		return oc.ErrLease
	}
	if (row.State != "dispatched" && row.State != "rejected" && row.State != "confirmed") || len(row.ResourceID) > 1024 || len(row.RequestID) > 256 || len(row.Code) > 80 || len(row.Proof) > 64<<10 || !json.Valid([]byte(row.Proof)) {
		return oc.ErrEffect
	}
	return s.within(ctx, lease, func(tx *gorm.DB, now time.Time) error {
		var current oc.Effect
		if tx.Table("operation_effects").Clauses(clause.Locking{Strength: "UPDATE"}).Where("step_id=?", row.StepID).Take(&current).Error != nil {
			return oc.ErrUnavailable
		}
		if current.Token != row.Token || current.SendCount != row.SendCount || current.SpecHash != row.SpecHash || current.Purpose != row.Purpose || current.JobID != row.JobID {
			return oc.ErrEffect
		}
		if current.State == "confirmed" && row.State != "confirmed" {
			return oc.ErrEffect
		}
		if current.ResourceID != "" && current.ResourceID != row.ResourceID {
			return oc.ErrEffect
		}
		if err := claimEffectResources(ctx, tx, lease, row.ResourceLocks); err != nil {
			return err
		}
		return convert(tx.Table("operation_effects").Where("step_id=?", row.StepID).Updates(map[string]any{"state": row.State, "resource_id": row.ResourceID, "request_id": row.RequestID, "code": row.Code, "proof": row.Proof, "updated_at": now}).Error)
	})
}

func claimEffectResources(ctx context.Context, tx *gorm.DB, lease oc.EffectLease, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	if len(keys) > 8 {
		return oc.ErrEffect
	}
	for _, key := range keys {
		if key == "" || len(key) > 256 {
			return oc.ErrEffect
		}
		for _, r := range key {
			if unicode.IsControl(r) {
				return oc.ErrEffect
			}
		}
	}
	var item entity.Item
	if tx.Table("job_items i").Select("i.*").Joins("JOIN job_steps s ON s.item_id=i.id").Clauses(clause.Locking{Strength: "UPDATE", Table: clause.Table{Name: "i"}}).Where("s.id=?", lease.StepID).Take(&item).Error != nil {
		return oc.ErrUnavailable
	}
	var binding oc.Binding
	var current []string
	if json.Unmarshal([]byte(item.Binding), &binding) != nil || json.Unmarshal([]byte(item.LockKeys), &current) != nil {
		return oc.ErrUnavailable
	}
	seen := map[string]bool{}
	for _, key := range current {
		seen[key] = true
	}
	for _, key := range keys {
		sum := sha256.Sum256([]byte(binding.Partition + "\x00" + binding.AWSAccountID + "\x00" + item.Region + "\x00" + key))
		seen[hex.EncodeToString(sum[:])] = true
	}
	if len(seen) > 32 {
		return oc.ErrEffect
	}
	current = current[:0]
	for key := range seen {
		current = append(current, key)
	}
	sort.Strings(current)
	raw, _ := json.Marshal(current)
	item.LockKeys = string(raw)
	if err := New(kernel.Connection{DB: tx}, nil).syncTargetLocks(ctx, item); err != nil {
		return err
	}
	return convert(tx.Table("job_items").Where("id=?", item.ID).Update("lock_keys", item.LockKeys).Error)
}
