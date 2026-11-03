package operations

import (
	"context"
	"encoding/json"

	"github.com/Ember-Moth/aws-panel/internal/auth"
	"github.com/Ember-Moth/aws-panel/internal/module/operations/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/operations/internal/diagnostic"
	"github.com/Ember-Moth/aws-panel/internal/module/operations/internal/repo"
	"github.com/Ember-Moth/aws-panel/internal/module/operations/internal/service"
	"github.com/Ember-Moth/aws-panel/internal/repository"
	"github.com/Ember-Moth/aws-panel/internal/repository/kernel"
)

type Deps = service.Deps
type Publisher = service.Publisher
type Connections = service.Connections
type Observer interface {
	LatestTargets(context.Context, string, []contract.TargetRef) ([]contract.TargetState, error)
}

func NewObserver(store repository.OperationsStore) Observer { return service.NewObserver(store) }

type Service interface {
	Prepare(context.Context, int64, contract.PlanRequest) (contract.Plan, error)
	Plan(context.Context, int64, string) (contract.Plan, error)
	Submit(context.Context, int64, contract.SubmitRequest, auth.RequestMeta) (contract.Job, error)
	Submission(context.Context, int64, string, string, int, json.RawMessage) (contract.Job, error)
	List(context.Context, int64, int) (contract.JobList, error)
	Detail(context.Context, int64, string) (contract.Detail, error)
	Events(context.Context, int64, string, int64) (contract.EventPage, error)
	Cancel(context.Context, int64, string, auth.RequestMeta) error
	Retry(context.Context, int64, string, contract.RetryRequest, auth.RequestMeta) error
	Run(context.Context, contract.Message) error
	Publish(context.Context) error
	Recover(context.Context) error
}

func New(deps Deps) (Service, error) { return service.New(deps) }
func NewRepository(connection kernel.Connection, audit kernel.AuditFactory, guards ...kernel.BindingGuardFactory) repository.OperationsStore {
	return repo.New(connection, audit, guards...)
}
func NewReferenceGuard(connection kernel.Connection) kernel.ReferenceGuard {
	return repo.New(connection, nil)
}
func NewExecutionGuard(connection kernel.Connection) kernel.ExecutionGuard {
	return repo.New(connection, nil)
}
func NewEffectStore(connection kernel.Connection) repository.EffectStore {
	return repo.NewEffects(connection)
}
func NewDiagnostic(store repository.OperationsStore) contract.Executor { return diagnostic.New(store) }
