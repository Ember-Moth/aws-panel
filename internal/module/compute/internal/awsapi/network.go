package awsapi

import (
	"context"
	"github.com/Ember-Moth/aws-panel/internal/module/compute/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/compute/internal/source"
	"github.com/Ember-Moth/aws-panel/internal/module/quotas"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/middleware"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"net/netip"
	"strings"
)

func ownedFilters(labels map[string]string) []types.Filter {
	return []types.Filter{filter("tag:aws-panel:network", labels["aws-panel:network"]), filter("tag:aws-panel:spec", labels["aws-panel:spec"])}
}
func ownedNetwork(values []types.Tag, labels map[string]string) bool {
	m := tags(values)
	return m["aws-panel:network"] == labels["aws-panel:network"] && m["aws-panel:spec"] == labels["aws-panel:spec"]
}
func (c Cloud) NetworkQuotas(ctx context.Context, cfg aws.Config, account string) ([]contract.Quota, error) {
	q := c.Quotas
	if q == nil {
		q = quotas.NewCloud()
	}
	report, err := q.Read(ctx, cfg, account, "vpc")
	if err != nil {
		return nil, err
	}
	result := []contract.Quota{}
	for _, v := range report.Entries {
		result = append(result, contract.Quota{Key: v.Key, Limit: v.Limit, Used: v.Used, LimitKnown: v.LimitKnown, UsageKnown: v.UsageKnown, Note: v.Note})
	}
	return result, nil
}
func matchingPermission(values []types.IpPermission, protocol string, port int32, cidr string, ipv6 bool) bool {
	for _, p := range values {
		if aws.ToString(p.IpProtocol) != protocol {
			continue
		}
		if protocol == "tcp" && (aws.ToInt32(p.FromPort) != port || aws.ToInt32(p.ToPort) != port) {
			continue
		}
		if protocol == "icmpv6" && (aws.ToInt32(p.FromPort) != -1 || aws.ToInt32(p.ToPort) != -1) {
			continue
		}
		if ipv6 {
			for _, r := range p.Ipv6Ranges {
				if aws.ToString(r.CidrIpv6) == cidr {
					return true
				}
			}
		} else {
			for _, r := range p.IpRanges {
				if aws.ToString(r.CidrIp) == cidr {
					return true
				}
			}
		}
	}
	return false
}
func networkRules(n contract.Network) []types.IpPermission {
	if n.AllowAll {
		r := types.IpPermission{IpProtocol: aws.String("-1"), IpRanges: []types.IpRange{{CidrIp: aws.String("0.0.0.0/0")}}}
		if n.WavelengthZoneID == "" {
			r.Ipv6Ranges = []types.Ipv6Range{{CidrIpv6: aws.String("::/0")}}
		}
		return []types.IpPermission{r}
	}

	if n.WavelengthZoneID != "" {
		return []types.IpPermission{{IpProtocol: aws.String("tcp"), FromPort: aws.Int32(22), ToPort: aws.Int32(22), IpRanges: []types.IpRange{{CidrIp: aws.String(n.SSHIPv4CIDR)}}}}
	}
	return []types.IpPermission{{IpProtocol: aws.String("tcp"), FromPort: aws.Int32(22), ToPort: aws.Int32(22), IpRanges: []types.IpRange{{CidrIp: aws.String(n.SSHIPv4CIDR)}}, Ipv6Ranges: []types.Ipv6Range{{CidrIpv6: aws.String(n.SSHIPv6CIDR)}}}, {IpProtocol: aws.String("icmpv6"), FromPort: aws.Int32(-1), ToPort: aws.Int32(-1), Ipv6Ranges: []types.Ipv6Range{{CidrIpv6: aws.String("::/0")}}}}
}

