package service

import (
	"context"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/herta-network/aws-panel/internal/catalogue"
	"github.com/herta-network/aws-panel/internal/infra/catalogcache"
	ac "github.com/herta-network/aws-panel/internal/module/accounts/contract"
	"github.com/herta-network/aws-panel/internal/module/lightsail/contract"
	"github.com/herta-network/aws-panel/internal/module/lightsail/internal/source"
	"golang.org/x/sync/errgroup"
)

func forceRefresh(v []bool) bool { return len(v) > 0 && v[0] }
func (s *Service) displayCatalogue(ctx context.Context, cfg aws.Config, scope ac.Snapshot, force bool) (contract.Catalogue, error) {
	parts, ok := s.deps.Cloud.(source.CatalogueParts)
	if s.deps.Cache == nil || !ok {
		return s.deps.Cloud.Catalogue(ctx, cfg)
	}
	key := func(part string) string {
		return catalogcache.Key("lightsail", scope.Partition, scope.AccountID, scope.ConnectionID, scope.Fingerprint, cfg.Region, part)
	}
	var products, environment contract.Catalogue
	var pf, ef catalogue.Freshness
	group, ctx := errgroup.WithContext(ctx)
	group.Go(func() error {
		var err error
		products, pf, err = catalogcache.Read(ctx, s.deps.Cache, key("products"), catalogcache.Products, force, func(ctx context.Context) (contract.Catalogue, error) { return parts.Products(ctx, cfg) })
		return err
	})
	group.Go(func() error {
		var err error
		environment, ef, err = catalogcache.Read(ctx, s.deps.Cache, key("environment"), catalogcache.Environment, force, func(ctx context.Context) (contract.Catalogue, error) { return parts.Environment(ctx, cfg) })
		return err
	})
	if err := group.Wait(); err != nil {
		return contract.Catalogue{}, err
	}
	environment.Blueprints = products.Blueprints
	environment.Bundles = products.Bundles
	environment.CheckedAt = ef.UpdatedAt
	environment.Freshness = map[string]catalogue.Freshness{"products": pf, "environment": ef}
	return environment, nil
}
