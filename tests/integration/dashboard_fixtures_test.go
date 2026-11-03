package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Ember-Moth/aws-panel/internal/module/dashboard"
	"github.com/Ember-Moth/aws-panel/internal/module/dashboard/entity"
	"github.com/Ember-Moth/aws-panel/internal/module/inventory"
	"github.com/Ember-Moth/aws-panel/internal/module/operations"
	oc "github.com/Ember-Moth/aws-panel/internal/module/operations/contract"
	"github.com/Ember-Moth/aws-panel/internal/repository"
	"github.com/aws/aws-sdk-go-v2/aws"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type dashboardAWS struct {
	mu           sync.Mutex
	compute      *computeAWS
	addresses    *addressAWS
	partial      bool
	catalogCalls map[string]int
	readCalls    map[string]int
}

func (f *dashboardAWS) Catalogue(c context.Context, cfg aws.Config) (dashboard.Catalogue, error) {
	return dashboard.NewCloud().Catalogue(c, addressConfig(cfg))
}
func (f *dashboardAWS) ReadRegion(c context.Context, cfg aws.Config, account string, ls bool) ([]dashboard.Observation, error) {
	return dashboard.NewCloud().ReadRegion(c, addressConfig(cfg), account, ls)
}
func (f *dashboardAWS) handle(w http.ResponseWriter, r *http.Request, account, region, service string, input map[string]any) bool {
	f.mu.Lock()
	partial := f.partial && account == "111111111111" && region == "us-east-1"
	if service == "ec2" {
		f.readCalls[account+"/"+region+"/"+fmt.Sprint(input["Action"])]++
	}
	f.mu.Unlock()
	if service == "account" {
		f.mu.Lock()
		f.catalogCalls[account]++
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"Regions": []any{map[string]any{"RegionName": "us-east-1", "RegionOptStatus": "ENABLED_BY_DEFAULT"}, map[string]any{"RegionName": "us-west-2", "RegionOptStatus": "ENABLED_BY_DEFAULT"}, map[string]any{"RegionName": "ap-east-1", "RegionOptStatus": "DISABLED"}}})
		return true
	}
	if service == "lightsail" {
		_, action, _ := strings.Cut(r.Header.Get("X-Amz-Target"), ".")
		if action == "GetRegions" {
			_ = json.NewEncoder(w).Encode(map[string]any{"regions": []any{map[string]any{"name": "us-east-1"}, map[string]any{"name": "us-west-2"}}})
			return true
		}
		return f.addresses.handle(w, r, account, region, service, input)
	}
	if service == "servicequotas" {
		value := 100.0
		if region == "us-west-2" {
			value += 100
		}
		if account == "222222222222" {
			value += 200
		}
		entries := []any{}
		switch input["ServiceCode"] {
		case "ec2":
			entries = []any{map[string]any{"QuotaCode": "L-1216C47A", "QuotaName": "Running On-Demand Standard (A, C, D, H, I, M, R, T, Z) instances", "Value": value, "Unit": "vCPU"}, map[string]any{"QuotaCode": "L-0263D0A3", "QuotaName": "EC2-VPC Elastic IPs", "Value": 5}}
		case "lightsail":
			entries = []any{map[string]any{"QuotaCode": "L-LIGHTSVC", "QuotaName": "Instances", "Description": "vCPUs per Region", "Value": 20}, map[string]any{"QuotaCode": "L-LIGHTSIP", "QuotaName": "Static IPs", "Value": 5}}
		case "bedrock":
			if input["QuotaAppliedAtLevel"] != "ACCOUNT" {
				f.compute.t.Error("Bedrock did not restrict applied account quotas")
			}
			if input["NextToken"] == nil {
				_ = json.NewEncoder(w).Encode(map[string]any{"Quotas": []any{map[string]any{"QuotaCode": "L-BEDTPM", "QuotaName": "On-demand InvokeModel tokens per minute for Model A", "Value": value, "Unit": "None"}}, "NextToken": "page-2"})
				return true
			}
			if partial {
				w.WriteHeader(403)
				_ = json.NewEncoder(w).Encode(map[string]any{"__type": "AccessDeniedException", "message": "fixture forbidden"})
				return true
			}
			entries = []any{map[string]any{"QuotaCode": "L-BEDRPM", "QuotaName": "InvokeModel requests per minute for Model A", "Value": value / 10, "Unit": "None"}, map[string]any{"QuotaCode": "L-BEDGLOBAL", "QuotaName": "Global fixture jobs", "Value": 500, "Unit": "Count", "GlobalQuota": true}}
		default:
			f.compute.t.Error("unknown quota service", input["ServiceCode"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Quotas": entries})
		return true
	}
	if service == "ec2" && partial && input["Action"] == "DescribeInstances" {
		w.Header().Set("Content-Type", "text/xml")
		w.WriteHeader(400)
		fmt.Fprint(w, `<Response><Errors><Error><Code>UnauthorizedOperation</Code><Message>fixture denied</Message></Error></Errors></Response>`)
		return true
	}
	return f.compute.handle(w, r, account, region, service, input)
}

type dashboardHarness struct {
	*inventoryHarness
	cloudDashboard *dashboardAWS
	dashboard      dashboard.Service
	dashboardStore repository.DashboardStore
}

func newDashboard(t *testing.T) *dashboardHarness {
	h := newInventory(t)
	cloud := &dashboardAWS{compute: newComputeAWS(t), addresses: newAddressAWS(), catalogCalls: map[string]int{}, readCalls: map[string]int{}}
	h.cloud.extra = cloud.handle
	for ai, account := range []string{h.accountA.AWSAccountID, h.accountB.AWSAccountID} {
		for ri, region := range []string{"us-east-1", "us-west-2"} {
			scope := account + "/" + region
			id := fmt.Sprintf("i-%08x", ai*20+ri+1)
			cloud.compute.instances[scope+"/"+id] = &computeInstance{id: id, scope: scope, state: "running", name: "pool-instance", image: "ami-11111111", kind: "t3.micro", subnet: "subnet-11111111", stamp: time.Now().UTC().Format(time.RFC3339), publicIP: "127.0.3.10", labels: map[string]string{"Name": "pool-instance"}}
			cloud.compute.ips[scope+"/eipalloc-11111111"] = &computeIP{id: "eipalloc-11111111", scope: scope, ip: "127.0.3.10", instance: id, association: "eipassoc-11111111", pool: "amazon", labels: map[string]string{"Name": "pool-ip"}}
		}
	}
	store := dashboard.NewRepository(h.runtime, operations.NewExecutionGuard)
	out := &dashboardHarness{inventoryHarness: h, cloudDashboard: cloud, dashboardStore: store}
	out.reset(t, store)
	return out
}
func (h *dashboardHarness) reset(t *testing.T, store repository.DashboardStore) {
	t.Helper()
	jobs, err := operations.New(operations.Deps{Store: h.op.store, Access: h.op.members, Connections: inventoryBindings{h.accounts}, Publisher: h.op.queue, Executors: []oc.Executor{inventory.NewExecutor(inventory.ExecutorDeps{Store: h.store, Clients: h.accounts, Cloud: h.cloud}), dashboard.NewExecutor(dashboard.ExecutorDeps{Clients: h.accounts, Store: store, Cloud: h.cloudDashboard})}, RedeliveryAfter: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	h.op.service = jobs
	h.dashboard = dashboard.New(dashboard.Deps{Access: h.op.members, Accounts: h.accounts, Inventory: h.service, Store: h.dashboardStore, Jobs: jobs})
}

type lostDashboardAck struct {
	repository.DashboardStore
	mu   sync.Mutex
	lost bool
}

func (s *lostDashboardAck) Advance(ctx context.Context, r entity.Run, rows []entity.Snapshot, l entity.Lease) (entity.Run, error) {
	value, err := s.DashboardStore.Advance(ctx, r, rows, l)
	if err != nil {
		return value, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.lost {
		s.lost = true
		return value, oc.ErrUnavailable
	}
	return value, nil
}
