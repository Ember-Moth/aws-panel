package integration

import (
	"context"
	"encoding/json"
	"github.com/redis/go-redis/v9"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/herta-network/aws-panel/internal/infra/catalogcache"
	"github.com/herta-network/aws-panel/internal/module/addresses"
	ad "github.com/herta-network/aws-panel/internal/module/addresses/contract"
	"github.com/herta-network/aws-panel/internal/module/compute"
	cp "github.com/herta-network/aws-panel/internal/module/compute/contract"
	"github.com/herta-network/aws-panel/internal/module/quotas"
	qc "github.com/herta-network/aws-panel/internal/module/quotas/contract"
	"github.com/herta-network/aws-panel/internal/module/regions"
	rc "github.com/herta-network/aws-panel/internal/module/regions/contract"
)

type pageQuotaCloud struct {
	limits, usage atomic.Int32
	incomplete    atomic.Bool
}

func (f *pageQuotaCloud) Regions(ctx context.Context, cfg aws.Config) ([]qc.Region, error) {
	return quotas.NewCloud().Regions(ctx, addressConfig(cfg))
}
func (f *pageQuotaCloud) Read(ctx context.Context, cfg aws.Config, a, s string) (qc.Report, error) {
	return quotas.NewCloud().Read(ctx, addressConfig(cfg), a, s)
}
func (f *pageQuotaCloud) Limits(ctx context.Context, cfg aws.Config, s string) (qc.Report, error) {
	f.limits.Add(1)
	v, err := quotas.NewCloud().(quotas.Parts).Limits(ctx, addressConfig(cfg), s)
	if f.incomplete.Load() {
		v.LimitsRead = false
		for i := range v.Entries {
			v.Entries[i].LimitKnown = false
		}
	}
	return v, err
}
func (f *pageQuotaCloud) Usage(ctx context.Context, cfg aws.Config, a, s string) (qc.Report, error) {
	f.usage.Add(1)
	return quotas.NewCloud().(quotas.Parts).Usage(ctx, addressConfig(cfg), a, s)
}
func quotaEntry(t *testing.T, v qc.Report, key string) qc.Entry {
	t.Helper()
	for _, e := range v.Entries {
		if e.Key == key {
			return e
		}
	}
	t.Fatal("missing quota", key)
	return qc.Entry{}
}
func TestPageCacheQuotaLimitsUsageAndIncompleteRefresh(t *testing.T) {
	h := newCompute(t, false)
	ctx := context.Background()
	cache := catalogcache.New(ctx, nil)
	defer cache.Close()
	cloud := &pageQuotaCloud{}
	service := quotas.New(quotas.Deps{Cache: cache, Accounts: h.accounts, Access: h.op.members, Cloud: cloud})
	query := qc.Query{ConnectionID: h.connectionA.ID, Region: "us-east-1", Service: "ec2"}
	first, err := service.Read(ctx, h.op.admin, query)
	if err != nil {
		t.Fatal(err)
	}
	if !first.LimitsRead || !quotaEntry(t, first, "ec2.ondemand.standard").UsageKnown {
		t.Fatal(first)
	}
	if first.Freshness["limits"].RefreshAfter.Sub(first.Freshness["limits"].UpdatedAt) != time.Hour || first.Freshness["usage"].RefreshAfter.Sub(first.Freshness["usage"].UpdatedAt) != time.Minute {
		t.Fatal("quota TTLs combined")
	}
	h.cloudCompute.mu.Lock()
	h.cloudCompute.quota = 250
	h.cloudCompute.mu.Unlock()
	hit, err := service.Read(ctx, h.op.admin, query)
	if err != nil || !hit.Freshness["limits"].Cached || !hit.Freshness["usage"].Cached || cloud.limits.Load() != 1 || cloud.usage.Load() != 1 {
		t.Fatal("quota miss", err)
	}
	if quotaEntry(t, hit, "ec2.ondemand.standard").Limit != 100 {
		t.Fatal("cache changed unexpectedly")
	}
	query.Refresh = true
	fresh, err := service.Read(ctx, h.op.admin, query)
	if err != nil || quotaEntry(t, fresh, "ec2.ondemand.standard").Limit != 250 || cloud.limits.Load() != 2 || cloud.usage.Load() != 2 {
		t.Fatal("force refresh did not read limits", err)
	}
	cloud.incomplete.Store(true)
	partial, err := service.Read(ctx, h.op.admin, query)
	if err != nil || partial.LimitsRead || !partial.Freshness["limits"].RefreshFailed {
		t.Fatal("incomplete limits disguised", err)
	}
	query.Refresh = false
	retained, err := service.Read(ctx, h.op.admin, query)
	if err != nil || !retained.LimitsRead || !retained.Freshness["limits"].Cached || quotaEntry(t, retained, "ec2.ondemand.standard").Limit != 250 {
		t.Fatal("partial page replaced successful cache", err)
	}
	cfg, err := h.accounts.ConfigFor(ctx, h.op.admin, h.connectionA.ID, "us-east-1", false)
	if err != nil {
		t.Fatal(err)
	}
	h.cloudCompute.mu.Lock()
	h.cloudCompute.quota = 300
	h.cloudCompute.mu.Unlock()
	direct, err := quotas.NewCloud().Read(ctx, addressConfig(cfg), h.accountA.AWSAccountID, "ec2")
	if err != nil || quotaEntry(t, direct, "ec2.ondemand.standard").Limit != 300 {
		t.Fatal("operation source used display cache", err)
	}
}

