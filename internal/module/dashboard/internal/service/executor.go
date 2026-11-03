package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/google/uuid"
	awsnet "github.com/herta-network/aws-panel/internal/infra/aws"
	ac "github.com/herta-network/aws-panel/internal/module/accounts/contract"
	"github.com/herta-network/aws-panel/internal/module/dashboard/contract"
	"github.com/herta-network/aws-panel/internal/module/dashboard/entity"
	"github.com/herta-network/aws-panel/internal/module/dashboard/internal/source"
	oc "github.com/herta-network/aws-panel/internal/module/operations/contract"
	"github.com/herta-network/aws-panel/internal/repository"
	"io"
	"slices"
	"time"
)

type ExecutorDeps struct {
	Clients Clients
	Cloud   source.Cloud
	Store   repository.DashboardStore
}
type Executor struct{ deps ExecutorDeps }

func NewExecutor(d ExecutorDeps) *Executor { return &Executor{deps: d} }
func (*Executor) Definition() oc.Definition {
	return oc.Definition{ID: Operation, Version: 1, InputVersion: 1, Title: "刷新账号池仪表盘", WritesCloud: false, MaxAttempts: 100, Deadline: 24 * time.Hour, CallTimeout: 2 * time.Minute}
}
func (*Executor) Validate(input json.RawMessage) (json.RawMessage, error) {
	var value contract.Input
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || len(value.Targets) < 1 || len(value.Targets) > 50 {
		return nil, contract.ErrInvalid
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, contract.ErrInvalid
	}
	seen := map[string]bool{}
	for _, v := range value.Targets {
		if _, err := uuid.Parse(v.ConnectionID); err != nil || !validRegion(v.Region) || seen[v.ConnectionID] {
			return nil, contract.ErrInvalid
		}
		seen[v.ConnectionID] = true
	}
	return json.Marshal(value)
}
func (*Executor) Plan(ctx context.Context, input json.RawMessage, targets []oc.Target) ([]oc.ItemPlan, error) {
	var value contract.Input
	if json.Unmarshal(input, &value) != nil || len(value.Targets) != len(targets) {
		return nil, contract.ErrInvalid
	}
	items := []oc.ItemPlan{}
	seen := map[string]bool{}
	for i, v := range value.Targets {
		target := targets[i]
		if target.Binding.Partition != "aws" || target.Binding.ConnectionID != v.ConnectionID || target.Binding.Region != "us-east-1" || seen[target.Binding.AccountID] {
			return nil, contract.ErrInvalid
		}
		seen[target.Binding.AccountID] = true
		target.Key = target.Binding.AccountID
		target.Label = target.Binding.AWSAccountID + " · 资源与配额"
		target.LockKey = "dashboard/account"
		items = append(items, oc.ItemPlan{Target: target, Steps: []oc.StepPlan{{Kind: "collect", Label: "分区域读取资源与配额", Input: json.RawMessage(raw(v))}}})
	}
	return items, nil
}
func lease(c oc.Call) entity.Lease {
	return entity.Lease{StepID: c.StepID, Owner: c.LeaseOwner, Epoch: c.LeaseEpoch}
}
func (e *Executor) configuration(ctx context.Context, c oc.Call, region string) (aws.Config, error) {
	if c.Check == nil {
		return aws.Config{}, contract.ErrInvalid
	}
	if err := c.Check(ctx); err != nil {
		return aws.Config{}, err
	}
	return e.deps.Clients.ConfigForBound(ctx, c.ActorID, c.Target.Binding.ConnectionID, region, false, c.Target.Binding.Fingerprint, c.Check)
}
func failed(err error) oc.Result {
	code := "DASHBOARD_READ_FAILED"
	temporary := false
	var own *contract.Error
	var account *ac.Error
	var operation *oc.Error
	switch {
	case errors.As(err, &own):
		code = own.Code
		temporary = code == contract.ErrUnavailable.Code
	case errors.As(err, &account):
		code = account.Code
		temporary = code == ac.ErrUnavailable.Code
	case errors.As(err, &operation):
		code = operation.Code
		temporary = code == oc.ErrUnavailable.Code
	default:
		info := awsnet.ClassifyError(err)
		code = info.Code
		temporary = info.Transient
	}
	state := oc.Failed
	if temporary {
		state = oc.Retry
	}
	return oc.Result{State: state, Code: code, ConfirmedAbsent: true, After: 5 * time.Second, Output: oc.Output{Summary: "仪表盘读取未完成，已有成功快照保留"}}
}
func finished(r entity.Run) oc.Result {
	code := "DASHBOARD_UPDATED"
	if r.Failures > 0 {
		code = "DASHBOARD_PARTIAL"
	}
	return oc.Result{State: oc.Succeeded, Code: code, Output: oc.Output{Summary: fmt.Sprintf("已读取 %d 个区域；%d 项查询不完整，仪表盘显示覆盖范围与已有快照", r.NextRegion, r.Failures)}}
}
func (e *Executor) Execute(ctx context.Context, c oc.Call) oc.Result   { return e.collect(ctx, c) }
func (e *Executor) Reconcile(ctx context.Context, c oc.Call) oc.Result { return e.collect(ctx, c) }
func (e *Executor) collect(ctx context.Context, c oc.Call) oc.Result {
	var input contract.Target
	if json.Unmarshal(c.Input, &input) != nil || input.ConnectionID != c.Target.Binding.ConnectionID || !validRegion(input.Region) || c.Kind != "collect" || c.Target.Binding.Partition != "aws" {
		return failed(contract.ErrInvalid)
	}
	if err := c.Check(ctx); err != nil {
		return failed(err)
	}
	run, err := e.deps.Store.Run(ctx, c.StepID)
	if err != nil {
		return failed(err)
	}
	scope := func(region, kind string) entity.Snapshot {
		return entity.Snapshot{AccountID: c.Target.Binding.AccountID, Region: region, Kind: kind, ConnectionID: input.ConnectionID, Fingerprint: c.Target.Binding.Fingerprint, Data: "{}"}
	}
	if run != nil && (run.JobID != c.JobID || run.AccountID != c.Target.Binding.AccountID || run.ConnectionID != input.ConnectionID || run.SpecHash != hash(string(c.Input))) {
		return failed(contract.ErrInvalid)
	}
	if run == nil || run.Fingerprint != c.Target.Binding.Fingerprint {
		cfg, err := e.configuration(ctx, c, "us-east-1")
		if err != nil {
			return failed(err)
		}
		catalog, err := e.deps.Cloud.Catalogue(ctx, cfg)
		if err != nil {
			row := scope("global", contract.Catalog)
			row.Code = awsnet.ClassifyError(err).Code
			row.Status = "failed"
			_ = e.deps.Store.RecordSnapshot(ctx, row, lease(c))
			return failed(err)
		}
		if len(catalog.Regions) > 80 {
			return failed(contract.ErrResponse)
		}
		for _, region := range catalog.Regions {
			if region == "" || !validRegion(region) {
				return failed(contract.ErrResponse)
			}
		}

		data := contract.Data{Regions: catalog.Regions, LightsailRegions: catalog.LightsailRegions, LightsailKnown: catalog.LightsailKnown}
		row := scope("global", contract.Catalog)
		row.Status = "succeeded"
		row.Data = raw(data)
		if input.Region != "" && !slices.Contains(catalog.Regions, input.Region) {
			if err := e.deps.Store.RecordSnapshot(ctx, row, lease(c)); err != nil {
				return failed(err)
			}
			return oc.Result{State: oc.Failed, Code: "REGION_NOT_ENABLED", ConfirmedAbsent: true, Output: oc.Output{Summary: "所选区域尚未启用，区域目录已更新，已有资源快照保留"}}
		}

		catalogCopy := catalog
		if input.Region != "" {
			catalogCopy.Regions = []string{input.Region}
		}
		initial := entity.Run{StepID: c.StepID, JobID: c.JobID, AccountID: c.Target.Binding.AccountID, ConnectionID: input.ConnectionID, Fingerprint: c.Target.Binding.Fingerprint, SpecHash: hash(string(c.Input)), Catalog: raw(catalogCopy)}
		if len(catalogCopy.Regions) == 0 {
			now := time.Now()
			initial.FinishedAt = &now
		}
		if !catalog.LightsailKnown {
			initial.Failures = 1
		}
		saved, err := e.deps.Store.Start(ctx, initial, row, lease(c))
		if err != nil {
			return failed(err)
		}
		run = &saved
	}
	if run.FinishedAt != nil {
		return finished(*run)
	}
	var catalog source.Catalogue
	if json.Unmarshal([]byte(run.Catalog), &catalog) != nil || run.NextRegion < 0 || run.NextRegion > len(catalog.Regions) {
		return failed(contract.ErrResponse)
	}
	if run.NextRegion == len(catalog.Regions) {
		return finished(*run)
	}
	region := catalog.Regions[run.NextRegion]
	cfg, err := e.configuration(ctx, c, region)
	if err != nil {
		return failed(err)
	}
	observations, err := e.deps.Cloud.ReadRegion(ctx, cfg, c.Target.Binding.AWSAccountID, !catalog.LightsailKnown || slices.Contains(catalog.LightsailRegions, region))
	if err != nil {
		return failed(err)
	}
	expected := []string{contract.EC2Instances, contract.EC2IPs, contract.EC2Quotas, contract.BedrockQuotas}
	if !catalog.LightsailKnown || slices.Contains(catalog.LightsailRegions, region) {
		expected = append(expected, contract.LightsailInstances, contract.LightsailIPs, contract.LightsailQuotas)
	}
	seen := map[string]bool{}
	snapshots := []entity.Snapshot{}
	for _, observation := range observations {
		if !slices.Contains(expected, observation.Kind) || seen[observation.Kind] {
			return failed(contract.ErrResponse)
		}
		seen[observation.Kind] = true
		row := scope(region, observation.Kind)
		row.Status = "failed"
		row.Code = observation.Code
		if observation.OK {
			row.Status = "succeeded"
			row.Code = ""
			row.Data = raw(observation.Data)
		} else {
			run.Failures++
		}
		snapshots = append(snapshots, row)
	}
	if len(seen) != len(expected) {
		return failed(contract.ErrResponse)
	}
	run.NextRegion++
	if run.NextRegion == len(catalog.Regions) {
		now := time.Now()
		run.FinishedAt = &now
	}
	saved, err := e.deps.Store.Advance(ctx, *run, snapshots, lease(c))
	if err != nil {
		return failed(err)
	}
	if saved.FinishedAt != nil {
		return finished(saved)
	}
	return oc.Result{State: oc.Waiting, Code: "DASHBOARD_COLLECTING", After: time.Second, Output: oc.Output{Summary: fmt.Sprintf("已完成 %d / %d 个区域，刚读取 %s", saved.NextRegion, len(catalog.Regions), region)}}
}
