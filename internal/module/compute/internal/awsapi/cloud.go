package awsapi

import (
	"context"
	"encoding/base64"
	"errors"
	"sort"
	"strings"

	"github.com/Ember-Moth/aws-panel/internal/infra/clock"
	"github.com/Ember-Moth/aws-panel/internal/module/compute/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/compute/internal/source"
	"github.com/Ember-Moth/aws-panel/internal/module/quotas"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/middleware"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
)

type Cloud struct {
	EC2Options []func(*ec2sdk.Options)
	Quotas     quotas.Cloud
}

func (c Cloud) client(cfg aws.Config, write bool) *ec2sdk.Client {
	options := append([]func(*ec2sdk.Options){}, c.EC2Options...)
	if write {
		options = append(options, func(o *ec2sdk.Options) {
			o.Retryer = retry.NewStandard(func(v *retry.StandardOptions) { v.MaxAttempts = 1 })
		})
	}
	return ec2sdk.NewFromConfig(cfg, options...)
}
func filter(name string, values ...string) types.Filter {
	return types.Filter{Name: aws.String(name), Values: values}
}
func page(token *string, seen map[string]bool) error {
	s := aws.ToString(token)
	if s != "" {
		if seen[s] || len(seen) >= 256 {
			return contract.ErrResponse
		}
		seen[s] = true
	}
	return nil
}
func tags(values []types.Tag) map[string]string {
	out := map[string]string{}
	for _, v := range values {
		out[aws.ToString(v.Key)] = aws.ToString(v.Value)
	}
	return out
}
func tagSpecs(kind types.ResourceType, values map[string]string) []types.TagSpecification {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := []types.Tag{}
	for _, key := range keys {
		result = append(result, types.Tag{Key: aws.String(key), Value: aws.String(values[key])})
	}
	return []types.TagSpecification{{ResourceType: kind, Tags: result}}
}
func notFound(err error) bool {
	var api smithy.APIError
	return errors.As(err, &api) && api.ErrorCode() == "InvalidInstanceID.NotFound"
}
func convert(v types.Instance, region, account string) (contract.Instance, error) {
	id := aws.ToString(v.InstanceId)
	if id == "" || v.State == nil || v.LaunchTime == nil {
		return contract.Instance{}, contract.ErrResponse
	}
	out := contract.Instance{ID: id, ARN: "arn:aws:ec2:" + region + ":" + account + ":instance/" + id, Tags: tags(v.Tags), State: string(v.State.Name), ImageID: aws.ToString(v.ImageId), Type: string(v.InstanceType), VPCID: aws.ToString(v.VpcId), SubnetID: aws.ToString(v.SubnetId), PublicIP: aws.ToString(v.PublicIpAddress), PrivateIP: aws.ToString(v.PrivateIpAddress), KeyName: aws.ToString(v.KeyName), RootDevice: aws.ToString(v.RootDeviceName), LaunchTime: clock.In(*v.LaunchTime), ClientToken: aws.ToString(v.ClientToken), IPv6: []string{}, SecurityGroupIDs: []string{}}
	out.Name = out.Tags["Name"]
	if out.Name == "" {
		out.Name = id
	}
	if v.Placement != nil {
		out.Zone = aws.ToString(v.Placement.AvailabilityZone)
	}
	for _, sg := range v.SecurityGroups {
		out.SecurityGroupIDs = append(out.SecurityGroupIDs, aws.ToString(sg.GroupId))
	}
	sort.Strings(out.SecurityGroupIDs)
	for _, eni := range v.NetworkInterfaces {
		if eni.Attachment != nil && aws.ToInt32(eni.Attachment.DeviceIndex) == 0 {
			out.PrimaryENI = aws.ToString(eni.NetworkInterfaceId)
			if eni.Association != nil {
				out.CarrierIP = aws.ToString(eni.Association.CarrierIp)
			}
			for _, ip := range eni.Ipv6Addresses {
				out.IPv6 = append(out.IPv6, aws.ToString(ip.Ipv6Address))
			}
		}
	}
	if out.CarrierIP != "" && out.PublicIP == out.CarrierIP {
		out.PublicIP = ""
	}
	for _, volume := range v.BlockDeviceMappings {
		if aws.ToString(volume.DeviceName) == out.RootDevice && volume.Ebs != nil {
			out.RootVolumeID = aws.ToString(volume.Ebs.VolumeId)
			out.DeleteRootOnTermination = aws.ToBool(volume.Ebs.DeleteOnTermination)
		}
	}
	return out, nil
}
func (c Cloud) list(ctx context.Context, cfg aws.Config, account string, input *ec2sdk.DescribeInstancesInput) ([]contract.Instance, error) {
	client := c.client(cfg, false)
	rows := []contract.Instance{}
	seen := map[string]bool{}
	ids := map[string]bool{}
	for {
		out, err := client.DescribeInstances(ctx, input)
		if err != nil {
			return nil, err
		}
		for _, res := range out.Reservations {
			if aws.ToString(res.OwnerId) != account {
				return nil, contract.ErrResponse
			}
			for _, v := range res.Instances {
				row, err := convert(v, cfg.Region, account)
				if err != nil || ids[row.ID] {
					return nil, contract.ErrResponse
				}
				ids[row.ID] = true
				rows = append(rows, row)
			}
		}
		if aws.ToString(out.NextToken) == "" {
			break
		}
		if err := page(out.NextToken, seen); err != nil {
			return nil, err
		}
		input.NextToken = out.NextToken
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows, nil
}
func (c Cloud) Instances(ctx context.Context, cfg aws.Config, account string) ([]contract.Instance, error) {
	return c.list(ctx, cfg, account, &ec2sdk.DescribeInstancesInput{MaxResults: aws.Int32(1000)})
}
func (c Cloud) Instance(ctx context.Context, cfg aws.Config, account, id string) (*contract.Instance, error) {
	rows, err := c.list(ctx, cfg, account, &ec2sdk.DescribeInstancesInput{InstanceIds: []string{id}})
	if notFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	if len(rows) != 1 || rows[0].ID != id {
		return nil, contract.ErrResponse
	}
	return &rows[0], nil
}
func (c Cloud) FindCreated(ctx context.Context, cfg aws.Config, account, token string) ([]contract.Instance, error) {
	return c.list(ctx, cfg, account, &ec2sdk.DescribeInstancesInput{Filters: []types.Filter{filter("client-token", token)}, MaxResults: aws.Int32(1000)})
}
func (c Cloud) Quota(ctx context.Context, cfg aws.Config, account, instanceType string) (contract.Quota, error) {
	key := "ec2.ondemand." + quotas.InstanceClass(instanceType)
	q := c.Quotas
	if q == nil {
		q = quotas.NewCloud()
	}
	report, err := q.Read(ctx, cfg, account, "ec2")
	if err != nil {
		return contract.Quota{}, err
	}
	for _, v := range report.Entries {
		if v.Key == key {
			return contract.Quota{Key: key, Limit: v.Limit, Used: v.Used, LimitKnown: v.LimitKnown, UsageKnown: v.UsageKnown, Note: v.Note}, nil
		}
	}
	return contract.Quota{Key: key, Note: "未查询到该实例类别的适用配额"}, nil
}
func (c Cloud) Launch(ctx context.Context, cfg aws.Config, spec contract.Spec, token, userData string, labels map[string]string) (source.Accepted, error) {
	v := spec.Target
	if len(v.SecurityGroupIDs) == 0 {
		v.SecurityGroupIDs = spec.BootstrapGroups
	}
	if spec.Image == nil {
		return source.Accepted{}, contract.ErrInvalid
	}
	labels["Name"] = v.Name
	ni := types.InstanceNetworkInterfaceSpecification{DeviceIndex: aws.Int32(0), DeleteOnTermination: aws.Bool(true), SubnetId: aws.String(v.SubnetID), Groups: v.SecurityGroupIDs, AssociatePublicIpAddress: aws.Bool(v.PublicIPv4)}
	if spec.AutomaticIP != nil {
		ni.AssociatePublicIpAddress = aws.Bool(false)
	}
	if v.WavelengthZoneID != "" {
		if v.IPv6 || v.PublicIPv4 || spec.VolumeType != "gp2" || spec.WavelengthZone == nil || spec.WavelengthZone.ID != v.WavelengthZoneID {
			return source.Accepted{}, contract.ErrNetwork
		}
		ni.AssociatePublicIpAddress = aws.Bool(false)
	}
	if v.IPv6 {
		ni.Ipv6AddressCount = aws.Int32(1)
	}
	in := &ec2sdk.RunInstancesInput{ImageId: aws.String(v.ImageID), InstanceType: types.InstanceType(v.InstanceType), MinCount: aws.Int32(1), MaxCount: aws.Int32(1), ClientToken: aws.String(token), CapacityReservationSpecification: &types.CapacityReservationSpecification{CapacityReservationPreference: types.CapacityReservationPreferenceNone}, NetworkInterfaces: []types.InstanceNetworkInterfaceSpecification{ni}, Placement: &types.Placement{Tenancy: types.TenancyDefault}, MetadataOptions: &types.InstanceMetadataOptionsRequest{HttpTokens: types.HttpTokensStateRequired, HttpEndpoint: types.InstanceMetadataEndpointStateEnabled, HttpPutResponseHopLimit: aws.Int32(1)}, BlockDeviceMappings: []types.BlockDeviceMapping{{DeviceName: aws.String(spec.Image.RootDevice), Ebs: &types.EbsBlockDevice{VolumeType: types.VolumeTypeGp3, VolumeSize: aws.Int32(v.DiskGB), Encrypted: aws.Bool(true), DeleteOnTermination: aws.Bool(true)}}}, TagSpecifications: append(tagSpecs(types.ResourceTypeInstance, labels), tagSpecs(types.ResourceTypeVolume, labels)...)}
	if v.WavelengthZoneID != "" {
		in.BlockDeviceMappings[0].Ebs.VolumeType = types.VolumeTypeGp2
	}
	if v.LoginMethod == "ssh_key" {
		in.KeyName = aws.String(v.KeyPairName)
	}
	if userData != "" {
		in.UserData = aws.String(base64.StdEncoding.EncodeToString([]byte(userData)))
	}
	out, err := c.client(cfg, true).RunInstances(ctx, in)
	if err != nil {
		return source.Accepted{}, err
	}
	if len(out.Instances) != 1 || aws.ToString(out.Instances[0].InstanceId) == "" {
		return source.Accepted{}, contract.ErrResponse
	}
	requestID, _ := middleware.GetRequestIDMetadata(out.ResultMetadata)
	return source.Accepted{ResourceID: aws.ToString(out.Instances[0].InstanceId), RequestID: requestID}, nil
}
func (c Cloud) Manage(ctx context.Context, cfg aws.Config, action string, instance contract.Instance) (source.Accepted, error) {
	client := c.client(cfg, true)
	id := instance.ID
	var requestID string
	var err error
	switch action {
	case "start":
		out, e := client.StartInstances(ctx, &ec2sdk.StartInstancesInput{InstanceIds: []string{id}})
		err = e
		if e == nil {
			requestID, _ = middleware.GetRequestIDMetadata(out.ResultMetadata)
		}
	case "stop":
		out, e := client.StopInstances(ctx, &ec2sdk.StopInstancesInput{InstanceIds: []string{id}})
		err = e
		if e == nil {
			requestID, _ = middleware.GetRequestIDMetadata(out.ResultMetadata)
		}
	case "reboot":
		out, e := client.RebootInstances(ctx, &ec2sdk.RebootInstancesInput{InstanceIds: []string{id}})
		err = e
		if e == nil {
			requestID, _ = middleware.GetRequestIDMetadata(out.ResultMetadata)
		}
	case "terminate":
		out, e := client.TerminateInstances(ctx, &ec2sdk.TerminateInstancesInput{InstanceIds: []string{id}})
		err = e
		if e == nil {
			requestID, _ = middleware.GetRequestIDMetadata(out.ResultMetadata)
		}
	case "enable_ipv6":
		out, e := client.AssignIpv6Addresses(ctx, &ec2sdk.AssignIpv6AddressesInput{NetworkInterfaceId: aws.String(instance.PrimaryENI), Ipv6AddressCount: aws.Int32(1)})
		err = e
		if e == nil {
			requestID, _ = middleware.GetRequestIDMetadata(out.ResultMetadata)
		}
	default:
		return source.Accepted{}, contract.ErrInvalid
	}
	return source.Accepted{ResourceID: id, RequestID: requestID}, err
}

func (c Cloud) Catalogue(ctx context.Context, cfg aws.Config) (contract.Catalogue, error) {
	return c.catalogue(ctx, cfg, nil)
}
func (c Cloud) catalogue(ctx context.Context, cfg aws.Config, wavelength *contract.WavelengthZone) (contract.Catalogue, error) {
	result, err := c.environment(ctx, cfg, wavelength)
	if err != nil {
		return result, err
	}
	images, err := c.Images(ctx, cfg)
	if err != nil {
		return result, err
	}
	result.Images = images.Images
	zone := ""
	if wavelength != nil {
		zone = wavelength.ID
	}
	kinds, err := c.InstanceTypes(ctx, cfg, zone)
	if err != nil {
		return result, err
	}
	result.Types = kinds.Types
	result.CheckedAt = clock.Now()
	return result, nil
}
func (c Cloud) Images(ctx context.Context, cfg aws.Config) (contract.Catalogue, error) {
	client := c.client(cfg, false)
	result := contract.Catalogue{Region: cfg.Region, Images: []contract.Image{}}
	// Owner filtering is applied by EC2 and checked again for non-Amazon publishers.
	latest := map[string]contract.Image{}
	for _, publisher := range []struct {
		owner  string
		names  []string
		family string
	}{{"099720109477", []string{"ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-*-server-*", "ubuntu/images/hvm-ssd-gp3/ubuntu-resolute-26.04-*-server-*"}, "ubuntu"}, {"136693071363", []string{"debian-12-*-*", "debian-13-*-*"}, "debian"}, {"amazon", []string{"al2023-ami-2023.*-kernel-*-x86_64", "al2023-ami-2023.*-kernel-*-arm64"}, "amzn"}} {
		input := &ec2sdk.DescribeImagesInput{Owners: []string{publisher.owner}, Filters: []types.Filter{filter("name", publisher.names...), filter("state", "available"), filter("root-device-type", "ebs"), filter("virtualization-type", "hvm"), filter("architecture", "x86_64", "arm64")}, MaxResults: aws.Int32(1000)}
		seen := map[string]bool{}
		for {
			out, e := client.DescribeImages(ctx, input)
			if e != nil {
				return result, e
			}
			for _, v := range out.Images {
				if (publisher.owner != "amazon" && aws.ToString(v.OwnerId) != publisher.owner) || v.State != types.ImageStateAvailable || v.RootDeviceType != types.DeviceTypeEbs || v.VirtualizationType != types.VirtualizationTypeHvm || len(v.ProductCodes) > 0 {
					continue
				}
				arch := string(v.Architecture)
				if arch != "x86_64" && arch != "arm64" {
					continue
				}
				root := aws.ToString(v.RootDeviceName)
				size := int32(0)
				extra := false
				for _, b := range v.BlockDeviceMappings {
					if b.Ebs != nil {
						if aws.ToString(b.DeviceName) == root {
							size = aws.ToInt32(b.Ebs.VolumeSize)
						} else {
							extra = true
						}
					}
				}
				if root == "" || size <= 0 || extra || aws.ToString(v.ImageId) == "" {
					continue
				}
				name := aws.ToString(v.Name)
				series := publisher.family
				if strings.Contains(name, "debian-13-") {
					series = "debian-13"
				}
				if strings.Contains(name, "resolute-") {
					series = "ubuntu-26"
				}
				row := contract.Image{ID: aws.ToString(v.ImageId), Name: name, Family: publisher.family, Architecture: arch, RootDevice: root, MinDiskGB: size, Owner: aws.ToString(v.OwnerId), CreatedAt: aws.ToString(v.CreationDate)}
				key := series + "/" + arch
				if latest[key].CreatedAt < row.CreatedAt {
					latest[key] = row
				}
			}
			if aws.ToString(out.NextToken) == "" {
				break
			}
			if e := page(out.NextToken, seen); e != nil {
				return result, e
			}
			input.NextToken = out.NextToken
		}
	}
	for _, v := range latest {
		result.Images = append(result.Images, v)
	}
	sort.Slice(result.Images, func(i, j int) bool { return result.Images[i].Name < result.Images[j].Name })
	result.CheckedAt = clock.Now()
	return result, nil
}
func (c Cloud) InstanceTypes(ctx context.Context, cfg aws.Config, zone string) (contract.Catalogue, error) {
	client := c.client(cfg, false)
	result := contract.Catalogue{Region: cfg.Region, Types: []contract.InstanceType{}}
	var wavelength *contract.WavelengthZone
	if zone != "" {
		wavelength = &contract.WavelengthZone{ID: zone}
	}
	offered := map[string]bool{}
	oi := &ec2sdk.DescribeInstanceTypeOfferingsInput{LocationType: types.LocationTypeRegion, MaxResults: aws.Int32(1000)}
	if wavelength != nil {
		oi.LocationType = types.LocationTypeAvailabilityZoneId
		oi.Filters = []types.Filter{filter("location", wavelength.ID)}
	}
	seen := map[string]bool{}
	for {
		out, e := client.DescribeInstanceTypeOfferings(ctx, oi)
		if e != nil {
			return result, e
		}
		for _, v := range out.InstanceTypeOfferings {
			if wavelength != nil && (aws.ToString(v.Location) != wavelength.ID || v.LocationType != types.LocationTypeAvailabilityZoneId) {
				return result, contract.ErrResponse
			}
			offered[string(v.InstanceType)] = true
		}
		if aws.ToString(out.NextToken) == "" {
			break
		}
		if e := page(out.NextToken, seen); e != nil {
			return result, e
		}
		oi.NextToken = out.NextToken
	}
	ti := &ec2sdk.DescribeInstanceTypesInput{MaxResults: aws.Int32(100), Filters: []types.Filter{filter("supported-usage-class", "on-demand"), filter("supported-root-device-type", "ebs"), filter("supported-virtualization-type", "hvm")}}
	seen = map[string]bool{}
	for {
		out, e := client.DescribeInstanceTypes(ctx, ti)
		if e != nil {
			return result, e
		}
		for _, v := range out.InstanceTypes {
			if !offered[string(v.InstanceType)] || v.VCpuInfo == nil || v.MemoryInfo == nil || v.ProcessorInfo == nil || v.NetworkInfo == nil || quotas.InstanceClass(string(v.InstanceType)) == "" {
				continue
			}
			row := contract.InstanceType{ID: string(v.InstanceType), VCPU: aws.ToInt32(v.VCpuInfo.DefaultVCpus), MemoryMiB: aws.ToInt64(v.MemoryInfo.SizeInMiB), IPv6: aws.ToBool(v.NetworkInfo.Ipv6Supported), Architectures: []string{}}
			for _, a := range v.ProcessorInfo.SupportedArchitectures {
				row.Architectures = append(row.Architectures, string(a))
			}
			if row.VCPU > 0 {
				result.Types = append(result.Types, row)
			}
		}
		if aws.ToString(out.NextToken) == "" {
			break
		}
		if e := page(out.NextToken, seen); e != nil {
			return result, e
		}
		ti.NextToken = out.NextToken
	}
	sort.Slice(result.Types, func(i, j int) bool { return result.Types[i].ID < result.Types[j].ID })
	result.CheckedAt = clock.Now()
	return result, nil
}
func (c Cloud) Environment(ctx context.Context, cfg aws.Config, zone string) (contract.Catalogue, error) {
	if zone == "" {
		return c.environment(ctx, cfg, nil)
	}
	zs, err := c.WavelengthZones(ctx, cfg)
	if err != nil {
		return contract.Catalogue{}, err
	}
	for _, z := range zs {
		if z.ID == zone && z.State == "available" && z.OptInStatus == "opted-in" {
			return c.environment(ctx, cfg, &z)
		}
	}
	return contract.Catalogue{}, contract.ErrNetwork
}
func (c Cloud) environment(ctx context.Context, cfg aws.Config, wavelength *contract.WavelengthZone) (contract.Catalogue, error) {
	client := c.client(cfg, false)
	result := contract.Catalogue{Region: cfg.Region, Images: []contract.Image{}, Types: []contract.InstanceType{}, Subnets: []contract.Subnet{}, SecurityGroups: []contract.SecurityGroup{}, KeyPairs: []string{}, Zones: []string{}, CheckedAt: clock.Now()}
	zones, err := client.DescribeAvailabilityZones(ctx, &ec2sdk.DescribeAvailabilityZonesInput{Filters: []types.Filter{filter("zone-type", "availability-zone"), filter("state", "available")}})
	if err != nil {
		return result, err
	}
	zoneSet := map[string]bool{}
	for _, v := range zones.AvailabilityZones {
		if aws.ToString(v.RegionName) == cfg.Region && aws.ToString(v.ZoneType) == "availability-zone" {
			z := aws.ToString(v.ZoneName)
			zoneSet[z] = true
			result.Zones = append(result.Zones, z)
		}
	}
	if wavelength != nil {
		result.WavelengthZone = wavelength
		result.Zones = []string{wavelength.Name}
		zoneSet = map[string]bool{wavelength.Name: true}
	}
	keys, e := client.DescribeKeyPairs(ctx, &ec2sdk.DescribeKeyPairsInput{})
	if e != nil {
		return result, e
	}
	for _, v := range keys.KeyPairs {
		if aws.ToString(v.KeyName) != "" {
			result.KeyPairs = append(result.KeyPairs, aws.ToString(v.KeyName))
		}
	}
	sort.Strings(result.KeyPairs)
	ri := &ec2sdk.DescribeRouteTablesInput{MaxResults: aws.Int32(100)}
	routes := []types.RouteTable{}
	seen := map[string]bool{}
	for {
		out, e := client.DescribeRouteTables(ctx, ri)
		if e != nil {
			return result, e
		}
		routes = append(routes, out.RouteTables...)
		if aws.ToString(out.NextToken) == "" {
			break
		}
		if e := page(out.NextToken, seen); e != nil {
			return result, e
		}
		ri.NextToken = out.NextToken
	}
	si := &ec2sdk.DescribeSubnetsInput{MaxResults: aws.Int32(1000)}
	seen = map[string]bool{}
	for {
		out, e := client.DescribeSubnets(ctx, si)
		if e != nil {
			return result, e
		}
		for _, v := range out.Subnets {
			if v.State != types.SubnetStateAvailable || aws.ToBool(v.Ipv6Native) || !zoneSet[aws.ToString(v.AvailabilityZone)] {
				continue
			}
			row := contract.Subnet{ID: aws.ToString(v.SubnetId), Name: tags(v.Tags)["Name"], VPCID: aws.ToString(v.VpcId), Zone: aws.ToString(v.AvailabilityZone), IPv4CIDR: aws.ToString(v.CidrBlock), AvailableIPs: aws.ToInt32(v.AvailableIpAddressCount)}
			for _, cidr := range v.Ipv6CidrBlockAssociationSet {
				if cidr.Ipv6CidrBlockState != nil && cidr.Ipv6CidrBlockState.State == types.SubnetCidrBlockStateCodeAssociated {
					row.IPv6CIDR = aws.ToString(cidr.Ipv6CidrBlock)
					break
				}
			}
			var chosen *types.RouteTable
			var main *types.RouteTable
			for i := range routes {
				rt := &routes[i]
				if aws.ToString(rt.VpcId) != row.VPCID {
					continue
				}
				for _, a := range rt.Associations {
					if aws.ToString(a.SubnetId) == row.ID {
						chosen = rt
					}
					if aws.ToBool(a.Main) {
						main = rt
					}
				}
			}
			if chosen == nil {
				chosen = main
			}
			if chosen != nil {
				for _, r := range chosen.Routes {
					if r.State == types.RouteStateActive && ((wavelength == nil && strings.HasPrefix(aws.ToString(r.GatewayId), "igw-")) || (wavelength != nil && strings.HasPrefix(aws.ToString(r.CarrierGatewayId), "cagw-"))) {
						if aws.ToString(r.DestinationCidrBlock) == "0.0.0.0/0" {
							row.PublicIPv4Route = true
						}
						if aws.ToString(r.DestinationIpv6CidrBlock) == "::/0" {
							row.PublicIPv6Route = true
						}
					}
				}
			}
			result.Subnets = append(result.Subnets, row)
		}
		if aws.ToString(out.NextToken) == "" {
			break
		}
		if e := page(out.NextToken, seen); e != nil {
			return result, e
		}
		si.NextToken = out.NextToken
	}
	gi := &ec2sdk.DescribeSecurityGroupsInput{MaxResults: aws.Int32(1000)}
	seen = map[string]bool{}
	for {
		out, e := client.DescribeSecurityGroups(ctx, gi)
		if e != nil {
			return result, e
		}
		for _, v := range out.SecurityGroups {
			row := contract.SecurityGroup{ID: aws.ToString(v.GroupId), Name: aws.ToString(v.GroupName), VPCID: aws.ToString(v.VpcId), SSHIPv4: []string{}, SSHIPv6: []string{}}
			for _, p := range v.IpPermissions {
				if aws.ToString(p.IpProtocol) == "-1" || (aws.ToString(p.IpProtocol) == "tcp" && aws.ToInt32(p.FromPort) <= 22 && aws.ToInt32(p.ToPort) >= 22) {
					for _, r := range p.IpRanges {
						row.SSHIPv4 = append(row.SSHIPv4, aws.ToString(r.CidrIp))
					}
					for _, r := range p.Ipv6Ranges {
						row.SSHIPv6 = append(row.SSHIPv6, aws.ToString(r.CidrIpv6))
					}
				}
			}
			result.SecurityGroups = append(result.SecurityGroups, row)
		}
		if aws.ToString(out.NextToken) == "" {
			break
		}
		if e := page(out.NextToken, seen); e != nil {
			return result, e
		}
		gi.NextToken = out.NextToken
	}
	return result, nil
}
