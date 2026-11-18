package awsapi

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Ember-Moth/aws-panel/internal/module/compute"
	"github.com/Ember-Moth/aws-panel/internal/module/inventory/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/inventory/internal/source"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/middleware"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwt "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/lightsail"
	lt "github.com/aws/aws-sdk-go-v2/service/lightsail/types"
)

// AWS states data transfer allowances in GB; billing treats 1 GB as 2^30 bytes.
const gigabyte = int64(1) << 30

// maxTrafficInstances bounds one regional traffic sync; larger Regions fail as
// incomplete instead of silently dropping instances.
const maxTrafficInstances = 1000

// trafficWindow starts at the previous UTC month so both month totals can be
// rebuilt while AWS still retains the metrics.
func trafficWindow(now time.Time) (time.Time, time.Time) {
	now = now.UTC()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0)
	return start, now.Truncate(time.Minute)
}
func utcDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// bytesValue converts a metric sum to whole bytes and rejects impossible values.
func bytesValue(value float64) (int64, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1<<62 {
		return 0, contract.ErrResponse
	}
	return int64(math.Round(value)), nil
}

type dayTotals struct{ in, out int64 }

func trafficRows(resourceID, name, bundle string, allowance int64, days map[time.Time]*dayTotals, observed time.Time) []source.TrafficRow {
	rows := make([]source.TrafficRow, 0, len(days))
	for day, value := range days {
		rows = append(rows, source.TrafficRow{ResourceID: resourceID, Name: name, BundleID: bundle, AllowanceBytes: allowance, Day: day, BytesIn: value.in, BytesOut: value.out, ObservedAt: observed})
	}
	return rows
}

func lightsailTraffic(ctx context.Context, cfg aws.Config, req source.Request, now time.Time) (source.Snapshot, error) {
	listed, err := instances(ctx, cfg, req)
	if err != nil {
		return source.Snapshot{}, err
	}
	if len(listed.Resources) > maxTrafficInstances {
		return source.Snapshot{}, contract.ErrIncomplete
	}
	client := lightsail.NewFromConfig(cfg)
	allowances := map[string]int64{}
	if len(listed.Resources) > 0 {
		if allowances, err = bundleAllowances(ctx, client); err != nil {
			return source.Snapshot{}, err
		}
	}
	start, end := trafficWindow(now)
	result := source.Snapshot{Resources: []source.Resource{}, Traffic: []source.TrafficRow{}, Pages: listed.Pages, RequestID: listed.RequestID}
	for _, instance := range listed.Resources {
		days := map[time.Time]*dayTotals{}
		for _, metric := range []lt.InstanceMetricName{lt.InstanceMetricNameNetworkIn, lt.InstanceMetricNameNetworkOut} {
			response, err := client.GetInstanceMetricData(ctx, &lightsail.GetInstanceMetricDataInput{InstanceName: aws.String(instance.Name), MetricName: metric, Period: aws.Int32(86400), StartTime: aws.Time(start), EndTime: aws.Time(end), Statistics: []lt.MetricStatistic{lt.MetricStatisticSum}, Unit: lt.MetricUnitBytes})
			if err != nil {
				return source.Snapshot{}, err
			}
			result.RequestID, _ = middleware.GetRequestIDMetadata(response.ResultMetadata)
			for _, point := range response.MetricData {
				if point.Timestamp == nil || point.Sum == nil {
					continue
				}
				value, err := bytesValue(*point.Sum)
				if err != nil {
					return source.Snapshot{}, err
				}
				day := utcDay(*point.Timestamp)
				if day.Before(start) || day.After(end) {
					continue
				}
				if days[day] == nil {
					days[day] = &dayTotals{}
				}
				if metric == lt.InstanceMetricNameNetworkIn {
					days[day].in += value
				} else {
					days[day].out += value
				}
			}
		}
		bundle := ""
		if instance.Details.Instance != nil {
			bundle = instance.Details.Instance.BundleID
		}
		result.Traffic = append(result.Traffic, trafficRows(instance.ID, instance.Name, bundle, allowances[bundle], days, now)...)
	}
	return result, nil
}

