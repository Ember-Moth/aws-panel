package awsapi

import (
	"context"
	"github.com/Ember-Moth/aws-panel/internal/module/compute/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/compute/internal/source"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/middleware"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"slices"
	"sort"
)

func firewallOwned(g types.SecurityGroup, n contract.FirewallState, labels map[string]string) bool {
	t := tags(g.Tags)
	return (n.GroupID == "" || aws.ToString(g.GroupId) == n.GroupID) && aws.ToString(g.OwnerId) == labels["aws-panel:account"] && aws.ToString(g.VpcId) == n.VPCID && aws.ToString(g.GroupName) == n.Name && t["aws-panel:firewall"] == labels["aws-panel:firewall"] && t["aws-panel:spec"] == labels["aws-panel:spec"] && t["aws-panel:primary"] == n.InstanceID && t["aws-panel:role"] == "instance-open"
}
func allTraffic(rules []types.IpPermission, ipv6 bool) bool {
	return matchingPermission(rules, "-1", 0, "0.0.0.0/0", false) && (!ipv6 || matchingPermission(rules, "-1", 0, "::/0", true))
}
func (c Cloud) firewallGroup(ctx context.Context, cfg aws.Config, n contract.FirewallState, labels map[string]string) (*types.SecurityGroup, error) {
	in := &ec2sdk.DescribeSecurityGroupsInput{Filters: []types.Filter{filter("tag:aws-panel:firewall", labels["aws-panel:firewall"]), filter("tag:aws-panel:spec", labels["aws-panel:spec"]), filter("vpc-id", n.VPCID)}}
	if n.GroupID != "" {
		in.GroupIds = []string{n.GroupID}
		in.Filters = nil
	}
	out, err := c.client(cfg, false).DescribeSecurityGroups(ctx, in)
	if err != nil {
		return nil, err
	}
	if len(out.SecurityGroups) > 1 || aws.ToString(out.NextToken) != "" {
		return nil, contract.ErrResponse
	}
	if len(out.SecurityGroups) == 0 {
		if n.GroupID != "" {
			return nil, contract.ErrConflict
		}
		return nil, nil
	}
	g := out.SecurityGroups[0]
	if !firewallOwned(g, n, labels) || aws.ToString(g.GroupId) == "" {
		return nil, contract.ErrConflict
	}
	return &g, nil
}
func (c Cloud) ObserveFirewall(ctx context.Context, cfg aws.Config, kind string, n contract.FirewallState, labels map[string]string) (contract.FirewallState, bool, error) {
	g, err := c.firewallGroup(ctx, cfg, n, labels)
	if err != nil {
		return n, false, err
	}
	if g == nil {
		return n, false, nil
	}
	n.GroupID = aws.ToString(g.GroupId)
	switch kind {
	case "firewall_group":
		return n, true, nil
	case "firewall_ingress":
		return n, allTraffic(g.IpPermissions, n.IPv6), nil
	case "firewall_egress":
		return n, allTraffic(g.IpPermissionsEgress, n.IPv6), nil
	case "firewall_attach":
		if !allTraffic(g.IpPermissions, n.IPv6) || !allTraffic(g.IpPermissionsEgress, n.IPv6) {
			return n, false, contract.ErrConflict
		}
		out, err := c.client(cfg, false).DescribeNetworkInterfaces(ctx, &ec2sdk.DescribeNetworkInterfacesInput{NetworkInterfaceIds: []string{n.ENI}})
		if err != nil {
			return n, false, err
		}
		if len(out.NetworkInterfaces) != 1 {
			return n, false, contract.ErrResponse
		}
		eni := out.NetworkInterfaces[0]
		if aws.ToString(eni.NetworkInterfaceId) != n.ENI || aws.ToString(eni.OwnerId) != labels["aws-panel:account"] || aws.ToString(eni.VpcId) != n.VPCID || aws.ToString(eni.PrivateIpAddress) != n.PrivateIP || eni.Attachment == nil || aws.ToString(eni.Attachment.InstanceId) != n.InstanceID || aws.ToInt32(eni.Attachment.DeviceIndex) != 0 {
			return n, false, contract.ErrConflict
		}
		actual := []string{}
		for _, v := range eni.Groups {
			actual = append(actual, aws.ToString(v.GroupId))
		}
		sort.Strings(actual)
		if slices.Equal(actual, []string{n.GroupID}) {
			return n, true, nil
		}
		previous := append([]string{}, n.OriginalGroups...)
		sort.Strings(previous)
		if !slices.Equal(actual, previous) {
			return n, false, contract.ErrConflict
		}
		return n, false, nil
	}
	return n, false, contract.ErrInvalid
}
func (c Cloud) ConfigureFirewall(ctx context.Context, cfg aws.Config, kind string, n contract.FirewallState, labels map[string]string) (source.Accepted, error) {
	client := c.client(cfg, true)
	accepted := source.Accepted{ResourceID: n.GroupID}
	switch kind {
	case "firewall_group":
		out, err := client.CreateSecurityGroup(ctx, &ec2sdk.CreateSecurityGroupInput{VpcId: aws.String(n.VPCID), GroupName: aws.String(n.Name), Description: aws.String("AWS Panel instance allow-all ingress and egress"), TagSpecifications: tagSpecs(types.ResourceTypeSecurityGroup, labels)})
		if err != nil {
			return accepted, err
		}
		accepted.ResourceID = aws.ToString(out.GroupId)
		accepted.RequestID, _ = middleware.GetRequestIDMetadata(out.ResultMetadata)
	case "firewall_ingress", "firewall_egress":
		g, err := c.firewallGroup(ctx, cfg, n, labels)
		if err != nil {
			return accepted, err
		}
		if g == nil {
			return accepted, contract.ErrConflict
		}
		rules := g.IpPermissions
		if kind == "firewall_egress" {
			rules = g.IpPermissionsEgress
		}
		rule := types.IpPermission{IpProtocol: aws.String("-1")}
		if !matchingPermission(rules, "-1", 0, "0.0.0.0/0", false) {
			rule.IpRanges = []types.IpRange{{CidrIp: aws.String("0.0.0.0/0")}}
		}
		if n.IPv6 && !matchingPermission(rules, "-1", 0, "::/0", true) {
			rule.Ipv6Ranges = []types.Ipv6Range{{CidrIpv6: aws.String("::/0")}}
		}
		if len(rule.IpRanges)+len(rule.Ipv6Ranges) == 0 {
			return accepted, nil
		}
		if kind == "firewall_ingress" {
			out, err := client.AuthorizeSecurityGroupIngress(ctx, &ec2sdk.AuthorizeSecurityGroupIngressInput{GroupId: aws.String(n.GroupID), IpPermissions: []types.IpPermission{rule}})
			if err != nil {
				return accepted, err
			}
			accepted.RequestID, _ = middleware.GetRequestIDMetadata(out.ResultMetadata)
		} else {
			out, err := client.AuthorizeSecurityGroupEgress(ctx, &ec2sdk.AuthorizeSecurityGroupEgressInput{GroupId: aws.String(n.GroupID), IpPermissions: []types.IpPermission{rule}})
			if err != nil {
				return accepted, err
			}
			accepted.RequestID, _ = middleware.GetRequestIDMetadata(out.ResultMetadata)
		}
	case "firewall_attach":
		out, err := client.ModifyNetworkInterfaceAttribute(ctx, &ec2sdk.ModifyNetworkInterfaceAttributeInput{NetworkInterfaceId: aws.String(n.ENI), Groups: []string{n.GroupID}})
		if err != nil {
			return accepted, err
		}
		accepted.RequestID, _ = middleware.GetRequestIDMetadata(out.ResultMetadata)
	default:
		return accepted, contract.ErrInvalid
	}
	return accepted, nil
}
