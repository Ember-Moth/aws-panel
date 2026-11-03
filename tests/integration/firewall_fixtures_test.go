package integration

import (
	"context"
	"fmt"
	"github.com/Ember-Moth/aws-panel/internal/module/compute"
	cp "github.com/Ember-Moth/aws-panel/internal/module/compute/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/lightsail"
	"github.com/aws/aws-sdk-go-v2/aws"
	"net/http"
	"strings"
)

type computeFirewall struct {
	id, scope, vpc, name string
	labels               map[string]string
	in4, in6, out4, out6 bool
}

func (f *computeAWS) ObserveFirewall(ctx context.Context, cfg aws.Config, kind string, n cp.FirewallState, tags map[string]string) (cp.FirewallState, bool, error) {
	return compute.NewCloud().(compute.FirewallCloud).ObserveFirewall(ctx, addressConfig(cfg), kind, n, tags)
}
func (f *computeAWS) ConfigureFirewall(ctx context.Context, cfg aws.Config, kind string, n cp.FirewallState, tags map[string]string) (compute.Accepted, error) {
	return compute.NewCloud().(compute.FirewallCloud).ConfigureFirewall(ctx, addressConfig(cfg), kind, n, tags)
}
func (f *launchAWS) FirewallOpen(ctx context.Context, cfg aws.Config, name, ipType string) (bool, error) {
	return lightsail.NewCloud().(lightsail.FirewallCloud).FirewallOpen(ctx, addressConfig(cfg), name, ipType)
}
func (f *launchAWS) OpenFirewall(ctx context.Context, cfg aws.Config, name, ipType string) (lightsail.Created, error) {
	return lightsail.NewCloud().(lightsail.FirewallCloud).OpenFirewall(ctx, addressConfig(cfg), name, ipType)
}
func permissionXML(v4, v6 bool) string {
	if !v4 && !v6 {
		return ""
	}
	s := `<item><ipProtocol>-1</ipProtocol>`
	if v4 {
		s += `<ipRanges><item><cidrIp>0.0.0.0/0</cidrIp></item></ipRanges>`
	}
	if v6 {
		s += `<ipv6Ranges><item><cidrIpv6>::/0</cidrIpv6></item></ipv6Ranges>`
	}
	return s + `</item>`
}
func (g *computeFirewall) xml() string {
	return `<item><groupId>` + g.id + `</groupId><groupName>` + g.name + `</groupName><vpcId>` + g.vpc + `</vpcId><ownerId>` + strings.Split(g.scope, "/")[0] + `</ownerId><ipPermissions>` + permissionXML(g.in4, g.in6) + `</ipPermissions><ipPermissionsEgress>` + permissionXML(g.out4, g.out6) + `</ipPermissionsEgress>` + xmlTags(g.labels) + `</item>`
}
func (f *computeAWS) firewallGroupsXML(scope string) string {
	body := ""
	for _, g := range f.firewalls {
		if g.scope == scope {
			body += g.xml()
		}
	}
	return body
}
func (f *computeAWS) firewallHandle(w http.ResponseWriter, scope, action string, input map[string]any, reply func(string), reject func(string)) bool {
	switch action {
	case "CreateSecurityGroup":
		labels := queryLabels(input, "security-group")
		if labels["aws-panel:role"] != "instance-open" {
			return false
		}
		f.calls[action]++
		if f.mode == "reject_firewall" {
			reject("UnauthorizedOperation")
			return true
		}
		f.next++
		id := fmt.Sprintf("sg-%08x", f.next)
		f.firewalls[id] = &computeFirewall{id: id, scope: scope, vpc: fmt.Sprint(input["VpcId"]), name: fmt.Sprint(input["GroupName"]), labels: labels, out4: true}
		if f.mode == "lost_firewall" || f.mode == "lost_ingress" {
			w.WriteHeader(502)
			return true
		}
		reply(`<groupId>` + id + `</groupId>`)
		return true
	case "DescribeSecurityGroups":
		id, _ := input["GroupId.1"].(string)
		ours := f.firewalls[id] != nil
		for i := 1; i < 12; i++ {
			ours = ours || input[fmt.Sprintf("Filter.%d.Name", i)] == "tag:aws-panel:firewall"
		}
		if !ours {
			return false
		}
		body := ""
		for _, g := range f.firewalls {
			if g.scope != scope || id != "" && id != g.id {
				continue
			}
			match := true
			for i := 1; i < 12; i++ {
				k, _ := input[fmt.Sprintf("Filter.%d.Name", i)].(string)
				v, _ := input[fmt.Sprintf("Filter.%d.Value.1", i)].(string)
				if strings.HasPrefix(k, "tag:") {
					match = match && g.labels[strings.TrimPrefix(k, "tag:")] == v
				}
				if k == "vpc-id" {
					match = match && g.vpc == v
				}
			}
			if match {
				body += g.xml()
			}
		}
		reply(`<securityGroupInfo>` + body + `</securityGroupInfo>`)
		return true
	case "AuthorizeSecurityGroupIngress", "AuthorizeSecurityGroupEgress":
		g := f.firewalls[fmt.Sprint(input["GroupId"])]
		if g == nil {
			return false
		}
		f.calls[action]++
		if input["IpPermissions.1.IpProtocol"] != "-1" {
			f.t.Error("not all protocols")
		}
		v4 := input["IpPermissions.1.IpRanges.1.CidrIp"] == "0.0.0.0/0"
		v6 := input["IpPermissions.1.Ipv6Ranges.1.CidrIpv6"] == "::/0"
		if action == "AuthorizeSecurityGroupIngress" {
			g.in4 = g.in4 || v4
			g.in6 = g.in6 || v6
		} else {
			g.out4 = g.out4 || v4
			g.out6 = g.out6 || v6
		}
		if f.mode == "lost_firewall" || f.mode == "lost_ingress" {
			w.WriteHeader(502)
			return true
		}
		reply(`<return>true</return>`)
		return true
	case "DescribeNetworkInterfaces", "ModifyNetworkInterfaceAttribute":
		eni, _ := input["NetworkInterfaceId.1"].(string)
		if action == "ModifyNetworkInterfaceAttribute" {
			eni, _ = input["NetworkInterfaceId"].(string)
		}
		v := f.instances[scope+"/i-"+strings.TrimPrefix(eni, "eni-")]
		if v == nil {
			reject("InvalidNetworkInterfaceID.NotFound")
			return true
		}
		if action == "ModifyNetworkInterfaceAttribute" {
			f.calls[action]++
			v.groups = nil
			for i := 1; i < 6; i++ {
				g, _ := input[fmt.Sprintf("SecurityGroupId.%d", i)].(string)
				if g != "" {
					v.groups = append(v.groups, g)
				}
			}
			if len(v.groups) == 0 {
				f.t.Error("empty group assignment")
			}
			if f.mode == "lost_firewall" || f.mode == "lost_ingress" {
				w.WriteHeader(502)
				return true
			}
		}
		groups := ""
		for _, g := range v.groups {
			groups += `<item><groupId>` + g + `</groupId></item>`
		}
		if groups == "" {
			groups = `<item><groupId>sg-11111111</groupId></item>`
		}
		ip := "10.0.0.10"
		if v.labels["aws-panel:role"] == "ingress" {
			ip = "10.0.1.10"
		}
		reply(`<networkInterfaceSet><item><networkInterfaceId>` + eni + `</networkInterfaceId><vpcId>vpc-11111111</vpcId><ownerId>` + strings.Split(scope, "/")[0] + `</ownerId><privateIpAddress>` + ip + `</privateIpAddress><groupSet>` + groups + `</groupSet><attachment><instanceId>` + v.id + `</instanceId><deviceIndex>0</deviceIndex></attachment></item></networkInterfaceSet><return>true</return>`)
		return true
	}
	return false
}
