package integration

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"github.com/Ember-Moth/aws-panel/internal/auth"
	"github.com/Ember-Moth/aws-panel/internal/module/addresses"
	"github.com/Ember-Moth/aws-panel/internal/module/compute"
	cp "github.com/Ember-Moth/aws-panel/internal/module/compute/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/inventory"
	"github.com/Ember-Moth/aws-panel/internal/module/operations"
	oc "github.com/Ember-Moth/aws-panel/internal/module/operations/contract"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/google/uuid"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type computeInstance struct {
	forwarding                                                                         bool
	groups                                                                             []string
	zone, carrierIP                                                                    string
	lifecycle, reservation                                                             string
	id, scope, state, name, image, kind, subnet, token, userData, stamp, key, publicIP string
	ipv6                                                                               bool
	labels                                                                             map[string]string
}
type computeIP struct {
	border                               string
	carrier                              bool
	pool                                 string
	id, scope, ip, instance, association string
	labels                               map[string]string
}
type computeAWS struct {
	restrictiveBootstrap bool
	firewalls            map[string]*computeFirewall
	transit              map[string]*transitGroup
	launches             []map[string]any
	wavelength           bool
	wavelengthEnabled    bool
	networks             map[string]*computeNetwork
	mu                   sync.Mutex
	t                    *testing.T
	instances            map[string]*computeInstance
	ips                  map[string]*computeIP
	calls                map[string]int
	mode                 string
	quota                float64
	next                 int
	noIPv6               bool
	lastLaunch           map[string]any
}