type pageRegionCloud struct {
	*inventoryAWS
	lists     atomic.Int32
	nextState atomic.Value
}

func (f *pageRegionCloud) List(ctx context.Context, cfg aws.Config) ([]regions.State, error) {
	f.lists.Add(1)
	rows, err := f.inventoryAWS.List(ctx, cfg)
	if state := f.nextState.Load(); state != nil {
		for i := range rows {
			if rows[i].Region == "ap-east-1" {
				rows[i].Status = state.(string)
			}
		}
	}
	return rows, err
}
func TestPageCacheRegionPreviewUsesCurrentState(t *testing.T) {
	h := newInventory(t)
	ctx := context.Background()
	cache := catalogcache.New(ctx, nil)
	defer cache.Close()
	jobs, _ := regionServices(t, h, false)
	cloud := &pageRegionCloud{inventoryAWS: h.cloud}
	service := regions.New(regions.Deps{Cache: cache, Accounts: h.accounts, Access: h.op.members, Jobs: jobs, Cloud: cloud})
	first, err := service.Catalogue(ctx, h.op.admin, h.connectionA.ID)
	if err != nil {
		t.Fatal(err)
	}
	h.cloud.mu.Lock()
	h.cloud.optStates[h.accountA.AWSAccountID+"/ap-east-1"] = "ENABLING"
	h.cloud.mu.Unlock()
	hit, err := service.Catalogue(ctx, h.op.admin, h.connectionA.ID)
	if err != nil || !hit.Freshness["regions"].Cached || !hit.CheckedAt.Equal(first.CheckedAt) || cloud.lists.Load() != 1 {
		t.Fatal("region miss", err)
	}
	preview, err := service.Prepare(ctx, h.op.admin, rc.EnableRequest{Targets: []rc.Target{{ConnectionID: h.connectionA.ID, Region: "ap-east-1"}}})
	if err != nil || preview.Targets[0].State != "ENABLING" {
		t.Fatal("region preview trusted cached state", err)
	}
	fresh, err := service.Catalogue(ctx, h.op.admin, h.connectionA.ID, true)
	if err != nil || fresh.Freshness["regions"].Cached || cloud.lists.Load() != 2 {
		t.Fatal(err)
	}
}

type pageInstanceCloud struct {
	*computeAWS
	lists atomic.Int32
}

