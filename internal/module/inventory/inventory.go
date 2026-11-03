package inventory

import (
	"context"

	"github.com/herta-network/aws-panel/internal/auth"
	"github.com/herta-network/aws-panel/internal/module/inventory/contract"
	"github.com/herta-network/aws-panel/internal/module/inventory/internal/awsapi"
	"github.com/herta-network/aws-panel/internal/module/inventory/internal/executor"
	"github.com/herta-network/aws-panel/internal/module/inventory/internal/repo"
	"github.com/herta-network/aws-panel/internal/module/inventory/internal/service"
	"github.com/herta-network/aws-panel/internal/module/inventory/internal/source"
	oc "github.com/herta-network/aws-panel/internal/module/operations/contract"
	"github.com/herta-network/aws-panel/internal/repository"
	"github.com/herta-network/aws-panel/internal/repository/kernel"
)

type Deps = service.Deps
type ExecutorDeps = executor.Deps
type Cloud = source.Cloud
type FetchRequest = source.Request
type Snapshot = source.Snapshot
type FetchedResource = source.Resource
type Service interface {
	Options(context.Context, int64) (contract.Options, error)
	Resources(context.Context, int64, contract.Filter) (contract.ResourceList, error)
	Scopes(context.Context, int64, contract.Filter) (contract.ScopeList, error)
	Resource(context.Context, int64, string) (contract.ResourceDetail, error)
	Refresh(context.Context, int64, contract.RefreshRequest, auth.RequestMeta) (oc.Job, error)
	Schedules(context.Context, int64, int) (contract.ScheduleList, error)
	Schedule(context.Context, int64, string) (contract.Schedule, error)
	SaveSchedule(context.Context, int64, string, contract.ScheduleRequest, auth.RequestMeta) (contract.Schedule, error)
	DeleteSchedule(context.Context, int64, string, int64, auth.RequestMeta) error
	Tick(context.Context) error
}

func New(deps Deps) Service { return service.New(deps) }
func NewExecutor(deps ExecutorDeps) oc.Executor {
	if deps.Cloud == nil {
		deps.Cloud = awsapi.Cloud{}
	}
	return executor.New(deps)
}
func NewCloud() Cloud { return awsapi.Cloud{} }
func NewRepository(connection kernel.Connection, audit kernel.AuditFactory, guard kernel.ExecutionGuardFactory) repository.InventoryStore {
	return repo.New(connection, audit, guard)
}