func newComputeAWS(t *testing.T) *computeAWS {
	return &computeAWS{firewalls: map[string]*computeFirewall{}, transit: map[string]*transitGroup{}, networks: map[string]*computeNetwork{}, t: t, instances: map[string]*computeInstance{}, ips: map[string]*computeIP{}, calls: map[string]int{}, quota: 100, next: 10}
}
func (f *computeAWS) Catalogue(c context.Context, cfg aws.Config) (cp.Catalogue, error) {
	return compute.NewCloud().Catalogue(c, addressConfig(cfg))
}
func (f *computeAWS) Instances(c context.Context, cfg aws.Config, a string) ([]cp.Instance, error) {
	return compute.NewCloud().Instances(c, addressConfig(cfg), a)
}
func (f *computeAWS) Instance(c context.Context, cfg aws.Config, a, id string) (*cp.Instance, error) {
	return compute.NewCloud().Instance(c, addressConfig(cfg), a, id)
}
func (f *computeAWS) FindCreated(c context.Context, cfg aws.Config, a, token string) ([]cp.Instance, error) {
	return compute.NewCloud().FindCreated(c, addressConfig(cfg), a, token)
}
func (f *computeAWS) Launch(c context.Context, cfg aws.Config, s cp.Spec, token, data string, labels map[string]string) (compute.Accepted, error) {
	return compute.NewCloud().Launch(c, addressConfig(cfg), s, token, data, labels)
}
func (f *computeAWS) Manage(c context.Context, cfg aws.Config, action string, v cp.Instance) (compute.Accepted, error) {
	return compute.NewCloud().Manage(c, addressConfig(cfg), action, v)
}
func (f *computeAWS) Quota(c context.Context, cfg aws.Config, a, kind string) (cp.Quota, error) {
	return compute.NewCloud().Quota(c, addressConfig(cfg), a, kind)
}
func xmlText(v string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(v))
	return b.String()
}
func xmlTags(labels map[string]string) string {
	var b strings.Builder
	keys := []string{}
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "<item><key>%s</key><value>%s</value></item>", xmlText(k), xmlText(labels[k]))
	}
	return "<tagSet>" + b.String() + "</tagSet>"
}
func queryLabels(input map[string]any, kind string) map[string]string {
	for n := 1; n < 8; n++ {
		prefix := fmt.Sprintf("TagSpecification.%d.", n)
		if input[prefix+"ResourceType"] != kind {
			continue
		}
		out := map[string]string{}
		for i := 1; i < 30; i++ {
			k, _ := input[fmt.Sprintf("%sTag.%d.Key", prefix, i)].(string)
			v, _ := input[fmt.Sprintf("%sTag.%d.Value", prefix, i)].(string)
			if k == "" {
				break
			}
			out[k] = v
		}
		return out
	}
	return map[string]string{}
}
func matches(input map[string]any, name, value string) bool {
	for n := 1; n < 20; n++ {
		if input[fmt.Sprintf("Filter.%d.Name", n)] == name {
			return input[fmt.Sprintf("Filter.%d.Value.1", n)] == value
		}
	}
	return true
}
func (v *computeInstance) xml(region string) string {
	publicIP := v.publicIP
	if publicIP == "" {
		publicIP = "127.0.2.10"
	}
	ipv6 := ""
	if v.ipv6 {
		ipv6 = "<ipv6AddressesSet><item><ipv6Address>2600:1f18:1::10</ipv6Address></item></ipv6AddressesSet>"
	}
	value := fmt.Sprintf(`<item><instanceId>%s</instanceId><imageId>%s</imageId><instanceState><code>16</code><name>%s</name></instanceState><instanceType>%s</instanceType><launchTime>%s</launchTime><clientToken>%s</clientToken><instanceLifecycle>%s</instanceLifecycle><capacityReservationId>%s</capacityReservationId><keyName>%s</keyName><rootDeviceType>ebs</rootDeviceType><rootDeviceName>/dev/sda1</rootDeviceName><vpcId>vpc-11111111</vpcId><subnetId>%s</subnetId><privateIpAddress>10.0.0.10</privateIpAddress><ipAddress>%s</ipAddress><placement><availabilityZone>%sa</availabilityZone><tenancy>default</tenancy></placement><groupSet><item><groupId>sg-11111111</groupId><groupName>fixture-ssh</groupName></item></groupSet><blockDeviceMapping><item><deviceName>/dev/sda1</deviceName><ebs><volumeId>vol-11111111</volumeId><deleteOnTermination>true</deleteOnTermination></ebs></item></blockDeviceMapping><networkInterfaceSet><item><networkInterfaceId>eni-%s</networkInterfaceId><subnetId>%s</subnetId><vpcId>vpc-11111111</vpcId><privateIpAddress>10.0.0.10</privateIpAddress><attachment><deviceIndex>0</deviceIndex></attachment>%s</item></networkInterfaceSet>%s</item>`, v.id, v.image, v.state, v.kind, v.stamp, xmlText(v.token), v.lifecycle, v.reservation, xmlText(v.key), v.subnet, publicIP, region, strings.TrimPrefix(v.id, "i-"), v.subnet, ipv6, xmlTags(v.labels))
	if v.zone != "" {
		value = strings.ReplaceAll(value, "<availabilityZone>"+region+"a</availabilityZone>", "<availabilityZone>"+v.zone+"</availabilityZone>")
		value = strings.ReplaceAll(value, "<ipAddress>"+publicIP+"</ipAddress>", "")
	}
	if v.carrierIP != "" {
		value = strings.Replace(value, "</attachment>", "</attachment><association><carrierIp>"+v.carrierIP+"</carrierIp></association>", 1)
	}
	if v.labels["aws-panel:role"] == "ingress" {
		value = strings.ReplaceAll(value, "10.0.0.10", "10.0.1.10")
	}
	if len(v.groups) > 0 {
		groups := ""
		for _, g := range v.groups {
			groups += "<item><groupId>" + g + "</groupId></item>"
		}
		start := strings.Index(value, "<groupSet>")
		end := strings.Index(value, "</groupSet>") + len("</groupSet>")
		value = value[:start] + "<groupSet>" + groups + "</groupSet>" + value[end:]
	}
	return value
}
func (f *computeAWS) handle(w http.ResponseWriter, r *http.Request, account, region, service string, input map[string]any) bool {
	if service != "ec2" && service != "servicequotas" {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	scope := account + "/" + region
	action, _ := input["Action"].(string)
	if service == "servicequotas" {
		if f.mode == "quota_denied" {
			w.WriteHeader(403)
			_ = json.NewEncoder(w).Encode(map[string]any{"__type": "AccessDeniedException"})
			return true
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Quotas": []any{map[string]any{"QuotaName": "VPCs per Region", "Value": 5, "GlobalQuota": false}, map[string]any{"QuotaName": "Internet gateways per Region", "Value": 5, "GlobalQuota": false}, map[string]any{"QuotaName": "Running On-Demand Standard (A, C, D, H, I, M, R, T, Z) instances", "QuotaCode": "L-1216C47A", "Value": f.quota, "GlobalQuota": false, "Unit": "vCPU"}, map[string]any{"QuotaName": "EC2-VPC Elastic IPs", "QuotaCode": "L-0263D0A3", "Value": 5, "GlobalQuota": false}}})
		return true
	}
	w.Header().Set("Content-Type", "text/xml")
	reply := func(body string) {
		fmt.Fprintf(w, `<%sResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><requestId>fixture-ec2-request</requestId>%s</%sResponse>`, action, body, action)
	}
	reject := func(code string) {
		w.WriteHeader(400)
		fmt.Fprintf(w, `<Response><Errors><Error><Code>%s</Code><Message>fixture EC2 failure</Message></Error></Errors><RequestID>fixture-ec2-rejected</RequestID></Response>`, code)
	}
	if f.firewallHandle(w, scope, action, input, reply, reject) {
		return true
	}
	if f.ingressHandle(w, scope, action, input, reply, reject) {
		return true
	}
	if f.networkHandle(w, scope, action, input, reply, reject) {
		return true
	}

	switch action {
	case "DescribeAvailabilityZones":
		if input["Filter.1.Value.1"] == "wavelength-zone" {
			if input["AllAvailabilityZones"] != "true" {
				f.t.Error("missing all zones for opt-in")
			}
			state := "not-opted-in"
			if f.wavelengthEnabled {
				state = "opted-in"
			}
			extra := ""
			if f.mode == "wavelength_group_changed" {
				extra = wavelengthZoneXML("use1-wl1-new-wlz1", "us-east-1-wl1-new-wlz-1", state, region)
			}
			reply(`<availabilityZoneInfo>` + wavelengthZoneXML(wavelengthZoneID, wavelengthZoneName, state, region) + wavelengthZoneXML("use1-wl1-nyc-wlz1", "us-east-1-wl1-nyc-wlz-1", state, region) + extra + `</availabilityZoneInfo>`)
			break
		}
		reply(`<availabilityZoneInfo><item><zoneName>` + region + `a</zoneName><regionName>` + region + `</regionName><zoneType>availability-zone</zoneType><zoneState>available</zoneState><networkBorderGroup>` + region + `</networkBorderGroup></item></availabilityZoneInfo>`)
	case "ModifyAvailabilityZoneGroup":
		f.calls[action]++
		if input["GroupName"] != "us-east-1-wl1" || input["OptInStatus"] != "opted-in" {
			f.t.Error("wrong opt-in scope or status")
		}
		if f.mode == "reject_wavelength" {
			reject("UnauthorizedOperation")
			break
		}
		if f.mode != "wavelength_never_accepted" {
			f.wavelengthEnabled = true
		}
		if f.mode == "lost_wavelength" || f.mode == "wavelength_never_accepted" {
			w.WriteHeader(502)
			break
		}
		reply(`<return>true</return>`)
	case "DescribeImages":
		owner, _ := input["Owner.1"].(string)
		if owner != "136693071363" {
			reply(`<imagesSet/>`)
			break
		}
		reply(`<imagesSet><item><imageId>ami-11111111</imageId><name>debian-13-amd64-20261001</name><imageOwnerId>136693071363</imageOwnerId><imageState>available</imageState><rootDeviceType>ebs</rootDeviceType><virtualizationType>hvm</virtualizationType><architecture>x86_64</architecture><creationDate>2026-10-01T00:00:00Z</creationDate><rootDeviceName>/dev/sda1</rootDeviceName><blockDeviceMapping><item><deviceName>/dev/sda1</deviceName><ebs><volumeSize>8</volumeSize></ebs></item></blockDeviceMapping></item></imagesSet>`)
	case "DescribeInstanceTypeOfferings":
		if input["LocationType"] == "availability-zone-id" {
			if input["Filter.1.Name"] != "location" || input["Filter.1.Value.1"] != wavelengthZoneID {
				f.t.Error("offering not scoped to selected Zone ID")
			}
			reply(`<instanceTypeOfferingSet><item><instanceType>t3.micro</instanceType><location>` + wavelengthZoneID + `</location><locationType>availability-zone-id</locationType></item></instanceTypeOfferingSet>`)
			break
		}
		reply(`<instanceTypeOfferingSet><item><instanceType>t3.micro</instanceType><location>` + region + `</location><locationType>region</locationType></item></instanceTypeOfferingSet>`)
	case "DescribeInstanceTypes":
		reply(`<instanceTypeSet><item><instanceType>t3.micro</instanceType><vCpuInfo><defaultVCpus>2</defaultVCpus></vCpuInfo><memoryInfo><sizeInMiB>1024</sizeInMiB></memoryInfo><processorInfo><supportedArchitectures><item>x86_64</item></supportedArchitectures></processorInfo><networkInfo><ipv6Supported>true</ipv6Supported></networkInfo></item></instanceTypeSet>`)
	case "DescribeKeyPairs":
		reply(`<keySet><item><keyName>fixture-key</keyName><keyPairId>key-11111111</keyPairId></item></keySet>`)
	case "DescribeRouteTables":
		if f.wavelength {
			reply(`<routeTableSet><item><routeTableId>rtb-11111111</routeTableId><vpcId>vpc-11111111</vpcId><associationSet><item><main>true</main></item></associationSet><routeSet><item><destinationCidrBlock>0.0.0.0/0</destinationCidrBlock><carrierGatewayId>cagw-11111111</carrierGatewayId><state>active</state></item></routeSet></item><item><routeTableId>rtb-33333333</routeTableId><vpcId>vpc-11111111</vpcId><associationSet><item><subnetId>subnet-33333333</subnetId></item></associationSet><routeSet><item><destinationCidrBlock>0.0.0.0/0</destinationCidrBlock><gatewayId>igw-33333333</gatewayId><state>active</state></item></routeSet></item></routeTableSet>`)
			break
		}
		reply(`<routeTableSet><item><routeTableId>rtb-11111111</routeTableId><vpcId>vpc-11111111</vpcId><associationSet><item><main>true</main></item></associationSet><routeSet><item><destinationCidrBlock>0.0.0.0/0</destinationCidrBlock><gatewayId>igw-11111111</gatewayId><state>active</state></item><item><destinationIpv6CidrBlock>::/0</destinationIpv6CidrBlock><gatewayId>igw-11111111</gatewayId><state>active</state></item></routeSet></item></routeTableSet>`)
	case "DescribeSubnets":
		if f.wavelength {
			reply(`<subnetSet><item><subnetId>subnet-11111111</subnetId><vpcId>vpc-11111111</vpcId><state>available</state><availabilityZone>` + wavelengthZoneName + `</availabilityZone><availabilityZoneId>` + wavelengthZoneID + `</availabilityZoneId><cidrBlock>10.0.0.0/24</cidrBlock><availableIpAddressCount>200</availableIpAddressCount></item><item><subnetId>subnet-33333333</subnetId><vpcId>vpc-11111111</vpcId><state>available</state><availabilityZone>` + region + `a</availabilityZone><cidrBlock>10.0.1.0/24</cidrBlock><availableIpAddressCount>200</availableIpAddressCount></item></subnetSet>`)
			break
		}
		cidr := ""
		if !f.noIPv6 {
			cidr = `<ipv6CidrBlockAssociationSet><item><ipv6CidrBlock>2600:1f18:1::/64</ipv6CidrBlock><ipv6CidrBlockState><state>associated</state></ipv6CidrBlockState></item></ipv6CidrBlockAssociationSet>`
		}
		reply(`<subnetSet><item><subnetId>subnet-11111111</subnetId><vpcId>vpc-11111111</vpcId><state>available</state><availabilityZone>` + region + `a</availabilityZone><cidrBlock>10.0.0.0/24</cidrBlock><availableIpAddressCount>200</availableIpAddressCount>` + cidr + `</item></subnetSet>`)
	case "DescribeSecurityGroups":
		if f.restrictiveBootstrap {
			reply(`<securityGroupInfo><item><groupId>sg-11111111</groupId><groupName>default</groupName><vpcId>vpc-11111111</vpcId><ipPermissions/></item>` + f.firewallGroupsXML(scope) + `</securityGroupInfo>`)
			break
		}
		reply(`<securityGroupInfo><item><groupId>sg-11111111</groupId><groupName>fixture-ssh</groupName><vpcId>vpc-11111111</vpcId><ipPermissions><item><ipProtocol>tcp</ipProtocol><fromPort>22</fromPort><toPort>22</toPort><ipRanges><item><cidrIp>10.0.0.0/8</cidrIp></item></ipRanges><ipv6Ranges><item><cidrIpv6>2600:1f18::/32</cidrIpv6></item></ipv6Ranges></item></ipPermissions></item>` + f.firewallGroupsXML(scope) + `</securityGroupInfo>`)
	case "DescribeCapacityReservations":
		if f.mode == "capacity" {
			reply(`<capacityReservationSet><item><capacityReservationId>cr-11111111</capacityReservationId><ownerId>` + account + `</ownerId><state>active</state><instanceType>t3.micro</instanceType><totalInstanceCount>2</totalInstanceCount><instancePlatform>Linux/UNIX</instancePlatform></item></capacityReservationSet>`)
		} else {
			reply(`<capacityReservationSet/>`)
		}
	case "DescribeInstances":
		if f.mode == "read_denied" {
			reject("UnauthorizedOperation")
			break
		}
		var b strings.Builder
		for _, v := range f.instances {
			if v.scope != scope {
				continue
			}
			if id, _ := input["InstanceId.1"].(string); id != "" && id != v.id {
				continue
			}
			if !matches(input, "client-token", v.token) || !matches(input, "instance-state-name", v.state) {
				continue
			}
			b.WriteString(v.xml(region))
		}
		reply(`<reservationSet><item><ownerId>` + account + `</ownerId><instancesSet>` + b.String() + `</instancesSet></item></reservationSet>`)
	case "RunInstances":
		f.calls[action]++
		f.lastLaunch = input
		f.launches = append(f.launches, input)
		if f.mode == "reject_launch" {
			reject("VcpuLimitExceeded")
			break
		}
		if f.mode == "never_accepted" {
			w.WriteHeader(502)
			break
		}
		token, _ := input["ClientToken"].(string)
		var v *computeInstance
		for _, a := range f.instances {
			if a.scope == scope && a.token == token {
				v = a
			}
		}
		if v == nil {
			f.next++
			id := fmt.Sprintf("i-%08x", f.next)
			labels := queryLabels(input, "instance")
			if f.mode == "legacy_launch" {
				delete(labels, "aws-panel:bundle")
			}
			v = &computeInstance{id: id, scope: scope, state: "running", name: labels["Name"], image: fmt.Sprint(input["ImageId"]), kind: fmt.Sprint(input["InstanceType"]), subnet: fmt.Sprint(input["NetworkInterface.1.SubnetId"]), token: token, labels: labels, stamp: time.Now().UTC().Format(time.RFC3339), ipv6: input["NetworkInterface.1.Ipv6AddressCount"] == "1"}
			if f.wavelength && v.subnet == "subnet-11111111" {
				v.zone = wavelengthZoneName
			}
			for j := 1; j <= 5; j++ {
				if g, ok := input[fmt.Sprintf("NetworkInterface.1.SecurityGroupId.%d", j)].(string); ok {
					v.groups = append(v.groups, g)
				}
			}
			v.key, _ = input["KeyName"].(string)
			if data, ok := input["UserData"].(string); ok {
				decoded, err := base64.StdEncoding.DecodeString(data)
				if err != nil {
					f.t.Error("EC2 user data not base64 encoded")
				}
				v.userData = string(decoded)
			}
			f.instances[scope+"/"+id] = v
		}
		if f.mode == "lost_launch" || f.mode == "legacy_launch" {
			w.WriteHeader(502)
			break
		}
		reply(`<reservationId>r-11111111</reservationId><ownerId>` + account + `</ownerId><instancesSet>` + v.xml(region) + `</instancesSet>`)
	case "StartInstances", "StopInstances", "RebootInstances", "TerminateInstances", "AssignIpv6Addresses":
		f.calls[action]++
		id, _ := input["InstanceId.1"].(string)
		if action == "AssignIpv6Addresses" {
			id = "i-" + strings.TrimPrefix(fmt.Sprint(input["NetworkInterfaceId"]), "eni-")
		}
		v := f.instances[scope+"/"+id]
		if v == nil {
			reject("InvalidInstanceID.NotFound")
			break
		}
		switch action {
		case "StartInstances":
			v.state = "running"
		case "StopInstances":
			v.state = "stopped"
		case "TerminateInstances":
			v.state = "terminated"
			v.publicIP = ""
			v.carrierIP = ""
			for _, a := range f.ips {
				if a.scope == scope && a.instance == v.id {
					a.instance = ""
					a.association = ""
				}
			}
			v.subnet = ""
		case "AssignIpv6Addresses":
			v.ipv6 = true
		}
		if f.mode == "lost_manage" {
			w.WriteHeader(502)
			break
		}
		reply(`<return>true</return>`)
	case "DescribeAddresses":
		var b strings.Builder
		for _, a := range f.ips {
			if a.scope != scope {
				continue
			}
			if id, _ := input["AllocationId.1"].(string); id != "" && id != a.id {
				continue
			}
			valid := true
			for n := 1; n < 20; n++ {
				key, _ := input[fmt.Sprintf("Filter.%d.Name", n)].(string)
				if strings.HasPrefix(key, "tag:") {
					want, _ := input[fmt.Sprintf("Filter.%d.Value.1", n)].(string)
					if a.labels[strings.TrimPrefix(key, "tag:")] != want {
						valid = false
					}
				}
			}

			if !valid {
				continue
			}
			eni := ""
			priv := ""
			if a.instance != "" {
				eni = "eni-" + strings.TrimPrefix(a.instance, "i-")
				priv = "10.0.0.10"
				if v := f.instances[scope+"/"+a.instance]; v != nil && v.labels["aws-panel:role"] == "ingress" {
					priv = "10.0.1.10"
				}
			}
			ipField := "publicIp"
			if a.carrier {
				ipField = "carrierIp"
			}
			border := a.border
			if border == "" {
				border = region
			}
			fmt.Fprintf(&b, `<item><allocationId>%s</allocationId><`+ipField+`>%s</`+ipField+`><domain>vpc</domain><networkBorderGroup>%s</networkBorderGroup><instanceId>%s</instanceId><networkInterfaceId>%s</networkInterfaceId><privateIpAddress>%s</privateIpAddress><associationId>%s</associationId><publicIpv4Pool>%s</publicIpv4Pool>%s</item>`, a.id, a.ip, border, a.instance, eni, priv, a.association, a.pool, xmlTags(a.labels))
		}
		reply(`<addressesSet>` + b.String() + `</addressesSet>`)
	case "AllocateAddress":
		f.calls[action]++
		f.next++
		id := fmt.Sprintf("eipalloc-%08x", f.next)
		a := &computeIP{id: id, scope: scope, ip: "127.0.3." + strconv.Itoa(f.next), labels: queryLabels(input, "elastic-ip")}
		a.border, _ = input["NetworkBorderGroup"].(string)
		a.carrier = a.border != "" && a.border != region
		f.ips[scope+"/"+id] = a
		if f.mode == "lost_addresses" {
			w.WriteHeader(502)
			break
		}
		reply(`<allocationId>` + id + `</allocationId><publicIp>` + a.ip + `</publicIp><domain>vpc</domain>`)
	case "AssociateAddress":
		f.calls[action]++
		id, _ := input["AllocationId"].(string)
		a := f.ips[scope+"/"+id]
		if a == nil {
			reject("InvalidAllocationID.NotFound")
			break
		}
		if input["AllowReassociation"] != "false" {
			f.t.Error("EIP reassociation was not guarded")
		}
		if f.mode == "attach_rejected" {
			reject("UnauthorizedOperation")
			break
		}
		instance, _ := input["InstanceId"].(string)
		if instance == "" {
			instance = "i-" + strings.TrimPrefix(fmt.Sprint(input["NetworkInterfaceId"]), "eni-")
		}
		for _, b := range f.ips {
			if b.scope == scope && b.instance == instance {
				b.instance = ""
				b.association = ""
			}
		}
		a.instance = instance
		if v := f.instances[scope+"/"+instance]; v != nil {
			if a.carrier {
				v.carrierIP = a.ip
			} else {
				v.publicIP = a.ip
			}
		}
		a.association = "eipassoc-" + strings.TrimPrefix(a.id, "eipalloc-")
		if f.mode == "lost_addresses" {
			w.WriteHeader(502)
			break
		}
		reply(`<associationId>` + a.association + `</associationId>`)
	case "DisassociateAddress":
		f.calls[action]++
		for _, a := range f.ips {
			if a.scope == scope && a.association == input["AssociationId"] {
				if v := f.instances[scope+"/"+a.instance]; v != nil && a.carrier {
					v.carrierIP = ""
				}
				a.instance = ""
				a.association = ""
			}
		}
		reply(`<return>true</return>`)
	case "ReleaseAddress":
		f.calls[action]++
		id, _ := input["AllocationId"].(string)
		delete(f.ips, scope+"/"+id)
		if f.mode == "lost_addresses" {
			w.WriteHeader(502)
			break
		}
		reply(`<return>true</return>`)
	default:
		f.t.Errorf("unexpected EC2 action %s", action)
		reject("InvalidAction")
	}
	return true
}

type computeHarness struct {
	*inventoryHarness
	cloudCompute     *computeAWS
	serviceCompute   compute.Service
	serviceAddresses addresses.Service
}

func newCompute(t *testing.T, enabled bool) *computeHarness {
	h := newInventory(t)
	cloud := newComputeAWS(t)
	h.cloud.extra = cloud.handle
	effects := operations.NewEffectStore(h.runtime)
	addressCloud := newAddressAWS()
	jobs, err := operations.New(operations.Deps{Store: h.op.store, Access: h.op.members, Connections: inventoryBindings{h.accounts}, Publisher: h.op.queue, CloudWritesEnabled: enabled, Executors: []oc.Executor{inventory.NewExecutor(inventory.ExecutorDeps{Store: h.store, Clients: h.accounts, Cloud: h.cloud}), compute.NewExecutor(compute.ExecutorDeps{Clients: h.accounts, LaunchSecrets: h.accounts, Cloud: cloud, AddressCloud: addressCloud, Effects: effects}), addresses.NewExecutor(addresses.ExecutorDeps{Clients: h.accounts, Cloud: addressCloud, Effects: effects})}, RedeliveryAfter: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	h.op.service = jobs
	h.service = inventory.New(inventory.Deps{Store: h.store, Accounts: h.accounts, Access: h.op.members, Jobs: jobs, Observer: operations.NewObserver(h.op.store)})
	return &computeHarness{inventoryHarness: h, cloudCompute: cloud, serviceCompute: compute.New(compute.Deps{Effects: effects, Accounts: h.accounts, Access: h.op.members, Jobs: jobs, Cloud: cloud, WritesEnabled: enabled}), serviceAddresses: addresses.New(addresses.Deps{Accounts: h.accounts, Access: h.op.members, Jobs: jobs, Cloud: addressCloud, WritesEnabled: enabled})}
}
func (h *computeHarness) target() cp.InputTarget {
	return cp.InputTarget{Target: cp.Target{ConnectionID: h.connectionA.ID, Region: "us-east-1", Action: "create", Name: "compute-fixture", ImageID: "ami-11111111", InstanceType: "t3.micro", SubnetID: "subnet-11111111", SecurityGroupIDs: []string{"sg-11111111"}, DiskGB: 16, PublicIPv4: true, IPv6: true, LoginMethod: "ssh_key", KeyPairName: "fixture-key"}}
}
func (h *computeHarness) submit(t *testing.T, v cp.InputTarget) oc.Job {
	t.Helper()
	plan, err := h.serviceCompute.Prepare(context.Background(), h.op.admin, cp.Request{Targets: []cp.InputTarget{v}})
	if err != nil {
		t.Fatal(err)
	}
	job, err := h.serviceCompute.Submit(context.Background(), h.op.admin, plan.ID, uuid.NewString(), auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	return job
}
func (h *computeHarness) tick(t *testing.T, id string) {
	t.Helper()
	h.op.expedite(t, id)
	if err := h.op.service.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := h.op.service.Publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, m := range h.op.queue.take() {
		if err := h.op.service.Run(context.Background(), m); err != nil {
			t.Fatal(err)
		}
	}
}

func (f *computeAWS) Network(c context.Context, cfg aws.Config, phase string, n cp.Network, labels map[string]string) (cp.Network, bool, error) {
	return compute.NewCloud().Network(c, addressConfig(cfg), phase, n, labels)
}
func (f *computeAWS) Provision(c context.Context, cfg aws.Config, phase string, n cp.Network, labels map[string]string) (compute.Accepted, error) {
	return compute.NewCloud().Provision(c, addressConfig(cfg), phase, n, labels)
}
func (f *computeAWS) NetworkQuotas(c context.Context, cfg aws.Config, account string) ([]cp.Quota, error) {
	return compute.NewCloud().NetworkQuotas(c, addressConfig(cfg), account)
}

const wavelengthZoneID = "use1-wl1-bos-wlz1"
const wavelengthZoneName = "us-east-1-wl1-bos-wlz-1"

func wavelengthZoneXML(id, name, status, region string) string {
	return `<item><zoneId>` + id + `</zoneId><zoneName>` + name + `</zoneName><regionName>` + region + `</regionName><zoneType>wavelength-zone</zoneType><zoneState>available</zoneState><groupName>us-east-1-wl1</groupName><networkBorderGroup>` + name + `</networkBorderGroup><optInStatus>` + status + `</optInStatus></item>`
}
func (f *computeAWS) WavelengthZones(ctx context.Context, cfg aws.Config) ([]cp.WavelengthZone, error) {
	return compute.NewCloud().(compute.WavelengthCloud).WavelengthZones(ctx, addressConfig(cfg))
}
func (f *computeAWS) WavelengthCatalogue(ctx context.Context, cfg aws.Config, id string) (cp.Catalogue, error) {
	return compute.NewCloud().(compute.WavelengthCloud).WavelengthCatalogue(ctx, addressConfig(cfg), id)
}
func (f *computeAWS) EnableWavelength(ctx context.Context, cfg aws.Config, group string) (compute.Accepted, error) {
	return compute.NewCloud().(compute.WavelengthCloud).EnableWavelength(ctx, addressConfig(cfg), group)
}
