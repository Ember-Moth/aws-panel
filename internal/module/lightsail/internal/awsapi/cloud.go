package awsapi

import (
	"context"
	"errors"
	"math"
	"net/url"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/aws-sdk-go-v2/aws/middleware"
	"github.com/aws/aws-sdk-go-v2/service/account"
	sdk "github.com/aws/aws-sdk-go-v2/service/lightsail"
	lt "github.com/aws/aws-sdk-go-v2/service/lightsail/types"
	"github.com/aws/aws-sdk-go-v2/service/servicequotas"
	"github.com/aws/smithy-go"
	"github.com/herta-network/aws-panel/internal/infra/clock"
	"github.com/herta-network/aws-panel/internal/module/lightsail/contract"
	"github.com/herta-network/aws-panel/internal/module/lightsail/internal/bootstrap"
	"github.com/herta-network/aws-panel/internal/module/lightsail/internal/source"
)

type Cloud struct{}

func (Cloud) Regions(ctx context.Context, cfg aws.Config) ([]contract.Region, error) {
	cfg.Region = "us-east-1"
	accountClient := account.NewFromConfig(cfg)
	states := map[string]string{}
	err := pages(ctx, func(token *string) (string, error) {
		out, err := accountClient.ListRegions(ctx, &account.ListRegionsInput{MaxResults: aws.Int32(50), NextToken: token})
		if err != nil {
			return "", err
		}
		for _, row := range out.Regions {
			states[aws.ToString(row.RegionName)] = string(row.RegionOptStatus)
		}
		return aws.ToString(out.NextToken), nil
	})
	if err != nil {
		return nil, err
	}
	out, err := sdk.NewFromConfig(cfg).GetRegions(ctx, &sdk.GetRegionsInput{IncludeAvailabilityZones: aws.Bool(true)})
	if err != nil {
		return nil, err
	}
	result := []contract.Region{}
	for _, row := range out.Regions {
		code := string(row.Name)
		if code == "" {
			return nil, contract.ErrResponse
		}
		state := states[code]
		if state == "" {
			state = "UNKNOWN"
		}
		zone := false
		for _, v := range row.AvailabilityZones {
			zone = zone || strings.EqualFold(aws.ToString(v.State), "available")
		}
		name := aws.ToString(row.DisplayName)
		if name == "" {
			name = code
		}
		result = append(result, contract.Region{Code: code, Name: name, State: state, Available: zone && (state == "ENABLED" || state == "ENABLED_BY_DEFAULT")})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Code < result[j].Code })
	return result, nil
}

