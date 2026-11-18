package repo

import (
	"context"
	"strings"
	"time"

	"github.com/Ember-Moth/aws-panel/internal/module/inventory/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/inventory/entity"
	"gorm.io/gorm"
)

// trafficRetention keeps a little over a year of daily rows per scope.
const trafficRetention = 400 * 24 * time.Hour

// CompleteTraffic upserts the fetched days under the same lease and generation
// checks as snapshot refreshes. Days AWS no longer returns are kept.
func (s *Store) CompleteTraffic(ctx context.Context, ticket entity.Ticket, values []entity.Traffic, pages int, requestID string) error {
	return s.finish(ctx, ticket, func(db *gorm.DB, scope *entity.Scope, now time.Time) error {
		instances := map[string]bool{}
		for start := 0; start < len(values); start += 200 {
			batch := values[start:min(start+200, len(values))]
			rows := make([]string, 0, len(batch))
			args := make([]any, 0, len(batch)*10)
			for _, v := range batch {
				instances[v.ResourceID] = true
				rows = append(rows, "(?,?,?::date,?,?,?,?,?,?,?)")
				args = append(args, scope.ID, v.ResourceID, v.Day, v.Name, v.BundleID, v.AllowanceBytes, v.BytesIn, v.BytesOut, ticket.Run.Generation, v.ObservedAt)
			}
			sql := `INSERT INTO instance_traffic(scope_id,resource_id,day,name,bundle_id,allowance_bytes,bytes_in,bytes_out,generation,observed_at) VALUES ` + strings.Join(rows, ",") +
				` ON CONFLICT (scope_id,resource_id,day) DO UPDATE SET name=EXCLUDED.name,bundle_id=EXCLUDED.bundle_id,allowance_bytes=EXCLUDED.allowance_bytes,bytes_in=EXCLUDED.bytes_in,bytes_out=EXCLUDED.bytes_out,generation=EXCLUDED.generation,observed_at=EXCLUDED.observed_at`
			if err := db.WithContext(ctx).Exec(sql, args...).Error; err != nil {
				return convert(err)
			}
		}
		cutoff := now.Add(-trafficRetention).UTC().Format(time.DateOnly)
		if err := db.WithContext(ctx).Exec("DELETE FROM instance_traffic WHERE scope_id=? AND day<?::date", scope.ID, cutoff).Error; err != nil {
			return convert(err)
		}
		scope.Status = "ready"
		scope.LastSuccessAt = &now
		scope.LastErrorCode = ""
		scope.ResourceCount = len(instances)
		return convert(db.WithContext(ctx).Table("inventory_runs").Where("id=?", ticket.Run.ID).Updates(map[string]any{"status": "complete", "finished_at": now, "page_count": pages, "resource_count": len(instances), "aws_request_id": requestID}).Error)
	})
}

// TrafficTotals sums the given instances from the previous month onwards; month
// and previous are the first UTC days of the current and previous month.
func (s *Store) TrafficTotals(ctx context.Context, accounts, resources []string, month, previous string) ([]entity.TrafficTotal, error) {
	rows := []entity.TrafficTotal{}
	if len(accounts) == 0 || len(resources) == 0 {
		return rows, nil
	}
	err := s.db.WithContext(ctx).Raw(`SELECT s.account_id,s.kind,s.region,t.resource_id,
 COALESCE((array_agg(t.bundle_id ORDER BY t.day DESC) FILTER (WHERE t.day>=?::date))[1],'') AS bundle_id,
 COALESCE((array_agg(t.allowance_bytes ORDER BY t.day DESC) FILTER (WHERE t.day>=?::date))[1],0) AS allowance_bytes,
 COALESCE(sum(t.bytes_in) FILTER (WHERE t.day>=?::date),0) AS bytes_in,
 COALESCE(sum(t.bytes_out) FILTER (WHERE t.day>=?::date),0) AS bytes_out,
 COALESCE(sum(t.bytes_in) FILTER (WHERE t.day<?::date),0) AS previous_in,
 COALESCE(sum(t.bytes_out) FILTER (WHERE t.day<?::date),0) AS previous_out,
 count(*) FILTER (WHERE t.day>=?::date) AS days,
 max(t.observed_at) AS observed_at
 FROM instance_traffic t JOIN inventory_scopes s ON s.id=t.scope_id
 WHERE s.kind IN ? AND s.account_id IN ? AND t.resource_id IN ? AND t.day>=?::date
 GROUP BY s.account_id,s.kind,s.region,t.resource_id`,
		month, month, month, month, month, month, month, trafficKinds(), accounts, resources, previous).Scan(&rows).Error
	return rows, convert(err)
}

