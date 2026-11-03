package awsapi

import (
	"context"
	"fmt"
	"github.com/Ember-Moth/aws-panel/internal/infra/clock"
	"github.com/Ember-Moth/aws-panel/internal/module/quotas/contract"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/servicequotas"
	qt "github.com/aws/aws-sdk-go-v2/service/servicequotas/types"
	"math"
	"sort"
	"strings"
)

func bedrockUnit(name, unit string) string {
	if unit != "" && !strings.EqualFold(unit, "none") {
		return unit
	}
	name = strings.ToLower(name)
	for _, p := range []struct{ needle, label string }{{"tokens per minute", "tokens/分钟"}, {"tokens per day", "tokens/天"}, {"requests per minute", "请求/分钟"}, {"requests per second", "请求/秒"}, {"requests per day", "请求/天"}} {
		if strings.Contains(name, p.needle) {
			return p.label
		}
	}
	return ""
}
func bedrock(ctx context.Context, cfg aws.Config) (contract.Report, error) {
	result := contract.Report{Region: cfg.Region, Service: "bedrock", Entries: []contract.Entry{}, Warnings: []string{"Bedrock 配额按模型、调用方式和区域分别生效；这里只读取账号级适用配额，不调用模型。", "Service Quotas 不提供实时 token/请求用量，用量与剩余额度保持未知。"}, CheckedAt: clock.Now()}
	seen := map[string]bool{}
	client := servicequotas.NewFromConfig(cfg)
	err := pages(ctx, func(token *string) (string, error) {
		response, err := client.ListServiceQuotas(ctx, &servicequotas.ListServiceQuotasInput{ServiceCode: aws.String("bedrock"), QuotaAppliedAtLevel: qt.AppliedLevelEnumAccount, MaxResults: aws.Int32(100), NextToken: token})
		if err != nil {
			return "", err
		}
		for _, q := range response.Quotas {
			if q.QuotaAppliedAtLevel != "" && q.QuotaAppliedAtLevel != qt.AppliedLevelEnumAccount {
				continue
			}
			code, name := aws.ToString(q.QuotaCode), aws.ToString(q.QuotaName)
			if code == "" || name == "" || len(name) > 1024 || len(code) > 128 || seen[code] {
				return "", contract.ErrResponse
			}
			seen[code] = true
			entry := contract.Entry{Key: "bedrock." + code, Name: name, QuotaCode: code, Unit: bedrockUnit(name, aws.ToString(q.Unit)), Global: q.GlobalQuota, Adjustable: q.Adjustable, Note: "实时用量未采集"}
			if q.Period != nil {
				entry.Period = fmt.Sprintf("%d %s", aws.ToInt32(q.Period.PeriodValue), string(q.Period.PeriodUnit))
			}
			if q.Value != nil && q.ErrorReason == nil {
				value := *q.Value
				if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1e15 {
					return "", contract.ErrResponse
				}
				entry.Limit = value
				entry.LimitKnown = true
			} else {
				entry.Note = "AWS 未返回可确认的适用配额"
			}
			result.Entries = append(result.Entries, entry)
			if len(result.Entries) > 5000 {
				return "", contract.ErrResponse
			}
		}
		return aws.ToString(response.NextToken), nil
	})
	result.LimitsRead = err == nil
	if err != nil {
		result.Entries = []contract.Entry{}
		result.Warnings = append(result.Warnings, "未能完整读取 Bedrock 适用配额，请检查 Service Quotas 权限、服务区域及连接。")
	}
	sort.Slice(result.Entries, func(i, j int) bool { return result.Entries[i].Name < result.Entries[j].Name })
	return result, nil
}
