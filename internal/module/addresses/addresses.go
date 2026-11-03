package addresses

import (
	"context"
	"github.com/herta-network/aws-panel/internal/auth"
	"github.com/herta-network/aws-panel/internal/module/addresses/contract"
	"github.com/herta-network/aws-panel/internal/module/addresses/internal/awsapi"
	"github.com/herta-network/aws-panel/internal/module/addresses/internal/service"
	"github.com/herta-network/aws-panel/internal/module/addresses/internal/source"
	oc "github.com/herta-network/aws-panel/internal/module/operations/contract"
)

type Deps = service.Deps
type ExecutorDeps = service.ExecutorDeps
type Cloud = source.Cloud
type Accepted = source.Accepted
type Operation = source.Operation
type Service interface {
	Options(context.Context, int64) (contract.Options, error)
	Catalogue(context.Context, int64, string, string, string) (contract.Catalogue, error)
	Prepare(context.Context, int64, contract.Request) (contract.Preview, error)
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
func InstanceLocks(kind, id, name string) []string { return service.InstanceLocks(kind, id, name) }
func AddressLocks(kind string, value contract.Address) []string {
	return service.AddressLocks(kind, value)
}