// TrafficDaily returns per-day totals for one instance since from.
func (s *Store) TrafficDaily(ctx context.Context, accounts []string, resource, from string) ([]entity.TrafficDay, error) {
	rows := []entity.TrafficDay{}
	if len(accounts) == 0 {
		return rows, nil
	}
	err := s.db.WithContext(ctx).Raw(`SELECT to_char(t.day,'YYYY-MM-DD') AS day,sum(t.bytes_in) AS bytes_in,sum(t.bytes_out) AS bytes_out
 FROM instance_traffic t JOIN inventory_scopes s ON s.id=t.scope_id
 WHERE s.kind IN ? AND s.account_id IN ? AND t.resource_id=? AND t.day>=?::date
 GROUP BY t.day ORDER BY t.day`, trafficKinds(), accounts, resource, from).Scan(&rows).Error
	return rows, convert(err)
}

// TrafficPools groups Lightsail instances by account, Region and bundle, the
// unit AWS uses to pool data transfer allowances.
func (s *Store) TrafficPools(ctx context.Context, accounts []string, region, month string) ([]entity.TrafficPool, error) {
	rows := []entity.TrafficPool{}
	if len(accounts) == 0 {
		return rows, nil
	}
	filter, args := regionFilter(region)
	err := s.db.WithContext(ctx).Raw(`WITH per AS (
 SELECT s.account_id,s.region,t.resource_id,
  (array_agg(t.bundle_id ORDER BY t.day DESC))[1] AS bundle_id,
  (array_agg(t.allowance_bytes ORDER BY t.day DESC))[1] AS allowance_bytes,
  sum(t.bytes_in+t.bytes_out) AS used
 FROM instance_traffic t JOIN inventory_scopes s ON s.id=t.scope_id
 WHERE s.kind=? AND s.account_id IN ? AND t.day>=?::date`+filter+`
 GROUP BY s.account_id,s.region,t.resource_id)
 SELECT account_id,region,bundle_id,count(*) AS instances,sum(used) AS used_bytes,sum(allowance_bytes) AS allowance_bytes
 FROM per WHERE bundle_id<>'' AND allowance_bytes>0
 GROUP BY account_id,region,bundle_id
 ORDER BY sum(used)::float8/sum(allowance_bytes) DESC,account_id,region,bundle_id
 LIMIT 200`, append([]any{contract.LightsailTraffic, accounts, month}, args...)...).Scan(&rows).Error
	return rows, convert(err)
}

// TrafficAggregate totals all collected instances for the month.
func (s *Store) TrafficAggregate(ctx context.Context, accounts []string, region, month string) (entity.TrafficAggregate, error) {
	var row entity.TrafficAggregate
	if len(accounts) == 0 {
		return row, nil
	}
	filter, args := regionFilter(region)
	err := s.db.WithContext(ctx).Raw(`SELECT count(DISTINCT (s.account_id,t.resource_id)) AS instances,COALESCE(sum(t.bytes_in),0) AS bytes_in,COALESCE(sum(t.bytes_out),0) AS bytes_out,max(t.observed_at) AS observed_at
 FROM instance_traffic t JOIN inventory_scopes s ON s.id=t.scope_id
 WHERE s.kind IN ? AND s.account_id IN ? AND t.day>=?::date`+filter, append([]any{trafficKinds(), accounts, month}, args...)...).Scan(&row).Error
	return row, convert(err)
}
func trafficKinds() []string { return []string{contract.LightsailTraffic, contract.EC2Traffic} }
func regionFilter(region string) (string, []any) {
	if region == "" {
		return "", nil
	}
	return " AND s.region=?", []any{region}
}
