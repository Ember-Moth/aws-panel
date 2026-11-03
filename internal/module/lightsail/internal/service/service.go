package service

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/herta-network/aws-panel/internal/catalogue"
	"github.com/herta-network/aws-panel/internal/infra/catalogcache"
	"strings"
	"time"

	"github.com/herta-network/aws-panel/internal/auth"
	"github.com/herta-network/aws-panel/internal/infra/clock"
	ac "github.com/herta-network/aws-panel/internal/module/accounts/contract"
	"github.com/herta-network/aws-panel/internal/module/lightsail/contract"
	"github.com/herta-network/aws-panel/internal/module/lightsail/internal/source"
	oc "github.com/herta-network/aws-panel/internal/module/operations/contract"
)

type Access interface {
	Principal(context.Context, int64) (auth.Principal, error)
	AccountAccess(context.Context, int64, string, bool) (bool, error)
}
type Accounts interface {
	Clients
	LaunchSecrets
	ListAccounts(context.Context, int64) ([]ac.Account, error)
	AccountDetail(context.Context, int64, string) (*ac.AccountDetail, error)
	Inspect(context.Context, int64, string, string, bool) (ac.Snapshot, error)
}
type Jobs interface {
	Prepare(context.Context, int64, oc.PlanRequest) (oc.Plan, error)
	Plan(context.Context, int64, string) (oc.Plan, error)
	Submit(context.Context, int64, oc.SubmitRequest, auth.RequestMeta) (oc.Job, error)
}
type Deps struct {
	Cache         *catalogcache.Cache
	Accounts      Accounts
	Access        Access
	Jobs          Jobs
	Cloud         source.Cloud
	WritesEnabled bool
}
type Service struct{ deps Deps }

