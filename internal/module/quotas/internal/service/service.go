package service

import (
	"context"
	"errors"
	"github.com/Ember-Moth/aws-panel/internal/catalogue"
	"github.com/Ember-Moth/aws-panel/internal/infra/catalogcache"
	"regexp"
	"strings"
	"time"

	"github.com/Ember-Moth/aws-panel/internal/auth"
	"github.com/Ember-Moth/aws-panel/internal/infra/clock"
	ac "github.com/Ember-Moth/aws-panel/internal/module/accounts/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/quotas/contract"
	"github.com/aws/aws-sdk-go-v2/aws"
)

type Access interface {
	Principal(context.Context, int64) (auth.Principal, error)
}
type Accounts interface {
	ListAccounts(context.Context, int64) ([]ac.Account, error)
	AccountDetail(context.Context, int64, string) (*ac.AccountDetail, error)
	Inspect(context.Context, int64, string, string, bool) (ac.Snapshot, error)
	ConfigFor(context.Context, int64, string, string, bool) (aws.Config, error)
}
type Cloud interface {
	Regions(context.Context, aws.Config) ([]contract.Region, error)
	Read(context.Context, aws.Config, string, string) (contract.Report, error)
}
type Deps struct {
	Cache    *catalogcache.Cache
	Accounts Accounts
	Access   Access
	Cloud    Cloud
}
type Service struct{ deps Deps }

func New(deps Deps) *Service { return &Service{deps: deps} }

var regionPattern = regexp.MustCompile(`^[a-z]{2}(?:-[a-z0-9]+)+-[0-9]+$`)

func (s *Service) Read(ctx context.Context, actorID int64, query contract.Query) (contract.Report, error) {
	if _, err := s.deps.Access.Principal(ctx, actorID); err != nil {
		return contract.Report{}, contract.ErrForbidden
	}
	if !regionPattern.MatchString(query.Region) || strings.HasPrefix(query.Region, "cn-") || strings.HasPrefix(query.Region, "us-gov-") || (query.Service != "lightsail" && query.Service != "ec2" && query.Service != "vpc" && query.Service != "bedrock") {
		return contract.Report{}, contract.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	snapshot, err := s.deps.Accounts.Inspect(ctx, actorID, query.ConnectionID, query.Region, false)
	if err != nil {
		return contract.Report{}, translate(err)
	}
	if snapshot.Partition != "aws" {
		return contract.Report{}, contract.ErrInvalid
	}
	cfg, err := s.deps.Accounts.ConfigFor(ctx, actorID, query.ConnectionID, query.Region, false)
	if err != nil {
		return contract.Report{}, translate(err)
	}
	report, err := s.readCached(ctx, cfg, snapshot, query.Service, query.Refresh)
	if err != nil {
		return contract.Report{}, translate(err)
	}
	report.ConnectionID = query.ConnectionID
	report.AccountID = snapshot.AccountID
	report.AWSAccountID = snapshot.AWSAccountID
	return report, nil
}
func translate(err error) error {
	var own *contract.Error
	if errors.As(err, &own) {
		return own
	}
	var account *ac.Error
	if errors.As(err, &account) {
		return &contract.Error{Code: account.Code, Message: account.Message}
	}
	return contract.ErrUnavailable
}

func (s *Service) Options(ctx context.Context, actorID int64) (contract.Options, error) {
	if _, err := s.deps.Access.Principal(ctx, actorID); err != nil {
		return contract.Options{}, contract.ErrForbidden
	}
	rows, err := s.deps.Accounts.ListAccounts(ctx, actorID)
	if err != nil {
		return contract.Options{}, translate(err)
	}
	result := contract.Options{Accounts: []contract.Account{}}
	for _, row := range rows {
		if row.Partition != "aws" {
			continue
		}
		detail, err := s.deps.Accounts.AccountDetail(ctx, actorID, row.ID)
		if err != nil {
			return result, translate(err)
		}
		account := contract.Account{ID: row.ID, Name: row.Name, AWSAccountID: row.AWSAccountID, Connections: []contract.Connection{}}
		for _, connection := range detail.Connections {
			account.Connections = append(account.Connections, contract.Connection{ID: connection.ID, Name: connection.Name, Status: connection.Status})
		}
		result.Accounts = append(result.Accounts, account)
	}
	return result, nil
}
func (s *Service) Regions(ctx context.Context, actorID int64, id string, refresh ...bool) (contract.Regions, error) {
	if _, err := s.deps.Access.Principal(ctx, actorID); err != nil {
		return contract.Regions{}, contract.ErrForbidden
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	info, err := s.deps.Accounts.Inspect(ctx, actorID, id, "us-east-1", false)
	if err != nil {
		return contract.Regions{}, translate(err)
	}
	if info.Partition != "aws" {
		return contract.Regions{}, contract.ErrInvalid
	}
	cfg, err := s.deps.Accounts.ConfigFor(ctx, actorID, id, "us-east-1", false)
	if err != nil {
		return contract.Regions{}, translate(err)
	}
	rows, fresh, err := catalogcache.Read(ctx, s.deps.Cache, catalogcache.Key("account-regions", info.Partition, info.AccountID, id, info.Fingerprint), catalogcache.Regions, len(refresh) > 0 && refresh[0], func(ctx context.Context) ([]catalogue.RegionState, error) {
		rows, err := s.deps.Cloud.Regions(ctx, cfg)
		if err != nil {
			return nil, err
		}
		out := make([]catalogue.RegionState, 0, len(rows))
		for _, v := range rows {
			out = append(out, catalogue.RegionState{Code: v.Code, State: v.State})
		}
		return out, nil
	})
	if err != nil {
		return contract.Regions{}, translate(err)
	}
	items := make([]contract.Region, 0, len(rows))
	for _, v := range rows {
		items = append(items, contract.Region{Code: v.Code, State: v.State, Available: v.State == "ENABLED" || v.State == "ENABLED_BY_DEFAULT"})
	}
	return contract.Regions{Items: items, CheckedAt: clock.In(fresh.UpdatedAt), Freshness: map[string]catalogue.Freshness{"regions": fresh}}, nil
}
