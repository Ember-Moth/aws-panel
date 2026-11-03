package integration

import (
	"fmt"
	cp "github.com/Ember-Moth/aws-panel/internal/module/compute/contract"
	"net/http"
	"strings"
)

type computeNetwork struct {
	scope                                                 string
	network                                               cp.Network
	labels                                                map[string]string
	attached, route4, route6, associated, ingress, egress bool
}

func (n *computeNetwork) vpcXML() string {
	v := n.network
	if v.VPCID == "" {
		return ""
	}
	return `<item><vpcId>` + v.VPCID + `</vpcId><ownerId>` + n.labels["aws-panel:account"] + `</ownerId><state>available</state><cidrBlock>` + v.IPv4CIDR + `</cidrBlock><ipv6CidrBlockAssociationSet><item><ipv6CidrBlock>` + v.IPv6CIDR + `</ipv6CidrBlock><ipv6CidrBlockState><state>associated</state></ipv6CidrBlockState></item></ipv6CidrBlockAssociationSet>` + xmlTags(n.labels) + `</item>`
}
func (n *computeNetwork) subnetXML() string {
	v := n.network
	if v.SubnetID == "" {
		return ""
	}
	return `<item><subnetId>` + v.SubnetID + `</subnetId><vpcId>` + v.VPCID + `</vpcId><state>available</state><cidrBlock>` + v.SubnetIPv4CIDR + `</cidrBlock><availabilityZone>` + v.Zone + `</availabilityZone><availabilityZoneId>` + v.WavelengthZoneID + `</availabilityZoneId><availableIpAddressCount>200</availableIpAddressCount><ipv6CidrBlockAssociationSet><item><ipv6CidrBlock>` + v.SubnetIPv6CIDR + `</ipv6CidrBlock><ipv6CidrBlockState><state>associated</state></ipv6CidrBlockState></item></ipv6CidrBlockAssociationSet>` + xmlTags(n.labels) + `</item>`
}
func (n *computeNetwork) gatewayXML() string {
	v := n.network
	if v.GatewayID == "" {
		return ""
	}
	if v.WavelengthZoneID != "" {
		return `<item><carrierGatewayId>` + v.GatewayID + `</carrierGatewayId><vpcId>` + v.VPCID + `</vpcId><ownerId>` + n.labels["aws-panel:account"] + `</ownerId><state>available</state>` + xmlTags(n.labels) + `</item>`
	}
	attachment := ""
	if n.attached {
		attachment = `<item><vpcId>` + v.VPCID + `</vpcId><state>available</state></item>`
	}
	return `<item><internetGatewayId>` + v.GatewayID + `</internetGatewayId><attachmentSet>` + attachment + `</attachmentSet>` + xmlTags(n.labels) + `</item>`
}
func (n *computeNetwork) routeXML() string {
	v := n.network
	if v.RouteTableID == "" {
		return ""
	}
	routes := ""
	if n.route4 {
		gatewayTag := "gatewayId"
		if v.WavelengthZoneID != "" {
			gatewayTag = "carrierGatewayId"
		}
		routes += `<item><destinationCidrBlock>0.0.0.0/0</destinationCidrBlock><` + gatewayTag + `>` + v.GatewayID + `</` + gatewayTag + `><state>active</state></item>`
	}
	if n.route6 {
		routes += `<item><destinationIpv6CidrBlock>::/0</destinationIpv6CidrBlock><gatewayId>` + v.GatewayID + `</gatewayId><state>active</state></item>`
	}
	association := ""
	if n.associated {
		association = `<item><subnetId>` + v.SubnetID + `</subnetId><associationState><state>associated</state></associationState></item>`
	}
	return `<item><routeTableId>` + v.RouteTableID + `</routeTableId><vpcId>` + v.VPCID + `</vpcId><routeSet>` + routes + `</routeSet><associationSet>` + association + `</associationSet>` + xmlTags(n.labels) + `</item>`
}
func (n *computeNetwork) groupXML() string {
	v := n.network
	if v.SecurityGroupID == "" {
		return ""
	}
	ingress := ""
	if n.ingress {
		ingress = `<item><ipProtocol>tcp</ipProtocol><fromPort>22</fromPort><toPort>22</toPort><ipRanges><item><cidrIp>` + v.SSHIPv4CIDR + `</cidrIp></item></ipRanges><ipv6Ranges><item><cidrIpv6>` + v.SSHIPv6CIDR + `</cidrIpv6></item></ipv6Ranges></item><item><ipProtocol>icmpv6</ipProtocol><fromPort>-1</fromPort><toPort>-1</toPort><ipv6Ranges><item><cidrIpv6>::/0</cidrIpv6></item></ipv6Ranges></item>`
	}
	if n.network.AllowAll {
		ingress = permissionXML(n.ingress, n.ingress && n.network.WavelengthZoneID == "")
	}
	egress := `<item><ipProtocol>-1</ipProtocol><ipRanges><item><cidrIp>0.0.0.0/0</cidrIp></item></ipRanges>`
	if n.egress {
		egress += `<ipv6Ranges><item><cidrIpv6>::/0</cidrIpv6></item></ipv6Ranges>`
	}
	egress += `</item>`
	return `<item><groupId>` + v.SecurityGroupID + `</groupId><groupName>managed-ssh</groupName><vpcId>` + v.VPCID + `</vpcId><ipPermissions>` + ingress + `</ipPermissions><ipPermissionsEgress>` + egress + `</ipPermissionsEgress>` + xmlTags(n.labels) + `</item>`
}
func (f *computeAWS) networkHandle(w http.ResponseWriter, scope, action string, input map[string]any, reply func(string), reject func(string)) bool {
	tagID := ""
	for n := 1; n < 12; n++ {
		if input[fmt.Sprintf("Filter.%d.Name", n)] == "tag:aws-panel:network" {
			tagID, _ = input[fmt.Sprintf("Filter.%d.Value.1", n)].(string)
		}
	}
	describe := map[string]string{"DescribeVpcs": "vpcSet", "DescribeSubnets": "subnetSet", "DescribeInternetGateways": "internetGatewaySet", "DescribeCarrierGateways": "carrierGatewaySet", "DescribeRouteTables": "routeTableSet", "DescribeSecurityGroups": "securityGroupInfo"}
	if set, ok := describe[action]; ok {
		has := tagID != "" || action == "DescribeVpcs" || action == "DescribeInternetGateways"
		for _, n := range f.networks {
			has = has || n.scope == scope
		}
		if !has {
			return false
		}
		var body strings.Builder
		for _, n := range f.networks {
			if n.scope != scope || tagID != "" && tagID != n.labels["aws-panel:network"] {
				continue
			}
			if id, _ := input["GroupId.1"].(string); id != "" && id != n.network.SecurityGroupID {
				continue
			}
			valid := true
			for i := 1; i < 15; i++ {
				key, _ := input[fmt.Sprintf("Filter.%d.Name", i)].(string)
				if strings.HasPrefix(key, "tag:") {
					valid = valid && n.labels[strings.TrimPrefix(key, "tag:")] == fmt.Sprint(input[fmt.Sprintf("Filter.%d.Value.1", i)])
				}
			}
			if !valid {
				continue
			}
			if action == "DescribeSubnets" && !matches(input, "cidr-block", n.network.SubnetIPv4CIDR) {
				continue
			}
			if action == "DescribeVpcs" && n.network.IPv4Only {
				continue
			}
			if action == "DescribeInternetGateways" && n.network.WavelengthZoneID != "" || action == "DescribeCarrierGateways" && n.network.WavelengthZoneID == "" {
				continue
			}
			switch action {
			case "DescribeVpcs":
				body.WriteString(n.vpcXML())
			case "DescribeSubnets":
				body.WriteString(n.subnetXML())
			case "DescribeInternetGateways", "DescribeCarrierGateways":
				body.WriteString(n.gatewayXML())
			case "DescribeRouteTables":
				body.WriteString(n.routeXML())
			case "DescribeSecurityGroups":
				body.WriteString(n.groupXML())
			}
		}
		reply("<" + set + ">" + body.String() + "</" + set + ">")
		return true
	}
	resourceTypes := map[string]string{"CreateVpc": "vpc", "CreateSubnet": "subnet", "CreateInternetGateway": "internet-gateway", "CreateCarrierGateway": "carrier-gateway", "CreateRouteTable": "route-table", "CreateSecurityGroup": "security-group"}
	_, create := resourceTypes[action]
	modify := action == "AttachInternetGateway" || action == "CreateRoute" || action == "AssociateRouteTable" || action == "AuthorizeSecurityGroupIngress" || action == "AuthorizeSecurityGroupEgress"
	if !create && !modify {
		return false
	}
	f.calls[action]++
	if f.mode == "reject_network_route6" && action == "CreateRoute" && input["DestinationIpv6CidrBlock"] != nil {
		reject("UnauthorizedOperation")
		return true
	}
	if f.mode == "network_never_accepted" && action == "CreateVpc" {
		w.WriteHeader(502)
		return true
	}
	labels := queryLabels(input, resourceTypes[action])
	var n *computeNetwork
	key := labels["aws-panel:network"]
	if action == "CreateVpc" {
		n = &computeNetwork{scope: scope, labels: labels, network: cp.Network{VPCID: "vpc-22222222", IPv4CIDR: fmt.Sprint(input["CidrBlock"]), IPv6CIDR: "2600:1f18:42::/56"}}
		n.network.WavelengthZoneID = labels["aws-panel:zone-id"]
		if n.network.WavelengthZoneID != "" {
			n.network.IPv6CIDR = ""
		}
		f.networks[key] = n
	} else if create {
		if labels["aws-panel:network-part"] == "parent" {
			base := f.networks[key]
			key += ":parent"
			if f.networks[key] == nil && base != nil {
				f.networks[key] = &computeNetwork{scope: scope, labels: labels, network: cp.Network{IPv4Only: true, VPCID: base.network.VPCID, IPv4CIDR: base.network.IPv4CIDR}}
			}
		}
		n = f.networks[key]
	} else {
		for _, candidate := range f.networks {
			if candidate.scope == scope && (input["InternetGatewayId"] == candidate.network.GatewayID || input["RouteTableId"] == candidate.network.RouteTableID || input["GroupId"] == candidate.network.SecurityGroupID) {
				n = candidate
				break
			}
		}
	}
	if n == nil {
		f.t.Error("network mutation without owned resource")
		reject("InvalidParameterValue")
		return true
	}
	body := ""
	switch action {
	case "CreateVpc":
		if n.network.WavelengthZoneID != "" {
			if input["AmazonProvidedIpv6CidrBlock"] == "true" {
				f.t.Error("Wavelength created IPv6")
			}
		} else if input["AmazonProvidedIpv6CidrBlock"] != "true" {
			f.t.Error("VPC IPv6 missing")
		}
		body = `<vpc><vpcId>` + n.network.VPCID + `</vpcId></vpc>`
	case "CreateSubnet":
		n.network.SubnetID = "subnet-22222222"
		if n.network.IPv4Only {
			n.network.SubnetID = "subnet-33333333"
		}
		n.network.SubnetIPv4CIDR = fmt.Sprint(input["CidrBlock"])
		n.network.SubnetIPv6CIDR, _ = input["Ipv6CidrBlock"].(string)
		if n.network.WavelengthZoneID != "" && input["Ipv6CidrBlock"] != nil {
			f.t.Error("IPv6 sent to Wavelength subnet")
		}
		n.network.Zone = fmt.Sprint(input["AvailabilityZone"])
		body = `<subnet><subnetId>` + n.network.SubnetID + `</subnetId></subnet>`
	case "CreateCarrierGateway":
		n.network.GatewayID = "cagw-22222222"
		n.attached = true
		body = `<carrierGateway><carrierGatewayId>` + n.network.GatewayID + `</carrierGatewayId></carrierGateway>`
	case "CreateInternetGateway":
		n.network.GatewayID = "igw-22222222"
		body = `<internetGateway><internetGatewayId>` + n.network.GatewayID + `</internetGatewayId></internetGateway>`
	case "AttachInternetGateway":
		n.attached = true
	case "CreateRouteTable":
		n.network.RouteTableID = "rtb-22222222"
		if n.network.IPv4Only {
			n.network.RouteTableID = "rtb-33333333"
		}
		body = `<routeTable><routeTableId>` + n.network.RouteTableID + `</routeTableId></routeTable>`
	case "CreateRoute":
		if n.network.WavelengthZoneID != "" && (input["GatewayId"] != nil || input["CarrierGatewayId"] != n.network.GatewayID || input["DestinationIpv6CidrBlock"] != nil) {
			f.t.Error("invalid carrier route")
		}
		if input["DestinationCidrBlock"] == "0.0.0.0/0" {
			n.route4 = true
		} else if input["DestinationIpv6CidrBlock"] == "::/0" {
			n.route6 = true
		}
	case "AssociateRouteTable":
		n.associated = true
		body = `<associationId>rtbassoc-22222222</associationId>`
	case "CreateSecurityGroup":
		n.network.SecurityGroupID = "sg-22222222"
		body = `<groupId>` + n.network.SecurityGroupID + `</groupId>`
	case "AuthorizeSecurityGroupIngress":
		n.ingress = true
		n.network.AllowAll = input["IpPermissions.1.IpProtocol"] == "-1"
		n.network.SSHIPv4CIDR = fmt.Sprint(input["IpPermissions.1.IpRanges.1.CidrIp"])
		n.network.SSHIPv6CIDR, _ = input["IpPermissions.1.Ipv6Ranges.1.CidrIpv6"].(string)
		if n.network.WavelengthZoneID != "" && input["IpPermissions.1.Ipv6Ranges.1.CidrIpv6"] != nil {
			f.t.Error("IPv6 SG in Wavelength")
		}
	case "AuthorizeSecurityGroupEgress":
		n.egress = true
	}
	if f.mode == "lost_network" {
		w.WriteHeader(502)
		return true
	}
	reply(body + `<return>true</return>`)
	return true
}
