package service

import (
	"context"
	"encoding/json"
	"errors"
	catalogmeta "github.com/Ember-Moth/aws-panel/internal/catalogue"
	"github.com/Ember-Moth/aws-panel/internal/infra/catalogcache"
	"strconv"
	"time"

	"github.com/Ember-Moth/aws-panel/internal/auth"
	"github.com/Ember-Moth/aws-panel/internal/infra/clock"
	ac "github.com/Ember-Moth/aws-panel/internal/module/accounts/contract"
	ad "github.com/Ember-Moth/aws-panel/internal/module/addresses/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/compute/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/compute/internal/source"
	oc "github.com/Ember-Moth/aws-panel/internal/module/operations/contract"
	"github.com/Ember-Moth/aws-panel/internal/repository"
	"github.com/aws/aws-sdk-go-v2/aws"
)

type Access interface {
	Principal(context.Context, int64) (auth.Principal, error)
	AccountAccess(context.Context, int64, string, bool) (bool, error)
}
type Clients interface {
	ConfigFor(context.Context, int64, string, string, bool) (aws.Config, error)
	ConfigForBound(context.Context, int64, string, string, bool, string, func(context.Context) error) (aws.Config, error)
}
type Accounts interface {
	Clients
	LaunchSecrets
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
	Cache         *catalogcache.Cache
	Effects       repository.EffectStore
	Accounts      Accounts
	Access        Access
	Jobs          Jobs
	Cloud         source.Cloud
	WritesEnabled bool
}
type Service struct{ deps Deps }

func New(deps Deps) *Service { return &Service{deps: deps} }
func (s *Service) actor(ctx context.Context, id int64) error {
	actor, err := s.deps.Access.Principal(ctx, id)
	if err != nil {
		return contract.ErrForbidden
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
			return result, contract.ErrUnavailable
		}
		if !allowed {
			continue
		}
		detail, err := s.deps.Accounts.AccountDetail(ctx, actorID, row.ID)
		if err != nil {
			return result, publicError(err)
		}
		account := contract.Account{ID: row.ID, Name: row.Name, AWSAccountID: row.AWSAccountID, Connections: []contract.Connection{}}
		for _, connection := range detail.Connections {
			account.Connections = append(account.Connections, contract.Connection{ID: connection.ID, Name: connection.Name, Status: connection.Status})
		}
		result.Accounts = append(result.Accounts, account)
	}
	return result, nil
}
func (s *Service) config(ctx context.Context, actorID int64, id, region string) (aws.Config, ac.Snapshot, error) {
	if err := s.actor(ctx, actorID); err != nil {
		return aws.Config{}, ac.Snapshot{}, err
	}
	if !validRegion(region) {
		return aws.Config{}, ac.Snapshot{}, contract.ErrInvalid
	}
	info, err := s.deps.Accounts.Inspect(ctx, actorID, id, region, true)
	if err != nil {
		return aws.Config{}, info, publicError(err)
	}
	if info.Partition != "aws" {
		return aws.Config{}, info, contract.ErrInvalid
	}
	cfg, err := s.deps.Accounts.ConfigFor(ctx, actorID, id, region, true)
	return cfg, info, publicError(err)
}
func (s *Service) Catalogue(ctx context.Context, actorID int64, id, region string, refresh ...bool) (contract.Catalogue, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cfg, info, err := s.config(ctx, actorID, id, region)
	if err != nil {
		return contract.Catalogue{}, err
	}
	v, err := s.displayCatalogue(ctx, cfg, info, "", forceRefresh(refresh))
	v.ConnectionID = id
	v.WritesEnabled = s.deps.WritesEnabled
	return v, publicError(err)
}
func (s *Service) Instances(ctx context.Context, actorID int64, id, region string, refresh ...bool) (contract.Listing, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cfg, info, err := s.config(ctx, actorID, id, region)
	if err != nil {
		return contract.Listing{}, err
	}
	result, fresh, err := catalogcache.Read(ctx, s.deps.Cache, catalogcache.Key("compute-instances", info.Partition, info.AccountID, id, info.Fingerprint, region), catalogcache.Resources, forceRefresh(refresh), func(ctx context.Context) (contract.Listing, error) {
		rows, err := s.deps.Cloud.Instances(ctx, cfg, info.AWSAccountID)
		if err == nil && s.deps.Effects != nil {
			rows, err = pairInstances(ctx, s.deps.Effects, info.AccountID, region, rows)
		}
		return contract.Listing{ConnectionID: id, Region: region, Items: rows}, err
	})
	result.CheckedAt = clock.In(fresh.UpdatedAt)
	result.Freshness = map[string]catalogmeta.Freshness{"resources": fresh}
	return result, publicError(err)
}
func preview(plan oc.Plan) (contract.Preview, error) {
	if plan.Operation != OperationID {
		return contract.Preview{}, contract.ErrInvalid
	}
	result := contract.Preview{ID: plan.ID, ExpiresAt: plan.ExpiresAt, CanSubmit: plan.CanSubmit, Targets: []contract.PreviewTarget{}}
	for _, item := range plan.Items {
		var spec contract.Spec
		if len(item.Steps) == 0 || json.Unmarshal(item.Steps[0].Input, &spec) != nil {
			return result, contract.ErrUnavailable
		}
		result.Targets = append(result.Targets, contract.PreviewTarget{AWSAccountID: item.Target.Binding.AWSAccountID, Spec: spec})
	}
	return result, nil
}
func (s *Service) Prepare(ctx context.Context, actorID int64, req contract.Request) (contract.Preview, error) {
	if err := s.actor(ctx, actorID); err != nil {
		return contract.Preview{}, err
	}
	targets, cleanup, err := s.prepareLogins(ctx, actorID, req)
	saved := false
	defer func() {
		if !saved {
			cleanup()
		}
	}()
	if err != nil {
		return contract.Preview{}, err
	}
	raw, _ := json.Marshal(contract.PlanInput{Targets: targets})
	bindings := make([]oc.Target, 0, len(targets))
	for i, target := range targets {
		bindings = append(bindings, oc.Target{Key: "ec2-" + strconv.Itoa(i), Label: "EC2 操作", Binding: oc.Binding{ConnectionID: target.ConnectionID, Region: target.Region}})
	}
	plan, err := s.deps.Jobs.Prepare(ctx, actorID, oc.PlanRequest{Operation: OperationID, InputVersion: 1, Input: raw, Targets: bindings})
	if err != nil {
		return contract.Preview{}, publicError(err)
	}
	result, err := preview(plan)
	saved = err == nil
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
func publicError(err error) error {
	if err == nil {
		return nil
	}
	var own *contract.Error
	if errors.As(err, &own) {
		return own
	}
	var address *ad.Error
	if errors.As(err, &address) {
		return &contract.Error{Code: address.Code, Message: address.Message}
	}
	var account *ac.Error
	if errors.As(err, &account) {
		return &contract.Error{Code: account.Code, Message: account.Message}
	}
	var op *oc.Error
	if errors.As(err, &op) {
		return &contract.Error{Code: op.Code, Message: op.Message}
	}
	return contract.ErrUnavailable
}
