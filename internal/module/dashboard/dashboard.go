package dashboard

import (
	"context"
	"github.com/Ember-Moth/aws-panel/internal/auth"
	"github.com/Ember-Moth/aws-panel/internal/module/dashboard/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/dashboard/internal/awsapi"
	"github.com/Ember-Moth/aws-panel/internal/module/dashboard/internal/repo"
	"github.com/Ember-Moth/aws-panel/internal/module/dashboard/internal/service"
	"github.com/Ember-Moth/aws-panel/internal/module/dashboard/internal/source"
	oc "github.com/Ember-Moth/aws-panel/internal/module/operations/contract"
	"github.com/Ember-Moth/aws-panel/internal/repository"
	"github.com/Ember-Moth/aws-panel/internal/repository/kernel"
)

type Deps = service.Deps
type ExecutorDeps = service.ExecutorDeps
type Cloud = source.Cloud
type Catalogue = source.Catalogue
type Observation = source.Observation
type Service interface {
	Read(context.Context, int64, contract.Query) (contract.Overview, error)
	Refresh(context.Context, int64, contract.RefreshRequest, auth.RequestMeta) (contract.Refresh, error)
}

func New(d Deps) Service { return service.New(d) }
func NewCloud() Cloud    { return awsapi.Cloud{} }
func NewExecutor(d ExecutorDeps) oc.Executor {
	if d.Cloud == nil {
		d.Cloud = awsapi.Cloud{}
	}
	return service.NewExecutor(d)
}
func NewRepository(c kernel.Connection, guard kernel.ExecutionGuardFactory) repository.DashboardStore {
	return repo.New(c, guard)
}
