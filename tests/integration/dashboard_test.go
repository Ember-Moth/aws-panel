package integration

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/herta-network/aws-panel/internal/auth"
	ac "github.com/herta-network/aws-panel/internal/module/accounts/contract"
	dc "github.com/herta-network/aws-panel/internal/module/dashboard/contract"
	ic "github.com/herta-network/aws-panel/internal/module/identity/contract"
	iv "github.com/herta-network/aws-panel/internal/module/inventory/contract"
	oc "github.com/herta-network/aws-panel/internal/module/operations/contract"
	"github.com/herta-network/aws-panel/internal/module/quotas"
	"os"
	"strings"
	"testing"
)

func metric(v dc.Overview, key string) dc.Metric {
	for _, m := range v.Metrics {
		if m.Key == key {
			return m
		}
	}
	return dc.Metric{}
}
func quota(v dc.Overview, key string) dc.PoolQuota {
	for _, q := range v.Quotas {
		if q.Key == key {
			return q
		}
	}
	return dc.PoolQuota{}
}
func refreshDashboard(t *testing.T, h *dashboardHarness) {
	t.Helper()
	v, err := h.dashboard.Refresh(context.Background(), h.op.admin, dc.RefreshRequest{IdempotencyKey: uuid.NewString()}, auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range v.Jobs {
		detail := h.op.drive(t, j.ID)
		if detail.Job.Status != oc.Succeeded {
			t.Fatal("dashboard collection failed", detail.Job.Status, detail.Items)
		}
	}
}
func TestDashboardResourceQuotaAggregationAndLostSaveRecovery(t *testing.T) {
	h := newDashboard(t)
	h.reset(t, &lostDashboardAck{DashboardStore: h.dashboardStore})
	duplicate, err := h.accounts.SaveConnection(context.Background(), h.op.admin, h.accountA.ID, "", ac.ConnectionRequest{Name: "Second verified connection", AuthType: "access_key", Region: "us-east-1", RouteMode: "socks5", ProxyID: &h.proxyA.ID, Credentials: &ac.Credentials{AccessKeyID: testKeyA, SecretAccessKey: testSecretA}}, auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := h.accounts.Verify(context.Background(), h.op.admin, duplicate.ID, auth.RequestMeta{}); err != nil || !result.OK {
		t.Fatal(err, result.Code)
	}

	refreshDashboard(t, h)
	v, err := h.dashboard.Read(context.Background(), h.op.admin, dc.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("AWS_PANEL_CAPTURE_DASHBOARD_UI") == "true" {
		encoded, _ := json.MarshalIndent(v, "", "  ")
		if err := os.WriteFile("../../.local/dashboard-preview-data.json", encoded, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if v.AccountCount != 2 || v.ReadyAccounts != 2 {
		t.Fatal(v.AccountCount, v.ReadyAccounts)
	}
	for key, want := range map[string]int64{dc.EC2Instances: 4, dc.LightsailInstances: 8, dc.EC2IPs: 4, dc.LightsailIPs: 4, "public_ipv4": 12, "enabled_regions": 4} {
		m := metric(v, key)
		if !m.Known || m.Count != want || m.Partial || m.Stale {
			t.Fatal("incorrect aggregate", key, m)
		}
	}
	if v.DistinctRegions != 2 {
		t.Fatal("region deduplication")
	}
	for key, want := range map[string]float64{"ec2.ondemand.standard": 1000, "ec2.ondemand.total": 1000, "lightsail.vcpu": 80, "bedrock.L-BEDTPM": 1000, "bedrock.L-BEDRPM": 100, "bedrock.L-BEDGLOBAL": 1000} {
		q := quota(v, key)
		if !q.Known || q.Limit != want {
			t.Fatal("incorrect quota", key, q)
		}
	}
	if q := quota(v, "bedrock.L-BEDGLOBAL"); q.KnownScopes != 2 || q.ExpectedScopes != 2 || q.UsageKnown {
		t.Fatal("global quota counted per region or usage invented", q)
	}
	h.cloudDashboard.mu.Lock()
	defer h.cloudDashboard.mu.Unlock()
	for _, account := range []string{h.accountA.AWSAccountID, h.accountB.AWSAccountID} {
		for _, region := range []string{"us-east-1", "us-west-2"} {
			if h.cloudDashboard.readCalls[account+"/"+region+"/DescribeInstances"] != 2 {
				t.Fatal("committed regional snapshot was fetched twice", account, region, h.cloudDashboard.readCalls)
			}
		}
	}
	if h.cloudDashboard.catalogCalls[h.accountA.AWSAccountID] != 1 || h.cloudDashboard.catalogCalls[h.accountB.AWSAccountID] != 1 {
		t.Fatal("committed discovery repeated after acknowledgement loss")
	}
}
func TestDashboardPartialReadPreservesOldDataAndBedrockPages(t *testing.T) {
	h := newDashboard(t)
	refreshDashboard(t, h)
	before, err := h.dashboard.Read(context.Background(), h.op.admin, dc.Query{})
	if err != nil {
		t.Fatal(err)
	}
	h.cloudDashboard.mu.Lock()
	h.cloudDashboard.partial = true
	h.cloudDashboard.mu.Unlock()
	refreshDashboard(t, h)
	after, err := h.dashboard.Read(context.Background(), h.op.admin, dc.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if metric(after, dc.EC2Instances).Count != metric(before, dc.EC2Instances).Count || !metric(after, dc.EC2Instances).Stale {
		t.Fatal("failed read erased resource count")
	}
	if q := quota(after, "bedrock.L-BEDTPM"); q.Limit != 1000 || !q.Stale {
		t.Fatal("partial quota pagination replaced previous complete report", q)
	}
	cfg, err := h.accounts.ConfigFor(context.Background(), h.op.admin, h.connectionA.ID, "us-east-1", false)
	if err != nil {
		t.Fatal(err)
	}
	report, err := quotas.NewCloud().Read(context.Background(), addressConfig(cfg), h.accountA.AWSAccountID, "bedrock")
	if err != nil || report.LimitsRead || len(report.Entries) != 0 {
		t.Fatal("partial Bedrock report presented as complete", report, err)
	}
}
func TestDashboardScopeAuthorizationAndUnknownIsNotZero(t *testing.T) {
	h := newDashboard(t)
	ctx := context.Background()
	empty, err := h.dashboard.Read(ctx, h.op.admin, dc.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if metric(empty, dc.EC2Instances).Known || quota(empty, "ec2.ondemand.total").Known {
		t.Fatal("unknown data converted to zero")
	}
	refreshDashboard(t, h)
	reader, err := h.op.members.CreateMember(ctx, h.op.admin, ic.CreateMemberRequest{Username: "pool-reader", DisplayName: "Pool reader", Password: "pool-reader-test-passphrase", Role: "viewer"}, auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.op.members.SetAccountGrant(ctx, h.op.admin, reader.ID, ic.GrantRequest{AccountID: h.accountA.ID, Access: "read"}, auth.RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	v, err := h.dashboard.Read(ctx, reader.ID, dc.Query{})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(v)
	if v.AccountCount != 1 || metric(v, dc.EC2Instances).Count != 2 || quota(v, "ec2.ondemand.standard").Limit != 300 || strings.Contains(string(encoded), h.accountB.AWSAccountID) {
		t.Fatal("account pool escaped grants", v.AccountCount, string(encoded))
	}
	if _, err := h.dashboard.Read(ctx, reader.ID, dc.Query{AccountID: h.accountB.ID}); !errors.Is(err, dc.ErrForbidden) {
		t.Fatal("forbidden account filter accepted", err)
	}
	v, err = h.dashboard.Read(ctx, reader.ID, dc.Query{Region: "us-west-2"})
	if err != nil || metric(v, dc.EC2Instances).Count != 1 || quota(v, "ec2.ondemand.standard").Limit != 200 {
		t.Fatal("regional quota filter incorrect", err)
	}
}

func TestDashboardUsesExistingInventoryAndExcludesDisabledQuotaCapacity(t *testing.T) {
	h := newDashboard(t)
	ctx := context.Background()
	job := h.refresh(t, iv.RefreshTarget{ConnectionID: h.connectionA.ID, Kind: iv.AccountRegion}, iv.RefreshTarget{ConnectionID: h.connectionA.ID, Kind: iv.LightsailInstance, Region: "us-east-1"}, iv.RefreshTarget{ConnectionID: h.connectionA.ID, Kind: iv.EC2Instance, Region: "us-east-1"})
	if detail := h.op.drive(t, job.ID); detail.Job.Status != oc.Succeeded {
		t.Fatal(detail.Job.Status, detail.Items)
	}
	v, err := h.dashboard.Read(ctx, h.op.admin, dc.Query{AccountID: h.accountA.ID, Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	if metric(v, dc.LightsailInstances).Count != 2 || !metric(v, dc.LightsailInstances).Known || metric(v, dc.EC2Instances).Count != 1 {
		t.Fatal("existing inventory was not projected", v.Metrics)
	}
	refreshDashboard(t, h)
	active := false
	_, err = h.accounts.SaveAccount(ctx, h.op.admin, h.accountA.ID, ac.AccountRequest{Name: h.accountA.Name, Partition: h.accountA.Partition, AWSAccountID: h.accountA.AWSAccountID, Version: h.accountA.Version, Active: &active}, auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	v, err = h.dashboard.Read(ctx, h.op.admin, dc.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if v.ActiveAccounts != 1 || quota(v, "ec2.ondemand.standard").Limit != 700 || metric(v, dc.EC2Instances).Count != 4 || !metric(v, dc.EC2Instances).Stale {
		t.Fatal("disabled account capacity or retained resources misrepresented", v.ActiveAccounts, v.Metrics, quota(v, "ec2.ondemand.standard"))
	}
}