func pages(ctx context.Context, fetch func(*string) (string, error)) error {
	var token *string
	seen := map[string]bool{}
	for range 256 {
		if err := ctx.Err(); err != nil {
			return err
		}
		next, err := fetch(token)
		if err != nil {
			return err
		}
		if next == "" {
			return nil
		}
		if seen[next] {
			return contract.ErrResponse
		}
		seen[next] = true
		token = aws.String(next)
	}
	return contract.ErrResponse
}
func (c Cloud) Catalogue(ctx context.Context, cfg aws.Config) (contract.Catalogue, error) {
	result, err := c.Environment(ctx, cfg)
	if err != nil {
		return result, err
	}
	products, err := c.Products(ctx, cfg)
	if err != nil {
		return result, err
	}
	result.Blueprints = products.Blueprints
	result.Bundles = products.Bundles
	result.CheckedAt = clock.Now()
	return result, nil
}
func (Cloud) Products(ctx context.Context, cfg aws.Config) (contract.Catalogue, error) {
	result := contract.Catalogue{Region: cfg.Region, Blueprints: []contract.Blueprint{}, Bundles: []contract.Bundle{}}
	client := sdk.NewFromConfig(cfg)
	var err error
	err = pages(ctx, func(token *string) (string, error) {
		out, err := client.GetBlueprints(ctx, &sdk.GetBlueprintsInput{IncludeInactive: aws.Bool(false), PageToken: token})
		if err != nil {
			return "", err
		}
		for _, value := range out.Blueprints {
			if !aws.ToBool(value.IsActive) || value.Platform != lt.InstancePlatformLinuxUnix || value.Type != lt.BlueprintTypeOs || value.AppCategory != "" {
				continue
			}
			license := aws.ToString(value.LicenseUrl)
			parsed, err := url.Parse(license)
			if err != nil || parsed.Scheme != "https" {
				license = ""
			}
			result.Blueprints = append(result.Blueprints, contract.Blueprint{ID: aws.ToString(value.BlueprintId), Name: aws.ToString(value.Name), Version: aws.ToString(value.Version), MinPower: aws.ToInt32(value.MinPower), LicenseURL: license, RootPasswordSupported: bootstrap.Supported(aws.ToString(value.BlueprintId))})
		}
		return aws.ToString(out.NextPageToken), nil
	})
	if err != nil {
		return result, err
	}
	err = pages(ctx, func(token *string) (string, error) {
		out, err := client.GetBundles(ctx, &sdk.GetBundlesInput{IncludeInactive: aws.Bool(false), PageToken: token})
		if err != nil {
			return "", err
		}
		for _, value := range out.Bundles {
			linux := false
			for _, platform := range value.SupportedPlatforms {
				linux = linux || platform == lt.InstancePlatformLinuxUnix
			}
			if !aws.ToBool(value.IsActive) || !linux || len(value.SupportedAppCategories) > 0 {
				continue
			}
			if value.Price == nil || value.CpuCount == nil || value.PublicIpv4AddressCount == nil {
				return "", contract.ErrResponse
			}
			price := float64(*value.Price)
			if math.IsNaN(price) || math.IsInf(price, 0) || price < 0 || price > 1e7 {
				return "", contract.ErrResponse
			}
			result.Bundles = append(result.Bundles, contract.Bundle{ID: aws.ToString(value.BundleId), Name: aws.ToString(value.Name), CPU: aws.ToInt32(value.CpuCount), RAMGB: aws.ToFloat32(value.RamSizeInGb), DiskGB: aws.ToInt32(value.DiskSizeInGb), Power: aws.ToInt32(value.Power), MonthlyCents: int64(math.Round(price * 100)), PublicIPv4: aws.ToInt32(value.PublicIpv4AddressCount) > 0})
		}
		return aws.ToString(out.NextPageToken), nil
	})
	if err != nil {
		return result, err
	}

	if len(result.Blueprints) == 0 || len(result.Bundles) == 0 {
		return result, contract.ErrCatalogue
	}
	sort.Slice(result.Blueprints, func(i, j int) bool { return result.Blueprints[i].Name < result.Blueprints[j].Name })
	sort.Slice(result.Bundles, func(i, j int) bool { return result.Bundles[i].MonthlyCents < result.Bundles[j].MonthlyCents })
	result.CheckedAt = clock.Now()
	return result, nil
}
func (Cloud) Environment(ctx context.Context, cfg aws.Config) (contract.Catalogue, error) {
	result := contract.Catalogue{Region: cfg.Region, Zones: []string{}, Blueprints: []contract.Blueprint{}, Bundles: []contract.Bundle{}, KeyPairs: []string{}}
	// Account Management uses the commercial partition's global endpoint.
	accountCfg := cfg
	accountCfg.Region = "us-east-1"
	status, err := account.NewFromConfig(accountCfg).GetRegionOptStatus(ctx, &account.GetRegionOptStatusInput{RegionName: aws.String(cfg.Region)})
	if err != nil {
		return result, err
	}
	if aws.ToString(status.RegionName) != cfg.Region || (string(status.RegionOptStatus) != "ENABLED" && string(status.RegionOptStatus) != "ENABLED_BY_DEFAULT") {
		return result, &contract.Error{Code: "REGION_NOT_READY", Message: "区域尚未启用或仍在准备，请先完成区域启用"}
	}
	client := sdk.NewFromConfig(cfg)
	regions, err := client.GetRegions(ctx, &sdk.GetRegionsInput{IncludeAvailabilityZones: aws.Bool(true)})
	if err != nil {
		return result, err
	}
	for _, region := range regions.Regions {
		if string(region.Name) == cfg.Region {
			for _, zone := range region.AvailabilityZones {
				if strings.EqualFold(aws.ToString(zone.State), "available") {
					result.Zones = append(result.Zones, aws.ToString(zone.ZoneName))
				}
			}
		}
	}
	if len(result.Zones) == 0 {
		return result, &contract.Error{Code: "REGION_NOT_READY", Message: "该账号区域没有可用的 Lightsail 可用区"}
	}
	err = pages(ctx, func(token *string) (string, error) {
		out, err := client.GetKeyPairs(ctx, &sdk.GetKeyPairsInput{PageToken: token})
		if err != nil {
			return "", err
		}
		for _, value := range out.KeyPairs {
			result.KeyPairs = append(result.KeyPairs, aws.ToString(value.Name))
		}
		return aws.ToString(out.NextPageToken), nil
	})
	if err != nil {
		return result, err
	}
	result.Quota = quota(ctx, cfg, client)
	sort.Strings(result.Zones)
	sort.Strings(result.KeyPairs)
	result.CheckedAt = clock.Now()
	return result, nil
}

