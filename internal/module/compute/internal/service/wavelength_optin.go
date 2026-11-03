package service

import (
	"context"
	awsnet "github.com/Ember-Moth/aws-panel/internal/infra/aws"
	"github.com/Ember-Moth/aws-panel/internal/infra/clock"
	"github.com/Ember-Moth/aws-panel/internal/module/compute/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/compute/internal/source"
	oc "github.com/Ember-Moth/aws-panel/internal/module/operations/contract"
	"github.com/aws/aws-sdk-go-v2/aws"
	"slices"
	"time"
)

func (e *Executor) planWavelengthOptIn(ctx context.Context, cfg aws.Config, b oc.Target, v contract.Target) (oc.ItemPlan, error) {
	zs, err := wavelengthZones(ctx, e.deps.Cloud, cfg)
	if err != nil {
		return oc.ItemPlan{}, publicError(err)
	}
	s := contract.Spec{Target: v, CheckedAt: clock.Now(), Warnings: []string{"按 Zone Group 开通，影响该组内全部 Wavelength Zones；退出需联系 AWS Support。开通本身不会创建实例、网络或地址。"}}
	for _, z := range zs {
		if z.ID == v.WavelengthZoneID {
			s.WavelengthZone = &z
		}
	}
	if s.WavelengthZone == nil {
		return oc.ItemPlan{}, contract.ErrNotFound
	}
	for _, z := range zs {
		if z.Group == s.WavelengthZone.Group {
			s.GroupZones = append(s.GroupZones, z)
		}
	}
	b.Key = b.Binding.AWSAccountID + "/" + v.Region + "/wavelength/" + s.WavelengthZone.Group
	b.LockKey = "ec2/wavelength-group/" + hash(s.WavelengthZone.Group)
	b.Label = b.Binding.AWSAccountID + " · " + v.Region + " · 开通 " + s.WavelengthZone.Group
	return oc.ItemPlan{Target: b, Steps: []oc.StepPlan{{Kind: v.Action, Label: "开通并确认 Wavelength Zone Group", Input: raw(s), WritesCloud: true}}}, nil
}
func (e *Executor) wavelengthOptIn(ctx context.Context, cfg aws.Config, c oc.Call, s contract.Spec) oc.Result {
	cloud, ok := e.deps.Cloud.(source.WavelengthCloud)
	if !ok {
		return before(contract.ErrUnavailable)
	}
	row, err := e.deps.Effects.Effect(ctx, c.StepID)
	if err != nil {
		return observing(err)
	}
	if row != nil && !sameEffect(row, c) {
		return attention("EFFECT_MISMATCH", "")
	}
	zs, err := cloud.WavelengthZones(ctx, cfg)
	if err != nil {
		return observing(err)
	}
	current := []contract.WavelengthZone{}
	done := true
	for _, z := range zs {
		if z.Group == s.WavelengthZone.Group {
			current = append(current, z)
			done = done && z.OptInStatus == "opted-in"
		}
	}
	conflict := func() oc.Result {
		if row != nil && (row.State == "dispatched" || row.State == "confirmed") {
			return attention("WAVELENGTH_GROUP_CHANGED", s.WavelengthZone.Group)
		}
		return before(contract.ErrConflict)
	}
	if len(current) != len(s.GroupZones) || len(current) == 0 {
		return conflict()
	}
	for i, z := range current {
		p := s.GroupZones[i]
		if z.ID != p.ID || z.Name != p.Name || z.Group != p.Group || z.BorderGroup != p.BorderGroup || z.Region != p.Region {
			return conflict()
		}
	}
	if done {
		if row == nil {
			r, err := e.deps.Effects.PrepareEffect(ctx, expected(c), lease(c))
			if err != nil {
				return before(err)
			}
			row = &r
		}
		row.State = "confirmed"
		row.ResourceID = s.WavelengthZone.Group
		row.Code = "WAVELENGTH_ENABLED"
		row.Proof = string(raw(current))
		if err := e.deps.Effects.RecordEffect(ctx, *row, lease(c)); err != nil {
			return observing(err)
		}
		return oc.Result{State: oc.Succeeded, Code: "WAVELENGTH_ENABLED", Output: oc.Output{ResourceID: row.ResourceID, Summary: "已确认 Zone Group 开通；可选择 Zone 准备网络和创建实例"}}
	}
	if row != nil && (row.State == "dispatched" || row.State == "confirmed") {
		return waiting("WAVELENGTH_OPT_IN_PENDING", s.WavelengthZone.Group)
	}
	for _, z := range current {
		if z.OptInStatus != "not-opted-in" && z.OptInStatus != "opted-in" {
			return before(contract.ErrConflict)
		}
	}
	prepared, err := e.deps.Effects.PrepareEffect(ctx, expected(c), lease(c))
	if err != nil {
		return before(err)
	}
	if prepared.State != "prepared" && prepared.State != "rejected" {
		return waiting("WAVELENGTH_OPT_IN_PENDING", s.WavelengthZone.Group)
	}
	cfg, err = e.config(ctx, c, true)
	if err != nil {
		return before(err)
	}
	sent, err := e.deps.Effects.DispatchEffect(ctx, c.StepID, lease(c))
	if err != nil {
		return observing(err)
	}
	accepted, err := cloud.EnableWavelength(ctx, cfg, s.WavelengthZone.Group)
	if err != nil {
		info := awsnet.ClassifyError(err)
		if slices.Contains([]string{"AWS_ACCESS_DENIED", "AWS_INVALID_REQUEST", "AWS_QUOTA_EXCEEDED", "AWS_STATE_CONFLICT"}, info.Code) {
			sent.State = "rejected"
			sent.Code = info.Code
			sent.RequestID = info.RequestID
			if e.deps.Effects.RecordEffect(ctx, sent, lease(c)) == nil {
				return oc.Result{State: oc.Failed, Code: info.Code, ConfirmedAbsent: true}
			}
		}
		return waiting("WAVELENGTH_RESULT_UNCONFIRMED", s.WavelengthZone.Group)
	}
	sent.ResourceID = accepted.ResourceID
	sent.RequestID = accepted.RequestID
	sent.Code = "WAVELENGTH_REQUEST_ACCEPTED"
	if e.deps.Effects.RecordEffect(ctx, sent, lease(c)) != nil {
		return waiting("WAVELENGTH_RECEIPT_UNCONFIRMED", s.WavelengthZone.Group)
	}
	return oc.Result{State: oc.Waiting, Code: "WAVELENGTH_OPT_IN_PENDING", After: 10 * time.Second, Output: oc.Output{ResourceID: sent.ResourceID, RequestID: sent.RequestID}}
}
