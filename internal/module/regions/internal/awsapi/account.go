package awsapi

import (
	"context"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/middleware"
	"github.com/aws/aws-sdk-go-v2/service/account"
	"github.com/herta-network/aws-panel/internal/module/regions/contract"
	"github.com/herta-network/aws-panel/internal/module/regions/internal/source"
	"sort"
)

type Cloud struct{}

func (Cloud) List(ctx context.Context, cfg aws.Config) ([]source.State, error) {
	cfg.Region = "us-east-1"
	client := account.NewFromConfig(cfg)
	result := []source.State{}
	seen, codes := map[string]bool{}, map[string]bool{}
	var token *string
	for range 256 {
		response, err := client.ListRegions(ctx, &account.ListRegionsInput{MaxResults: aws.Int32(50), NextToken: token})
		if err != nil {
			return nil, err
		}
		for _, row := range response.Regions {
			code, status := aws.ToString(row.RegionName), string(row.RegionOptStatus)
			if code == "" || codes[code] {
				return nil, contract.ErrResponse
			}
			codes[code] = true
			switch status {
			case "ENABLED", "ENABLED_BY_DEFAULT", "ENABLING", "DISABLED", "DISABLING":
			default:
				return nil, contract.ErrResponse
			}
			result = append(result, source.State{Region: code, Status: status})
		}
		next := aws.ToString(response.NextToken)
		if next == "" {
			sort.Slice(result, func(i, j int) bool { return result[i].Region < result[j].Region })
			return result, nil
		}
		if seen[next] {
			return nil, contract.ErrResponse
		}
		seen[next] = true
		token = aws.String(next)
	}
	return nil, contract.ErrResponse
}

func (Cloud) Status(ctx context.Context, cfg aws.Config, region string) (source.State, error) {
	response, err := account.NewFromConfig(cfg).GetRegionOptStatus(ctx, &account.GetRegionOptStatusInput{RegionName: aws.String(region)})
	if err != nil {
		return source.State{}, err
	}
	value := source.State{Region: aws.ToString(response.RegionName), Status: string(response.RegionOptStatus)}
	value.RequestID, _ = middleware.GetRequestIDMetadata(response.ResultMetadata)
	if value.Region != region {
		return source.State{}, contract.ErrResponse
	}
	switch value.Status {
	case "ENABLED", "ENABLED_BY_DEFAULT", "ENABLING", "DISABLED", "DISABLING":
	default:
		return source.State{}, contract.ErrResponse
	}
	return value, nil
}
func (Cloud) Enable(ctx context.Context, cfg aws.Config, region string) (string, error) {
	cfg.Retryer = func() aws.Retryer { return aws.NopRetryer{} }
	response, err := account.NewFromConfig(cfg).EnableRegion(ctx, &account.EnableRegionInput{RegionName: aws.String(region)})
	if err != nil {
		return "", err
	}
	id, _ := middleware.GetRequestIDMetadata(response.ResultMetadata)
	return id, nil
}
