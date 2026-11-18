package service

import (
	"testing"
	"time"

	"github.com/Ember-Moth/aws-panel/internal/module/inventory/entity"
)

func TestCurrentMonthUsesUTCAndBoundsEarlyProjection(t *testing.T) {
	month := currentMonth(time.Date(2026, 12, 1, 3, 0, 0, 0, time.FixedZone("CST", 8*3600)))
	if month.label != "2026-11" || month.start != "2026-11-01" || month.previous != "2026-10-01" {
		t.Fatal(month)
	}
	early := currentMonth(time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC))
	if early.elapsed != 1.0/30 {
		t.Fatal("projection should assume at least one day elapsed", early.elapsed)
	}
}

func TestTrafficSummaryPercentAndProjection(t *testing.T) {
	month := billingMonth{label: "2026-11", elapsed: 0.5}
	summary := trafficSummary(entity.TrafficTotal{BytesIn: 100, BytesOut: 300, AllowanceBytes: 1000, Days: 15}, month)
	if summary.Percent != 40 || summary.ProjectedBytes != 800 || summary.Total() != 400 {
		t.Fatalf("%+v", summary)
	}
	if empty := trafficSummary(entity.TrafficTotal{}, month); empty.Percent != 0 || empty.ProjectedBytes != 0 {
		t.Fatalf("%+v", empty)
	}
}
