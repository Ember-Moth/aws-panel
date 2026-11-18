package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	iv "github.com/Ember-Moth/aws-panel/internal/module/inventory/contract"
	oc "github.com/Ember-Moth/aws-panel/internal/module/operations/contract"
	"github.com/aws/smithy-go/encoding/cbor"
)

const gib = int64(1) << 30

// trafficDays are the UTC days the fixtures report: the first day of the
// current month and the last day of the previous month.
func trafficDays() (time.Time, time.Time) {
	now := time.Now().UTC()
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	return month, month.AddDate(0, 0, -1)
}

type lightsailMetrics struct {
	mu      sync.Mutex
	scale   int64
	metrics []string
}

func (m *lightsailMetrics) handle(t *testing.T) func(http.ResponseWriter, *http.Request, string, string, string, map[string]any) bool {
	current, previous := trafficDays()
	return func(w http.ResponseWriter, r *http.Request, account, region, service string, input map[string]any) bool {
		if service != "lightsail" {
			return false
		}
		action := r.Header.Get("X-Amz-Target")
		switch {
		case strings.HasSuffix(action, ".GetBundles"):
			if input["includeInactive"] != true {
				t.Error("retired bundles must be included")
			}
			json.NewEncoder(w).Encode(map[string]any{"bundles": []any{map[string]any{"bundleId": "small_3_0", "transferPerMonthInGb": 1024}}})
			return true
		case strings.HasSuffix(action, ".GetInstanceMetricData"):
			metric, _ := input["metricName"].(string)
			if input["period"] != float64(86400) || input["unit"] != "Bytes" || len(input["statistics"].([]any)) != 1 || input["instanceName"] == "" {
				t.Error("unexpected metric request", input)
			}
			m.mu.Lock()
			m.metrics = append(m.metrics, metric)
			scale := m.scale
			m.mu.Unlock()
			today, before := 2*gib*scale, gib
			if metric == "NetworkOut" {
				today = 6 * gib * scale
			}
			json.NewEncoder(w).Encode(map[string]any{"metricName": metric, "metricData": []any{
				map[string]any{"sum": float64(today), "timestamp": current.Unix(), "unit": "Bytes"},
				map[string]any{"sum": float64(before), "timestamp": previous.Unix(), "unit": "Bytes"},
			}})
			return true
		}
		return false
	}
}

func TestLightsailTrafficAccumulatesDailyRowsAndAllowancePools(t *testing.T) {
	h := newInventory(t)
	ctx := context.Background()
	metrics := &lightsailMetrics{scale: 1}
	h.cloud.extra = metrics.handle(t)
	job := h.refresh(t, iv.RefreshTarget{ConnectionID: h.connectionA.ID, Region: "us-east-1", Kind: iv.LightsailInstance}, iv.RefreshTarget{ConnectionID: h.connectionA.ID, Region: "us-east-1", Kind: iv.LightsailTraffic})
	if d := h.op.drive(t, job.ID); d.Job.Status != oc.Succeeded {
		t.Fatal(d.Job.Status, d.Items)
	}
	rows, err := h.service.Resources(ctx, h.op.admin, iv.Filter{Page: 1, Kind: iv.LightsailInstance})
	if err != nil || len(rows.Items) != 2 {
		t.Fatal("missing Lightsail instances", err, len(rows.Items))
	}
	for _, item := range rows.Items {
		traffic := item.Traffic
		if traffic == nil || traffic.BytesIn != 2*gib || traffic.BytesOut != 6*gib || traffic.PreviousIn != gib || traffic.PreviousOut != gib {
			t.Fatalf("unexpected traffic for %s: %+v", item.Name, traffic)
		}
		if traffic.BundleID != "small_3_0" || traffic.AllowanceBytes != 1024*gib || traffic.Percent != 0.8 || traffic.Days != 1 || traffic.ProjectedBytes < traffic.Total() {
			t.Fatalf("unexpected allowance for %s: %+v", item.Name, traffic)
		}
	}
	detail, err := h.service.Resource(ctx, h.op.admin, rows.Items[0].ID)
	current, _ := trafficDays()
	if err != nil || detail.Resource.Traffic == nil || len(detail.TrafficDaily) == 0 {
		t.Fatal("missing traffic detail", err)
	}
	found := false
	for _, day := range detail.TrafficDaily {
		found = found || day.Day == current.Format(time.DateOnly) && day.BytesOut == 6*gib
	}
	if !found {
		t.Fatal("daily traffic is missing the current month", detail.TrafficDaily)
	}
	overview, err := h.service.Traffic(ctx, h.op.admin, "", "")
	if err != nil || !overview.Known || overview.Instances != 2 || overview.BytesIn != 4*gib || overview.BytesOut != 12*gib {
		t.Fatalf("unexpected traffic overview: %+v %v", overview, err)
	}
	if len(overview.Pools) != 1 || overview.Pools[0].Instances != 2 || overview.Pools[0].UsedBytes != 16*gib || overview.Pools[0].AllowanceBytes != 2048*gib || overview.Pools[0].AccountName != h.accountA.Name {
		t.Fatalf("unexpected allowance pool: %+v", overview.Pools)
	}
	other, err := h.service.Traffic(ctx, h.op.admin, h.accountB.ID, "")
	if err != nil || other.Known || len(other.Pools) != 0 {
		t.Fatalf("traffic leaked across accounts: %+v %v", other, err)
	}
	var count int64
	if err := h.db.QueryRow("SELECT count(*) FROM instance_traffic").Scan(&count); err != nil || count != 4 {
		t.Fatal("unexpected stored traffic rows", count, err)
	}

	// A later sync replaces the same UTC days instead of adding rows.
	metrics.mu.Lock()
	metrics.scale = 2
	metrics.mu.Unlock()
	job = h.refresh(t, iv.RefreshTarget{ConnectionID: h.connectionA.ID, Region: "us-east-1", Kind: iv.LightsailTraffic})
	if d := h.op.drive(t, job.ID); d.Job.Status != oc.Succeeded {
		t.Fatal(d.Job.Status, d.Items)
	}
	if err := h.db.QueryRow("SELECT count(*) FROM instance_traffic").Scan(&count); err != nil || count != 4 {
		t.Fatal("traffic sync duplicated rows", count, err)
	}
	rows, err = h.service.Resources(ctx, h.op.admin, iv.Filter{Page: 1, Kind: iv.LightsailInstance})
	if err != nil || rows.Items[0].Traffic == nil || rows.Items[0].Traffic.BytesOut != 12*gib || rows.Items[0].Traffic.PreviousOut != gib {
		t.Fatal("traffic upsert did not refresh the current day", err)
	}
	scopes, err := h.service.Scopes(ctx, h.op.admin, iv.Filter{Page: 1, Kind: iv.LightsailTraffic})
	if err != nil || len(scopes.Items) != 1 || scopes.Items[0].Status != "ready" || scopes.Items[0].ResourceCount != 2 {
		t.Fatal("traffic scope status not recorded", err, scopes.Items)
	}
}

