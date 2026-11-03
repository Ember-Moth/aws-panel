package service

import (
	"context"
	"github.com/Ember-Moth/aws-panel/internal/infra/clock"
	"github.com/Ember-Moth/aws-panel/internal/module/accounts/entity"
)

func (s *Service) currentStatus(ctx context.Context, value entity.Connection) entity.Connection {
	nodes, err := s.deps.Store.Graph(ctx, value.ID)
	if err != nil {
		value.VerificationStatus = "pending"
		return value
	}
	for _, node := range nodes {
		if !node.Connection.Active || !node.Account.Active || node.Account.DeletedAt != nil || node.Proxy != nil && !node.Proxy.Active {
			value.VerificationStatus = "disabled"
			return value
		}
		if node.Connection.CredentialExpiresAt != nil && !node.Connection.CredentialExpiresAt.After(clock.Now()) {
			value.VerificationStatus = "expired"
			return value
		}
	}
	stamp, _ := stamps(nodes, nil)
	if value.VerificationFingerprint != stamp {
		value.VerificationStatus = "pending"
	}
	return value
}
