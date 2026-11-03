package service

import (
	"context"
	catalogmeta "github.com/Ember-Moth/aws-panel/internal/catalogue"
	"github.com/Ember-Moth/aws-panel/internal/infra/catalogcache"
	ac "github.com/Ember-Moth/aws-panel/internal/module/accounts/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/compute/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/compute/internal/source"
	"github.com/aws/aws-sdk-go-v2/aws"
	"golang.org/x/sync/errgroup"
)

func forceRefresh(v []bool) bool { return len(v) > 0 && v[0] }
func (s *Service) displayCatalogue(ctx context.Context, cfg aws.Config, scope ac.Snapshot, zone string, force bool) (contract.Catalogue, error) {
	parts, ok := s.deps.Cloud.(source.CatalogueParts)
	if s.deps.Cache == nil || !ok {
		return catalogue(ctx, s.deps.Cloud, cfg, zone)
	}
	key := func(part, z string) string {
		return catalogcache.Key("compute", scope.Partition, scope.AccountID, scope.ConnectionID, scope.Fingerprint, cfg.Region, z, part)
	}
	var images, kinds, environment, parent contract.Catalogue
	var imf, tf, ef catalogmeta.Freshness
	group, ctx := errgroup.WithContext(ctx)
	group.Go(func() error {
		var err error
		images, imf, err = catalogcache.Read(ctx, s.deps.Cache, key("images", ""), catalogcache.Images, force, func(ctx context.Context) (contract.Catalogue, error) { return parts.Images(ctx, cfg) })
		return err
	})
	group.Go(func() error {
		var err error
		kinds, tf, err = catalogcache.Read(ctx, s.deps.Cache, key("types", zone), catalogcache.Products, force, func(ctx context.Context) (contract.Catalogue, error) { return parts.InstanceTypes(ctx, cfg, zone) })
		return err
	})
	group.Go(func() error {
		var err error
		environment, ef, err = catalogcache.Read(ctx, s.deps.Cache, key("environment", zone), catalogcache.Environment, force, func(ctx context.Context) (contract.Catalogue, error) { return parts.Environment(ctx, cfg, zone) })
		return err
	})
	// Wavelength needs regional ingress choices; image reads share the same cache key.
	if zone != "" {
		group.Go(func() error { var err error; parent, err = s.displayCatalogue(ctx, cfg, scope, "", force); return err })
	}
	if err := group.Wait(); err != nil {
		return contract.Catalogue{}, err
	}
	environment.Images = images.Images
	environment.Types = kinds.Types
	environment.CheckedAt = ef.UpdatedAt
	environment.Freshness = map[string]catalogmeta.Freshness{"images": imf, "types": tf, "environment": ef}
	if zone != "" {
		environment.ParentSubnets = parent.Subnets
		environment.ParentTypes = parent.Types
		environment.ParentZones = parent.Zones
		for k, v := range parent.Freshness {
			environment.Freshness["parent_"+k] = v
		}
	}
	return environment, nil
}
