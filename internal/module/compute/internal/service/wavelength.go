package service

import (
	"context"
	"github.com/aws/aws-sdk-go-v2/aws"
	catalogmeta "github.com/herta-network/aws-panel/internal/catalogue"
	"github.com/herta-network/aws-panel/internal/infra/catalogcache"
	"github.com/herta-network/aws-panel/internal/infra/clock"
	"github.com/herta-network/aws-panel/internal/module/compute/contract"
	"github.com/herta-network/aws-panel/internal/module/compute/internal/source"
	"time"
)

func catalogue(ctx context.Context, cloud source.Cloud, cfg aws.Config, zone string) (contract.Catalogue, error) {
	if zone == "" {
		return cloud.Catalogue(ctx, cfg)
	}
	c, ok := cloud.(source.WavelengthCloud)
	if !ok {
		return contract.Catalogue{}, contract.ErrUnavailable
	}
	v, err := c.WavelengthCatalogue(ctx, cfg, zone)
	if err != nil {
		return v, err
	}
	parent, err := cloud.Catalogue(ctx, cfg)
	if err != nil {
		return v, err
	}
	v.ParentSubnets = parent.Subnets
	v.ParentTypes = parent.Types
	v.ParentZones = parent.Zones
	return v, nil
}
func wavelengthZones(ctx context.Context, cloud source.Cloud, cfg aws.Config) ([]contract.WavelengthZone, error) {
	c, ok := cloud.(source.WavelengthCloud)
	if !ok {
		return nil, contract.ErrUnavailable
	}
	return c.WavelengthZones(ctx, cfg)
}
func (s *Service) WavelengthZones(ctx context.Context, actor int64, id, region string, refresh ...bool) (contract.WavelengthListing, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cfg, info, err := s.config(ctx, actor, id, region)
	if err != nil {
		return contract.WavelengthListing{}, err
	}
	zs, fresh, err := catalogcache.Read(ctx, s.deps.Cache, catalogcache.Key("wavelength-zones", info.Partition, info.AccountID, id, info.Fingerprint, region), catalogcache.Environment, forceRefresh(refresh), func(ctx context.Context) ([]contract.WavelengthZone, error) {
		return wavelengthZones(ctx, s.deps.Cloud, cfg)
	})
	return contract.WavelengthListing{ConnectionID: id, Region: region, Zones: zs, CheckedAt: clock.In(fresh.UpdatedAt), Freshness: map[string]catalogmeta.Freshness{"regions": fresh}}, publicError(err)
}
func (s *Service) WavelengthCatalogue(ctx context.Context, actor int64, id, region, zone string, refresh ...bool) (contract.Catalogue, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cfg, info, err := s.config(ctx, actor, id, region)
	if err != nil {
		return contract.Catalogue{}, err
	}
	v, err := s.displayCatalogue(ctx, cfg, info, zone, forceRefresh(refresh))
	v.ConnectionID = id
	v.WritesEnabled = s.deps.WritesEnabled
	return v, publicError(err)
}
