package service

import (
	"context"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/herta-network/aws-panel/internal/catalogue"
	"github.com/herta-network/aws-panel/internal/infra/catalogcache"
	ac "github.com/herta-network/aws-panel/internal/module/accounts/contract"
	"github.com/herta-network/aws-panel/internal/module/quotas/contract"
	reportdata "github.com/herta-network/aws-panel/internal/module/quotas/internal/report"
	"golang.org/x/sync/errgroup"
)

type Parts interface {
	Limits(context.Context, aws.Config, string) (contract.Report, error)
	Usage(context.Context, aws.Config, string, string) (contract.Report, error)
}

func (s *Service) readCached(ctx context.Context, cfg aws.Config, scope ac.Snapshot, service string, force bool) (contract.Report, error) {
	parts, ok := s.deps.Cloud.(Parts)
	if s.deps.Cache == nil || !ok {
		return s.deps.Cloud.Read(ctx, cfg, scope.AWSAccountID, service)
	}
	key := func(part string) string {
		return catalogcache.Key("quota-report", scope.Partition, scope.AccountID, scope.ConnectionID, scope.Fingerprint, cfg.Region, service, part)
	}
	var limits, usage contract.Report
	var lf, uf catalogue.Freshness
	group, ctx := errgroup.WithContext(ctx)
	group.Go(func() error {
		var err error
		limits, lf, err = catalogcache.ReadChecked(ctx, s.deps.Cache, key("limits"), catalogcache.QuotaLimits, force, func(ctx context.Context) (contract.Report, error) { return parts.Limits(ctx, cfg, service) }, func(v contract.Report) bool { return v.LimitsRead })
		return err
	})
	if service != "bedrock" {
		group.Go(func() error {
			var err error
			usage, uf, err = catalogcache.ReadChecked(ctx, s.deps.Cache, key("usage"), catalogcache.QuotaUsage, force, func(ctx context.Context) (contract.Report, error) {
				return parts.Usage(ctx, cfg, scope.AWSAccountID, service)
			}, func(v contract.Report) bool {
				if len(v.Entries) == 0 {
					return false
				}
				for _, e := range v.Entries {
					if !e.UsageKnown {
						return false
					}
				}
				return true
			})
			return err
		})
	}
	if err := group.Wait(); err != nil {
		return contract.Report{}, err
	}
	limits.CheckedAt = lf.UpdatedAt
	limits.Freshness = map[string]catalogue.Freshness{"limits": lf}
	if service == "bedrock" {
		return limits, nil
	}
	usage.CheckedAt = uf.UpdatedAt
	usage.Freshness = map[string]catalogue.Freshness{"usage": uf}
	return reportdata.Merge(limits, usage), nil
}
