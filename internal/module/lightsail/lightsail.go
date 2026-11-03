package lightsail

import (
	"context"

	"github.com/herta-network/aws-panel/internal/auth"
	"github.com/herta-network/aws-panel/internal/module/lightsail/contract"
	"github.com/herta-network/aws-panel/internal/module/lightsail/internal/awsapi"
	"github.com/herta-network/aws-panel/internal/module/lightsail/internal/repo"
	"github.com/herta-network/aws-panel/internal/module/lightsail/internal/service"
	"github.com/herta-network/aws-panel/internal/module/lightsail/internal/source"
	oc "github.com/herta-network/aws-panel/internal/module/operations/contract"
	"github.com/herta-network/aws-panel/internal/repository"
	"github.com/herta-network/aws-panel/internal/repository/kernel"
)

type Deps = service.Deps
type ExecutorDeps = service.ExecutorDeps
type Cloud = source.Cloud
type Instance = source.Instance
type Operation = source.Operation
type Created = source.Created
type Service interface {
	Regions(context.Context, int64, string) (contract.Regions, error)
	Options(context.Context, int64) (contract.Options, error)
	Catalogue(context.Context, int64, string, string) (contract.Catalogue, error)
	Prepare(context.Context, int64, contract.CreateRequest) (contract.Preview, error)
	Plan(context.Context, int64, string) (contract.Preview, error)
	Submit(context.Context, int64, string, string, auth.RequestMeta) (oc.Job, error)
}

func New(deps Deps) Service {
	if deps.Cloud == nil {
		deps.Cloud = awsapi.Cloud{}
	}
	return service.New(deps)
}
func NewExecutor(deps ExecutorDeps) oc.Executor {
	if deps.Cloud == nil {
		deps.Cloud = awsapi.Cloud{}
	}
	return service.NewExecutor(deps)
}
func NewCloud() Cloud { return awsapi.Cloud{} }
func NewRepository(connection kernel.Connection, guard kernel.ExecutionGuardFactory) repository.LightsailStore {
	return repo.New(connection, guard)
}
