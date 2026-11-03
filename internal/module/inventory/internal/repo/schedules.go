package repo

import (
	"context"
	"time"

	"github.com/Ember-Moth/aws-panel/internal/module/inventory/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/inventory/entity"
	"github.com/Ember-Moth/aws-panel/internal/repository/kernel"
	"gorm.io/gorm/clause"
)

func (s *Store) Schedules(ctx context.Context, actor int64, admin bool, page, size int) ([]entity.Schedule, error) {
	rows := []entity.Schedule{}
	db := s.db.WithContext(ctx).Table("inventory_schedules")
	if !admin {
		db = db.Where("actor_id=?", actor)
	}
	err := db.Order("created_at DESC,id").Offset((page - 1) * size).Limit(size + 1).Find(&rows).Error
	return rows, convert(err)
}
func (s *Store) Schedule(ctx context.Context, id string) (entity.Schedule, error) {
	var row entity.Schedule
	err := s.db.WithContext(ctx).Table("inventory_schedules").Where("id=?", id).Take(&row).Error
	return row, convert(err)
}
func (s *Store) ScheduleForUpdate(ctx context.Context, id string) (entity.Schedule, error) {
	var row entity.Schedule
	err := s.db.WithContext(ctx).Table("inventory_schedules").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&row).Error
	return row, convert(err)
}
func (s *Store) SaveSchedule(ctx context.Context, row entity.Schedule, version int64) error {
	if version == 0 {
		return convert(s.db.WithContext(ctx).Table("inventory_schedules").Create(&row).Error)
	}
	result := s.db.WithContext(ctx).Table("inventory_schedules").Where("id=? AND version=?", row.ID, version).Select("*").Updates(&row)
	if result.Error != nil {
		return convert(result.Error)
	}
	if result.RowsAffected != 1 {
		return contract.ErrConflict
	}
	return nil
}
func (s *Store) DeleteSchedule(ctx context.Context, id string, version int64) error {
	result := s.db.WithContext(ctx).Exec("DELETE FROM inventory_schedules WHERE id=? AND version=?", id, version)
	if result.Error != nil {
		return convert(result.Error)
	}
	if result.RowsAffected != 1 {
		return contract.ErrConflict
	}
	return nil
}
func (s *Store) ClaimSchedules(ctx context.Context, owner string, limit int, lease time.Duration) ([]entity.Schedule, error) {
	rows := []entity.Schedule{}
	err := s.db.WithContext(ctx).Raw(`WITH due AS (SELECT id FROM inventory_schedules WHERE active AND next_run_at<=clock_timestamp() AND (lease_until IS NULL OR lease_until<=clock_timestamp())
	ORDER BY next_run_at,id FOR UPDATE SKIP LOCKED LIMIT ?)
	UPDATE inventory_schedules s SET lease_owner=?,lease_until=clock_timestamp()+(? * interval '1 millisecond') FROM due WHERE s.id=due.id RETURNING s.*`, limit, owner, lease.Milliseconds()).Scan(&rows).Error
	return rows, convert(err)
}
func (s *Store) FinishSchedule(ctx context.Context, id string, version int64, owner string, jobID *string, code string) error {
	return convert(s.db.WithContext(ctx).Exec(`UPDATE inventory_schedules SET lease_owner='',lease_until=NULL,last_job_id=COALESCE(?,last_job_id),last_error_code=?,last_run_at=clock_timestamp(),next_run_at=clock_timestamp()+(interval_seconds*interval '1 second')
	WHERE id=? AND version=? AND lease_owner=?`, jobID, code, id, version, owner).Error)
}
func (s *Store) AppendAudit(ctx context.Context, event kernel.AuditEntry) error {
	if s.audit == nil {
		return contract.ErrUnavailable
	}
	return convert(s.audit(kernel.Connection{DB: s.db}).AppendAudit(ctx, event))
}