func quota(ctx context.Context, cfg aws.Config, client *sdk.Client) contract.Quota {
	result := contract.Quota{Code: "QUOTA_UNAVAILABLE"}
	quotaClient := servicequotas.NewFromConfig(cfg)
	known := false
	err := pages(ctx, func(token *string) (string, error) {
		out, err := quotaClient.ListServiceQuotas(ctx, &servicequotas.ListServiceQuotasInput{ServiceCode: aws.String("lightsail"), MaxResults: aws.Int32(100), NextToken: token})
		if err != nil {
			return "", err
		}
		for _, value := range out.Quotas {
			description := strings.ToLower(aws.ToString(value.Description) + " " + aws.ToString(value.Unit))
			if strings.EqualFold(aws.ToString(value.QuotaName), "Instances") && (strings.Contains(description, "vcpu") || strings.Contains(description, "virtual cpu")) && value.Value != nil && !value.GlobalQuota {
				limit := *value.Value
				if !math.IsNaN(limit) && !math.IsInf(limit, 0) && limit >= 0 && limit <= 1e9 {
					result.LimitVCPU = int64(math.Floor(limit))
					known = true
				}
			}
		}
		return aws.ToString(out.NextToken), nil
	})
	if err != nil || !known {
		return result
	}
	err = pages(ctx, func(token *string) (string, error) {
		out, err := client.GetInstances(ctx, &sdk.GetInstancesInput{PageToken: token})
		if err != nil {
			return "", err
		}
		for _, value := range out.Instances {
			if value.State != nil && aws.ToString(value.State.Name) == "terminated" {
				continue
			}
			if value.Hardware == nil || value.Hardware.CpuCount == nil {
				return "", contract.ErrResponse
			}
			result.UsedVCPU += int64(*value.Hardware.CpuCount)
		}
		return aws.ToString(out.NextPageToken), nil
	})
	if err != nil {
		return result
	}
	result.Known = true
	result.Code = ""
	return result
}
func (Cloud) Instance(ctx context.Context, cfg aws.Config, name string) (*source.Instance, error) {
	out, err := sdk.NewFromConfig(cfg).GetInstance(ctx, &sdk.GetInstanceInput{InstanceName: aws.String(name)})
	if err != nil {
		var api smithy.APIError
		if errors.As(err, &api) && api.ErrorCode() == "NotFoundException" {
			return nil, nil
		}
		return nil, err
	}
	if out.Instance == nil {
		return nil, contract.ErrResponse
	}
	value := out.Instance
	parsed, err := arn.Parse(aws.ToString(value.Arn))
	if err != nil || parsed.Service != "lightsail" || parsed.Region != cfg.Region || !strings.HasPrefix(parsed.Resource, "Instance/") || aws.ToString(value.Name) != name || value.CreatedAt == nil || value.Location == nil || value.State == nil {
		return nil, contract.ErrResponse
	}
	result := &source.Instance{ARN: aws.ToString(value.Arn), Name: name, Region: string(value.Location.RegionName), Zone: aws.ToString(value.Location.AvailabilityZone), BlueprintID: aws.ToString(value.BlueprintId), BundleID: aws.ToString(value.BundleId), IPType: string(value.IpAddressType), KeyPairName: aws.ToString(value.SshKeyName), State: aws.ToString(value.State.Name), CreatedAt: *value.CreatedAt, Tags: map[string]string{}}
	result.RequestID, _ = middleware.GetRequestIDMetadata(out.ResultMetadata)
	for _, tag := range value.Tags {
		key := aws.ToString(tag.Key)
		if key == "aws-panel:step" || key == "aws-panel:token" || key == "aws-panel:spec" {
			result.Tags[key] = aws.ToString(tag.Value)
		}
	}
	return result, nil
}
func operation(value lt.Operation) source.Operation {
	result := source.Operation{ID: aws.ToString(value.Id), Name: aws.ToString(value.ResourceName), Type: string(value.OperationType), State: string(value.Status), Terminal: aws.ToBool(value.IsTerminal), Code: aws.ToString(value.ErrorCode)}
	if value.CreatedAt != nil {
		result.CreatedAt = *value.CreatedAt
	}
	if value.Location != nil {
		result.Region = string(value.Location.RegionName)
	}
	return result
}
func (Cloud) Create(ctx context.Context, cfg aws.Config, target contract.Target, tags map[string]string, userData string) (source.Created, error) {
	cfg.Retryer = func() aws.Retryer { return aws.NopRetryer{} }
	input := &sdk.CreateInstancesInput{InstanceNames: []string{target.Name}, AvailabilityZone: aws.String(target.Zone), BlueprintId: aws.String(target.BlueprintID), BundleId: aws.String(target.BundleID), IpAddressType: lt.IpAddressType(target.IPType)}
	if userData != "" {
		input.UserData = aws.String(userData)
	}
	if target.KeyPairName != "" {
		input.KeyPairName = aws.String(target.KeyPairName)
	}
	keys := []string{"aws-panel:step", "aws-panel:token", "aws-panel:spec"}
	for _, key := range keys {
		input.Tags = append(input.Tags, lt.Tag{Key: aws.String(key), Value: aws.String(tags[key])})
	}
	out, err := sdk.NewFromConfig(cfg).CreateInstances(ctx, input)
	if err != nil {
		return source.Created{}, err
	}
	result := source.Created{Operations: []source.Operation{}}
	result.RequestID, _ = middleware.GetRequestIDMetadata(out.ResultMetadata)
	for _, value := range out.Operations {
		result.Operations = append(result.Operations, operation(value))
	}
	return result, nil
}
func (Cloud) Operations(ctx context.Context, cfg aws.Config, name string) ([]source.Operation, error) {
	result := []source.Operation{}
	client := sdk.NewFromConfig(cfg)
	err := pages(ctx, func(token *string) (string, error) {
		out, err := client.GetOperationsForResource(ctx, &sdk.GetOperationsForResourceInput{ResourceName: aws.String(name), PageToken: token})
		if err != nil {
			var api smithy.APIError
			if errors.As(err, &api) && api.ErrorCode() == "NotFoundException" {
				return "", nil
			}
			return "", err
		}
		for _, value := range out.Operations {
			result = append(result, operation(value))
		}
		if len(result) > 5000 {
			return "", contract.ErrResponse
		}
		return aws.ToString(out.NextPageToken), nil
	})
	return result, err
}
