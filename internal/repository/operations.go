package repository

import (
	"context"
	"time"

	"github.com/Ember-Moth/aws-panel/internal/module/operations/entity"
	"github.com/Ember-Moth/aws-panel/internal/repository/kernel"
)

type OperationsStore interface {
	Plan(context.Context, string) (entity.Plan, error)
	Job(context.Context, string) (entity.Job, error)
	JobByKey(context.Context, int64, string, string) (entity.Job, error)
	Jobs(context.Context, entity.Scope, int, int) ([]entity.Job, error)
	Items(context.Context, string) ([]entity.Item, error)
	Steps(context.Context, string) ([]entity.Step, error)
	Attempts(context.Context, string) ([]entity.Attempt, error)
	Events(context.Context, string, int64, int) ([]entity.Event, error)
	LatestTargets(context.Context, string, string) ([]entity.TargetState, error)
	InOperationsTx(context.Context, func(OperationsTx) error) error
	RenewLease(context.Context, string, int64, string, time.Duration) error
	LeaseState(context.Context, string, int64, string) (bool, error)
	RecordAttemptBinding(context.Context, string, int64, string, string) error
	DueJobs(context.Context, int, time.Duration) ([]string, error)
	ClaimOutbox(context.Context, string, int, time.Duration) ([]entity.Outbox, error)
	FinishOutbox(context.Context, string, string, bool) error
	DiagnosticApply(context.Context, string, time.Duration) (time.Time, error)
	DiagnosticEffect(context.Context, string) (*time.Time, error)
}
type OperationsTx interface {
	OperationsStore
	Now(context.Context) (time.Time, error)
	LockSubmit(context.Context, int64, string, string) error
	LockCloudReferences(context.Context) error
	CheckBinding(context.Context, string, string) error
	JobForUpdate(context.Context, string) (entity.Job, error)
	StepsForUpdate(context.Context, string) ([]entity.Step, error)
	CreatePlan(context.Context, entity.Plan) error
	CreateJob(context.Context, entity.Job) error
	CreateItem(context.Context, entity.Item) error
	CreateStep(context.Context, entity.Step) error
	SaveJob(context.Context, entity.Job) error
	SaveItem(context.Context, entity.Item) error
	SaveStep(context.Context, entity.Step) error
	CreateAttempt(context.Context, entity.Attempt) error
	FinishAttempt(context.Context, string, int64, string, string, string, time.Time) error
	AppendEvent(context.Context, entity.Event) error
	AddOutbox(context.Context, entity.Outbox) error
	kernel.AuditAppender
}
