package addresses

import (
	"context"
	"github.com/Ember-Moth/aws-panel/internal/auth"
	"github.com/Ember-Moth/aws-panel/internal/module/addresses/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/addresses/internal/awsapi"
	"github.com/Ember-Moth/aws-panel/internal/module/addresses/internal/service"
	"github.com/Ember-Moth/aws-panel/internal/module/addresses/internal/source"
	oc "github.com/Ember-Moth/aws-panel/internal/module/operations/contract"
	"github.com/Ember-Moth/aws-panel/internal/repository"
	"github.com/aws/aws-sdk-go-v2/aws"
)

type Deps = service.Deps
type ExecutorDeps = service.ExecutorDeps
type Cloud = source.Cloud
type Accepted = source.Accepted
type Operation = source.Operation
type Service interface {
	Options(context.Context, int64) (contract.Options, error)
	Catalogue(context.Context, int64, string, string, string, ...bool) (contract.Catalogue, error)
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

// AllocatedEC2Addresses returns ordinary and carrier allocations for inventory snapshots.
func AllocatedEC2Addresses(ctx context.Context, cfg aws.Config, account string) ([]contract.Address, error) {
	return (awsapi.Cloud{}).AllocatedEC2Addresses(ctx, cfg, account)
}

// PlanAutomatic is used by instance creation workflows; IPs are subordinate resources.
func PlanAutomatic(ctx context.Context, cloud Cloud, cfg aws.Config, kind, account, zone, border string) (contract.Automatic, error) {
	if cloud == nil {
		cloud = awsapi.Cloud{}
	}
	return service.PlanAutomatic(ctx, cloud, cfg, kind, account, zone, border)
}
func RunAutomatic(ctx context.Context, deps ExecutorDeps, call oc.Call, prefix string, policy contract.Automatic, instance contract.Instance, reconcile bool) oc.Result {
	if deps.Cloud == nil {
		deps.Cloud = awsapi.Cloud{}
	}
	return service.NewExecutor(deps).Automatic(ctx, call, prefix, policy, instance, reconcile)
}

func OwnedAllocation(ctx context.Context, store repository.EffectStore, account, region string, address contract.Address) (*contract.ManagedIP, error) {
	return service.OwnedAllocation(ctx, store, account, region, address)
}
func ReleaseOwned(ctx context.Context, deps ExecutorDeps, call oc.Call, address contract.ManagedIP, reconcile bool) oc.Result {
	return service.NewExecutor(deps).ReleaseOwned(ctx, call, address, reconcile)
}
