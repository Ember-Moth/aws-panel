package compute

import (
	"context"
	"github.com/herta-network/aws-panel/internal/auth"
	"github.com/herta-network/aws-panel/internal/module/addresses"
	"github.com/herta-network/aws-panel/internal/module/compute/contract"
	"github.com/herta-network/aws-panel/internal/module/compute/internal/awsapi"
	"github.com/herta-network/aws-panel/internal/module/compute/internal/service"
	"github.com/herta-network/aws-panel/internal/module/compute/internal/source"
	oc "github.com/herta-network/aws-panel/internal/module/operations/contract"
)

type Deps = service.Deps
type ExecutorDeps = service.ExecutorDeps
type Cloud = source.Cloud
type CatalogueParts = source.CatalogueParts
type WavelengthCloud = source.WavelengthCloud
type IngressCloud = source.IngressCloud
type FirewallCloud = source.FirewallCloud
type Accepted = source.Accepted
type Service interface {
	WavelengthZones(context.Context, int64, string, string, ...bool) (contract.WavelengthListing, error)
	WavelengthCatalogue(context.Context, int64, string, string, string, ...bool) (contract.Catalogue, error)
	Options(context.Context, int64) (contract.Options, error)
	Catalogue(context.Context, int64, string, string, ...bool) (contract.Catalogue, error)
	Instances(context.Context, int64, string, string, ...bool) (contract.Listing, error)
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
	if d.AddressCloud == nil {
		d.AddressCloud = addresses.NewCloud()
	}
	if d.Cloud == nil {
		d.Cloud = awsapi.Cloud{}
	}
	return service.NewExecutor(d)
}
func NewCloud() Cloud { return awsapi.Cloud{} }
