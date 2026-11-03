package awsapi

import (
	"context"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/middleware"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/herta-network/aws-panel/internal/module/compute/contract"
	"github.com/herta-network/aws-panel/internal/module/compute/internal/source"
	"slices"
	"sort"
)

func (c Cloud) ObserveIngress(ctx context.Context, cfg aws.Config, kind string, n contract.IngressState, labels map[string]string) (contract.IngressState, bool, error) {
	client := c.client(cfg, false)
	if kind == "relay_forwarding" {
		out, err := client.DescribeInstanceAttribute(ctx, &ec2sdk.DescribeInstanceAttributeInput{InstanceId: aws.String(n.GatewayID), Attribute: types.InstanceAttributeNameSourceDestCheck})
		if err != nil {
			return n, false, err
		}
		if aws.ToString(out.InstanceId) != n.GatewayID || out.SourceDestCheck == nil || out.SourceDestCheck.Value == nil {
			return n, false, contract.ErrResponse
		}
		return n, !aws.ToBool(out.SourceDestCheck.Value), nil
	}
	in := &ec2sdk.DescribeSecurityGroupsInput{Filters: []types.Filter{filter("tag:aws-panel:bundle", labels["aws-panel:bundle"]), filter("tag:aws-panel:primary", n.PrimaryID), filter("tag:aws-panel:role", "ingress-transit")}}
	if n.GroupID != "" {
		in.GroupIds = []string{n.GroupID}
		in.Filters = nil
	}
	groups, err := client.DescribeSecurityGroups(ctx, in)
	if err != nil {
		return n, false, err
	}
	if len(groups.SecurityGroups) > 1 || aws.ToString(groups.NextToken) != "" {
		return n, false, contract.ErrResponse
	}
	if len(groups.SecurityGroups) == 0 {
		if n.GroupID != "" {
			return n, false, contract.ErrConflict
		}
		return n, false, nil
	}
	group := groups.SecurityGroups[0]
	tag := tags(group.Tags)
	if aws.ToString(group.OwnerId) != labels["aws-panel:account"] || aws.ToString(group.VpcId) != n.VPCID || tag["aws-panel:bundle"] != labels["aws-panel:bundle"] || tag["aws-panel:primary"] != n.PrimaryID || tag["aws-panel:spec"] != labels["aws-panel:spec"] || tag["aws-panel:role"] != "ingress-transit" {
		return n, false, contract.ErrConflict
	}
	n.GroupID = aws.ToString(group.GroupId)
	if n.GroupID == "" {
		return n, false, contract.ErrResponse
	}
	// This private trust boundary accepts only the companion's exact address.
	allowed := false
	for _, p := range group.IpPermissions {
		if aws.ToString(p.IpProtocol) != "-1" || len(p.IpRanges) != 1 || aws.ToString(p.IpRanges[0].CidrIp) != n.GatewayPrivateIP+"/32" || len(p.Ipv6Ranges) > 0 || len(p.UserIdGroupPairs) > 0 || len(p.PrefixListIds) > 0 {
			return n, false, contract.ErrConflict
		}
		allowed = true
	}
	if kind == "relay_group" {
		return n, true, nil
	}
	if kind == "relay_ingress" {
		return n, allowed, nil
	}
	if kind == "relay_egress" {
		return n, len(group.IpPermissionsEgress) == 0, nil
	}
	if kind != "relay_attach" {
		return n, false, contract.ErrInvalid
	}
	if !allowed || len(group.IpPermissionsEgress) != 0 {
		return n, false, contract.ErrConflict
	}
	out, err := client.DescribeNetworkInterfaces(ctx, &ec2sdk.DescribeNetworkInterfacesInput{NetworkInterfaceIds: []string{n.PrimaryENI}})
	if err != nil {
		return n, false, err
	}
	if len(out.NetworkInterfaces) != 1 {
		return n, false, contract.ErrResponse
	}
	eni := out.NetworkInterfaces[0]
	if aws.ToString(eni.OwnerId) != labels["aws-panel:account"] || aws.ToString(eni.VpcId) != n.VPCID || aws.ToString(eni.PrivateIpAddress) != n.PrimaryPrivateIP || eni.Attachment == nil || aws.ToString(eni.Attachment.InstanceId) != n.PrimaryID || aws.ToInt32(eni.Attachment.DeviceIndex) != 0 {
		return n, false, contract.ErrConflict
	}
	actual := []string{}
	for _, g := range eni.Groups {
		actual = append(actual, aws.ToString(g.GroupId))
	}
	sort.Strings(actual)
	base := append([]string{}, n.SecurityGroupIDs...)
	sort.Strings(base)
	full := append(append([]string{}, base...), n.GroupID)
	sort.Strings(full)
	if slices.Equal(actual, full) {
		return n, true, nil
	}
	if !slices.Equal(actual, base) {
		return n, false, contract.ErrConflict
	}
	return n, false, nil
}
func (c Cloud) ConfigureIngress(ctx context.Context, cfg aws.Config, kind string, n contract.IngressState, labels map[string]string) (source.Accepted, error) {
	client := c.client(cfg, true)
	accepted := source.Accepted{ResourceID: n.GatewayID}
	switch kind {
	case "relay_forwarding":
		out, err := client.ModifyInstanceAttribute(ctx, &ec2sdk.ModifyInstanceAttributeInput{InstanceId: aws.String(n.GatewayID), SourceDestCheck: &types.AttributeBooleanValue{Value: aws.Bool(false)}})
		if err != nil {
			return accepted, err
		}
		accepted.RequestID, _ = middleware.GetRequestIDMetadata(out.ResultMetadata)
	case "relay_group":
		out, err := client.CreateSecurityGroup(ctx, &ec2sdk.CreateSecurityGroupInput{VpcId: aws.String(n.VPCID), GroupName: aws.String("panel-ingress-" + labels["aws-panel:bundle"]), Description: aws.String("Wavelength ingress companion private forwarding"), TagSpecifications: tagSpecs(types.ResourceTypeSecurityGroup, labels)})
		if err != nil {
			return accepted, err
		}
		accepted.ResourceID = aws.ToString(out.GroupId)
		accepted.RequestID, _ = middleware.GetRequestIDMetadata(out.ResultMetadata)
	case "relay_ingress":
		out, err := client.AuthorizeSecurityGroupIngress(ctx, &ec2sdk.AuthorizeSecurityGroupIngressInput{GroupId: aws.String(n.GroupID), IpPermissions: []types.IpPermission{{IpProtocol: aws.String("-1"), IpRanges: []types.IpRange{{CidrIp: aws.String(n.GatewayPrivateIP + "/32")}}}}})
		if err != nil {
			return accepted, err
		}
		accepted.ResourceID = n.GroupID
		accepted.RequestID, _ = middleware.GetRequestIDMetadata(out.ResultMetadata)
	case "relay_egress":
		// A newly created group permits all egress by default; remove that rule so
		// attaching the private ingress group does not widen the instance's egress.
		current, err := c.client(cfg, false).DescribeSecurityGroups(ctx, &ec2sdk.DescribeSecurityGroupsInput{GroupIds: []string{n.GroupID}})
		if err != nil {
			return accepted, err
		}
		if len(current.SecurityGroups) != 1 {
			return accepted, contract.ErrResponse
		}
		group := current.SecurityGroups[0]
		tag := tags(group.Tags)
		if tag["aws-panel:primary"] != n.PrimaryID || tag["aws-panel:bundle"] != labels["aws-panel:bundle"] || tag["aws-panel:spec"] != labels["aws-panel:spec"] {
			return accepted, contract.ErrConflict
		}
		accepted.ResourceID = n.GroupID
		if len(group.IpPermissionsEgress) == 0 {
			return accepted, nil
		}
		out, err := client.RevokeSecurityGroupEgress(ctx, &ec2sdk.RevokeSecurityGroupEgressInput{GroupId: aws.String(n.GroupID), IpPermissions: group.IpPermissionsEgress})
		if err != nil {
			return accepted, err
		}
		accepted.RequestID, _ = middleware.GetRequestIDMetadata(out.ResultMetadata)
	case "relay_attach":
		groups := append(append([]string{}, n.SecurityGroupIDs...), n.GroupID)
		sort.Strings(groups)
		out, err := client.ModifyNetworkInterfaceAttribute(ctx, &ec2sdk.ModifyNetworkInterfaceAttributeInput{NetworkInterfaceId: aws.String(n.PrimaryENI), Groups: groups})
		if err != nil {
			return accepted, err
		}
		accepted.ResourceID = n.GroupID
		accepted.RequestID, _ = middleware.GetRequestIDMetadata(out.ResultMetadata)
	default:
		return accepted, contract.ErrInvalid
	}
	return accepted, nil
}
