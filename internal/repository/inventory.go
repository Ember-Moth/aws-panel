package repository

import (
	"context"
	"time"

	"github.com/Ember-Moth/aws-panel/internal/module/inventory/entity"
	"github.com/Ember-Moth/aws-panel/internal/repository/kernel"
)

type InventoryStore interface {
	Overview(context.Context, []string) ([]entity.Overview, error)
	EnsureScopes(context.Context, []entity.Scope) error
	Scopes(context.Context, entity.Query) ([]entity.Scope, error)
	Scope(context.Context, string) (entity.Scope, error)
	Resources(context.Context, entity.Query) ([]entity.Resource, error)
	Resource(context.Context, string) (entity.Resource, error)
	KnownRegions(context.Context, []string) ([]string, error)
	CompletedRun(context.Context, string, string) (*entity.Run, error)
	BeginRefresh(context.Context, entity.Scope, entity.Lease, string, string) (entity.Ticket, error)
	CompleteRefresh(context.Context, entity.Ticket, []entity.Snapshot, int, string) error
	FailRefresh(context.Context, entity.Ticket, string) error
	Schedules(context.Context, int64, bool, int, int) ([]entity.Schedule, error)
	Schedule(context.Context, string) (entity.Schedule, error)
	ClaimSchedules(context.Context, string, int, time.Duration) ([]entity.Schedule, error)
	FinishSchedule(context.Context, string, int64, string, *string, string) error
	InInventoryTx(context.Context, func(InventoryTx) error) error
}
type InventoryTx interface {
	InventoryStore
	Now(context.Context) (time.Time, error)
	ScheduleForUpdate(context.Context, string) (entity.Schedule, error)
	SaveSchedule(context.Context, entity.Schedule, int64) error
	DeleteSchedule(context.Context, string, int64) error
	kernel.AuditAppender
}
