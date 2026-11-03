package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/google/uuid"
	"github.com/herta-network/aws-panel/internal/auth"
	"github.com/herta-network/aws-panel/internal/infra/clock"
	"github.com/herta-network/aws-panel/internal/module/addresses"
	"github.com/herta-network/aws-panel/internal/module/compute/contract"
	"github.com/herta-network/aws-panel/internal/module/compute/internal/source"
	oc "github.com/herta-network/aws-panel/internal/module/operations/contract"
	"github.com/herta-network/aws-panel/internal/repository"
	"io"
	"net/netip"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

const OperationID = "ec2.manage"

var regionPattern = regexp.MustCompile(`^[a-z]{2}(?:-[a-z0-9]+)+-[0-9]+$`)
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)
var resourcePattern = regexp.MustCompile(`^(?:i|ami|subnet|sg)-[a-f0-9]{8,32}$`)
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
		if v.Action == "network" {
			if !namePattern.MatchString(v.Name) || !strings.HasPrefix(v.Zone, v.Region) || len(v.Zone) != len(v.Region)+1 || v.InstanceID != "" || v.ImageID != "" || v.InstanceType != "" || v.SubnetID != "" || len(v.SecurityGroupIDs) > 0 || v.DiskGB != 0 || v.PublicIPv4 || v.IPv6 || v.LoginMethod != "" || v.KeyPairName != "" || v.LaunchSecretID != "" {
				return nil, contract.ErrInvalid
			}
			ipv4, e4 := netip.ParsePrefix(v.SSHIPv4CIDR)
			ipv6, e6 := netip.ParsePrefix(v.SSHIPv6CIDR)
			if e4 != nil || e6 != nil || !ipv4.Addr().Is4() || !ipv6.Addr().Is6() || ipv6.Addr().Is4In6() {
				return nil, contract.ErrNetwork
			}
			v.SSHIPv4CIDR = ipv4.Masked().String()
			v.SSHIPv6CIDR = ipv6.Masked().String()
			continue
		}
		if v.Zone != "" || v.SSHIPv4CIDR != "" || v.SSHIPv6CIDR != "" {
			return nil, contract.ErrInvalid
		}
		if v.Action == "create" {
			if !namePattern.MatchString(v.Name) || !resource(v.ImageID, "ami") || !typePattern.MatchString(v.InstanceType) || !resource(v.SubnetID, "subnet") || v.InstanceID != "" || v.DiskGB < 8 || v.DiskGB > 16384 || len(v.SecurityGroupIDs) < 1 || len(v.SecurityGroupIDs) > 5 {
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
	s := contract.Spec{Target: v, CheckedAt: c.CheckedAt, Warnings: []string{"价格未锁定：按需实例、gp3 磁盘、公网 IPv4 与流量分别计费；提交前请核对 AWS 区域价格。", "网络 ACL、系统防火墙与真实 SSH/IPv6 连通性仍需启动后验证。"}}
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
	if s.Subnet.AvailableIPs < 1 || v.IPv6 && (!s.Type.IPv6 || s.Subnet.IPv6CIDR == "" || !s.Subnet.PublicIPv6Route) || v.PublicIPv4 && !s.Subnet.PublicIPv4Route {
		return s, contract.ErrNetwork
	}
	ipv4, ipv6 := false, false
	for _, id := range v.SecurityGroupIDs {
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
	if v.IPv6 && !ipv6 || v.PublicIPv4 && !ipv4 {
		return s, contract.ErrNetwork
	}
	if !v.IPv6 && !v.PublicIPv4 {
		s.Warnings = append(s.Warnings, "未分配公网地址，登录需要私网路径或后续绑定弹性 IP。")
	}
	if v.LoginMethod == "ssh_key" && !slices.Contains(c.KeyPairs, v.KeyPairName) {
		return s, contract.ErrLogin
	}
	if v.LoginMethod == "root_password" {
		s.Warnings = append(s.Warnings, "初始化将启用 root 密码 SSH 登录；请确保所选安全组来源仅覆盖可信管理网络。初始化状态需在系统内核对。")
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
		group := v.ConnectionID + "/" + v.Region
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
			b.Label = b.Binding.AWSAccountID + " · " + v.Region + " · 准备双栈网络"
			b.LockKey = lock
			item := oc.ItemPlan{Target: b, Steps: []oc.StepPlan{}}
			for _, kind := range networkSteps {
				item.Steps = append(item.Steps, oc.StepPlan{Kind: kind, Label: networkStepLabel(kind), Input: raw(spec), WritesCloud: true})
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
				c, e := e.deps.Cloud.Catalogue(ctx, cfg)
				if e != nil {
					return nil, publicError(e)
				}
				cats[group] = c
			}
		}
		if v.Action == "create" {
			spec, err = selectCreate(v, cats[group])
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
			kinds = append(kinds, "ready")
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
			if v.Action == "enable_ipv6" && !ipv6Ready(*a, cats[group]) {
				return nil, contract.ErrNetwork
			}
			if v.Action == "terminate" {
				spec.Warnings = append(spec.Warnings, "终止不可撤销；标记为随实例删除的卷会被删除，弹性 IP 不会自动释放。")
			}
			if v.Action == "stop" {
				spec.Warnings = append(spec.Warnings, "停止会中断服务；磁盘和保留的公网 IPv4 仍可能计费。")
			}
			if v.Action == "reboot" {
				spec.Warnings = append(spec.Warnings, "成功仅代表 AWS 接受重启请求，操作系统恢复情况需另行检查。")
			}
			keys = addresses.InstanceLocks("ec2", a.ID, a.Name)
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
					c, err = e.deps.Cloud.Catalogue(ctx, cfg)
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
			item.Steps = append(item.Steps, oc.StepPlan{Kind: kind, Label: actionLabel(kind), Input: raw(spec), WritesCloud: kind != "ready"})
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
	return map[string]string{"network": "准备双栈网络", "create": "创建 EC2 实例", "ready": "检查实例运行状态", "start": "启动实例", "stop": "停止实例", "reboot": "重启实例", "terminate": "终止实例", "enable_ipv6": "启用 IPv6"}[v]
}