func New(deps Deps) *Service { return &Service{deps: deps} }
func (s *Service) Regions(ctx context.Context, actorID int64, id string, refresh ...bool) (contract.Regions, error) {
	if err := s.actor(ctx, actorID); err != nil {
		return contract.Regions{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	info, err := s.deps.Accounts.Inspect(ctx, actorID, id, "us-east-1", true)
	if err != nil {
		return contract.Regions{}, publicError(err)
	}
	if info.Partition != "aws" {
		return contract.Regions{}, contract.ErrInvalid
	}
	cfg, err := s.deps.Accounts.ConfigFor(ctx, actorID, id, "us-east-1", true)
	if err != nil {
		return contract.Regions{}, publicError(err)
	}
	rows, fresh, err := catalogcache.Read(ctx, s.deps.Cache, catalogcache.Key("lightsail-regions", info.Partition, info.AccountID, id, info.Fingerprint), catalogcache.Regions, forceRefresh(refresh), func(ctx context.Context) ([]contract.Region, error) { return s.deps.Cloud.Regions(ctx, cfg) })
	if err != nil {
		return contract.Regions{}, publicError(err)
	}
	return contract.Regions{ConnectionID: id, Items: rows, CheckedAt: clock.In(fresh.UpdatedAt), Freshness: map[string]catalogue.Freshness{"regions": fresh}}, nil
}
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
func (s *Service) Options(ctx context.Context, actorID int64) (contract.Options, error) {
	if err := s.actor(ctx, actorID); err != nil {
		return contract.Options{}, err
	}
	rows, err := s.deps.Accounts.ListAccounts(ctx, actorID)
	if err != nil {
		return contract.Options{}, publicError(err)
	}
	result := contract.Options{Accounts: []contract.Account{}, WritesEnabled: s.deps.WritesEnabled}
	for _, row := range rows {
		if row.Partition != "aws" {
			continue
		}
		allowed, err := s.deps.Access.AccountAccess(ctx, actorID, row.ID, true)
		if err != nil {
			return contract.Options{}, contract.ErrUnavailable
		}
		if !allowed {
			continue
		}
		detail, err := s.deps.Accounts.AccountDetail(ctx, actorID, row.ID)
		if err != nil {
			return contract.Options{}, publicError(err)
		}
		value := contract.Account{ID: row.ID, Name: row.Name, AWSAccountID: row.AWSAccountID, Connections: []contract.Connection{}}
		for _, connection := range detail.Connections {
			value.Connections = append(value.Connections, contract.Connection{ID: connection.ID, Name: connection.Name, Status: connection.Status})
		}
		result.Accounts = append(result.Accounts, value)
	}
	return result, nil
}
func (s *Service) Catalogue(ctx context.Context, actorID int64, id, region string, refresh ...bool) (contract.Catalogue, error) {
	if err := s.actor(ctx, actorID); err != nil {
		return contract.Catalogue{}, err
	}
	if !regionPattern.MatchString(region) || len(region) > 30 || strings.HasPrefix(region, "cn-") || strings.HasPrefix(region, "us-gov-") {
		return contract.Catalogue{}, contract.ErrInvalid
	}
	snapshot, err := s.deps.Accounts.Inspect(ctx, actorID, id, region, true)
	if err != nil {
		return contract.Catalogue{}, publicError(err)
	}
	if snapshot.Partition != "aws" {
		return contract.Catalogue{}, contract.ErrInvalid
	}
	cfg, err := s.deps.Accounts.ConfigFor(ctx, actorID, id, region, true)
	if err != nil {
		return contract.Catalogue{}, publicError(err)
	}
	result, err := s.displayCatalogue(ctx, cfg, snapshot, forceRefresh(refresh))
	if err != nil {
		return contract.Catalogue{}, publicError(err)
	}
	result.ConnectionID = id
	result.WritesEnabled = s.deps.WritesEnabled
	return result, nil
}
func preview(plan oc.Plan) (contract.Preview, error) {
	if plan.Operation != Operation {
		return contract.Preview{}, contract.ErrInvalid
	}
	result := contract.Preview{ID: plan.ID, ExpiresAt: plan.ExpiresAt, CanSubmit: plan.CanSubmit, Targets: []contract.PreviewTarget{}}
	for _, item := range plan.Items {
		var spec contract.StepSpec
		if len(item.Steps) < 2 || json.Unmarshal(item.Steps[0].Input, &spec) != nil {
			return contract.Preview{}, contract.ErrUnavailable
		}
		spec.CheckedAt = clock.In(spec.CheckedAt)
		result.Targets = append(result.Targets, contract.PreviewTarget{AccountID: item.Target.Binding.AccountID, AWSAccountID: item.Target.Binding.AWSAccountID, Spec: spec})
		result.MonthlyCents += spec.Bundle.MonthlyCents
	}
	return result, nil
}
func (s *Service) Prepare(ctx context.Context, actorID int64, req contract.CreateRequest) (contract.Preview, error) {
	if err := s.actor(ctx, actorID); err != nil {
		return contract.Preview{}, err
	}
	values, cleanup, err := s.prepareLogins(ctx, actorID, req)
	saved := false
	defer func() {
		if !saved {
			cleanup()
		}
	}()
	if err != nil {
		return contract.Preview{}, err
	}
	raw, _ := json.Marshal(contract.PlanInput{Targets: values})
	targets := make([]oc.Target, 0, len(values))
	for _, value := range values {
		targets = append(targets, oc.Target{Key: key(value), Label: value.Name, Binding: oc.Binding{ConnectionID: value.ConnectionID, Region: value.Region}})
	}
	plan, err := s.deps.Jobs.Prepare(ctx, actorID, oc.PlanRequest{Operation: Operation, InputVersion: 1, Input: raw, Targets: targets})
	if err != nil {
		return contract.Preview{}, publicError(err)
	}
	result, err := preview(plan)
	if err == nil {
		saved = true
	}
	return result, err
}
func (s *Service) Plan(ctx context.Context, actorID int64, id string) (contract.Preview, error) {
	if err := s.actor(ctx, actorID); err != nil {
		return contract.Preview{}, err
	}
	plan, err := s.deps.Jobs.Plan(ctx, actorID, id)
	if err != nil {
		return contract.Preview{}, publicError(err)
	}
	return preview(plan)
}
func (s *Service) Submit(ctx context.Context, actorID int64, id, key string, meta auth.RequestMeta) (oc.Job, error) {
	if _, err := s.Plan(ctx, actorID, id); err != nil {
		return oc.Job{}, err
	}
	job, err := s.deps.Jobs.Submit(ctx, actorID, oc.SubmitRequest{PlanID: id, IdempotencyKey: key}, meta)
	return job, publicError(err)
}
