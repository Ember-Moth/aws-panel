package service

import (
	"context"

	"github.com/Ember-Moth/aws-panel/internal/infra/clock"
	"github.com/Ember-Moth/aws-panel/internal/module/accounts/contract"
	"github.com/Ember-Moth/aws-panel/internal/repository"
)

// Task submissions hold the common cloud-configuration lock. This reader
// checks references in that same SQL transaction without borrowing a pool slot.
type BindingGuard struct{ store repository.AccountsStore }

func NewBindingGuard(store repository.AccountsStore) *BindingGuard {
	return &BindingGuard{store: store}
}
func (g *BindingGuard) CheckBinding(ctx context.Context, id, fingerprint string) error {
	if !validID(id) || fingerprint == "" {
		return contract.ErrInvalid
	}
	nodes, err := g.store.Graph(ctx, id)
	if err != nil {
		return err
	}
	if len(nodes) == 0 {
		return contract.ErrNotFound
	}
	for _, node := range nodes {
		if node.Account.DeletedAt != nil || !node.Account.Active || !node.Connection.Active || node.Proxy != nil && !node.Proxy.Active {
			return contract.ErrDisabled
		}
		if node.Connection.CredentialExpiresAt != nil && !node.Connection.CredentialExpiresAt.After(clock.Now()) {
			return contract.ErrExpired
		}
	}
	if err := verified(nodes, true); err != nil {
		return err
	}
	actual, _ := stamps(nodes, nil)
	if actual != fingerprint {
		return contract.ErrStale
	}
	return nil
}
