package service

import (
	"context"
	"encoding/json"
	"github.com/herta-network/aws-panel/internal/module/addresses/contract"
	oc "github.com/herta-network/aws-panel/internal/module/operations/contract"
	"github.com/herta-network/aws-panel/internal/repository"
)

func ownedReceipt(ctx context.Context, store repository.EffectStore, account, region string, v contract.ManagedIP) (bool, error) {
	if v.OwnerStepID == "" || v.OwnerToken == "" || v.OwnerSpecHash == "" {
		return false, nil
	}
	r, err := store.Effect(ctx, v.OwnerStepID)
	if err != nil {
		return false, err
	}
	if r == nil || r.State != "confirmed" || r.AccountID != account || r.Region != region || r.Purpose != "addresses.allocate" || r.ResourceID != v.Address.ID || r.Token != v.OwnerToken || r.SpecHash != v.OwnerSpecHash {
		return false, nil
	}
	var proof effectProof
	if json.Unmarshal([]byte(r.Proof), &proof) != nil || !sameAddress(proof.Address, &v.Address) {
		return false, nil
	}
	return true, nil
}
func OwnedAllocation(ctx context.Context, store repository.EffectStore, account, region string, a contract.Address) (*contract.ManagedIP, error) {
	if a.Tags["aws-panel:role"] != "instance-ip" || a.Tags["aws-panel:instance"] == "" || !a.Attached || a.InstanceID != a.Tags["aws-panel:instance"] {
		return nil, nil
	}
	service := "ec2"
	if a.Kind == "wavelength" {
		service = "wavelength"
	}
	v := contract.ManagedIP{Address: a, Service: service, InstanceID: a.InstanceID, OwnerStepID: a.Tags["aws-panel:step"], OwnerToken: a.Tags["aws-panel:token"], OwnerSpecHash: a.Tags["aws-panel:spec"]}
	ok, err := ownedReceipt(ctx, store, account, region, v)
	if err != nil || !ok {
		return nil, err
	}
	return &v, nil
}
func (e *Executor) ReleaseOwned(ctx context.Context, c oc.Call, v contract.ManagedIP, reconcile bool) oc.Result {
	ok, err := ownedReceipt(ctx, e.deps.Effects, c.Target.Binding.AccountID, c.Target.Binding.Region, v)
	if err != nil {
		return observeError(err)
	}
	if !ok {
		return attention("IP_OWNERSHIP_UNCONFIRMED", v.Address.ARN)
	}
	cfg, err := e.config(ctx, c, false)
	if err != nil {
		return observeError(err)
	}
	live, err := e.deps.Cloud.Address(ctx, cfg, v.Service, c.Target.Binding.AWSAccountID, v.Address.ID)
	if err != nil {
		return observeError(err)
	}
	if live != nil && (live.Tags["aws-panel:step"] != v.OwnerStepID || live.Tags["aws-panel:token"] != v.OwnerToken || live.Tags["aws-panel:spec"] != v.OwnerSpecHash || live.Tags["aws-panel:instance"] != v.InstanceID) {
		return attention("IP_OWNERSHIP_CHANGED", live.ARN)
	}
	c.Kind = "release"
	c.Completed = nil
	c.Input = mustJSON(contract.Spec{Target: contract.Target{ConnectionID: c.Target.Binding.ConnectionID, Region: c.Target.Binding.Region, Service: v.Service, Action: "release", AddressID: v.Address.ID}, Address: &v.Address})
	if reconcile {
		return e.Reconcile(ctx, c)
	}
	return e.Execute(ctx, c)
}
