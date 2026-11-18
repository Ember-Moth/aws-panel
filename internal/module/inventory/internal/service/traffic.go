package service

import (
	"context"
	"math"
	"time"

	"github.com/Ember-Moth/aws-panel/internal/infra/clock"
	"github.com/Ember-Moth/aws-panel/internal/module/inventory/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/inventory/entity"
	"github.com/Ember-Moth/aws-panel/internal/module/inventory/internal/scope"
)

// billingMonth returns the UTC calendar month AWS bills data transfer by.
type billingMonth struct {
	label, start, previous string
	elapsed                float64 // fraction of the month already passed, (0,1]
}

func currentMonth(now time.Time) billingMonth {
	now = now.UTC()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	elapsed := now.Sub(start).Seconds() / end.Sub(start).Seconds()
	// Avoid projecting a whole month from the first few hours of data.
	elapsed = max(elapsed, 24*time.Hour.Seconds()/end.Sub(start).Seconds())
	return billingMonth{label: start.Format("2006-01"), start: start.Format(time.DateOnly), previous: start.AddDate(0, -1, 0).Format(time.DateOnly), elapsed: min(elapsed, 1)}
}
func percent(used, allowance int64) float64 {
	if allowance <= 0 {
		return 0
	}
	return math.Round(float64(used)/float64(allowance)*1000) / 10
}
func trafficSummary(row entity.TrafficTotal, month billingMonth) *contract.TrafficSummary {
	used := row.BytesIn + row.BytesOut
	value := &contract.TrafficSummary{Month: month.label, BytesIn: row.BytesIn, BytesOut: row.BytesOut, PreviousIn: row.PreviousIn, PreviousOut: row.PreviousOut, Days: row.Days, BundleID: row.BundleID, AllowanceBytes: row.AllowanceBytes, Percent: percent(used, row.AllowanceBytes), UpdatedAt: timeView(row.ObservedAt)}
	if row.Days > 0 {
		value.ProjectedBytes = int64(float64(used) / month.elapsed)
	}
	return value
}

// trafficKind maps an instance snapshot kind to the scope collecting its metrics.
func trafficKind(kind string) string {
	switch kind {
	case contract.LightsailInstance:
		return contract.LightsailTraffic
	case contract.EC2Instance:
		return contract.EC2Traffic
	}
	return ""
}

// attachTraffic fills the current month for instance rows that have collected traffic.
func (s *Service) attachTraffic(ctx context.Context, accounts []string, items []contract.Resource) error {
	resources := []string{}
	for _, item := range items {
		if trafficKind(item.Kind) != "" {
			resources = append(resources, item.ResourceID)
		}
	}
	if len(resources) == 0 {
		return nil
	}
	month := currentMonth(clock.Now())
	rows, err := s.deps.Store.TrafficTotals(ctx, accounts, resources, month.start, month.previous)
	if err != nil {
		return err
	}
	byKey := map[string]entity.TrafficTotal{}
	for _, row := range rows {
		byKey[row.AccountID+"/"+row.Kind+"/"+row.ResourceID] = row
	}
	for i := range items {
		if row, ok := byKey[items[i].AccountID+"/"+trafficKind(items[i].Kind)+"/"+items[i].ResourceID]; ok {
			items[i].Traffic = trafficSummary(row, month)
		}
	}
	return nil
}

// Traffic summarizes the current UTC month across the actor's accounts.
func (s *Service) Traffic(ctx context.Context, actorID int64, accountID, region string) (contract.TrafficOverview, error) {
	rows, names, err := s.accounts(ctx, actorID)
	if err != nil {
		return contract.TrafficOverview{}, err
	}
	if region != "" && !scope.RegionOK(region) {
		return contract.TrafficOverview{}, contract.ErrInvalid
	}
	accounts := ids(rows)
	if accountID != "" {
		if _, ok := names[accountID]; !ok {
			return contract.TrafficOverview{}, contract.ErrForbidden
		}
		accounts = []string{accountID}
	}
	month := currentMonth(clock.Now())
	result := contract.TrafficOverview{Month: month.label, Pools: []contract.TrafficPool{}}
	aggregate, err := s.deps.Store.TrafficAggregate(ctx, accounts, region, month.start)
	if err != nil {
		return result, err
	}
	result.Known, result.Instances, result.BytesIn, result.BytesOut, result.UpdatedAt = aggregate.Instances > 0, aggregate.Instances, aggregate.BytesIn, aggregate.BytesOut, timeView(aggregate.ObservedAt)
	pools, err := s.deps.Store.TrafficPools(ctx, accounts, region, month.start)
	if err != nil {
		return result, err
	}
	for _, pool := range pools {
		result.Pools = append(result.Pools, contract.TrafficPool{AccountID: pool.AccountID, AccountName: names[pool.AccountID], Region: pool.Region, BundleID: pool.BundleID, Instances: pool.Instances, UsedBytes: pool.UsedBytes, AllowanceBytes: pool.AllowanceBytes, Percent: percent(pool.UsedBytes, pool.AllowanceBytes)})
	}
	return result, nil
}
