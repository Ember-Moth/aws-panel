package quotas

import (
	"context"
	"github.com/herta-network/aws-panel/internal/module/quotas/contract"
	"github.com/herta-network/aws-panel/internal/module/quotas/internal/awsapi"
	"github.com/herta-network/aws-panel/internal/module/quotas/internal/service"
)

type Deps = service.Deps
type Cloud = service.Cloud
type Service interface {
	Options(context.Context, int64) (contract.Options, error)
	Regions(context.Context, int64, string) (contract.Regions, error)
	Read(context.Context, int64, contract.Query) (contract.Report, error)
}

func New(deps Deps) Service {
	if deps.Cloud == nil {
		deps.Cloud = awsapi.Cloud{}
	}
	return service.New(deps)
}
func NewCloud() Cloud                   { return awsapi.Cloud{} }
func InstanceClass(value string) string { return awsapi.InstanceClass(value) }
func Check(report contract.Report, key string, additional float64) error {
	for _, entry := range report.Entries {
		if entry.Key == key && entry.LimitKnown && entry.UsageKnown && entry.Used+additional > entry.Limit {
			return contract.ErrInsufficient
		}
	}
	return nil
}
