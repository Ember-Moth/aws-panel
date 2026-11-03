package regions

import (
	"context"

	"github.com/herta-network/aws-panel/internal/auth"
	oc "github.com/herta-network/aws-panel/internal/module/operations/contract"
	"github.com/herta-network/aws-panel/internal/module/regions/contract"
	"github.com/herta-network/aws-panel/internal/module/regions/internal/awsapi"
	"github.com/herta-network/aws-panel/internal/module/regions/internal/service"
	"github.com/herta-network/aws-panel/internal/module/regions/internal/source"
)

type Deps = service.Deps
type ExecutorDeps = service.ExecutorDeps
type Cloud = source.Cloud
type State = source.State
type Service interface {
	Catalogue(context.Context, int64, string, ...bool) (contract.Catalogue, error)
	Options(context.Context, int64) (contract.Options, error)
	Prepare(context.Context, int64, contract.EnableRequest) (contract.Preview, error)
	Plan(context.Context, int64, string) (contract.Preview, error)
	Submit(context.Context, int64, string, string, auth.RequestMeta) (oc.Job, error)
}

func New(deps Deps) Service {
	if deps.Cloud == nil {
		deps.Cloud = awsapi.Cloud{}
	}
	return service.New(deps)
}
func NewCloud() Cloud { return awsapi.Cloud{} }
func NewExecutor(deps ExecutorDeps) oc.Executor {
	if deps.Cloud == nil {
		deps.Cloud = awsapi.Cloud{}
	}
	return service.NewExecutor(deps)
}