func (f *pageInstanceCloud) Instances(ctx context.Context, cfg aws.Config, a string) ([]cp.Instance, error) {
	f.lists.Add(1)
	return f.computeAWS.Instances(ctx, cfg, a)
}
func TestPageCacheInstancesShortLivedAndRefreshable(t *testing.T) {
	h := newCompute(t, false)
	ctx := context.Background()
	cache := catalogcache.New(ctx, nil)
	defer cache.Close()
	cloud := &pageInstanceCloud{computeAWS: h.cloudCompute}
	service := compute.New(compute.Deps{Cache: cache, Accounts: h.accounts, Access: h.op.members, Cloud: cloud})
	first, err := service.Instances(ctx, h.op.admin, h.connectionA.ID, "us-east-1")
	if err != nil || len(first.Items) != 0 {
		t.Fatal(first, err)
	}
	scope := h.accountA.AWSAccountID + "/us-east-1"
	h.cloudCompute.mu.Lock()
	h.cloudCompute.instances[scope+"/i-00000001"] = &computeInstance{id: "i-00000001", scope: scope, state: "running", kind: "t3.micro", image: "ami-11111111", subnet: "subnet-11111111", stamp: time.Now().UTC().Format(time.RFC3339), labels: map[string]string{}}
	h.cloudCompute.mu.Unlock()
	hit, err := service.Instances(ctx, h.op.admin, h.connectionA.ID, "us-east-1")
	if err != nil || len(hit.Items) != 0 || cloud.lists.Load() != 1 || !hit.Freshness["resources"].Cached {
		t.Fatal(hit, err)
	}
	fresh, err := service.Instances(ctx, h.op.admin, h.connectionA.ID, "us-east-1", true)
	if err != nil || len(fresh.Items) != 1 || cloud.lists.Load() != 2 || fresh.Freshness["resources"].Cached {
		t.Fatal(fresh, err)
	}
}

type pageAddressCloud struct {
	*addressAWS
	lists atomic.Int32
}

func (f *pageAddressCloud) Catalogue(ctx context.Context, cfg aws.Config, s, a string) (ad.Catalogue, error) {
	f.lists.Add(1)
	return f.addressAWS.Catalogue(ctx, cfg, s, a)
}
func TestPageCacheAddressPreviewReadsCurrentAssociation(t *testing.T) {
	h := newAddresses(t, false)
	ctx := context.Background()
	cache := catalogcache.New(ctx, nil)
	defer cache.Close()
	cloud := &pageAddressCloud{addressAWS: h.cloudAddresses}
	service := addresses.New(addresses.Deps{Cache: cache, Accounts: h.accounts, Access: h.op.members, Jobs: h.op.service, Cloud: cloud})
	first, err := service.Catalogue(ctx, h.op.admin, h.connectionA.ID, "us-east-1", "lightsail")
	if err != nil {
		t.Fatal(err)
	}
	scope := h.accountA.AWSAccountID + "/us-east-1/"
	h.cloudAddresses.mu.Lock()
	h.cloudAddresses.ips[scope+"old-ip"]["ipAddress"] = "127.0.1.20"
	h.cloudAddresses.instances[scope+"instance-a"]["publicIpAddress"] = "127.0.1.20"
	h.cloudAddresses.mu.Unlock()
	hit, err := service.Catalogue(ctx, h.op.admin, h.connectionA.ID, "us-east-1", "lightsail")
	if err != nil || !hit.Freshness["resources"].Cached || !hit.CheckedAt.Equal(first.CheckedAt) || cloud.lists.Load() != 1 {
		t.Fatal(err)
	}
	target := h.target("replace")
	target.InstanceID = "instance-a"
	target.ReleasePrevious = true
	preview, err := service.Prepare(ctx, h.op.admin, ad.Request{Targets: []ad.Target{target}})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Targets[0].Spec.Previous == nil || preview.Targets[0].Spec.Previous.IP != "127.0.1.20" {
		t.Fatal("address preview used cached association")
	}
	fresh, err := service.Catalogue(ctx, h.op.admin, h.connectionA.ID, "us-east-1", "lightsail", true)
	if err != nil || fresh.Freshness["resources"].Cached || cloud.lists.Load() != 2 {
		t.Fatal(err)
	}
}

