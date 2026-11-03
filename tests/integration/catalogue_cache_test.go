package integration

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Ember-Moth/aws-panel/internal/auth"
	"github.com/Ember-Moth/aws-panel/internal/infra/catalogcache"
	ac "github.com/Ember-Moth/aws-panel/internal/module/accounts/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/compute"
	cp "github.com/Ember-Moth/aws-panel/internal/module/compute/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/lightsail"
	lc "github.com/Ember-Moth/aws-panel/internal/module/lightsail/contract"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

type cachedLaunchFixture struct {
	*launchAWS
	products, environment atomic.Int32
}

func (f *cachedLaunchFixture) Products(ctx context.Context, cfg aws.Config) (lc.Catalogue, error) {
	f.products.Add(1)
	cfg.BaseEndpoint = aws.String("http://aws-fixture.invalid")
	return lightsail.NewCloud().(lightsail.CatalogueParts).Products(ctx, cfg)
}
func (f *cachedLaunchFixture) Environment(ctx context.Context, cfg aws.Config) (lc.Catalogue, error) {
	f.environment.Add(1)
	cfg.BaseEndpoint = aws.String("http://aws-fixture.invalid")
	return lightsail.NewCloud().(lightsail.CatalogueParts).Environment(ctx, cfg)
}
func TestCatalogueCacheLightsailPreviewAndAuthorization(t *testing.T) {
	h := newLaunch(t, false)
	ctx := context.Background()
	cache := catalogcache.New(ctx, nil)
	defer cache.Close()
	cloud := &cachedLaunchFixture{launchAWS: h.cloudLaunch}
	service := lightsail.New(lightsail.Deps{Cache: cache, Accounts: h.accounts, Access: h.op.members, Jobs: h.op.service, Cloud: cloud})
	first, err := service.Catalogue(ctx, h.op.admin, h.connectionA.ID, "us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	h.cloudLaunch.mu.Lock()
	h.cloudLaunch.price = 9
	h.cloudLaunch.mu.Unlock()
	hit, err := service.Catalogue(ctx, h.op.admin, h.connectionA.ID, "us-east-1")
	if err != nil || !hit.Freshness["products"].Cached || !hit.Freshness["environment"].Cached || hit.Bundles[1].MonthlyCents != first.Bundles[1].MonthlyCents || cloud.products.Load() != 1 || cloud.environment.Load() != 1 {
		t.Fatal("catalogue was not served from cache", hit.Freshness, err)
	}
	preview, err := service.Prepare(ctx, h.op.admin, lc.CreateRequest{Targets: []lc.CreateTarget{h.target("cache-preview")}})
	if err != nil || preview.MonthlyCents != 900 {
		t.Fatal("creation preview used stale price", preview.MonthlyCents, err)
	}
	forced, err := service.Catalogue(ctx, h.op.admin, h.connectionA.ID, "us-east-1", true)
	if err != nil || forced.Bundles[1].MonthlyCents != 900 || forced.Freshness["products"].Cached || cloud.products.Load() != 2 || cloud.environment.Load() != 2 {
		t.Fatal("manual refresh did not bypass cache", err)
	}
	if _, err = service.Catalogue(ctx, h.op.admin, h.connectionB.ID, "us-east-1"); err != nil {
		t.Fatal(err)
	}
	if cloud.products.Load() != 3 {
		t.Fatal("accounts shared directory cache")
	}
	active := false
	_, err = h.accounts.SaveConnection(ctx, h.op.admin, h.accountA.ID, h.connectionA.ID, ac.ConnectionRequest{Name: h.connectionA.Name, AuthType: h.connectionA.AuthType, Region: h.connectionA.Region, RouteMode: h.connectionA.RouteMode, ProxyID: h.connectionA.ProxyID, Active: &active, Version: h.connectionA.Version}, auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Catalogue(ctx, h.op.admin, h.connectionA.ID, "us-east-1"); err == nil {
		t.Fatal("disabled connection read cached data")
	}
	if cloud.products.Load() != 3 {
		t.Fatal("disabled connection started refresh")
	}
}

type cachedComputeFixture struct {
	*computeAWS
	images, kinds, environment atomic.Int32
}

func (f *cachedComputeFixture) Images(ctx context.Context, cfg aws.Config) (cp.Catalogue, error) {
	f.images.Add(1)
	return compute.NewCloud().(compute.CatalogueParts).Images(ctx, addressConfig(cfg))
}
func (f *cachedComputeFixture) InstanceTypes(ctx context.Context, cfg aws.Config, zone string) (cp.Catalogue, error) {
	f.kinds.Add(1)
	return compute.NewCloud().(compute.CatalogueParts).InstanceTypes(ctx, addressConfig(cfg), zone)
}
func (f *cachedComputeFixture) Environment(ctx context.Context, cfg aws.Config, zone string) (cp.Catalogue, error) {
	f.environment.Add(1)
	return compute.NewCloud().(compute.CatalogueParts).Environment(ctx, addressConfig(cfg), zone)
}
func TestCatalogueCacheComputeUsesLiveNetworkForPreview(t *testing.T) {
	h := newCompute(t, false)
	ctx := context.Background()
	cache := catalogcache.New(ctx, nil)
	defer cache.Close()
	cloud := &cachedComputeFixture{computeAWS: h.cloudCompute}
	service := compute.New(compute.Deps{Cache: cache, Accounts: h.accounts, Access: h.op.members, Jobs: h.op.service, Cloud: cloud})
	first, err := service.Catalogue(ctx, h.op.admin, h.connectionA.ID, "us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Images) == 0 || len(first.Types) == 0 || len(first.Subnets) == 0 {
		t.Fatal("split catalogue lost a section")
	}
	h.cloudCompute.mu.Lock()
	h.cloudCompute.noIPv6 = true
	h.cloudCompute.mu.Unlock()
	hit, err := service.Catalogue(ctx, h.op.admin, h.connectionA.ID, "us-east-1")
	if err != nil || !hit.Freshness["images"].Cached || hit.Subnets[0].IPv6CIDR == "" || cloud.images.Load() != 1 || cloud.kinds.Load() != 1 || cloud.environment.Load() != 1 {
		t.Fatal("cache miss", err)
	}
	_, err = service.Prepare(ctx, h.op.admin, cp.Request{Targets: []cp.InputTarget{h.target()}})
	if !errors.Is(err, cp.ErrNetwork) {
		t.Fatal("preview trusted cached network state", err)
	}
	forced, err := service.Catalogue(ctx, h.op.admin, h.connectionA.ID, "us-east-1", true)
	if err != nil || forced.Subnets[0].IPv6CIDR != "" || cloud.images.Load() != 2 || cloud.kinds.Load() != 2 || cloud.environment.Load() != 2 {
		t.Fatal("manual refresh returned old network", err)
	}
	// A changed connection fingerprint cannot reuse a previously authorized result.
	active := true
	updated, err := h.accounts.SaveConnection(ctx, h.op.admin, h.accountA.ID, h.connectionA.ID, ac.ConnectionRequest{Name: "cache version two", Active: &active, AuthType: h.connectionA.AuthType, Region: h.connectionA.Region, RouteMode: h.connectionA.RouteMode, ProxyID: h.connectionA.ProxyID, Version: h.connectionA.Version}, auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.accounts.Verify(ctx, h.op.admin, updated.ID, auth.RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Catalogue(ctx, h.op.admin, updated.ID, "us-east-1"); err != nil {
		t.Fatal(err)
	}
	if cloud.images.Load() != 3 {
		t.Fatal("connection change reused old cache")
	}
}
func TestCatalogueCacheRedisSurvivesServiceRestart(t *testing.T) {
	address := os.Getenv("AWS_PANEL_TEST_REDIS_ADDRESS")
	if address == "" {
		t.Skip("isolated Redis not configured")
	}
	ctx := context.Background()
	client := redis.NewClient(&redis.Options{Addr: address})
	defer client.Close()
	key := catalogcache.Key("integration", uuid.NewString())
	defer client.Del(ctx, key)
	first := catalogcache.New(ctx, client)
	value, meta, err := catalogcache.Read(ctx, first, key, catalogcache.Products, false, func(context.Context) ([]string, error) { return []string{"debian-13"}, nil })
	if err != nil || len(value) != 1 || meta.Cached {
		t.Fatal(value, meta, err)
	}
	if ttl := client.PTTL(ctx, key).Val(); ttl < 6*24*time.Hour {
		t.Fatal("persistent cache TTL missing", ttl)
	}
	first.Close()
	second := catalogcache.New(ctx, client)
	defer second.Close()
	value, meta, err = catalogcache.Read(ctx, second, key, catalogcache.Products, false, func(context.Context) ([]string, error) {
		t.Error("restart queried AWS despite Redis snapshot")
		return nil, errors.New("offline")
	})
	if err != nil || !meta.Cached || value[0] != "debian-13" {
		t.Fatal(value, meta, err)
	}
}
