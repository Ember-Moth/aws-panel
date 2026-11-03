package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/google/uuid"
	"github.com/herta-network/aws-panel/internal/auth"
	"github.com/herta-network/aws-panel/internal/module/addresses/contract"
	oc "github.com/herta-network/aws-panel/internal/module/operations/contract"
	"github.com/herta-network/aws-panel/internal/repository"
)

const OperationID = "addresses.manage"

var regionPattern = regexp.MustCompile(`^[a-z]{2}(?:-[a-z0-9]+)+-[0-9]+$`)
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,28}[a-z0-9]$`)
var lightName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,253}[A-Za-z0-9]$`)
var ec2InstanceID = regexp.MustCompile(`^i-[a-f0-9]{8,32}$`)
var ec2AddressID = regexp.MustCompile(`^eipalloc-[a-f0-9]{8,32}$`)

func validRegion(region string) bool {
	return len(region) <= 30 && regionPattern.MatchString(region) && !strings.HasPrefix(region, "cn-") && !strings.HasPrefix(region, "us-gov-")
}
func hash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func InstanceLocks(kind, id, name string) []string {
	if kind == "lightsail" {
		if len(id) <= 63 && regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`).MatchString(id) {
			return []string{"lightsail/instance/" + id}
		}
		return []string{"lightsail/instance-hash/" + hash(id)}
	}
	result := []string{"ec2/instance/" + id}
	if name != "" && name != id {
		result = append(result, "ec2/instance-name/"+hash(name))
	}
	return result
}
func AddressLock(kind, id string) string { return kind + "/address/" + hash(id) }
func normalize(values []contract.Target) ([]contract.Target, error) {
	if len(values) < 1 || len(values) > 50 {
		return nil, contract.ErrInvalid
	}
	result := append([]contract.Target{}, values...)
	for i := range result {
		v := &result[i]
		id, err := uuid.Parse(v.ConnectionID)
		if err != nil || !validRegion(v.Region) || (v.Service != "lightsail" && v.Service != "ec2") {
			return nil, contract.ErrInvalid
		}
		v.ConnectionID = id.String()
		if v.InstanceID != "" && ((v.Service == "lightsail" && !lightName.MatchString(v.InstanceID)) || (v.Service == "ec2" && !ec2InstanceID.MatchString(v.InstanceID))) {
			return nil, contract.ErrInvalid
		}
		if v.AddressID != "" && ((v.Service == "lightsail" && !lightName.MatchString(v.AddressID)) || (v.Service == "ec2" && !ec2AddressID.MatchString(v.AddressID))) {
			return nil, contract.ErrInvalid
		}
		switch v.Action {
		case "allocate", "replace":
			if v.AddressID != "" || (v.Action == "replace" && v.InstanceID == "") {
				return nil, contract.ErrInvalid
			}
			if v.NamePrefix == "" {
				v.NamePrefix = "panel-ip"
			}
			if !namePattern.MatchString(v.NamePrefix) {
				return nil, contract.ErrInvalid
			}
		case "attach":
			if v.AddressID == "" || v.InstanceID == "" || v.NamePrefix != "" {
				return nil, contract.ErrInvalid
			}
		case "detach", "release":
			if v.AddressID == "" || v.NamePrefix != "" || v.ReleasePrevious || v.InstanceID != "" {
				return nil, contract.ErrInvalid
			}
		default:
			return nil, contract.ErrInvalid
		}
		if v.ReleasePrevious && v.InstanceID == "" {
			return nil, contract.ErrInvalid
		}
	}
	return result, nil
}
func AddressLocks(kind string, address contract.Address) []string {
	keys := []string{AddressLock(kind, address.ID)}
	if kind == "ec2" && address.Name != "" && address.Name != address.ID {
		keys = append(keys, AddressLock(kind, "name/"+address.Name))
	}
	return keys
}

type ExecutorDeps struct {
	Clients Clients
	Cloud   Cloud
	Effects repository.EffectStore
}
type Executor struct{ deps ExecutorDeps }

func NewExecutor(deps ExecutorDeps) *Executor { return &Executor{deps: deps} }
func (*Executor) Definition() oc.Definition {
	return oc.Definition{ID: OperationID, Version: 1, InputVersion: 1, Title: "公网地址管理", WritesCloud: true, MaxAttempts: 60, Deadline: 30 * time.Minute, CallTimeout: 90 * time.Second}
}
func (*Executor) Validate(raw json.RawMessage) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var req contract.Request
	if decoder.Decode(&req) != nil {
		return nil, contract.ErrInvalid
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, contract.ErrInvalid
	}
	values, err := normalize(req.Targets)
	if err != nil {
		return nil, err
	}
	return json.Marshal(contract.Request{Targets: values})
}
func (e *Executor) Plan(ctx context.Context, raw json.RawMessage, targets []oc.Target) ([]oc.ItemPlan, error) {
	actor, ok := auth.Current(ctx)
	if !ok {
		return nil, contract.ErrForbidden
	}
	var req contract.Request
	if json.Unmarshal(raw, &req) != nil || len(req.Targets) != len(targets) {
		return nil, contract.ErrInvalid
	}
	catalogues := map[string]contract.Catalogue{}
	configs := map[string]aws.Config{}
	quotas := map[string]contract.Quota{}
	required := map[string]int{}
	budgets := map[string]contract.Quota{}
	locksSeen := map[string]bool{}
	result := []oc.ItemPlan{}
	for index, value := range req.Targets {
		target := targets[index]
		if target.Binding.Partition != "aws" || target.Binding.ConnectionID != value.ConnectionID || target.Binding.Region != value.Region {
			return nil, contract.ErrInvalid
		}
		group := value.ConnectionID + "/" + value.Region + "/" + value.Service
		physical := target.Binding.AccountID + "/" + value.Region + "/" + value.Service
		catalogue, ok := catalogues[group]
		if !ok {
			cfg, err := e.deps.Clients.ConfigFor(ctx, actor.ID, value.ConnectionID, value.Region, true)
			if err != nil {
				return nil, publicError(err)
			}
			configs[group] = cfg
			catalogue, err = e.deps.Cloud.Catalogue(ctx, cfg, value.Service, target.Binding.AWSAccountID)
			if err != nil {
				return nil, publicError(err)
			}
			catalogues[group] = catalogue
		}
		spec := contract.Spec{Target: value, CheckedAt: catalogue.CheckedAt}
		for _, v := range catalogue.Addresses {
			if v.ID == value.AddressID {
				copy := v
				spec.Address = &copy
			}
		}
		for _, v := range catalogue.Instances {
			if v.ID == value.InstanceID {
				copy := v
				spec.Instance = &copy
			}
		}
		if value.AddressID != "" && spec.Address == nil || value.InstanceID != "" && spec.Instance == nil {
			return nil, contract.ErrNotFound
		}
		if spec.Instance != nil {
			if spec.Instance.State != "running" && spec.Instance.State != "stopped" {
				return nil, contract.ErrConflict
			}
			if value.Service == "lightsail" && spec.Instance.IPType == "ipv6" {
				return nil, contract.ErrUnsupported
			}
			if value.Service == "ec2" && (spec.Instance.NetworkInterfaceID == "" || spec.Instance.BorderGroup == "") {
				return nil, contract.ErrUnsupported
			}
			for _, v := range catalogue.Addresses {
				if v.ID == spec.Instance.AddressID {
					copy := v
					spec.Previous = &copy
				}
			}
		}
		if value.Action == "attach" && spec.Address.Attached && spec.Address.InstanceID != value.InstanceID {
			return nil, contract.ErrAttached
		}
		if value.Action == "release" && spec.Address.Attached {
			return nil, contract.ErrAttached
		}
		if value.Action == "detach" && spec.Address.Attached {
			for _, v := range catalogue.Instances {
				if v.ID == spec.Address.InstanceID {
					copy := v
					spec.Instance = &copy
				}
			}
			if spec.Instance == nil {
				return nil, contract.ErrUnsupported
			}
		}
		if value.Service == "ec2" && spec.Address != nil && spec.Address.Attached && spec.Instance != nil && (spec.Address.NetworkInterfaceID != spec.Instance.NetworkInterfaceID || spec.Address.PrivateIP != spec.Instance.PrivateIP) {
			return nil, contract.ErrUnsupported
		}
		if spec.Address != nil && spec.Instance != nil && value.Service == "ec2" && spec.Address.BorderGroup != spec.Instance.BorderGroup {
			return nil, contract.ErrUnsupported
		}
		kinds := []string{}
		if value.Action == "allocate" || value.Action == "replace" {
			spec.NewName = value.NamePrefix + "-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24]
			quota, ok := quotas[group]
			if !ok {
				var err error
				quota, err = e.deps.Cloud.Quota(ctx, configs[group], value.Service, target.Binding.AWSAccountID)
				if err != nil {
					return nil, publicError(err)
				}
				quotas[group] = quota
			}
			spec.Quota = quota
			required[physical]++
			if quota.LimitKnown && quota.UsageKnown {
				current, ok := budgets[physical]
				if !ok {
					budgets[physical] = quota
				} else {
					current.Limit = min(current.Limit, quota.Limit)
					current.Used = max(current.Used, quota.Used)
					budgets[physical] = current
				}
			}
			kinds = append(kinds, "allocate")
		}
		if spec.Instance != nil && (value.Action == "allocate" || value.Action == "replace" || value.Action == "attach") {
			different := spec.Previous != nil && (spec.Address == nil || spec.Previous.ID != spec.Address.ID)
			if different && value.Service == "lightsail" {
				kinds = append(kinds, "detach_previous")
			}
			kinds = append(kinds, "attach")
			if different && value.ReleasePrevious {
				kinds = append(kinds, "release_previous")
			}
		} else if value.Action == "detach" || value.Action == "release" {
			kinds = append(kinds, value.Action)
		}
		if len(kinds) == 0 {
			return nil, contract.ErrInvalid
		}
		resourceLocks := []string{}
		if spec.Instance != nil {
			resourceLocks = append(resourceLocks, InstanceLocks(value.Service, spec.Instance.ID, spec.Instance.Name)...)
		}
		if spec.Address != nil {
			resourceLocks = append(resourceLocks, AddressLocks(value.Service, *spec.Address)...)
		}
		if spec.Previous != nil {
			resourceLocks = append(resourceLocks, AddressLocks(value.Service, *spec.Previous)...)
		}
		if spec.NewName != "" {
			name := spec.NewName
			if value.Service == "ec2" {
				name = "name/" + name
			}
			resourceLocks = append(resourceLocks, AddressLock(value.Service, name))
		}
		unique := []string{}
		local := map[string]bool{}
		for _, lock := range resourceLocks {
			if local[lock] {
				continue
			}
			local[lock] = true
			if locksSeen[physical+"/"+lock] {
				return nil, contract.ErrConflict
			}
			locksSeen[physical+"/"+lock] = true
			unique = append(unique, lock)
		}
		target.Key = physical + "/" + hash(string(mustJSON(spec)))
		target.Label = target.Binding.AWSAccountID + " · " + value.Region + " · " + actionLabel(value.Action)
		target.LockKey = unique[0]
		target.LockKeys = unique[1:]
		body := mustJSON(spec)
		item := oc.ItemPlan{Target: target, Steps: []oc.StepPlan{}}
		for _, kind := range kinds {
			item.Steps = append(item.Steps, oc.StepPlan{Kind: kind, Label: stepLabel(kind), Input: body, WritesCloud: true})
		}
		result = append(result, item)
	}
	for scope, amount := range required {
		if quota, ok := budgets[scope]; ok && quota.Used+float64(amount) > quota.Limit {
			return nil, contract.ErrQuota
		}
	}
	return result, nil
}
func mustJSON(value any) []byte { raw, _ := json.Marshal(value); return raw }
func actionLabel(value string) string {
	return map[string]string{"allocate": "创建静态 IP", "replace": "更换实例 IP", "attach": "绑定地址", "detach": "解绑地址", "release": "释放地址"}[value]
}
func stepLabel(value string) string {
	return map[string]string{"allocate": "分配并核对新地址", "detach_previous": "解绑旧地址", "attach": "绑定并核对新地址", "release_previous": "释放旧地址", "detach": "解绑并核对", "release": "释放并核对"}[value]
}
