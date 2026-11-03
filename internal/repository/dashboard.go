package repository

import (
	"context"
	"github.com/herta-network/aws-panel/internal/module/dashboard/entity"
)

type DashboardStore interface {
	RecordSnapshot(context.Context, entity.Snapshot, entity.Lease) error
	Snapshots(context.Context, []string) ([]entity.Snapshot, error)
	Run(context.Context, string) (*entity.Run, error)
	Start(context.Context, entity.Run, entity.Snapshot, entity.Lease) (entity.Run, error)
	Advance(context.Context, entity.Run, []entity.Snapshot, entity.Lease) (entity.Run, error)
}