func TestPageCacheQuotaUsageExpiresIndependently(t *testing.T) {
	address := os.Getenv("AWS_PANEL_TEST_REDIS_ADDRESS")
	if address == "" {
		t.Skip("isolated Redis not configured")
	}
	h := newCompute(t, false)
	ctx := context.Background()
	client := redis.NewClient(&redis.Options{Addr: address})
	defer client.Close()
	cloud := &pageQuotaCloud{}
	cache := catalogcache.New(ctx, client)
	service := quotas.New(quotas.Deps{Cache: cache, Accounts: h.accounts, Access: h.op.members, Cloud: cloud})
	query := qc.Query{ConnectionID: h.connectionA.ID, Region: "us-east-1", Service: "ec2"}
	if _, err := service.Read(ctx, h.op.admin, query); err != nil {
		cache.Close()
		t.Fatal(err)
	}
	cache.Close()
	scope, err := h.accounts.Inspect(ctx, h.op.admin, h.connectionA.ID, "us-east-1", false)
	if err != nil {
		t.Fatal(err)
	}
	key := catalogcache.Key("quota-report", scope.Partition, scope.AccountID, scope.ConnectionID, scope.Fingerprint, "us-east-1", "ec2", "usage")
	raw, err := client.Get(ctx, key).Bytes()
	if err != nil {
		t.Fatal(err)
	}
	var entry map[string]json.RawMessage
	if json.Unmarshal(raw, &entry) != nil {
		t.Fatal("invalid cache fixture")
	}
	entry["updated"], _ = json.Marshal(time.Now().Add(-2 * time.Minute))
	entry["fresh_until"], _ = json.Marshal(time.Now().Add(-time.Second))
	entry["expires"], _ = json.Marshal(time.Now().Add(3 * time.Minute))
	raw, _ = json.Marshal(entry)
	if err = client.Set(ctx, key, raw, 3*time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	resumed := catalogcache.New(ctx, client)
	defer resumed.Close()
	service = quotas.New(quotas.Deps{Cache: resumed, Accounts: h.accounts, Access: h.op.members, Cloud: cloud})
	value, err := service.Read(ctx, h.op.admin, query)
	if err != nil || !value.Freshness["usage"].Stale || value.Freshness["limits"].Stale {
		t.Fatal("section expiry was combined", value.Freshness, err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		value, err = service.Read(ctx, h.op.admin, query)
		if err != nil {
			t.Fatal(err)
		}
		if !value.Freshness["usage"].Stale {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if value.Freshness["usage"].Stale || cloud.usage.Load() != 2 || cloud.limits.Load() != 1 {
		t.Fatal("usage refresh reread slow quota limits", cloud.limits.Load(), cloud.usage.Load(), value.Freshness)
	}
}

func TestPageCacheSharesRegionChoicesAcrossPages(t *testing.T) {
	h := newInventory(t)
	ctx := context.Background()
	cache := catalogcache.New(ctx, nil)
	defer cache.Close()
	picker := quotas.New(quotas.Deps{Cache: cache, Accounts: h.accounts, Access: h.op.members, Cloud: &pageQuotaCloud{}})
	first, err := picker.Regions(ctx, h.op.admin, h.connectionA.ID)
	if err != nil {
		t.Fatal(err)
	}
	cloud := &pageRegionCloud{inventoryAWS: h.cloud}
	management := regions.New(regions.Deps{Cache: cache, Accounts: h.accounts, Access: h.op.members, Cloud: cloud})
	shared, err := management.Catalogue(ctx, h.op.admin, h.connectionA.ID)
	if err != nil || !shared.Freshness["regions"].Cached || cloud.lists.Load() != 0 || !shared.CheckedAt.Equal(first.CheckedAt) {
		t.Fatal("cross-page region cache missed", err)
	}
	h.cloud.mu.Lock()
	h.cloud.optStates[h.accountA.AWSAccountID+"/ap-east-1"] = "ENABLING"
	h.cloud.mu.Unlock()
	cloud.nextState.Store("ENABLING")
	if _, err = management.Catalogue(ctx, h.op.admin, h.connectionA.ID, true); err != nil {
		t.Fatal(err)
	}
	updated, err := picker.Regions(ctx, h.op.admin, h.connectionA.ID)
	if err != nil || !updated.Freshness["regions"].Cached {
		t.Fatal(err)
	}
	found := false
	for _, v := range updated.Items {
		if v.Code == "ap-east-1" {
			found = true
			if v.State != "ENABLING" || v.Available {
				t.Fatal("picker missed forced management update", v)
			}
		}
	}
	if !found {
		t.Fatal("region missing")
	}
}
