package integration

import (
	"context"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/herta-network/aws-panel/internal/module/compute"
	cp "github.com/herta-network/aws-panel/internal/module/compute/contract"
	"net/http"
	"strings"
)

type transitGroup struct {
	noEgress             bool
	id, scope, vpc, cidr string
	labels               map[string]string
}

func (f *computeAWS) ObserveIngress(ctx context.Context, cfg aws.Config, kind string, n cp.IngressState, tags map[string]string) (cp.IngressState, bool, error) {
	return compute.NewCloud().(compute.IngressCloud).ObserveIngress(ctx, addressConfig(cfg), kind, n, tags)
}
func (f *computeAWS) ConfigureIngress(ctx context.Context, cfg aws.Config, kind string, n cp.IngressState, tags map[string]string) (compute.Accepted, error) {
	return compute.NewCloud().(compute.IngressCloud).ConfigureIngress(ctx, addressConfig(cfg), kind, n, tags)
}
func (f *computeAWS) ingressHandle(w http.ResponseWriter, scope, action string, input map[string]any, reply func(string), reject func(string)) bool {
	switch action {
	case "DescribeInstanceAttribute", "ModifyInstanceAttribute":
		id, _ := input["InstanceId"].(string)
		v := f.instances[scope+"/"+id]
		if v == nil {
			reject("InvalidInstanceID.NotFound")
			return true
		}
		if action == "ModifyInstanceAttribute" {
			f.calls[action]++
			if input["SourceDestCheck.Value"] != "false" {
				f.t.Error("incorrect forwarding flag")
			}
			v.forwarding = true
			if f.mode == "lost_ingress" {
				w.WriteHeader(502)
				return true
			}
		}
		reply(fmt.Sprintf(`<instanceId>%s</instanceId><sourceDestCheck><value>%t</value></sourceDestCheck><return>true</return>`, id, !v.forwarding))
		return true
	case "CreateSecurityGroup":
		labels := queryLabels(input, "security-group")
		if labels["aws-panel:role"] != "ingress-transit" {
			return false
		}
		f.calls[action]++
		f.next++
		id := fmt.Sprintf("sg-%08x", f.next)
		f.transit[id] = &transitGroup{id: id, scope: scope, vpc: fmt.Sprint(input["VpcId"]), labels: labels}
		if f.mode == "lost_ingress" {
			w.WriteHeader(502)
			return true
		}
		reply(`<groupId>` + id + `</groupId>`)
		return true
	case "RevokeSecurityGroupEgress":
		g := f.transit[fmt.Sprint(input["GroupId"])]
		if g == nil {
			return false
		}
		f.calls[action]++
		g.noEgress = true
		if f.mode == "lost_ingress" {
			w.WriteHeader(502)
			return true
		}
		reply(`<return>true</return>`)
		return true
	case "AuthorizeSecurityGroupIngress":
		g := f.transit[fmt.Sprint(input["GroupId"])]
		if g == nil {
			return false
		}
		f.calls[action]++
		if input["IpPermissions.1.IpProtocol"] != "-1" {
			f.t.Error("incorrect relay trust rule")
		}
		g.cidr = fmt.Sprint(input["IpPermissions.1.IpRanges.1.CidrIp"])
		if f.mode == "lost_ingress" {
			w.WriteHeader(502)
			return true
		}
		reply(`<return>true</return>`)
		return true
	case "DescribeSecurityGroups":
		id, _ := input["GroupId.1"].(string)
		isTransit := f.transit[id] != nil
		for i := 1; i < 10; i++ {
			isTransit = isTransit || input[fmt.Sprintf("Filter.%d.Name", i)] == "tag:aws-panel:bundle"
		}
		if !isTransit {
			return false
		}
		var body strings.Builder
		for _, g := range f.transit {
			if g.scope != scope || id != "" && g.id != id {
				continue
			}
			match := true
			for i := 1; i < 10; i++ {
				key, _ := input[fmt.Sprintf("Filter.%d.Name", i)].(string)
				if strings.HasPrefix(key, "tag:") {
					match = match && g.labels[strings.TrimPrefix(key, "tag:")] == fmt.Sprint(input[fmt.Sprintf("Filter.%d.Value.1", i)])
				}
			}
			if !match {
				continue
			}
			rules := ""
			if g.cidr != "" {
				rules = `<item><ipProtocol>-1</ipProtocol><ipRanges><item><cidrIp>` + g.cidr + `</cidrIp></item></ipRanges></item>`
			}
			egress := ""
			if !g.noEgress {
				egress = `<item><ipProtocol>-1</ipProtocol><ipRanges><item><cidrIp>0.0.0.0/0</cidrIp></item></ipRanges></item>`
			}
			fmt.Fprintf(&body, `<item><groupId>%s</groupId><vpcId>%s</vpcId><ownerId>%s</ownerId><ipPermissions>%s</ipPermissions><ipPermissionsEgress>%s</ipPermissionsEgress>%s</item>`, g.id, g.vpc, strings.Split(scope, "/")[0], rules, egress, xmlTags(g.labels))
		}
		reply(`<securityGroupInfo>` + body.String() + `</securityGroupInfo>`)
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
			if len(v.groups) != 2 {
				f.t.Errorf("wrong groups %#v", input)
			}
			if f.mode == "lost_ingress" {
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
		reply(`<networkInterfaceSet><item><networkInterfaceId>` + eni + `</networkInterfaceId><vpcId>vpc-11111111</vpcId><ownerId>` + strings.Split(scope, "/")[0] + `</ownerId><privateIpAddress>10.0.0.10</privateIpAddress><groupSet>` + groups + `</groupSet><attachment><instanceId>` + v.id + `</instanceId><deviceIndex>0</deviceIndex></attachment></item></networkInterfaceSet><return>true</return>`)
		return true
	}
	return false
}
