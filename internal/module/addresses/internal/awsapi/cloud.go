package awsapi

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"github.com/Ember-Moth/aws-panel/internal/infra/clock"
	"github.com/Ember-Moth/aws-panel/internal/module/addresses/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/addresses/internal/source"
	"github.com/Ember-Moth/aws-panel/internal/module/quotas"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/aws-sdk-go-v2/aws/middleware"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	et "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/lightsail"
	lt "github.com/aws/aws-sdk-go-v2/service/lightsail/types"
	"github.com/aws/smithy-go"
)

type Cloud struct{}

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
func absent(err error) bool {
	var api smithy.APIError
	if !errors.As(err, &api) {
		return false
	}
	switch api.ErrorCode() {
	case "NotFoundException", "InvalidAllocationID.NotFound", "InvalidAddress.NotFound", "InvalidInstanceID.NotFound":
		return true
	}
	return false
}
func noRetry(cfg aws.Config) aws.Config {
	cfg.Retryer = func() aws.Retryer { return aws.NopRetryer{} }
	return cfg
}
func staticIP(value lt.StaticIp, region, account string) (contract.Address, error) {
	parsed, err := arn.Parse(aws.ToString(value.Arn))
	ip, ipErr := netip.ParseAddr(aws.ToString(value.IpAddress))
	if err != nil || parsed.Partition != "aws" || parsed.Service != "lightsail" || parsed.Region != region || parsed.AccountID != account || !strings.HasPrefix(parsed.Resource, "StaticIp/") || ipErr != nil || !ip.Is4() || value.Location == nil || string(value.Location.RegionName) != region || value.CreatedAt == nil || aws.ToString(value.Name) == "" || value.IsAttached == nil {
		return contract.Address{}, contract.ErrResponse
	}
	attached := aws.ToBool(value.IsAttached)
	instance := aws.ToString(value.AttachedTo)
	if attached != (instance != "") {
		return contract.Address{}, contract.ErrResponse
	}
	return contract.Address{ID: aws.ToString(value.Name), Name: aws.ToString(value.Name), ARN: aws.ToString(value.Arn), IP: ip.String(), InstanceID: instance, Attached: attached, CreatedAt: value.CreatedAt}, nil
}
func elasticIP(value et.Address, region, account string) (contract.Address, error) {
	id := aws.ToString(value.AllocationId)
	addressIP := aws.ToString(value.PublicIp)
	kind := "ec2"
	if aws.ToString(value.CarrierIp) != "" {
		addressIP = aws.ToString(value.CarrierIp)
		kind = "wavelength"
	}
	ip, err := netip.ParseAddr(addressIP)
	if id == "" || !strings.HasPrefix(id, "eipalloc-") || err != nil || !ip.Is4() || value.Domain != et.DomainTypeVpc {
		return contract.Address{}, contract.ErrResponse
	}
	tags := map[string]string{}
	name := id
	for _, tag := range value.Tags {
		k, v := aws.ToString(tag.Key), aws.ToString(tag.Value)
		if k == "Name" {
			name = v
		}
		if strings.HasPrefix(k, "aws-panel:") {
			tags[k] = v
		}
	}
	return contract.Address{Kind: kind, ID: id, Name: name, ARN: fmt.Sprintf("arn:aws:ec2:%s:%s:elastic-ip/%s", region, account, id), IP: ip.String(), InstanceID: aws.ToString(value.InstanceId), NetworkInterfaceID: aws.ToString(value.NetworkInterfaceId), PrivateIP: aws.ToString(value.PrivateIpAddress), AssociationID: aws.ToString(value.AssociationId), BorderGroup: aws.ToString(value.NetworkBorderGroup), Attached: aws.ToString(value.AssociationId) != "" || aws.ToString(value.NetworkInterfaceId) != "", Tags: tags}, nil
}
func lsInstance(value lt.Instance, region, account string) (contract.Instance, error) {
	parsed, err := arn.Parse(aws.ToString(value.Arn))
	if err != nil || parsed.Partition != "aws" || parsed.Service != "lightsail" || parsed.Region != region || parsed.AccountID != account || !strings.HasPrefix(parsed.Resource, "Instance/") || value.State == nil || value.Location == nil || string(value.Location.RegionName) != region || aws.ToString(value.Name) == "" {
		return contract.Instance{}, contract.ErrResponse
	}
	return contract.Instance{ID: aws.ToString(value.Name), Name: aws.ToString(value.Name), ARN: aws.ToString(value.Arn), State: aws.ToString(value.State.Name), IPType: string(value.IpAddressType), PublicIP: aws.ToString(value.PublicIpAddress), PrivateIP: aws.ToString(value.PrivateIpAddress)}, nil
}
func ec2Instance(value et.Instance, region, account string) (contract.Instance, error) {
	id := aws.ToString(value.InstanceId)
	if id == "" || !strings.HasPrefix(id, "i-") || value.State == nil {
		return contract.Instance{}, contract.ErrResponse
	}
	name := id
	for _, tag := range value.Tags {
		if aws.ToString(tag.Key) == "Name" && aws.ToString(tag.Value) != "" {
			name = aws.ToString(tag.Value)
		}
	}
	result := contract.Instance{ID: id, Name: name, ARN: fmt.Sprintf("arn:aws:ec2:%s:%s:instance/%s", region, account, id), State: string(value.State.Name), PublicIP: aws.ToString(value.PublicIpAddress), PrivateIP: aws.ToString(value.PrivateIpAddress), IPType: "ipv4", BorderGroup: region}
	for _, nic := range value.NetworkInterfaces {
		if nic.Attachment != nil && aws.ToInt32(nic.Attachment.DeviceIndex) == 0 {
			result.NetworkInterfaceID = aws.ToString(nic.NetworkInterfaceId)
			if nic.Association != nil && aws.ToString(nic.Association.CarrierIp) != "" {
				result.PublicIP = aws.ToString(nic.Association.CarrierIp)
			}
			if len(nic.Ipv6Addresses) > 0 {
				result.IPType = "dualstack"
			}
			break
		}
	}
	if value.Placement != nil && aws.ToString(value.Placement.AvailabilityZone) != "" {
		zone := aws.ToString(value.Placement.AvailabilityZone)
		result.Zone = zone
		if len(zone) != len(region)+1 || !strings.HasPrefix(zone, region) {
			result.BorderGroup = ""
		}
	}
	return result, nil
}
func (Cloud) Catalogue(ctx context.Context, cfg aws.Config, kind, account string) (contract.Catalogue, error) {
	result := contract.Catalogue{Region: cfg.Region, Service: kind, AWSAccountID: account, Addresses: []contract.Address{}, Instances: []contract.Instance{}}
	switch kind {
	case "lightsail":
		client := lightsail.NewFromConfig(cfg)
		err := pages(ctx, func(token *string) (string, error) {
			out, err := client.GetStaticIps(ctx, &lightsail.GetStaticIpsInput{PageToken: token})
			if err != nil {
				return "", err
			}
			for _, row := range out.StaticIps {
				value, err := staticIP(row, cfg.Region, account)
				if err != nil {
					return "", err
				}
				result.Addresses = append(result.Addresses, value)
			}
			return aws.ToString(out.NextPageToken), nil
		})
		if err != nil {
			return result, err
		}
		err = pages(ctx, func(token *string) (string, error) {
			out, err := client.GetInstances(ctx, &lightsail.GetInstancesInput{PageToken: token})
			if err != nil {
				return "", err
			}
			for _, row := range out.Instances {
				value, err := lsInstance(row, cfg.Region, account)
				if err != nil {
					return "", err
				}
				result.Instances = append(result.Instances, value)
			}
			return aws.ToString(out.NextPageToken), nil
		})
		if err != nil {
			return result, err
		}
	case "ec2", "wavelength":
		client := ec2.NewFromConfig(cfg)
		zoneMap := map[string]contract.WavelengthZone{}
		if kind == "wavelength" {
			zones, err := client.DescribeAvailabilityZones(ctx, &ec2.DescribeAvailabilityZonesInput{AllAvailabilityZones: aws.Bool(true), Filters: []et.Filter{{Name: aws.String("zone-type"), Values: []string{"wavelength-zone"}}}})
			if err != nil {
				return result, err
			}
			for _, z := range zones.AvailabilityZones {
				if aws.ToString(z.ZoneType) != "wavelength-zone" || aws.ToString(z.RegionName) != cfg.Region || aws.ToString(z.ZoneId) == "" || aws.ToString(z.NetworkBorderGroup) == "" || aws.ToString(z.ZoneName) == "" {
					return result, contract.ErrResponse
				}
				value := contract.WavelengthZone{ID: aws.ToString(z.ZoneId), Name: aws.ToString(z.ZoneName), BorderGroup: aws.ToString(z.NetworkBorderGroup)}
				zoneMap[value.Name] = value
				if z.OptInStatus == et.AvailabilityZoneOptInStatusOptedIn && z.State == et.AvailabilityZoneStateAvailable {
					result.WavelengthZones = append(result.WavelengthZones, value)
				}
			}
		}
		out, err := client.DescribeAddresses(ctx, &ec2.DescribeAddressesInput{})
		if err != nil {
			return result, err
		}
		for _, row := range out.Addresses {
			if row.Domain != et.DomainTypeVpc || (kind == "wavelength") != (aws.ToString(row.CarrierIp) != "") || aws.ToString(row.CustomerOwnedIp) != "" {
				continue
			}
			value, err := elasticIP(row, cfg.Region, account)
			if err != nil {
				return result, err
			}
			result.Addresses = append(result.Addresses, value)
		}
		err = pages(ctx, func(token *string) (string, error) {
			out, err := client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{NextToken: token, MaxResults: aws.Int32(1000)})
			if err != nil {
				return "", err
			}
			for _, reservation := range out.Reservations {
				if owner := aws.ToString(reservation.OwnerId); owner != "" && owner != account {
					return "", contract.ErrResponse
				}
				for _, row := range reservation.Instances {
					value, err := ec2Instance(row, cfg.Region, account)
					if err != nil {
						return "", err
					}
					if kind == "wavelength" {
						z, ok := zoneMap[value.Zone]
						if !ok {
							continue
						}
						value.WavelengthZoneID = z.ID
						value.BorderGroup = z.BorderGroup
					} else if value.BorderGroup == "" {
						continue
					}
					if value.State != "terminated" {
						result.Instances = append(result.Instances, value)
					}
				}
			}
			return aws.ToString(out.NextToken), nil
		})
		if err != nil {
			return result, err
		}
	default:
		return result, contract.ErrInvalid
	}
	for i := range result.Instances {
		for _, address := range result.Addresses {
			if address.InstanceID == result.Instances[i].ID && address.Attached && (kind == "lightsail" || (address.NetworkInterfaceID == result.Instances[i].NetworkInterfaceID && address.PrivateIP == result.Instances[i].PrivateIP)) {
				result.Instances[i].AddressID = address.ID
			}
		}
	}
	sort.Slice(result.Addresses, func(i, j int) bool { return result.Addresses[i].Name < result.Addresses[j].Name })
	sort.Slice(result.Instances, func(i, j int) bool { return result.Instances[i].Name < result.Instances[j].Name })
	result.CheckedAt = clock.Now()
	return result, nil
}
func (Cloud) Address(ctx context.Context, cfg aws.Config, kind, account, id string) (*contract.Address, error) {
	if kind == "lightsail" {
		out, err := lightsail.NewFromConfig(cfg).GetStaticIp(ctx, &lightsail.GetStaticIpInput{StaticIpName: aws.String(id)})
		if absent(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if out.StaticIp == nil {
			return nil, contract.ErrResponse
		}
		value, err := staticIP(*out.StaticIp, cfg.Region, account)
		return &value, err
	}
	out, err := ec2.NewFromConfig(cfg).DescribeAddresses(ctx, &ec2.DescribeAddressesInput{AllocationIds: []string{id}})
	if absent(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(out.Addresses) == 0 {
		return nil, nil
	}
	if len(out.Addresses) != 1 {
		return nil, contract.ErrResponse
	}
	value, err := elasticIP(out.Addresses[0], cfg.Region, account)
	if err == nil && value.Kind != kind {
		return nil, contract.ErrUnsupported
	}
	return &value, err
}
func (c Cloud) Instance(ctx context.Context, cfg aws.Config, kind, account, id string) (*contract.Instance, error) {
	catalogue, err := c.Catalogue(ctx, cfg, kind, account)
	if err != nil {
		return nil, err
	}
	for _, row := range catalogue.Instances {
		if row.ID == id {
			return &row, nil
		}
	}
	return nil, nil
}
func (c Cloud) FindAllocated(ctx context.Context, cfg aws.Config, kind, account, name string, tags map[string]string) ([]contract.Address, error) {
	if kind == "lightsail" {
		value, err := c.Address(ctx, cfg, kind, account, name)
		if err != nil {
			return nil, err
		}
		if value == nil {
			return []contract.Address{}, nil
		}
		return []contract.Address{*value}, nil
	}
	filters := []et.Filter{}
	for _, key := range []string{"aws-panel:step", "aws-panel:token", "aws-panel:spec"} {
		filters = append(filters, et.Filter{Name: aws.String("tag:" + key), Values: []string{tags[key]}})
	}
	out, err := ec2.NewFromConfig(cfg).DescribeAddresses(ctx, &ec2.DescribeAddressesInput{Filters: filters})
	if err != nil {
		return nil, err
	}
	result := []contract.Address{}
	for _, row := range out.Addresses {
		value, err := elasticIP(row, cfg.Region, account)
		if err != nil {
			return nil, err
		}
		if value.Kind != kind {
			return nil, contract.ErrUnsupported
		}
		result = append(result, value)
	}
	return result, nil
}
func lsAccepted(operations []lt.Operation, requestID string) source.Accepted {
	result := source.Accepted{RequestID: requestID, Operations: []source.Operation{}}
	for _, value := range operations {
		row := source.Operation{ID: aws.ToString(value.Id), Name: aws.ToString(value.ResourceName), Type: string(value.OperationType), State: string(value.Status), Terminal: aws.ToBool(value.IsTerminal)}
		if value.Location != nil {
			row.Region = string(value.Location.RegionName)
		}
		if value.CreatedAt != nil {
			row.CreatedAt = *value.CreatedAt
		}
		result.Operations = append(result.Operations, row)
	}
	return result
}
func (Cloud) History(ctx context.Context, cfg aws.Config, kind, name string) ([]source.Operation, error) {
	if kind != "lightsail" {
		return []source.Operation{}, nil
	}
	result := []source.Operation{}
	client := lightsail.NewFromConfig(cfg)
	err := pages(ctx, func(token *string) (string, error) {
		out, err := client.GetOperationsForResource(ctx, &lightsail.GetOperationsForResourceInput{ResourceName: aws.String(name), PageToken: token})
		if absent(err) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		result = append(result, lsAccepted(out.Operations, "").Operations...)
		return aws.ToString(out.NextPageToken), nil
	})
	return result, err
}
func (Cloud) Operation(ctx context.Context, cfg aws.Config, kind, id string) (*source.Operation, error) {
	if kind != "lightsail" {
		return nil, contract.ErrInvalid
	}
	out, err := lightsail.NewFromConfig(cfg).GetOperation(ctx, &lightsail.GetOperationInput{OperationId: aws.String(id)})
	if absent(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if out.Operation == nil {
		return nil, contract.ErrResponse
	}
	rows := lsAccepted([]lt.Operation{*out.Operation}, "").Operations
	return &rows[0], nil
}
func (Cloud) Allocate(ctx context.Context, cfg aws.Config, kind, name, border string, tags map[string]string) (source.Accepted, error) {
	cfg = noRetry(cfg)
	if kind == "lightsail" {
		out, err := lightsail.NewFromConfig(cfg).AllocateStaticIp(ctx, &lightsail.AllocateStaticIpInput{StaticIpName: aws.String(name)})
		if err != nil {
			return source.Accepted{}, err
		}
		id, _ := middleware.GetRequestIDMetadata(out.ResultMetadata)
		return lsAccepted(out.Operations, id), nil
	}
	values := []et.Tag{{Key: aws.String("Name"), Value: aws.String(name)}}
	for key, value := range tags {
		values = append(values, et.Tag{Key: aws.String(key), Value: aws.String(value)})
	}
	sort.Slice(values, func(i, j int) bool { return aws.ToString(values[i].Key) < aws.ToString(values[j].Key) })
	out, err := ec2.NewFromConfig(cfg).AllocateAddress(ctx, &ec2.AllocateAddressInput{Domain: et.DomainTypeVpc, NetworkBorderGroup: aws.String(border), TagSpecifications: []et.TagSpecification{{ResourceType: et.ResourceTypeElasticIp, Tags: values}}})
	if err != nil {
		return source.Accepted{}, err
	}
	id, _ := middleware.GetRequestIDMetadata(out.ResultMetadata)
	return source.Accepted{ResourceID: aws.ToString(out.AllocationId), RequestID: id}, nil
}
func (Cloud) Attach(ctx context.Context, cfg aws.Config, kind string, address contract.Address, instance contract.Instance) (source.Accepted, error) {
	cfg = noRetry(cfg)
	if kind == "lightsail" {
		out, err := lightsail.NewFromConfig(cfg).AttachStaticIp(ctx, &lightsail.AttachStaticIpInput{StaticIpName: aws.String(address.ID), InstanceName: aws.String(instance.ID)})
		if err != nil {
			return source.Accepted{}, err
		}
		id, _ := middleware.GetRequestIDMetadata(out.ResultMetadata)
		return lsAccepted(out.Operations, id), nil
	}
	if address.Kind != kind || address.BorderGroup != instance.BorderGroup || address.BorderGroup == "" {
		return source.Accepted{}, contract.ErrUnsupported
	}
	out, err := ec2.NewFromConfig(cfg).AssociateAddress(ctx, &ec2.AssociateAddressInput{AllocationId: aws.String(address.ID), NetworkInterfaceId: aws.String(instance.NetworkInterfaceID), PrivateIpAddress: aws.String(instance.PrivateIP), AllowReassociation: aws.Bool(false)})
	if err != nil {
		return source.Accepted{}, err
	}
	id, _ := middleware.GetRequestIDMetadata(out.ResultMetadata)
	return source.Accepted{ResourceID: address.ID, RequestID: id}, nil
}
func (Cloud) Detach(ctx context.Context, cfg aws.Config, kind string, address contract.Address) (source.Accepted, error) {
	cfg = noRetry(cfg)
	if kind == "lightsail" {
		out, err := lightsail.NewFromConfig(cfg).DetachStaticIp(ctx, &lightsail.DetachStaticIpInput{StaticIpName: aws.String(address.ID)})
		if err != nil {
			return source.Accepted{}, err
		}
		id, _ := middleware.GetRequestIDMetadata(out.ResultMetadata)
		return lsAccepted(out.Operations, id), nil
	}
	if address.Kind != kind {
		return source.Accepted{}, contract.ErrUnsupported
	}
	out, err := ec2.NewFromConfig(cfg).DisassociateAddress(ctx, &ec2.DisassociateAddressInput{AssociationId: aws.String(address.AssociationID)})
	if err != nil {
		return source.Accepted{}, err
	}
	id, _ := middleware.GetRequestIDMetadata(out.ResultMetadata)
	return source.Accepted{ResourceID: address.ID, RequestID: id}, nil
}
func (Cloud) Release(ctx context.Context, cfg aws.Config, kind string, address contract.Address) (source.Accepted, error) {
	cfg = noRetry(cfg)
	if kind == "lightsail" {
		out, err := lightsail.NewFromConfig(cfg).ReleaseStaticIp(ctx, &lightsail.ReleaseStaticIpInput{StaticIpName: aws.String(address.ID)})
		if err != nil {
			return source.Accepted{}, err
		}
		id, _ := middleware.GetRequestIDMetadata(out.ResultMetadata)
		return lsAccepted(out.Operations, id), nil
	}
	if address.Kind != kind {
		return source.Accepted{}, contract.ErrUnsupported
	}
	out, err := ec2.NewFromConfig(cfg).ReleaseAddress(ctx, &ec2.ReleaseAddressInput{AllocationId: aws.String(address.ID)})
	if err != nil {
		return source.Accepted{}, err
	}
	id, _ := middleware.GetRequestIDMetadata(out.ResultMetadata)
	return source.Accepted{ResourceID: address.ID, RequestID: id}, nil
}
func (Cloud) Quota(ctx context.Context, cfg aws.Config, kind, account string) (contract.Quota, error) {
	quotaKind := kind
	if kind == "wavelength" {
		quotaKind = "ec2"
	}
	report, err := quotas.NewCloud().Read(ctx, cfg, account, quotaKind)
	if err != nil {
		return contract.Quota{}, err
	}
	key := "lightsail.static_ips"
	if kind == "ec2" {
		key = "ec2.elastic_ips"
	}
	if kind == "wavelength" {
		key = "ec2.carrier_ips"
	}
	for _, row := range report.Entries {
		if row.Key == key {
			return contract.Quota{Limit: row.Limit, Used: row.Used, LimitKnown: row.LimitKnown, UsageKnown: row.UsageKnown, Note: row.Note}, nil
		}
	}
	return contract.Quota{Note: "配额不可查询"}, nil
}

func (Cloud) AllocatedEC2Addresses(ctx context.Context, cfg aws.Config, account string) ([]contract.Address, error) {
	out, err := ec2.NewFromConfig(cfg).DescribeAddresses(ctx, &ec2.DescribeAddressesInput{})
	if err != nil {
		return nil, err
	}
	rows := []contract.Address{}
	seen := map[string]bool{}
	for _, v := range out.Addresses {
		if v.Domain != et.DomainTypeVpc || aws.ToString(v.CustomerOwnedIp) != "" {
			continue
		}
		row, err := elasticIP(v, cfg.Region, account)
		if err != nil {
			return nil, err
		}
		if seen[row.ID] {
			return nil, contract.ErrResponse
		}
		seen[row.ID] = true
		rows = append(rows, row)
	}
	return rows, nil
}
