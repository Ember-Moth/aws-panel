package service

import (
	"encoding/json"
	"github.com/herta-network/aws-panel/internal/module/dashboard/contract"
	"github.com/herta-network/aws-panel/internal/module/dashboard/entity"
	"math"
	"net/netip"
	"sort"
	"strings"
	"time"
)

type decoded struct {
	row   entity.Snapshot
	data  contract.Data
	known bool
}

func stale(v decoded, now time.Time) bool {
	return !v.known || v.row.LastSuccessAt.Before(now.Add(-15*time.Minute)) || v.row.Status == "failed" || v.row.Status == "cancelled"
}
func summarize(view contract.Overview, rows map[string]entity.Snapshot) contract.Overview {
	values := map[string]decoded{}
	for k, row := range rows {
		v := decoded{row: row}
		if row.LastSuccessAt != nil && json.Unmarshal([]byte(row.Data), &v.data) == nil && v.data.Count >= 0 && v.data.Running >= 0 {
			v.known = true
		}
		values[k] = v
	}
	resourceKinds := []string{contract.EC2Instances, contract.LightsailInstances, contract.EC2IPs, contract.LightsailIPs}
	quotaKinds := []string{contract.EC2Quotas, contract.LightsailQuotas, contract.BedrockQuotas}
	allKinds := append(append([]string{}, resourceKinds...), quotaKinds...)
	expected := map[string]map[string]bool{}
	unknown := map[string]int{}
	uncertain := map[string]bool{}
	for _, kind := range allKinds {
		expected[kind] = map[string]bool{}
	}
	catalogs := map[string]decoded{}
	accounts := map[string]contract.Account{}
	regionSet := map[string]bool{}
	distinctEnabled := map[string]bool{}
	regionMetric := contract.Metric{Key: "enabled_regions", Name: "已启用账号区域"}
	globalExpected := map[string]bool{}
	for i := range view.Accounts {
		a := &view.Accounts[i]
		accounts[a.ID] = *a
		catalog := values[key(a.ID, "global", contract.Catalog)]
		catalogs[a.ID] = catalog
		regionMetric.ExpectedScopes++
		if catalog.known {
			a.RegionsKnown = true
			a.EnabledRegions = len(catalog.data.Regions)
			a.UpdatedAt = minTime(nil, catalog.row.LastSuccessAt)
			regionMetric.Known = true
			regionMetric.KnownScopes++
			regionMetric.UpdatedAt = minTime(regionMetric.UpdatedAt, catalog.row.LastSuccessAt)
			for _, region := range catalog.data.Regions {
				regionSet[region] = true
				if view.Query.Region == "" || view.Query.Region == region {
					regionMetric.Count++
					distinctEnabled[region] = true
				}
			}
			if stale(catalog, view.CheckedAt) || a.Status != "ready" {
				regionMetric.Stale = true
			}
		}
		if !catalog.known {
			regionMetric.UnknownAccounts++
		}
		if a.Status != "ready" {
			regionMetric.Partial = true
		}
		for _, kind := range allKinds {
			isQuota := strings.HasSuffix(kind, "_quotas")
			if isQuota && !a.Active {
				continue
			}
			if !catalog.known {
				unknown[kind]++
				continue
			}
			ls := strings.HasPrefix(kind, "lightsail_")
			if ls && !catalog.data.LightsailKnown {
				unknown[kind]++
			}
			if stale(catalog, view.CheckedAt) || a.Status != "ready" {
				uncertain[kind] = true
			}
			for _, region := range catalog.data.Regions {
				if view.Query.Region != "" && view.Query.Region != region {
					continue
				}
				if ls && catalog.data.LightsailKnown && !member(catalog.data.LightsailRegions, region) {
					continue
				}
				expected[kind][key(a.ID, region, kind)] = true
			}
		}
		if a.Active {
			globalExpected[a.ID] = true
		}
	}
	for k, v := range values {
		if v.row.Region != "global" {
			regionSet[v.row.Region] = true
		}
		if _, ok := accounts[v.row.AccountID]; !ok {
			continue
		}
		if view.Query.Region != "" && v.row.Region != "global" && v.row.Region != view.Query.Region {
			continue
		}
		if v.row.Status == "failed" || v.row.Status == "cancelled" {
			for i := range view.Accounts {
				if view.Accounts[i].ID == v.row.AccountID {
					view.Accounts[i].FailedScopes++
				}
			}
		}
		if !strings.HasSuffix(v.row.Kind, "_quotas") && v.row.Kind != contract.Catalog && expected[v.row.Kind] != nil {
			expected[v.row.Kind][k] = true
		}
	}
	regionMetric.Partial = regionMetric.Partial || regionMetric.UnknownAccounts > 0 || regionMetric.KnownScopes < regionMetric.ExpectedScopes
	view.DistinctRegions = len(distinctEnabled)
	names := map[string]string{contract.EC2Instances: "EC2 实例", contract.LightsailInstances: "Lightsail 实例", contract.EC2IPs: "弹性 IP", contract.LightsailIPs: "Lightsail 静态 IP"}
	ipSet := map[string]bool{}
	ipMetric := contract.Metric{Key: "public_ipv4", Name: "公网 IPv4"}
	ipUnknownAccounts := map[string]bool{}
	for _, kind := range resourceKinds {
		metric := contract.Metric{Key: kind, Name: names[kind], ExpectedScopes: len(expected[kind]), UnknownAccounts: unknown[kind], Partial: uncertain[kind]}
		for k := range expected[kind] {
			v := values[k]
			if !v.known || (kind == contract.EC2IPs && !v.data.CarrierKnown) {
				continue
			}
			metric.Known = true
			metric.KnownScopes++
			metric.Count += v.data.Count
			metric.Running += v.data.Running
			metric.UpdatedAt = minTime(metric.UpdatedAt, v.row.LastSuccessAt)
			if stale(v, view.CheckedAt) || accounts[v.row.AccountID].Status != "ready" {
				metric.Stale = true
			}
			catalog := catalogs[v.row.AccountID]
			if catalog.known && !member(catalog.data.Regions, v.row.Region) {
				metric.Stale = true
				metric.Partial = true
			}
			for _, text := range v.data.IPv4 {
				ip, err := netip.ParseAddr(text)
				if err != nil || !ip.Is4() {
					ipMetric.Partial = true
					continue
				}
				ipSet[v.row.AccountID+"/"+v.row.Region+"/"+ip.String()] = true
			}
		}
		metric.Partial = metric.Partial || metric.UnknownAccounts > 0 || metric.KnownScopes < metric.ExpectedScopes
		view.Metrics = append(view.Metrics, metric)
		ipMetric.ExpectedScopes += metric.ExpectedScopes
		ipMetric.KnownScopes += metric.KnownScopes
		ipMetric.Known = ipMetric.Known || metric.Known
		ipMetric.Partial = ipMetric.Partial || metric.Partial
		ipMetric.Stale = ipMetric.Stale || metric.Stale
		ipMetric.UpdatedAt = minTime(ipMetric.UpdatedAt, metric.UpdatedAt)
	}
	for _, a := range view.Accounts {
		if !catalogs[a.ID].known {
			ipUnknownAccounts[a.ID] = true
		}
	}
	ipMetric.UnknownAccounts = len(ipUnknownAccounts)
	ipMetric.Count = int64(len(ipSet))
	carrier := contract.Metric{Key: "carrier_ips", Name: "Carrier IP", ExpectedScopes: len(expected[contract.EC2IPs]), UnknownAccounts: unknown[contract.EC2IPs], Partial: uncertain[contract.EC2IPs]}
	for k := range expected[contract.EC2IPs] {
		v := values[k]
		if !v.known || !v.data.CarrierKnown || v.data.CarrierCount < 0 {
			continue
		}
		carrier.Known = true
		carrier.KnownScopes++
		carrier.Count += v.data.CarrierCount
		carrier.UpdatedAt = minTime(carrier.UpdatedAt, v.row.LastSuccessAt)
		carrier.Stale = carrier.Stale || stale(v, view.CheckedAt) || accounts[v.row.AccountID].Status != "ready"
	}
	carrier.Partial = carrier.Partial || carrier.UnknownAccounts > 0 || carrier.KnownScopes < carrier.ExpectedScopes
	view.Metrics = append(view.Metrics, ipMetric, regionMetric, carrier)
	type quotaObservation struct {
		scope string
		quota contract.Quota
		data  decoded
	}
	grouped := map[string]map[string]quotaObservation{}
	definitions := map[string]contract.PoolQuota{}
	bedrockCoverage := contract.Metric{Key: contract.BedrockQuotas, Name: "Bedrock 配额覆盖", ExpectedScopes: len(expected[contract.BedrockQuotas]), UnknownAccounts: unknown[contract.BedrockQuotas], Partial: uncertain[contract.BedrockQuotas]}
	for _, kind := range quotaKinds {
		service := strings.TrimSuffix(kind, "_quotas")
		for k := range expected[kind] {
			v := values[k]
			if !v.known {
				continue
			}
			if kind == contract.BedrockQuotas {
				bedrockCoverage.Known = true
				bedrockCoverage.KnownScopes++
				bedrockCoverage.UpdatedAt = minTime(bedrockCoverage.UpdatedAt, v.row.LastSuccessAt)
				bedrockCoverage.Stale = bedrockCoverage.Stale || stale(v, view.CheckedAt)
			}
			for _, q := range v.data.Quotas {
				if q.Key == "" || q.Name == "" || math.IsNaN(q.Limit) || math.IsInf(q.Limit, 0) || q.Limit < 0 || math.IsNaN(q.Used) || math.IsInf(q.Used, 0) || q.Used < 0 {
					continue
				}
				group := service + "\x00" + q.Key + "\x00" + q.Name + "\x00" + q.Unit + "\x00" + q.Period
				scope := v.row.AccountID + "/" + v.row.Region
				if q.Global {
					scope = v.row.AccountID + "/global"
					group += "\x00global"
				}
				if grouped[group] == nil {
					grouped[group] = map[string]quotaObservation{}
					count := len(expected[kind])
					if q.Global {
						count = len(globalExpected)
					}
					definitions[group] = contract.PoolQuota{Key: q.Key, Name: q.Name, Code: q.Code, Unit: q.Unit, Period: q.Period, Global: q.Global, Service: service, ExpectedScopes: count, UnknownAccounts: unknown[kind], Stale: uncertain[kind]}
				}
				old, exists := grouped[group][scope]
				if !exists || old.data.row.LastSuccessAt.Before(*v.row.LastSuccessAt) {
					grouped[group][scope] = quotaObservation{scope: scope, quota: q, data: v}
				}
			}
		}
	}
	for group, observations := range grouped {
		value := definitions[group]
		paired := 0
		for _, observation := range observations {
			q, v := observation.quota, observation.data
			if q.LimitKnown {
				value.Known = true
				value.KnownScopes++
				value.Limit += q.Limit
			}
			if q.UsageKnown {
				value.UsageKnown = true
				value.UsageScopes++
				value.Used += q.Used
			}
			if q.LimitKnown && q.UsageKnown {
				paired++
			}
			value.UpdatedAt = minTime(value.UpdatedAt, v.row.LastSuccessAt)
			value.Stale = value.Stale || stale(v, view.CheckedAt) || accounts[v.row.AccountID].Status != "ready"
		}
		value.Complete = value.ExpectedScopes > 0 && value.KnownScopes == value.ExpectedScopes && value.UsageScopes == value.ExpectedScopes && paired == value.ExpectedScopes && value.UnknownAccounts == 0 && !value.Stale
		if value.Complete {
			value.Available = math.Max(0, value.Limit-value.Used)
			if value.Limit > 0 {
				value.Percent = math.Min(100, value.Used/value.Limit*100)
			} else if value.Used > 0 {
				value.Percent = 100
			}
		}
		view.Quotas = append(view.Quotas, value)
	}
	for _, spec := range []struct{ key, name, service, kind string }{{"ec2.ondemand.standard", "标准按需实例", "ec2", contract.EC2Quotas}, {"lightsail.vcpu", "Lightsail 实例 vCPU", "lightsail", contract.LightsailQuotas}} {
		found := false
		for _, q := range view.Quotas {
			if q.Key == spec.key {
				found = true
			}
		}
		if !found {
			view.Quotas = append(view.Quotas, contract.PoolQuota{Key: spec.key, Name: spec.name, Service: spec.service, Unit: "vCPU", ExpectedScopes: len(expected[spec.kind]), UnknownAccounts: unknown[spec.kind]})
		}
	}
	total := contract.PoolQuota{Key: "ec2.ondemand.total", Name: "EC2 按需 vCPU 合计", Service: "ec2", Unit: "vCPU", Complete: true}
	categories := 0
	for _, q := range view.Quotas {
		if strings.HasPrefix(q.Key, "ec2.ondemand.") {
			categories++
			total.Limit += q.Limit
			total.Used += q.Used
			total.Known = total.Known || q.Known
			total.UsageKnown = total.UsageKnown || q.UsageKnown
			total.KnownScopes += q.KnownScopes
			total.UsageScopes += q.UsageScopes
			total.ExpectedScopes += q.ExpectedScopes
			total.UnknownAccounts = max(total.UnknownAccounts, q.UnknownAccounts)
			total.Stale = total.Stale || q.Stale
			total.Complete = total.Complete && q.Complete
			total.UpdatedAt = minTime(total.UpdatedAt, q.UpdatedAt)
		}
	}
	total.Complete = total.Complete && categories > 0
	if total.Complete {
		total.Available = math.Max(0, total.Limit-total.Used)
		if total.Limit > 0 {
			total.Percent = math.Min(100, total.Used/total.Limit*100)
		}
	}
	view.Quotas = append(view.Quotas, total)
	sort.Slice(view.Quotas, func(i, j int) bool {
		a, b := view.Quotas[i], view.Quotas[j]
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Key < b.Key
	})
	for _, q := range view.Quotas {
		if q.Service == "bedrock" {
			bedrockCoverage.Count++
		}
	}
	bedrockCoverage.Partial = bedrockCoverage.Partial || bedrockCoverage.UnknownAccounts > 0 || bedrockCoverage.KnownScopes < bedrockCoverage.ExpectedScopes
	view.BedrockCoverage = bedrockCoverage
	for region := range regionSet {
		view.Regions = append(view.Regions, region)
	}
	sort.Strings(view.Regions)
	if view.Query.Region != "" && !member(view.Regions, view.Query.Region) {
		view.Regions = append(view.Regions, view.Query.Region)
		sort.Strings(view.Regions)
	}
	view.CanRefresh = view.ReadyAccounts > 0
	return view
}
