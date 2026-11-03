package service

import (
	"context"
	"github.com/Ember-Moth/aws-panel/internal/module/addresses"
	oc "github.com/Ember-Moth/aws-panel/internal/module/operations/contract"
	"strings"
)

func (e *Executor) automaticIP(ctx context.Context, c oc.Call, reconcile bool) oc.Result {
	spec, err := decodeSpec(c)
	if err != nil {
		return noSend(err)
	}
	if spec.AutomaticIP == nil || e.deps.Effects == nil {
		return oc.Result{State: oc.Attention, Code: "AUTOMATIC_IP_POLICY_MISSING"}
	}
	stepID := ""
	for _, s := range c.Completed {
		if s.Kind == "create" {
			stepID = s.ID
		}
	}
	if stepID == "" {
		return oc.Result{State: oc.Attention, Code: "CREATE_REFERENCE_MISSING"}
	}
	receipt, err := e.deps.Store.Creation(ctx, stepID)
	if err != nil {
		return observeFailure(err)
	}
	if receipt == nil || receipt.JobID != c.JobID || receipt.AccountID != c.Target.Binding.AccountID || receipt.ResourceARN == "" {
		return oc.Result{State: oc.Attention, Code: "CREATE_REFERENCE_MISSING"}
	}
	cfg, err := e.config(ctx, c, false)
	if err != nil {
		return observeFailure(err)
	}
	live, err := e.deps.Cloud.Instance(ctx, cfg, spec.Target.Name)
	if err != nil {
		return observeFailure(err)
	}
	if live == nil || !owned(*live, spec, *receipt, c.Target.Binding.AWSAccountID) {
		return oc.Result{State: oc.Attention, Code: "CREATED_RESOURCE_CHANGED"}
	}
	instance, err := e.deps.AddressCloud.Instance(ctx, cfg, "lightsail", c.Target.Binding.AWSAccountID, spec.Target.Name)
	if err != nil {
		return observeFailure(err)
	}
	if instance == nil || instance.ARN != receipt.ResourceARN {
		return oc.Result{State: oc.Attention, Code: "CREATED_RESOURCE_CHANGED"}
	}
	return addresses.RunAutomatic(ctx, addresses.ExecutorDeps{Clients: e.deps.Clients, Cloud: e.deps.AddressCloud, Effects: e.deps.Effects}, c, "ip_", *spec.AutomaticIP, *instance, reconcile)
}
func isIPStep(kind string) bool { return strings.HasPrefix(kind, "ip_") }