func TestEC2TrafficReadsCloudWatchDailySums(t *testing.T) {
	h := newCompute(t, true)
	ctx := context.Background()
	job := h.submit(t, h.target())
	if d := h.op.drive(t, job.ID); d.Job.Status != oc.Succeeded {
		t.Fatal(d.Job.Status)
	}
	current, previous := trafficDays()
	var mu sync.Mutex
	queried := map[string]bool{}
	h.cloud.monitoring = func(w http.ResponseWriter, body []byte, account, region string) {
		value, err := cbor.Decode(body)
		request, ok := value.(cbor.Map)
		if err != nil || !ok {
			t.Error("invalid CloudWatch CBOR request", err)
			w.WriteHeader(400)
			return
		}
		results := cbor.List{}
		for _, raw := range request["MetricDataQueries"].(cbor.List) {
			query := raw.(cbor.Map)
			stat := query["MetricStat"].(cbor.Map)
			metric := stat["Metric"].(cbor.Map)
			period, _ := cbor.AsInt64(stat["Period"])
			name := string(metric["MetricName"].(cbor.String))
			dimension := metric["Dimensions"].(cbor.List)[0].(cbor.Map)
			if period != 86400 || string(stat["Stat"].(cbor.String)) != "Sum" || string(metric["Namespace"].(cbor.String)) != "AWS/EC2" || string(dimension["Name"].(cbor.String)) != "InstanceId" {
				t.Error("unexpected CloudWatch query", query)
			}
			mu.Lock()
			queried[string(dimension["Value"].(cbor.String))] = true
			mu.Unlock()
			today := cbor.Float64(3 * gib)
			if name == "NetworkOut" {
				today = cbor.Float64(5 * gib)
			}
			results = append(results, cbor.Map{
				"Id":         query["Id"],
				"StatusCode": cbor.String("Complete"),
				"Timestamps": cbor.List{cbor.Tag{ID: 1, Value: cbor.Float64(previous.Unix())}, cbor.Tag{ID: 1, Value: cbor.Float64(current.Unix())}},
				"Values":     cbor.List{cbor.Float64(gib), today},
			})
		}
		w.Header().Set("Smithy-Protocol", "rpc-v2-cbor")
		w.Header().Set("Content-Type", "application/cbor")
		w.Write(cbor.Encode(cbor.Map{"MetricDataResults": results}))
	}
	job = h.refresh(t, iv.RefreshTarget{ConnectionID: h.connectionA.ID, Region: "us-east-1", Kind: iv.EC2Instance}, iv.RefreshTarget{ConnectionID: h.connectionA.ID, Region: "us-east-1", Kind: iv.EC2Traffic})
	if d := h.op.drive(t, job.ID); d.Job.Status != oc.Succeeded {
		t.Fatal(d.Job.Status, d.Items)
	}
	rows, err := h.service.Resources(ctx, h.op.admin, iv.Filter{Page: 1, Kind: iv.EC2Instance})
	if err != nil || len(rows.Items) != 1 || rows.Items[0].Details.EC2 == nil {
		t.Fatal("missing EC2 snapshot", err)
	}
	item := rows.Items[0]
	if !queried[item.Details.EC2.ID] {
		t.Fatal("CloudWatch was not queried for the instance", queried)
	}
	traffic := item.Traffic
	if traffic == nil || traffic.BytesIn != 3*gib || traffic.BytesOut != 5*gib || traffic.PreviousIn != gib || traffic.PreviousOut != gib || traffic.AllowanceBytes != 0 || traffic.Percent != 0 {
		t.Fatalf("unexpected EC2 traffic: %+v", traffic)
	}
	overview, err := h.service.Traffic(ctx, h.op.admin, "", "us-east-1")
	if err != nil || overview.Instances != 1 || overview.BytesOut != 5*gib || len(overview.Pools) != 0 {
		t.Fatalf("unexpected EC2 traffic overview: %+v %v", overview, err)
	}
}
