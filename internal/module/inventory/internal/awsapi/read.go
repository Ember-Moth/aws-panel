package awsapi

import (
	"context"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Ember-Moth/aws-panel/internal/infra/clock"
	"github.com/Ember-Moth/aws-panel/internal/module/inventory/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/inventory/internal/scope"
	"github.com/Ember-Moth/aws-panel/internal/module/inventory/internal/source"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/aws-sdk-go-v2/aws/middleware"
	"github.com/aws/aws-sdk-go-v2/service/account"
	"github.com/aws/aws-sdk-go-v2/service/lightsail"
	lt "github.com/aws/aws-sdk-go-v2/service/lightsail/types"
)

type Cloud struct{}

func (Cloud) Fetch(ctx context.Context, cfg aws.Config, req source.Request) (source.Snapshot, error) {
	if req.Partition != "aws" {
		return source.Snapshot{}, contract.ErrPartition
	}
	switch req.Kind {
	case contract.AccountRegion:
		return regions(ctx, cfg)
	case contract.LightsailRegion:
		return lightsailRegions(ctx, cfg)
	case contract.LightsailInstance:
		return instances(ctx, cfg, req)
	case contract.EC2Instance, contract.EC2Address, contract.LightsailStaticIP:
		return computeResources(ctx, cfg, req)
	default:
		return source.Snapshot{}, contract.ErrInvalid
	}
}
func safeName(value string, limit int) bool {
	return value != "" && utf8.ValidString(value) && len(value) <= limit && strings.IndexFunc(value, unicode.IsControl) < 0
}
func regions(ctx context.Context, cfg aws.Config) (source.Snapshot, error) {
	client := account.NewFromConfig(cfg)
	result := source.Snapshot{Resources: []source.Resource{}}
	var token *string
	seen := map[string]bool{}
	for result.Pages < 256 {
		// Omit AccountId: every connection already belongs to the desired
		// account; organization/delegated-administrator access is not assumed.
		response, err := client.ListRegions(ctx, &account.ListRegionsInput{MaxResults: aws.Int32(50), NextToken: token})
		if err != nil {
			return source.Snapshot{}, err
		}
		result.Pages++
		result.RequestID, _ = middleware.GetRequestIDMetadata(response.ResultMetadata)
		now := clock.Now()
		for _, value := range response.Regions {
			name := aws.ToString(value.RegionName)
			if !scope.RegionOK(name) {
				return source.Snapshot{}, contract.ErrResponse
			}
			status := string(value.RegionOptStatus)
			if !safeName(status, 80) {
				return source.Snapshot{}, contract.ErrResponse
			}
			result.Resources = append(result.Resources, source.Resource{ID: name, Name: name, Region: name, State: status, Details: contract.Details{Region: &contract.RegionDetails{OptStatus: status}}, ObservedAt: now})
		}
		next := aws.ToString(response.NextToken)
		if next == "" {
			if len(result.Resources) == 0 {
				return source.Snapshot{}, contract.ErrResponse
			}
			return result, nil
		}
		if seen[next] {
			return source.Snapshot{}, contract.ErrIncomplete
		}
		seen[next] = true
		token = aws.String(next)
	}
	return source.Snapshot{}, contract.ErrIncomplete
}
func lightsailRegions(ctx context.Context, cfg aws.Config) (source.Snapshot, error) {
	response, err := lightsail.NewFromConfig(cfg).GetRegions(ctx, &lightsail.GetRegionsInput{IncludeAvailabilityZones: aws.Bool(true)})
	if err != nil {
		return source.Snapshot{}, err
	}
	result := source.Snapshot{Resources: []source.Resource{}, Pages: 1}
	result.RequestID, _ = middleware.GetRequestIDMetadata(response.ResultMetadata)
	now := clock.Now()
	for _, value := range response.Regions {
		name := string(value.Name)
		if !scope.RegionOK(name) {
			return source.Snapshot{}, contract.ErrResponse
		}
		display := aws.ToString(value.DisplayName)
		if display == "" {
			display = name
		}
		if !safeName(display, 160) {
			return source.Snapshot{}, contract.ErrResponse
		}
		details := &contract.RegionDetails{DisplayName: display, AvailabilityZones: []contract.Zone{}}
		for _, zone := range value.AvailabilityZones {
			name := aws.ToString(zone.ZoneName)
			if !safeName(name, 80) {
				return source.Snapshot{}, contract.ErrResponse
			}
			details.AvailabilityZones = append(details.AvailabilityZones, contract.Zone{Name: name, State: aws.ToString(zone.State)})
		}
		result.Resources = append(result.Resources, source.Resource{ID: name, Name: display, Region: name, State: "available", Details: contract.Details{Region: details}, ObservedAt: now})
	}
	if len(result.Resources) == 0 {
		return source.Snapshot{}, contract.ErrResponse
	}
	return result, nil
}
func instances(ctx context.Context, cfg aws.Config, req source.Request) (source.Snapshot, error) {
	client := lightsail.NewFromConfig(cfg)
	result := source.Snapshot{Resources: []source.Resource{}}
	var token *string
	seen := map[string]bool{}
	for result.Pages < 256 {
		response, err := client.GetInstances(ctx, &lightsail.GetInstancesInput{PageToken: token})
		if err != nil {
			return source.Snapshot{}, err
		}
		result.Pages++
		result.RequestID, _ = middleware.GetRequestIDMetadata(response.ResultMetadata)
		now := clock.Now()
		for _, value := range response.Instances {
			resource, err := instance(value, req, now)
			if err != nil {
				return source.Snapshot{}, err
			}
			result.Resources = append(result.Resources, resource)
		}
		if len(result.Resources) > 20000 {
			return source.Snapshot{}, contract.ErrIncomplete
		}
		next := aws.ToString(response.NextPageToken)
		if next == "" {
			return result, nil
		}
		if seen[next] {
			return source.Snapshot{}, contract.ErrIncomplete
		}
		seen[next] = true
		token = aws.String(next)
	}
	return source.Snapshot{}, contract.ErrIncomplete
}
func instance(value lt.Instance, req source.Request, observed time.Time) (source.Resource, error) {
	resourceID, name := aws.ToString(value.Arn), aws.ToString(value.Name)
	parsed, err := arn.Parse(resourceID)
	if err != nil || parsed.Service != "lightsail" || parsed.Partition != req.Partition || parsed.AccountID != req.AWSAccountID || parsed.Region != req.Region || !strings.HasPrefix(parsed.Resource, "Instance/") || !safeName(name, 255) {
		return source.Resource{}, contract.ErrResponse
	}
	details := &contract.InstanceDetails{BlueprintID: aws.ToString(value.BlueprintId), BlueprintName: aws.ToString(value.BlueprintName), BundleID: aws.ToString(value.BundleId), IPType: string(value.IpAddressType), PublicIP: aws.ToString(value.PublicIpAddress), PrivateIP: aws.ToString(value.PrivateIpAddress), IPv6: value.Ipv6Addresses, StaticIP: aws.ToBool(value.IsStaticIp), Username: aws.ToString(value.Username), SSHKeyName: aws.ToString(value.SshKeyName)}
	if value.CreatedAt != nil {
		created := clock.In(*value.CreatedAt)
		details.CreatedAt = &created
	}
	if value.Location != nil {
		if string(value.Location.RegionName) != req.Region {
			return source.Resource{}, contract.ErrResponse
		}
		details.AvailabilityZone = aws.ToString(value.Location.AvailabilityZone)
	}
	if value.Hardware != nil {
		details.CPU = aws.ToInt32(value.Hardware.CpuCount)
		details.RAMGB = aws.ToFloat32(value.Hardware.RamSizeInGb)
		for _, disk := range value.Hardware.Disks {
			details.Disks = append(details.Disks, contract.Disk{SizeGB: aws.ToInt32(disk.SizeInGb), System: aws.ToBool(disk.IsSystemDisk)})
		}
	}
	if value.Networking != nil {
		for _, port := range value.Networking.Ports {
			details.Ports = append(details.Ports, contract.Port{Protocol: string(port.Protocol), From: port.FromPort, To: port.ToPort, CIDRs: port.Cidrs, IPv6CIDRs: port.Ipv6Cidrs})
		}
	}
	state := "unknown"
	if value.State != nil && aws.ToString(value.State.Name) != "" {
		state = aws.ToString(value.State.Name)
	}
	if !safeName(state, 80) {
		return source.Resource{}, contract.ErrResponse
	}
	return source.Resource{ID: resourceID, Name: name, Region: req.Region, State: state, Details: contract.Details{Instance: details}, ObservedAt: observed}, nil
}