// Network reads only resources tagged for this exact task and specification.
// A fresh observation is required before every mutation and on every recovery.
func (c Cloud) Network(ctx context.Context, cfg aws.Config, phase string, expected contract.Network, labels map[string]string) (contract.Network, bool, error) {
	if strings.HasPrefix(phase, "net_parent_") {
		parent := parentNetwork(expected)
		v, done, err := c.Network(ctx, cfg, "net_"+strings.TrimPrefix(phase, "net_parent_"), parent, labels)
		expected.ParentSubnetID = v.SubnetID
		expected.ParentGatewayID = v.GatewayID
		expected.ParentRouteTableID = v.RouteTableID
		return expected, done, err
	}
	n := expected
	client := c.client(cfg, false)
	filters := ownedFilters(labels)
	vpcs, err := client.DescribeVpcs(ctx, &ec2sdk.DescribeVpcsInput{Filters: filters})
	if err != nil {
		return n, false, err
	}
	if len(vpcs.Vpcs) > 1 || aws.ToString(vpcs.NextToken) != "" {
		return n, false, contract.ErrResponse
	}
	if len(vpcs.Vpcs) == 0 {
		if expected.VPCID != "" {
			return n, false, contract.ErrConflict
		}
		return n, false, nil
	}
	vpc := vpcs.Vpcs[0]
	if aws.ToString(vpc.OwnerId) != labels["aws-panel:account"] || !ownedNetwork(vpc.Tags, labels) || aws.ToString(vpc.CidrBlock) != n.IPv4CIDR || expected.VPCID != "" && expected.VPCID != aws.ToString(vpc.VpcId) {
		return n, false, contract.ErrConflict
	}
	n.VPCID = aws.ToString(vpc.VpcId)
	if n.VPCID == "" {
		return n, false, contract.ErrResponse
	}
	for _, cidr := range vpc.Ipv6CidrBlockAssociationSet {
		if cidr.Ipv6CidrBlockState != nil && cidr.Ipv6CidrBlockState.State == types.VpcCidrBlockStateCodeAssociated {
			n.IPv6CIDR = aws.ToString(cidr.Ipv6CidrBlock)
		}
	}
	if expected.IPv6CIDR != "" && expected.IPv6CIDR != n.IPv6CIDR {
		return n, false, contract.ErrConflict
	}
	if n.IPv6CIDR != "" {
		prefix, e := netip.ParsePrefix(n.IPv6CIDR)
		if e != nil || !prefix.Addr().Is6() || prefix.Bits() > 64 {
			return n, false, contract.ErrResponse
		}
		n.SubnetIPv6CIDR = netip.PrefixFrom(prefix.Masked().Addr(), 64).String()
	}
	if phase == "net_vpc" {
		return n, vpc.State == types.VpcStateAvailable && (n.WavelengthZoneID != "" || n.IPv4Only || n.SubnetIPv6CIDR != ""), nil
	}
	if vpc.State != types.VpcStateAvailable || (n.WavelengthZoneID == "" && !n.IPv4Only && n.SubnetIPv6CIDR == "") {
		return n, false, contract.ErrConflict
	}
	subnets, err := client.DescribeSubnets(ctx, &ec2sdk.DescribeSubnetsInput{Filters: append(append([]types.Filter{}, filters...), filter("cidr-block", n.SubnetIPv4CIDR))})
	if err != nil {
		return n, false, err
	}
	if len(subnets.Subnets) > 1 || aws.ToString(subnets.NextToken) != "" {
		return n, false, contract.ErrResponse
	}
	if len(subnets.Subnets) == 0 && expected.SubnetID != "" {
		return n, false, contract.ErrConflict
	}
	subnetReady := false
	if len(subnets.Subnets) == 1 {
		s := subnets.Subnets[0]
		if !ownedNetwork(s.Tags, labels) || aws.ToString(s.VpcId) != n.VPCID || aws.ToString(s.CidrBlock) != n.SubnetIPv4CIDR || aws.ToString(s.AvailabilityZone) != n.Zone || expected.SubnetID != "" && expected.SubnetID != aws.ToString(s.SubnetId) {
			return n, false, contract.ErrConflict
		}
		n.SubnetID = aws.ToString(s.SubnetId)
		if n.IPv4Only {
			subnetReady = s.State == types.SubnetStateAvailable
		}
		if n.WavelengthZoneID != "" {
			if aws.ToString(s.AvailabilityZoneId) != n.WavelengthZoneID {
				return n, false, contract.ErrConflict
			}
			subnetReady = s.State == types.SubnetStateAvailable
		}
		for _, cidr := range s.Ipv6CidrBlockAssociationSet {
			if aws.ToString(cidr.Ipv6CidrBlock) == n.SubnetIPv6CIDR && cidr.Ipv6CidrBlockState != nil && cidr.Ipv6CidrBlockState.State == types.SubnetCidrBlockStateCodeAssociated {
				subnetReady = s.State == types.SubnetStateAvailable
			}
		}
	}
	if phase == "net_subnet" {
		return n, subnetReady, nil
	}
	if !subnetReady {
		return n, false, contract.ErrConflict
	}
	attached := false
	if n.WavelengthZoneID != "" {
		out, err := client.DescribeCarrierGateways(ctx, &ec2sdk.DescribeCarrierGatewaysInput{Filters: filters})
		if err != nil {
			return n, false, err
		}
		if len(out.CarrierGateways) > 1 || aws.ToString(out.NextToken) != "" {
			return n, false, contract.ErrResponse
		}
		if len(out.CarrierGateways) == 0 && expected.GatewayID != "" {
			return n, false, contract.ErrConflict
		}
		if len(out.CarrierGateways) == 1 {
			g := out.CarrierGateways[0]
			if !ownedNetwork(g.Tags, labels) || aws.ToString(g.VpcId) != n.VPCID || aws.ToString(g.OwnerId) != labels["aws-panel:account"] || expected.GatewayID != "" && expected.GatewayID != aws.ToString(g.CarrierGatewayId) {
				return n, false, contract.ErrConflict
			}
			n.GatewayID = aws.ToString(g.CarrierGatewayId)
			attached = string(g.State) == "available"
		}
		if phase == "net_gateway" {
			return n, attached, nil
		}
		if !attached {
			return n, false, contract.ErrConflict
		}
	} else {
		gateways, err := client.DescribeInternetGateways(ctx, &ec2sdk.DescribeInternetGatewaysInput{Filters: filters})
		if err != nil {
			return n, false, err
		}
		if len(gateways.InternetGateways) > 1 || aws.ToString(gateways.NextToken) != "" {
			return n, false, contract.ErrResponse
		}
		if len(gateways.InternetGateways) == 0 && expected.GatewayID != "" {
			return n, false, contract.ErrConflict
		}
		if len(gateways.InternetGateways) == 1 {
			g := gateways.InternetGateways[0]
			if !ownedNetwork(g.Tags, labels) || expected.GatewayID != "" && expected.GatewayID != aws.ToString(g.InternetGatewayId) {
				return n, false, contract.ErrConflict
			}
			n.GatewayID = aws.ToString(g.InternetGatewayId)
			for _, a := range g.Attachments {
				if aws.ToString(a.VpcId) != n.VPCID {
					return n, false, contract.ErrConflict
				}
				attached = string(a.State) == "available"
			}
		}
		if phase == "net_gateway" {
			return n, n.GatewayID != "", nil
		}
		if n.GatewayID == "" {
			return n, false, contract.ErrConflict
		}
		if phase == "net_attach_gateway" {
			return n, attached, nil
		}
		if !attached {
			return n, false, contract.ErrConflict
		}

	}
	tableFilters := append([]types.Filter{}, filters...)
	if n.IPv4Only {
		tableFilters = append(tableFilters, filter("tag:aws-panel:network-part", "parent"))
	}
	tables, err := client.DescribeRouteTables(ctx, &ec2sdk.DescribeRouteTablesInput{Filters: tableFilters})
	if err != nil {
		return n, false, err
	}
	if len(tables.RouteTables) > 1 || aws.ToString(tables.NextToken) != "" {
		return n, false, contract.ErrResponse
	}
	if len(tables.RouteTables) == 0 && expected.RouteTableID != "" {
		return n, false, contract.ErrConflict
	}
	route4, route6, associated := false, false, false
	if len(tables.RouteTables) == 1 {
		rt := tables.RouteTables[0]
		if !ownedNetwork(rt.Tags, labels) || aws.ToString(rt.VpcId) != n.VPCID || expected.RouteTableID != "" && expected.RouteTableID != aws.ToString(rt.RouteTableId) {
			return n, false, contract.ErrConflict
		}
		n.RouteTableID = aws.ToString(rt.RouteTableId)
		for _, r := range rt.Routes {
			ipv4 := aws.ToString(r.DestinationCidrBlock) == "0.0.0.0/0"
			ipv6 := aws.ToString(r.DestinationIpv6CidrBlock) == "::/0"
			gateway := aws.ToString(r.GatewayId)
			if n.WavelengthZoneID != "" {
				gateway = aws.ToString(r.CarrierGatewayId)
			}
			if (ipv4 || ipv6) && gateway != n.GatewayID {
				return n, false, contract.ErrConflict
			}
			if r.State == types.RouteStateActive {
				route4 = route4 || ipv4
				route6 = route6 || ipv6
			}
		}
		for _, a := range rt.Associations {
			if aws.ToString(a.SubnetId) == n.SubnetID {
				associated = a.AssociationState != nil && a.AssociationState.State == types.RouteTableAssociationStateCodeAssociated
			}
		}
	}
	if phase == "net_route_table" {
		return n, n.RouteTableID != "", nil
	}
	if n.RouteTableID == "" {
		return n, false, contract.ErrConflict
	}
	if phase == "net_route_ipv4" {
		return n, route4, nil
	}
	if phase == "net_route_ipv6" {
		return n, route6, nil
	}
	if phase == "net_associate_route" {
		return n, associated, nil
	}
	if !route4 || (n.WavelengthZoneID == "" && !n.IPv4Only && !route6) || !associated {
		return n, false, contract.ErrConflict
	}
	groups, err := client.DescribeSecurityGroups(ctx, &ec2sdk.DescribeSecurityGroupsInput{Filters: filters})
	if err != nil {
		return n, false, err
	}
	if len(groups.SecurityGroups) > 1 || aws.ToString(groups.NextToken) != "" {
		return n, false, contract.ErrResponse
	}
	if len(groups.SecurityGroups) == 0 && expected.SecurityGroupID != "" {
		return n, false, contract.ErrConflict
	}
	var group *types.SecurityGroup
	if len(groups.SecurityGroups) == 1 {
		group = &groups.SecurityGroups[0]
		if !ownedNetwork(group.Tags, labels) || aws.ToString(group.VpcId) != n.VPCID || expected.SecurityGroupID != "" && expected.SecurityGroupID != aws.ToString(group.GroupId) {
			return n, false, contract.ErrConflict
		}
		n.SecurityGroupID = aws.ToString(group.GroupId)
	}
	if phase == "net_security_group" {
		return n, n.SecurityGroupID != "", nil
	}
	if group == nil {
		return n, false, contract.ErrConflict
	}
	switch phase {
	case "net_ingress":
		if n.AllowAll {
			return n, matchingPermission(group.IpPermissions, "-1", 0, "0.0.0.0/0", false) && (n.WavelengthZoneID != "" || matchingPermission(group.IpPermissions, "-1", 0, "::/0", true)), nil
		}
		return n, matchingPermission(group.IpPermissions, "tcp", 22, n.SSHIPv4CIDR, false) && (n.WavelengthZoneID != "" || (matchingPermission(group.IpPermissions, "tcp", 22, n.SSHIPv6CIDR, true) && matchingPermission(group.IpPermissions, "icmpv6", -1, "::/0", true))), nil
	case "net_egress":
		return n, matchingPermission(group.IpPermissionsEgress, "-1", 0, "0.0.0.0/0", false) && (n.WavelengthZoneID != "" || matchingPermission(group.IpPermissionsEgress, "-1", 0, "::/0", true)), nil
	}
	return n, false, contract.ErrInvalid
}
func (c Cloud) Provision(ctx context.Context, cfg aws.Config, phase string, n contract.Network, labels map[string]string) (source.Accepted, error) {
	if strings.HasPrefix(phase, "net_parent_") {
		return c.Provision(ctx, cfg, "net_"+strings.TrimPrefix(phase, "net_parent_"), parentNetwork(n), labels)
	}
	client := c.client(cfg, true)
	result := source.Accepted{}
	var err error
	switch phase {
	case "net_vpc":
		out, e := client.CreateVpc(ctx, &ec2sdk.CreateVpcInput{CidrBlock: aws.String(n.IPv4CIDR), AmazonProvidedIpv6CidrBlock: aws.Bool(n.WavelengthZoneID == ""), InstanceTenancy: types.TenancyDefault, TagSpecifications: tagSpecs(types.ResourceTypeVpc, labels)})
		err = e
		if e == nil {
			if out.Vpc != nil {
				result.ResourceID = aws.ToString(out.Vpc.VpcId)
			}
			result.RequestID, _ = middleware.GetRequestIDMetadata(out.ResultMetadata)
		}
	case "net_subnet":
		out, e := client.CreateSubnet(ctx, &ec2sdk.CreateSubnetInput{VpcId: aws.String(n.VPCID), CidrBlock: aws.String(n.SubnetIPv4CIDR), Ipv6CidrBlock: optionalString(n.SubnetIPv6CIDR), AvailabilityZone: aws.String(n.Zone), TagSpecifications: tagSpecs(types.ResourceTypeSubnet, labels)})
		err = e
		if e == nil {
			if out.Subnet != nil {
				result.ResourceID = aws.ToString(out.Subnet.SubnetId)
			}
			result.RequestID, _ = middleware.GetRequestIDMetadata(out.ResultMetadata)
		}
	case "net_gateway":
		if n.WavelengthZoneID != "" {
			out, e := client.CreateCarrierGateway(ctx, &ec2sdk.CreateCarrierGatewayInput{VpcId: aws.String(n.VPCID), TagSpecifications: tagSpecs(types.ResourceTypeCarrierGateway, labels)})
			err = e
			if e == nil {
				if out.CarrierGateway != nil {
					result.ResourceID = aws.ToString(out.CarrierGateway.CarrierGatewayId)
				}
				result.RequestID, _ = middleware.GetRequestIDMetadata(out.ResultMetadata)
			}
			break
		}
		out, e := client.CreateInternetGateway(ctx, &ec2sdk.CreateInternetGatewayInput{TagSpecifications: tagSpecs(types.ResourceTypeInternetGateway, labels)})
		err = e
		if e == nil {
			if out.InternetGateway != nil {
				result.ResourceID = aws.ToString(out.InternetGateway.InternetGatewayId)
			}
			result.RequestID, _ = middleware.GetRequestIDMetadata(out.ResultMetadata)
		}
	case "net_attach_gateway":
		out, e := client.AttachInternetGateway(ctx, &ec2sdk.AttachInternetGatewayInput{VpcId: aws.String(n.VPCID), InternetGatewayId: aws.String(n.GatewayID)})
		err = e
		if e == nil {
			result.ResourceID = n.GatewayID
			result.RequestID, _ = middleware.GetRequestIDMetadata(out.ResultMetadata)
		}
	case "net_route_table":
		out, e := client.CreateRouteTable(ctx, &ec2sdk.CreateRouteTableInput{VpcId: aws.String(n.VPCID), TagSpecifications: tagSpecs(types.ResourceTypeRouteTable, labels)})
		err = e
		if e == nil {
			if out.RouteTable != nil {
				result.ResourceID = aws.ToString(out.RouteTable.RouteTableId)
			}
			result.RequestID, _ = middleware.GetRequestIDMetadata(out.ResultMetadata)
		}
	case "net_route_ipv4", "net_route_ipv6":
		in := &ec2sdk.CreateRouteInput{RouteTableId: aws.String(n.RouteTableID), GatewayId: aws.String(n.GatewayID)}
		if n.WavelengthZoneID != "" {
			in.GatewayId = nil
			in.CarrierGatewayId = aws.String(n.GatewayID)
		}
		if phase == "net_route_ipv4" {
			in.DestinationCidrBlock = aws.String("0.0.0.0/0")
		} else {
			in.DestinationIpv6CidrBlock = aws.String("::/0")
		}
		out, e := client.CreateRoute(ctx, in)
		err = e
		if e == nil {
			result.ResourceID = n.RouteTableID
			result.RequestID, _ = middleware.GetRequestIDMetadata(out.ResultMetadata)
		}
	case "net_associate_route":
		out, e := client.AssociateRouteTable(ctx, &ec2sdk.AssociateRouteTableInput{SubnetId: aws.String(n.SubnetID), RouteTableId: aws.String(n.RouteTableID)})
		err = e
		if e == nil {
			result.ResourceID = n.RouteTableID
			result.RequestID, _ = middleware.GetRequestIDMetadata(out.ResultMetadata)
		}
	case "net_security_group":
		out, e := client.CreateSecurityGroup(ctx, &ec2sdk.CreateSecurityGroupInput{VpcId: aws.String(n.VPCID), GroupName: aws.String("aws-panel-" + labels["aws-panel:network"]), Description: aws.String("AWS Panel managed instance network"), TagSpecifications: tagSpecs(types.ResourceTypeSecurityGroup, labels)})
		err = e
		if e == nil {
			result.ResourceID = aws.ToString(out.GroupId)
			result.RequestID, _ = middleware.GetRequestIDMetadata(out.ResultMetadata)
		}
	case "net_ingress", "net_egress":
		// Default egress varies with VPC IPv6 state. Send only missing exact rules.
		groups, e := c.client(cfg, false).DescribeSecurityGroups(ctx, &ec2sdk.DescribeSecurityGroupsInput{GroupIds: []string{n.SecurityGroupID}})
		if e != nil {
			return result, e
		}
		if len(groups.SecurityGroups) != 1 || !ownedNetwork(groups.SecurityGroups[0].Tags, labels) {
			return result, contract.ErrConflict
		}
		group := groups.SecurityGroups[0]
		rules := []types.IpPermission{}
		if phase == "net_ingress" {
			for _, rule := range networkRules(n) {
				copy := rule
				copy.IpRanges = nil
				copy.Ipv6Ranges = nil
				for _, r := range rule.IpRanges {
					if !matchingPermission(group.IpPermissions, aws.ToString(rule.IpProtocol), aws.ToInt32(rule.FromPort), aws.ToString(r.CidrIp), false) {
						copy.IpRanges = append(copy.IpRanges, r)
					}
				}
				for _, r := range rule.Ipv6Ranges {
					if !matchingPermission(group.IpPermissions, aws.ToString(rule.IpProtocol), aws.ToInt32(rule.FromPort), aws.ToString(r.CidrIpv6), true) {
						copy.Ipv6Ranges = append(copy.Ipv6Ranges, r)
					}
				}
				if len(copy.IpRanges)+len(copy.Ipv6Ranges) > 0 {
					rules = append(rules, copy)
				}
			}
			if len(rules) == 0 {
				return source.Accepted{ResourceID: n.SecurityGroupID}, nil
			}
			out, e := client.AuthorizeSecurityGroupIngress(ctx, &ec2sdk.AuthorizeSecurityGroupIngressInput{GroupId: aws.String(n.SecurityGroupID), IpPermissions: rules})
			err = e
			if e == nil {
				result.RequestID, _ = middleware.GetRequestIDMetadata(out.ResultMetadata)
			}
		} else {
			rule := types.IpPermission{IpProtocol: aws.String("-1")}
			if !matchingPermission(group.IpPermissionsEgress, "-1", 0, "0.0.0.0/0", false) {
				rule.IpRanges = []types.IpRange{{CidrIp: aws.String("0.0.0.0/0")}}
			}
			if n.WavelengthZoneID == "" && !matchingPermission(group.IpPermissionsEgress, "-1", 0, "::/0", true) {
				rule.Ipv6Ranges = []types.Ipv6Range{{CidrIpv6: aws.String("::/0")}}
			}
			if len(rule.IpRanges)+len(rule.Ipv6Ranges) == 0 {
				return source.Accepted{ResourceID: n.SecurityGroupID}, nil
			}
			out, e := client.AuthorizeSecurityGroupEgress(ctx, &ec2sdk.AuthorizeSecurityGroupEgressInput{GroupId: aws.String(n.SecurityGroupID), IpPermissions: []types.IpPermission{rule}})
			err = e
			if e == nil {
				result.RequestID, _ = middleware.GetRequestIDMetadata(out.ResultMetadata)
			}
		}
		result.ResourceID = n.SecurityGroupID
	default:
		return result, contract.ErrInvalid
	}
	if err != nil {
		return result, err
	}
	if strings.TrimSpace(result.ResourceID) == "" {
		return result, contract.ErrResponse
	}
	return result, nil
}

func optionalString(v string) *string {
	if v == "" {
		return nil
	}
	return aws.String(v)
}

func parentNetwork(n contract.Network) contract.Network {
	n.IPv4Only = true
	n.WavelengthZoneID = ""
	n.Zone = n.ParentZone
	n.SubnetID = n.ParentSubnetID
	n.SubnetIPv4CIDR = "10.42.1.0/24"
	n.IPv6CIDR = ""
	n.SubnetIPv6CIDR = ""
	n.GatewayID = n.ParentGatewayID
	n.RouteTableID = n.ParentRouteTableID
	return n
}
