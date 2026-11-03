package platform

import (
	"context"
	"time"

	"github.com/herta-network/aws-panel/internal/module/platform/contract"
	"github.com/herta-network/aws-panel/internal/module/platform/internal/repo"
	"github.com/herta-network/aws-panel/internal/module/platform/internal/system"
	"github.com/herta-network/aws-panel/internal/repository/kernel"
)

type Service interface {
	LoadRuntime(context.Context) (contract.Runtime, error)
	Status(context.Context) (*contract.SystemStatus, error)
	RecordHeartbeat(context.Context) error
	ListAudit(context.Context, int64) (*contract.AuditList, error)
}

type Deps struct {
	Store             kernel.PlatformStore
	Runtime           func() contract.Runtime
	Version           string
	Revision          string
	HeartbeatInterval time.Duration
}

func New(deps Deps) Service {
	return system.New(system.Deps{
		Store: deps.Store, Runtime: deps.Runtime,
		Version: deps.Version, Revision: deps.Revision,
		HeartbeatInterval: deps.HeartbeatInterval,
	})
}

func NewRepository(connection kernel.Connection) kernel.PlatformStore {
	return repo.New(connection)
}

func NewAuditAppender(connection kernel.Connection) kernel.AuditAppender { return repo.New(connection) }