// bundleAllowances maps every bundle, including retired ones still used by
// older instances, to its monthly per-instance allowance in bytes.
func bundleAllowances(ctx context.Context, client *lightsail.Client) (map[string]int64, error) {
	result := map[string]int64{}
	var token *string
	seen := map[string]bool{}
	for pages := 0; pages < 64; pages++ {
		response, err := client.GetBundles(ctx, &lightsail.GetBundlesInput{IncludeInactive: aws.Bool(true), PageToken: token})
		if err != nil {
			return nil, err
		}
		for _, bundle := range response.Bundles {
			id, gb := aws.ToString(bundle.BundleId), aws.ToInt32(bundle.TransferPerMonthInGb)
			if id != "" && gb > 0 {
				result[id] = int64(gb) * gigabyte
			}
		}
		next := aws.ToString(response.NextPageToken)
		if next == "" {
			return result, nil
		}
		if seen[next] {
			return nil, contract.ErrIncomplete
		}
		seen[next] = true
		token = aws.String(next)
	}
	return nil, contract.ErrIncomplete
}

// ec2Traffic reads NetworkIn/NetworkOut for every instance in the Region.
// These metrics cover all interfaces, including private VPC traffic.
func ec2Traffic(ctx context.Context, cfg aws.Config, req source.Request, now time.Time) (source.Snapshot, error) {
	rows, err := compute.NewCloud().Instances(ctx, cfg, req.AWSAccountID)
	if err != nil {
		return source.Snapshot{}, err
	}
	if len(rows) > maxTrafficInstances {
		return source.Snapshot{}, contract.ErrIncomplete
	}
	result := source.Snapshot{Resources: []source.Resource{}, Traffic: []source.TrafficRow{}, Pages: 1}
	client := cloudwatch.NewFromConfig(cfg)
	start, end := trafficWindow(now)
	// GetMetricData accepts at most 500 queries: two per instance.
	for offset := 0; offset < len(rows); offset += 250 {
		batch := rows[offset:min(offset+250, len(rows))]
		queries := make([]cwt.MetricDataQuery, 0, len(batch)*2)
		days := make([]map[time.Time]*dayTotals, len(batch))
		for i, instance := range batch {
			if !strings.HasPrefix(instance.ID, "i-") || instance.ARN == "" {
				return source.Snapshot{}, contract.ErrResponse
			}
			days[i] = map[time.Time]*dayTotals{}
			for _, metric := range []string{"NetworkIn", "NetworkOut"} {
				queries = append(queries, cwt.MetricDataQuery{Id: aws.String(fmt.Sprintf("m%d%s", i, strings.ToLower(metric[7:]))), ReturnData: aws.Bool(true), MetricStat: &cwt.MetricStat{Stat: aws.String("Sum"), Period: aws.Int32(86400), Unit: cwt.StandardUnitBytes, Metric: &cwt.Metric{Namespace: aws.String("AWS/EC2"), MetricName: aws.String(metric), Dimensions: []cwt.Dimension{{Name: aws.String("InstanceId"), Value: aws.String(instance.ID)}}}}})
			}
		}
		var token *string
		seen := map[string]bool{}
		for {
			response, err := client.GetMetricData(ctx, &cloudwatch.GetMetricDataInput{StartTime: aws.Time(start), EndTime: aws.Time(end), MetricDataQueries: queries, ScanBy: cwt.ScanByTimestampAscending, NextToken: token})
			if err != nil {
				return source.Snapshot{}, err
			}
			result.Pages++
			if result.Pages > 256 {
				return source.Snapshot{}, contract.ErrIncomplete
			}
			result.RequestID, _ = middleware.GetRequestIDMetadata(response.ResultMetadata)
			for _, series := range response.MetricDataResults {
				index, inbound, ok := parseQueryID(aws.ToString(series.Id), len(batch))
				if !ok || len(series.Timestamps) != len(series.Values) {
					return source.Snapshot{}, contract.ErrResponse
				}
				for i, at := range series.Timestamps {
					value, err := bytesValue(series.Values[i])
					if err != nil {
						return source.Snapshot{}, err
					}
					day := utcDay(at)
					if days[index][day] == nil {
						days[index][day] = &dayTotals{}
					}
					if inbound {
						days[index][day].in += value
					} else {
						days[index][day].out += value
					}
				}
			}
			next := aws.ToString(response.NextToken)
			if next == "" {
				break
			}
			if seen[next] {
				return source.Snapshot{}, contract.ErrIncomplete
			}
			seen[next] = true
			token = aws.String(next)
		}
		for i, instance := range batch {
			name := instance.Name
			if name == "" {
				name = instance.ID
			}
			result.Traffic = append(result.Traffic, trafficRows(instance.ARN, name, "", 0, days[i], now)...)
		}
	}
	return result, nil
}
func parseQueryID(id string, size int) (int, bool, bool) {
	var index int
	var direction string
	if _, err := fmt.Sscanf(id, "m%d%s", &index, &direction); err != nil || index < 0 || index >= size || (direction != "in" && direction != "out") {
		return 0, false, false
	}
	return index, direction == "in", true
}
