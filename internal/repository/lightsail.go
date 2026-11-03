package repository

import (
	"context"
	"github.com/herta-network/aws-panel/internal/module/lightsail/entity"
)

type LightsailStore interface {
	Creation(context.Context, string) (*entity.Creation, error)
	PrepareCreation(context.Context, entity.Creation, entity.Lease) (entity.Creation, error)
	DispatchCreation(context.Context, string, entity.Lease) (entity.Creation, error)
	RecordCreation(context.Context, entity.Creation, entity.Lease) error
}
