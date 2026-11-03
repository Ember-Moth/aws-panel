package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/Ember-Moth/aws-panel/internal/auth"
	"github.com/Ember-Moth/aws-panel/internal/infra/clock"
	"github.com/Ember-Moth/aws-panel/internal/module/addresses"
	"github.com/Ember-Moth/aws-panel/internal/module/compute/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/compute/internal/source"
	oc "github.com/Ember-Moth/aws-panel/internal/module/operations/contract"
	"github.com/Ember-Moth/aws-panel/internal/repository"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/google/uuid"
	"io"
	"net/netip"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

const OperationID = "ec2.manage"

var regionPattern = regexp.MustCompile(`^[a-z]{2}(?:-[a-z0-9]+)+-[0-9]+$`)
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)
var resourcePattern = regexp.MustCompile(`^(?:i|ami|subnet|sg)-[a-f0-9]{8,32}$`)
var wavelengthZonePattern = regexp.MustCompile(`^[a-z0-9-]{6,80}$`)
var typePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*\.[a-z0-9]+$`)

func validRegion(v string) bool {
	return len(v) <= 30 && regionPattern.MatchString(v) && !strings.HasPrefix(v, "cn-") && !strings.HasPrefix(v, "us-gov-")
}
func resource(v, prefix string) bool {
	return strings.HasPrefix(v, prefix+"-") && resourcePattern.MatchString(v)
}
func hash(v string) string { sum := sha256.Sum256([]byte(v)); return hex.EncodeToString(sum[:]) }
func raw(v any) []byte     { r, _ := json.Marshal(v); return r }
func normalize(values []contract.Target) ([]contract.Target, error) {
	if len(values) < 1 || len(values) > 50 {
		return nil, contract.ErrInvalid
	}
	out := append([]contract.Target{}, values...)
	for i := range out {
		v := &out[i]
		id, err := uuid.Parse(v.ConnectionID)
		if err != nil || !validRegion(v.Region) {
			return nil, contract.ErrInvalid
		}
		v.ConnectionID = id.String()
		if (v.IngressSubnetID != "" || v.IngressType != "") && (v.Action != "create" || v.WavelengthZoneID == "" || !resource(v.IngressSubnetID, "subnet") || !typePattern.MatchString(v.IngressType)) {
			return nil, contract.ErrInvalid
		}
		if v.WavelengthZoneID != "" && (!wavelengthZonePattern.MatchString(v.WavelengthZoneID) || v.IPv6 || v.PublicIPv4 || v.SSHIPv6CIDR != "" || v.Action == "enable_ipv6") {
			return nil, contract.ErrNetwork
		}
		if v.Action == "wavelength_opt_in" {
			copy := *v
			copy.ConnectionID = ""
			copy.Region = ""
			copy.Action = ""
			copy.WavelengthZoneID = ""
			if v.WavelengthZoneID == "" || !bytes.Equal(raw(copy), raw(contract.Target{})) {
				return nil, contract.ErrInvalid
			}
			continue
		}
		if v.Action == "network" {
			if !namePattern.MatchString(v.Name) || (v.WavelengthZoneID == "" && (!strings.HasPrefix(v.Zone, v.Region) || len(v.Zone) != len(v.Region)+1)) || (v.WavelengthZoneID != "" && v.Zone != "") || v.InstanceID != "" || v.ImageID != "" || v.InstanceType != "" || v.SubnetID != "" || len(v.SecurityGroupIDs) > 0 || v.DiskGB != 0 || v.PublicIPv4 || v.IPv6 || v.LoginMethod != "" || v.KeyPairName != "" || v.LaunchSecretID != "" {
				return nil, contract.ErrInvalid
			}
			if v.SSHIPv4CIDR == "" {
				v.SSHIPv4CIDR = "0.0.0.0/0"
			}
			if v.WavelengthZoneID == "" && v.SSHIPv6CIDR == "" {
				v.SSHIPv6CIDR = "::/0"
			}
			ipv4, e4 := netip.ParsePrefix(v.SSHIPv4CIDR)
			ipv6, e6 := netip.ParsePrefix(v.SSHIPv6CIDR)
			if e4 != nil || !ipv4.Addr().Is4() || (v.WavelengthZoneID == "" && (e6 != nil || !ipv6.Addr().Is6() || ipv6.Addr().Is4In6())) {
				return nil, contract.ErrNetwork
			}
			v.SSHIPv4CIDR = ipv4.Masked().String()
			if v.WavelengthZoneID == "" {
				v.SSHIPv6CIDR = ipv6.Masked().String()
			}
			continue
		}
		if v.Zone != "" || v.SSHIPv4CIDR != "" || v.SSHIPv6CIDR != "" {
			return nil, contract.ErrInvalid
		}
		if v.Action == "create" {
			if !namePattern.MatchString(v.Name) || !resource(v.ImageID, "ami") || !typePattern.MatchString(v.InstanceType) || !resource(v.SubnetID, "subnet") || v.InstanceID != "" || v.DiskGB < 8 || v.DiskGB > 16384 || len(v.SecurityGroupIDs) > 5 {
				return nil, contract.ErrInvalid
			}
			v.SecurityGroupIDs = append([]string{}, v.SecurityGroupIDs...)
			sort.Strings(v.SecurityGroupIDs)
			for j, g := range v.SecurityGroupIDs {
				if !resource(g, "sg") || (j > 0 && g == v.SecurityGroupIDs[j-1]) {
					return nil, contract.ErrInvalid
				}
			}
			if v.LoginMethod == "" {
				v.LoginMethod = "ssh_key"
			}
			switch v.LoginMethod {
			case "ssh_key":
				if v.KeyPairName == "" || len(v.KeyPairName) > 255 || v.LaunchSecretID != "" {
					return nil, contract.ErrLogin
				}
			case "root_password":
				if _, err := uuid.Parse(v.LaunchSecretID); err != nil || v.KeyPairName != "" {
					return nil, contract.ErrLogin
				}
			default:
				return nil, contract.ErrLogin
			}
		} else {
			if !resource(v.InstanceID, "i") || v.Name != "" || v.ImageID != "" || v.InstanceType != "" || v.SubnetID != "" || len(v.SecurityGroupIDs) > 0 || v.DiskGB != 0 || v.PublicIPv4 || v.IPv6 || v.LoginMethod != "" || v.KeyPairName != "" || v.LaunchSecretID != "" {
				return nil, contract.ErrInvalid
			}
			switch v.Action {
			case "start", "stop", "reboot", "terminate", "enable_ipv6":
			default:
				return nil, contract.ErrInvalid
			}
		}
	}
	return out, nil
}

type ExecutorDeps struct {
	AddressCloud  addresses.Cloud
	Clients       Clients
	LaunchSecrets LaunchSecrets
	Cloud         source.Cloud
	Effects       repository.EffectStore
}
type Executor struct{ deps ExecutorDeps }

func NewExecutor(d ExecutorDeps) *Executor { return &Executor{deps: d} }
func (*Executor) Definition() oc.Definition {
	return oc.Definition{ID: OperationID, Version: 1, InputVersion: 1, Title: "EC2 实例管理", WritesCloud: true, MaxAttempts: 90, Deadline: 45 * time.Minute, CallTimeout: 2 * time.Minute}
}
func (*Executor) Validate(input json.RawMessage) (json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(input))
	d.DisallowUnknownFields()
	var v contract.PlanInput
	if d.Decode(&v) != nil {
		return nil, contract.ErrInvalid
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return nil, contract.ErrInvalid
	}
	var err error
	v.Targets, err = normalize(v.Targets)
	if err != nil {
		return nil, err
	}
	return json.Marshal(v)
}
func selectCreate(v contract.Target, c contract.Catalogue) (contract.Spec, error) {
	return selectCreateWithFirewall(v, c, nil)
}
func selectCreateWithFirewall(v contract.Target, c contract.Catalogue, firewall *contract.FirewallPlan) (contract.Spec, error) {
	s := contract.Spec{Target: v, Firewall: firewall, WavelengthZone: c.WavelengthZone, CheckedAt: c.CheckedAt, Warnings: []string{"价格未锁定：按需实例、gp3 磁盘、公网 IPv4 与流量分别计费；提交前请核对 AWS 区域价格。", "网络 ACL、系统防火墙与真实 SSH/IPv6 连通性仍需启动后验证。"}}
	if v.WavelengthZoneID != "" {
		if c.WavelengthZone == nil || c.WavelengthZone.ID != v.WavelengthZoneID || c.WavelengthZone.OptInStatus != "opted-in" || v.IPv6 || v.PublicIPv4 {
			return s, contract.ErrNetwork
		}
		s.VolumeType = "gp2"
		s.Warnings = []string{"Wavelength 使用加密 gp2 根盘和 IPv4 私网；实例、磁盘、Carrier IP 和流量按 AWS 价格计费。", "自动配置父区域入口 EC2 与弹性 IP 承接公网入站，Wavelength 实例默认出站走 Carrier IP。"}
	}
	for _, a := range c.Images {
		if a.ID == v.ImageID {
			s.Image = &a
		}
	}
	for _, a := range c.Types {
		if a.ID == v.InstanceType {
			s.Type = &a
		}
	}
	for _, a := range c.Subnets {
		if a.ID == v.SubnetID {
			s.Subnet = &a
		}
	}
	if s.Image == nil || s.Type == nil || s.Subnet == nil || !slices.Contains(s.Type.Architectures, s.Image.Architecture) || v.DiskGB < s.Image.MinDiskGB {
		return s, contract.ErrCatalogue
	}
	if s.Subnet.AvailableIPs < 1 || (v.WavelengthZoneID != "" && s.Subnet.Zone != c.WavelengthZone.Name) || v.IPv6 && (!s.Type.IPv6 || s.Subnet.IPv6CIDR == "" || !s.Subnet.PublicIPv6Route) || v.PublicIPv4 && !s.Subnet.PublicIPv4Route {
		return s, contract.ErrNetwork
	}
	groups := append([]string{}, v.SecurityGroupIDs...)
	if len(groups) == 0 && firewall != nil {
		for _, g := range c.SecurityGroups {
			if g.VPCID == s.Subnet.VPCID && (len(groups) == 0 || g.Name == "default") {
				groups = []string{g.ID}
				if g.Name == "default" {
					break
				}
			}
		}
	}
	if len(groups) == 0 {
		return s, contract.ErrNetwork
	}
	if len(v.SecurityGroupIDs) == 0 {
		s.BootstrapGroups = groups
	}
	ipv4, ipv6 := false, false
	for _, id := range groups {
		found := false
		for _, g := range c.SecurityGroups {
			if g.ID == id && g.VPCID == s.Subnet.VPCID {
				found = true
				s.SecurityGroups = append(s.SecurityGroups, g)
				ipv4 = ipv4 || len(g.SSHIPv4) > 0
				ipv6 = ipv6 || len(g.SSHIPv6) > 0
			}
		}
		if !found {
			return s, contract.ErrNetwork
		}
	}
	if firewall == nil && (v.IPv6 && !ipv6 || v.PublicIPv4 && !ipv4) {
		return s, contract.ErrNetwork
	}

	if v.LoginMethod == "ssh_key" && !slices.Contains(c.KeyPairs, v.KeyPairName) {
		return s, contract.ErrLogin
	}
	if v.LoginMethod == "root_password" {
		s.Warnings = append(s.Warnings, "初始化将启用 root 密码 SSH 登录；初始化状态需在系统内核对。")
	}
	if firewall != nil {
		s.Warnings = append(s.Warnings, "自动创建并绑定实例专用安全组，所有来源、协议和端口的入站与出站全部放行；系统防火墙和网络 ACL 独立生效。")
	}
	return s, nil
}
func validAction(action string, v contract.Instance) bool {
	switch action {
	case "start":
		return v.State == "stopped" || v.State == "running"
	case "stop":
		return v.State == "running" || v.State == "stopped"
	case "reboot":
		return v.State == "running"
	case "terminate":
		return v.State == "running" || v.State == "stopped" || v.State == "pending" || v.State == "terminated"
	case "enable_ipv6":
		return (v.State == "running" || v.State == "stopped") && v.PrimaryENI != ""
	}
	return false
}
func ipv6Ready(v contract.Instance, c contract.Catalogue) bool {
	supported := false
	for _, t := range c.Types {
		if t.ID == v.Type {
			supported = t.IPv6
		}
	}
	subnet := false
	for _, s := range c.Subnets {
		if s.ID == v.SubnetID && s.VPCID == v.VPCID {
			subnet = s.IPv6CIDR != "" && s.PublicIPv6Route
		}
	}
	allowed := false
	for _, g := range c.SecurityGroups {
		if slices.Contains(v.SecurityGroupIDs, g.ID) && g.VPCID == v.VPCID {
			allowed = allowed || len(g.SSHIPv6) > 0
		}
	}
	return supported && subnet && allowed
}
func (e *Executor) Plan(ctx context.Context, input json.RawMessage, bindings []oc.Target) ([]oc.ItemPlan, error) {
	actor, ok := auth.Current(ctx)
	if !ok {
		return nil, contract.ErrForbidden
	}
	var req contract.PlanInput
	if json.Unmarshal(input, &req) != nil || len(req.Targets) != len(bindings) {
		return nil, contract.ErrInvalid
	}
	configs := map[string]aws.Config{}
	cats := map[string]contract.Catalogue{}
	lists := map[string][]contract.Instance{}
	budgets := map[string]contract.Quota{}
	required := map[string]float64{}
	locks := map[string]bool{}
	result := []oc.ItemPlan{}
	for i, v := range req.Targets {
		b := bindings[i]
		if b.Binding.Partition != "aws" || b.Binding.ConnectionID != v.ConnectionID || b.Binding.Region != v.Region {
			return nil, contract.ErrInvalid
		}
		group := v.ConnectionID + "/" + v.Region + "/" + v.WavelengthZoneID
		physical := b.Binding.AWSAccountID + "/" + v.Region
		cfg, ok := configs[group]
		if !ok {
			var err error
			cfg, err = e.deps.Clients.ConfigFor(ctx, actor.ID, v.ConnectionID, v.Region, true)
			if err != nil {
				return nil, publicError(err)
			}
			configs[group] = cfg
		}
		if v.Action == "wavelength_opt_in" {
			item, err := e.planWavelengthOptIn(ctx, cfg, b, v)
			if err != nil {
				return nil, err
			}
			key := physical + "/" + item.Target.LockKey
			if locks[key] {
				return nil, contract.ErrConflict
			}
			locks[key] = true
			result = append(result, item)
			continue
		}
		if v.Action == "network" {
			spec, err := e.planNetwork(ctx, cfg, b, v)
			if err != nil {
				return nil, err
			}
			lock := "ec2/network-name/" + hash(v.Name)
			if locks[physical+"/"+lock] {
				return nil, contract.ErrConflict
			}
			locks[physical+"/"+lock] = true
			for _, q := range spec.NetworkQuotas {
				key := physical + "/" + q.Key
				required[key]++
				if q.LimitKnown && q.UsageKnown {
					existing, ok := budgets[key]
					if !ok {
						budgets[key] = q
					} else {
						existing.Limit = min(existing.Limit, q.Limit)
						existing.Used = max(existing.Used, q.Used)
						budgets[key] = existing
					}
				}
			}
			b.Key = physical + "/network/" + hash(string(raw(spec)))
			b.Label = b.Binding.AWSAccountID + " · " + v.Region + " · 准备网络"
			b.LockKey = lock
			item := oc.ItemPlan{Target: b, Steps: []oc.StepPlan{}}
			for _, kind := range stepsForSpec(spec) {
				item.Steps = append(item.Steps, oc.StepPlan{Kind: kind, Label: networkStepLabelFor(kind, v.WavelengthZoneID), Input: raw(spec), WritesCloud: true})
			}
			result = append(result, item)
			continue
		}
		var spec contract.Spec
		keys := []string{}
		kinds := []string{v.Action}
		var err error
		if v.Action == "create" || v.Action == "enable_ipv6" {
			if _, ok := cats[group]; !ok {
				c, e := catalogue(ctx, e.deps.Cloud, cfg, v.WavelengthZoneID)
				if e != nil {
					return nil, publicError(e)
				}
				cats[group] = c
			}
		}
		if v.Action == "create" {
			if _, ok := e.deps.Cloud.(source.FirewallCloud); !ok {
				return nil, contract.ErrUnavailable
			}
			policy := &contract.FirewallPlan{Name: "panel-open-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24], IPv6: v.WavelengthZoneID == ""}
			spec, err = selectCreateWithFirewall(v, cats[group], policy)
			if err != nil {
				return nil, err
			}
			if _, ok := lists[group]; !ok {
				lists[group], err = e.deps.Cloud.Instances(ctx, cfg, b.Binding.AWSAccountID)
				if err != nil {
					return nil, publicError(err)
				}
			}
			for _, a := range lists[group] {
				if a.Name == v.Name && a.State != "terminated" {
					return nil, contract.ErrConflict
				}
			}
			keys = []string{"ec2/instance-name/" + hash(v.Name)}
			if err := e.planAutomaticIP(ctx, cfg, b.Binding.AWSAccountID, &spec); err != nil {
				return nil, err
			}
			ipKey := "ec2.elastic_ips"
			if v.WavelengthZoneID != "" {
				ipKey = "ec2.carrier_ips"
			}
			required[physical+"/"+ipKey]++
			aq := addressQuota(ipKey, spec.AutomaticIP.Quota)
			if aq.LimitKnown && aq.UsageKnown {
				old, ok := budgets[physical+"/"+ipKey]
				if !ok {
					budgets[physical+"/"+ipKey] = aq
				} else {
					old.Limit = min(old.Limit, aq.Limit)
					old.Used = max(old.Used, aq.Used)
					budgets[physical+"/"+ipKey] = old
				}
			}
			kinds = append(kinds, "ready")
			kinds = append(kinds, firewallSteps...)
			kinds = append(kinds, "ip_allocate", "ip_attach")
			if v.WavelengthZoneID != "" {
				if err := e.planIngress(ctx, cfg, b.Binding.AWSAccountID, cats[group], &spec); err != nil {
					return nil, err
				}
				for _, existing := range lists[group] {
					if existing.Name == spec.Ingress.Target.Name && existing.State != "terminated" {
						return nil, contract.ErrConflict
					}
				}
				keys = append(keys, "ec2/instance-name/"+hash(spec.Ingress.Target.Name))
				for _, q := range []contract.Quota{spec.Ingress.Quota, addressQuota("ec2.elastic_ips", spec.Ingress.AutomaticIP.Quota)} {
					key := physical + "/" + q.Key
					n := float64(1)
					if q.Key == spec.Ingress.Quota.Key {
						n = float64(spec.Ingress.Type.VCPU)
					}
					required[key] += n
					if q.LimitKnown && q.UsageKnown {
						old, ok := budgets[key]
						if ok {
							q.Limit = min(q.Limit, old.Limit)
							q.Used = max(q.Used, old.Used)
						}
						budgets[key] = q
					}
				}
				kinds = append(kinds, ingressStepsFor(spec)...)
			}
			if v.LoginMethod == "root_password" {
				if e.deps.LaunchSecrets == nil {
					return nil, contract.ErrUnavailable
				}
				if _, err = e.deps.LaunchSecrets.LaunchSecret(ctx, actor.ID, v.LaunchSecretID, launchBinding(v, b.Binding.AccountID)); err != nil {
					return nil, publicError(err)
				}
			}
		} else {
			a, e2 := e.deps.Cloud.Instance(ctx, cfg, b.Binding.AWSAccountID, v.InstanceID)
			if e2 != nil {
				return nil, publicError(e2)
			}
			if a == nil {
				return nil, contract.ErrNotFound
			}
			if !validAction(v.Action, *a) {
				return nil, contract.ErrConflict
			}
			spec = contract.Spec{Target: v, Instance: a, CheckedAt: clock.Now(), Warnings: []string{}}
			if v.WavelengthZoneID != "" {
				zs, err := wavelengthZones(ctx, e.deps.Cloud, cfg)
				if err != nil {
					return nil, publicError(err)
				}
				for _, z := range zs {
					if z.ID == v.WavelengthZoneID && z.Name == a.Zone {
						spec.WavelengthZone = &z
					}
				}
				if spec.WavelengthZone == nil {
					return nil, contract.ErrConflict
				}
			}
			if v.Action == "enable_ipv6" && !ipv6Ready(*a, cats[group]) {
				return nil, contract.ErrNetwork
			}
			if v.Action == "terminate" {
				spec.Warnings = append(spec.Warnings, "终止不可撤销；随实例删除的卷、经创建记录验证的入口实例与固定 IP 会一并清理。其他地址及共享网络保留。")
			}
			if v.Action == "stop" {
				spec.Warnings = append(spec.Warnings, "停止会中断服务；磁盘和保留的公网 IPv4 仍可能计费。")
			}
			if v.Action == "reboot" {
				spec.Warnings = append(spec.Warnings, "成功仅代表 AWS 接受重启请求，操作系统恢复情况需另行检查。")
			}
			keys = addresses.InstanceLocks("ec2", a.ID, a.Name)
			if v.Action == "terminate" {
				var cleanupKeys []string
				cleanupKeys, err = e.planCleanup(ctx, cfg, b, &spec)
				if err != nil {
					return nil, err
				}
				keys = append(keys, cleanupKeys...)
				if spec.CompanionInstance != nil {
					kinds = append(kinds, "companion_terminate")
				}
				for i := range spec.CleanupIPs {
					kinds = append(kinds, "cleanup_ip_"+strconv.Itoa(i))
				}
			}

		}
		if v.Action == "create" || (v.Action == "start" && spec.Instance.State == "stopped") {
			kind := v.InstanceType
			count := float64(0)
			if spec.Type != nil {
				count = float64(spec.Type.VCPU)
			} else {
				kind = spec.Instance.Type
				c, ok := cats[group]
				if !ok {
					c, err = catalogue(ctx, e.deps.Cloud, cfg, v.WavelengthZoneID)
					if err != nil {
						return nil, publicError(err)
					}
					cats[group] = c
				}
				for _, t := range c.Types {
					if t.ID == kind {
						count = float64(t.VCPU)
					}
				}
				if count == 0 {
					return nil, contract.ErrCatalogue
				}
			}
			spec.Quota, err = e.deps.Cloud.Quota(ctx, cfg, b.Binding.AWSAccountID, kind)
			if err != nil {
				return nil, publicError(err)
			}
			key := physical + "/" + spec.Quota.Key
			required[key] += count
			if spec.Quota.LimitKnown && spec.Quota.UsageKnown {
				q, ok := budgets[key]
				if !ok {
					budgets[key] = spec.Quota
				} else {
					q.Limit = min(q.Limit, spec.Quota.Limit)
					q.Used = max(q.Used, spec.Quota.Used)
					budgets[key] = q
				}
			}
			if !spec.Quota.LimitKnown || !spec.Quota.UsageKnown {
				spec.Warnings = append(spec.Warnings, "配额或用量无法完整查询，执行时仍由 AWS 最终判定。")
			}
		}
		for _, k := range keys {
			if locks[physical+"/"+k] {
				return nil, contract.ErrConflict
			}
			locks[physical+"/"+k] = true
		}
		b.Key = physical + "/" + hash(string(raw(spec)))
		b.Label = b.Binding.AWSAccountID + " · " + v.Region + " · " + actionLabel(v.Action)
		b.LockKey = keys[0]
		b.LockKeys = keys[1:]
		item := oc.ItemPlan{Target: b, Steps: []oc.StepPlan{}}
		for _, kind := range kinds {
			item.Steps = append(item.Steps, oc.StepPlan{Kind: kind, Label: actionLabel(kind), Input: raw(spec), WritesCloud: kind != "ready" && kind != "ingress_ready"})
		}
		result = append(result, item)
	}
	for key, n := range required {
		if q, ok := budgets[key]; ok && q.Used+n > q.Limit {
			return nil, contract.ErrQuota
		}
	}
	return result, nil
}
func actionLabel(v string) string {
	if label := firewallLabel(v); label != "" {
		return label
	}
	if strings.HasPrefix(v, "cleanup_ip_") {
		return "释放实例固定 IP"
	}
	if v == "companion_terminate" {
		return "终止配套入口 EC2"
	}
	return map[string]string{"network": "准备双栈网络", "create": "创建 EC2 实例", "ip_allocate": "自动分配固定 IP", "ip_attach": "自动绑定固定 IP", "ready": "检查实例运行状态", "start": "启动实例", "stop": "停止实例", "reboot": "重启实例", "terminate": "终止实例", "enable_ipv6": "启用 IPv6", "wavelength_opt_in": "开通 Wavelength Zone Group", "ingress_create": "创建父区域入口 EC2", "ingress_ready": "等待入口实例运行", "ingress_ip_allocate": "分配入口弹性 IP", "ingress_ip_attach": "绑定入口弹性 IP", "relay_forwarding": "允许入口转发流量", "relay_group": "创建实例专用转发安全组", "relay_ingress": "允许入口私网转发", "relay_egress": "移除转发安全组默认出站", "relay_attach": "关联实例转发安全组"}[v]
}
