package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/herta-network/aws-panel/internal/auth"
	"github.com/herta-network/aws-panel/internal/infra/clock"
	ac "github.com/herta-network/aws-panel/internal/module/accounts/contract"
	oc "github.com/herta-network/aws-panel/internal/module/operations/contract"
	"github.com/herta-network/aws-panel/internal/module/regions/contract"
	"github.com/herta-network/aws-panel/internal/module/regions/internal/source"
)

type Access interface {
	Principal(context.Context, int64) (auth.Principal, error)
	AccountAccess(context.Context, int64, string, bool) (bool, error)
}
type Accounts interface {
	Clients
	Inspect(context.Context, int64, string, string, bool) (ac.Snapshot, error)
	ListAccounts(context.Context, int64) ([]ac.Account, error)
	AccountDetail(context.Context, int64, string) (*ac.AccountDetail, error)
}
type Jobs interface {
	Prepare(context.Context, int64, oc.PlanRequest) (oc.Plan, error)
	Plan(context.Context, int64, string) (oc.Plan, error)
	Submit(context.Context, int64, oc.SubmitRequest, auth.RequestMeta) (oc.Job, error)
}
type Deps struct {
	Accounts      Accounts
	Access        Access
	Jobs          Jobs
	WritesEnabled bool
	Cloud         source.Cloud
}

func (s *Service) Catalogue(ctx context.Context, actorID int64, id string) (contract.Catalogue, error) {
	if err := s.actor(ctx, actorID); err != nil {
		return contract.Catalogue{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	info, err := s.deps.Accounts.Inspect(ctx, actorID, id, "us-east-1", true)
	if err != nil {
		return contract.Catalogue{}, Translate(err)
	}
	if info.Partition != "aws" {
		return contract.Catalogue{}, contract.ErrInvalid
	}
	cfg, err := s.deps.Accounts.ConfigFor(ctx, actorID, id, "us-east-1", true)
	if err != nil {
		return contract.Catalogue{}, Translate(err)
	}
	rows, err := s.deps.Cloud.List(ctx, cfg)
	if err != nil {
		return contract.Catalogue{}, contract.ErrUnavailable
	}
	result := contract.Catalogue{ConnectionID: id, Regions: []contract.Region{}, CheckedAt: clock.Now()}
	for _, row := range rows {
		result.Regions = append(result.Regions, contract.Region{Code: row.Region, State: row.Status, Selectable: row.Status == "DISABLED"})
	}
	return result, nil
}

type Service struct{ deps Deps }

func New(deps Deps) *Service { return &Service{deps: deps} }
func (s *Service) actor(ctx context.Context, id int64) error {
	actor, err := s.deps.Access.Principal(ctx, id)
	if errors.Is(err, auth.ErrDenied) {
		return contract.ErrForbidden
	}
	if err != nil {
		return contract.ErrUnavailable
	}
	if actor.Role != "admin" && actor.Role != "operator" {
		return contract.ErrForbidden
	}
	return nil
}
func Translate(err error) error {
	var op *oc.Error
	if errors.As(err, &op) {
		if op.Code == "CONNECTION_CHANGED" {
			return &contract.Error{Code: op.Code, Message: "连接不可用或配置已变化，请检查连接并重新预览"}
		}
		return &contract.Error{Code: op.Code, Message: op.Message}
	}
	var account *ac.Error
	if errors.As(err, &account) {
		return &contract.Error{Code: account.Code, Message: account.Message}
	}
	return err
}
func (s *Service) Options(ctx context.Context, actor int64) (contract.Options, error) {
	if err := s.actor(ctx, actor); err != nil {
		return contract.Options{}, err
	}
	rows, err := s.deps.Accounts.ListAccounts(ctx, actor)
	if err != nil {
		return contract.Options{}, Translate(err)
	}
	result := contract.Options{Accounts: []contract.Account{}, WritesEnabled: s.deps.WritesEnabled}
	for _, row := range rows {
		if row.Partition != "aws" {
			continue
		}
		allowed, err := s.deps.Access.AccountAccess(ctx, actor, row.ID, true)
		if err != nil {
			return contract.Options{}, contract.ErrUnavailable
		}
		if !allowed {
			continue
		}
		detail, err := s.deps.Accounts.AccountDetail(ctx, actor, row.ID)
		if err != nil {
			return contract.Options{}, Translate(err)
		}
		value := contract.Account{ID: row.ID, Name: row.Name, AWSAccountID: row.AWSAccountID, Connections: []contract.Connection{}}
		for _, connection := range detail.Connections {
			value.Connections = append(value.Connections, contract.Connection{ID: connection.ID, Name: connection.Name, Status: connection.Status})
		}
		result.Accounts = append(result.Accounts, value)
	}
	return result, nil
}
func preview(plan oc.Plan) (contract.Preview, error) {
	if plan.Operation != Operation {
		return contract.Preview{}, contract.ErrInvalid
	}
	result := contract.Preview{ID: plan.ID, ExpiresAt: plan.ExpiresAt, CanSubmit: plan.CanSubmit, Targets: []contract.PreviewTarget{}}
	for _, item := range plan.Items {
		var input contract.StepSpec
		if len(item.Steps) != 1 || json.Unmarshal(item.Steps[0].Input, &input) != nil {
			return contract.Preview{}, contract.ErrUnavailable
		}
		result.Targets = append(result.Targets, contract.PreviewTarget{AccountID: item.Target.Binding.AccountID, AWSAccountID: item.Target.Binding.AWSAccountID, Region: input.Region, State: input.ObservedState, CheckedAt: clock.In(input.CheckedAt)})
	}
	return result, nil
}
func (s *Service) Prepare(ctx context.Context, actorID int64, req contract.EnableRequest) (contract.Preview, error) {
	if err := s.actor(ctx, actorID); err != nil {
		return contract.Preview{}, err
	}
	values, err := normalize(req.Targets)
	if err != nil {
		return contract.Preview{}, err
	}
	raw, _ := json.Marshal(contract.EnableRequest{Targets: values})
	targets := make([]oc.Target, 0, len(values))
	for _, value := range values {
		targets = append(targets, oc.Target{Key: targetKey(value), Label: value.Region, Binding: oc.Binding{ConnectionID: value.ConnectionID, Region: "us-east-1"}})
	}
	plan, err := s.deps.Jobs.Prepare(ctx, actorID, oc.PlanRequest{Operation: Operation, InputVersion: 1, Input: raw, Targets: targets})
	if err != nil {
		return contract.Preview{}, Translate(err)
	}
	return preview(plan)
}
func (s *Service) Plan(ctx context.Context, actorID int64, id string) (contract.Preview, error) {
	if err := s.actor(ctx, actorID); err != nil {
		return contract.Preview{}, err
	}
	plan, err := s.deps.Jobs.Plan(ctx, actorID, id)
	if err != nil {
		return contract.Preview{}, Translate(err)
	}
	return preview(plan)
}
func (s *Service) Submit(ctx context.Context, actorID int64, planID, key string, meta auth.RequestMeta) (oc.Job, error) {
	if _, err := s.Plan(ctx, actorID, planID); err != nil {
		return oc.Job{}, err
	}
	job, err := s.deps.Jobs.Submit(ctx, actorID, oc.SubmitRequest{PlanID: planID, IdempotencyKey: key}, meta)
	return job, Translate(err)
}
