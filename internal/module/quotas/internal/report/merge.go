package report

import (
	"github.com/herta-network/aws-panel/internal/catalogue"
	"github.com/herta-network/aws-panel/internal/module/quotas/contract"
	"math"
)

// Merge keeps independent observation timestamps and unknown fields. A newer
// usage result must not imply that an older limit was queried again.
func Merge(limits, usage contract.Report) contract.Report {
	out := limits
	out.Entries = append([]contract.Entry{}, limits.Entries...)
	out.Warnings = append(append([]string{}, limits.Warnings...), usage.Warnings...)
	out.Freshness = map[string]catalogue.Freshness{}
	for k, v := range limits.Freshness {
		out.Freshness[k] = v
	}
	for k, v := range usage.Freshness {
		out.Freshness[k] = v
	}
	if out.CheckedAt.IsZero() || (!usage.CheckedAt.IsZero() && usage.CheckedAt.Before(out.CheckedAt)) {
		out.CheckedAt = usage.CheckedAt
	}
	values := map[string]contract.Entry{}
	for _, v := range usage.Entries {
		values[v.Key] = v
	}
	for i := range out.Entries {
		row := &out.Entries[i]
		v, ok := values[row.Key]
		row.Used = v.Used
		row.UsageKnown = ok && v.UsageKnown
		row.Available = 0
		if !row.UsageKnown {
			row.Note = "用量无法完整核对，请检查资源读取权限或连接"
		}
		if row.LimitKnown && row.UsageKnown {
			row.Available = math.Max(0, row.Limit-row.Used)
		}
	}
	return out
}
