package compute

import (
	"context"
	"github.com/herta-network/aws-panel/internal/auth"
	"github.com/herta-network/aws-panel/internal/module/compute/contract"
	"github.com/herta-network/aws-panel/internal/module/compute/internal/awsapi"
	"github.com/herta-network/aws-panel/internal/module/compute/internal/service"
	"github.com/herta-network/aws-panel/internal/module/compute/internal/source"
	oc "github.com/herta-network/aws-panel/internal/module/operations/contract"
)

type Deps = service.Deps
type ExecutorDeps = service.ExecutorDeps
type Cloud = source.Cloud
type Accepted = source.Accepted
type Service interface {
	Options(context.Context, int64) (contract.Options, error)
	Catalogue(context.Context, int64, string, string) (contract.Catalogue, error)
	Instances(context.Context, int64, string, string) (contract.Listing, error)
	Prepare(context.Context, int64, contract.Request) (contract.Preview, error)
	Plan(context.Context, int64, string) (contract.Preview, error)
	Submit(context.Context, int64, string, string, auth.RequestMeta) (oc.Job, error)
}

func New(d Deps) Service {
	if d.Cloud == nil {
		d.Cloud = awsapi.Cloud{}
	}
	return service.New(d)
}
func NewExecutor(d ExecutorDeps) oc.Executor {
	if d.Cloud == nil {
		d.Cloud = awsapi.Cloud{}
	}
	return service.NewExecutor(d)
}
func NewCloud() Cloud { return awsapi.Cloud{} }
