package service

import (
	"context"
	"github.com/Ember-Moth/aws-panel/internal/module/addresses/contract"
	oc "github.com/Ember-Moth/aws-panel/internal/module/operations/contract"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/google/uuid"
	"strings"
)

// PlanAutomatic fixes the allocation identity before a create request is submitted.
func PlanAutomatic(ctx context.Context, cloud Cloud, cfg aws.Config, kind, account, zone, border string) (contract.Automatic, error) {
	q, err := cloud.Quota(ctx, cfg, kind, account)
	if err != nil {
		return contract.Automatic{}, err
	}
	if q.LimitKnown && q.UsageKnown && q.Used+1 > q.Limit {
		return contract.Automatic{}, contract.ErrQuota
	}
	return contract.Automatic{Service: kind, NewName: "panel-ip-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24], WavelengthZoneID: zone, BorderGroup: border, Quota: q}, nil
}

// Automatic uses the same allocation and association receipts as explicit address
// operations. Only the parent's own named phases enter the dependency list.
func (e *Executor) Automatic(ctx context.Context, call oc.Call, prefix string, p contract.Automatic, instance contract.Instance, reconcile bool) oc.Result {
	if !strings.HasPrefix(call.Kind, prefix) || p.NewName == "" || instance.ID == "" || instance.ARN == "" {
		return attention("AUTOMATIC_IP_REFERENCE_MISSING", instance.ARN)
	}
	// These fields change when this workflow attaches the new IP. They must not
	// change the immutable specification used to identify an uncertain write.
	instance.Name = instance.ID
	instance.IPType = ""
	instance.PublicIP = ""
	instance.AddressID = ""
	instance.State = "running"
	spec := contract.Spec{Target: contract.Target{ConnectionID: call.Target.Binding.ConnectionID, Region: call.Target.Binding.Region, Service: p.Service, Action: "allocate", InstanceID: instance.ID, NamePrefix: "panel-ip", WavelengthZoneID: p.WavelengthZoneID}, NewName: p.NewName, BorderGroup: p.BorderGroup, Instance: &instance, Quota: p.Quota, Managed: true}
	call.Kind = strings.TrimPrefix(call.Kind, prefix)
	call.Input = mustJSON(spec)
	completed := []oc.StepOutput{}
	for _, step := range call.Completed {
		if strings.HasPrefix(step.Kind, prefix) {
			step.Kind = strings.TrimPrefix(step.Kind, prefix)
			completed = append(completed, step)
		}
	}
	call.Completed = completed
	if call.Kind != "allocate" && call.Kind != "attach" {
		return attention("INVALID_AUTOMATIC_IP_STEP", instance.ARN)
	}
	if reconcile {
		return e.Reconcile(ctx, call)
	}
	return e.Execute(ctx, call)
}
