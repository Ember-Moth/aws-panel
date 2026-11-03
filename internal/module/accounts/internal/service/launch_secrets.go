package service

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"

	"github.com/Ember-Moth/aws-panel/internal/auth"
	"github.com/Ember-Moth/aws-panel/internal/auth/unixpassword"
	"github.com/Ember-Moth/aws-panel/internal/auth/vault"
	"github.com/Ember-Moth/aws-panel/internal/module/accounts/contract"
	"github.com/Ember-Moth/aws-panel/internal/repository"
	"github.com/google/uuid"
)

type launchPayload struct {
	ActorID      int64                  `json:"actor_id"`
	Binding      contract.LaunchBinding `json:"binding"`
	PasswordHash string                 `json:"password_hash"`
}

var launchTargetHash = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (s *Service) launchAccess(ctx context.Context, actorID int64, binding contract.LaunchBinding) (auth.Principal, error) {
	actor, err := s.actor(ctx, actorID, false)
	if err != nil {
		return actor, err
	}
	if actor.Role != "admin" && actor.Role != "operator" {
		return actor, contract.ErrForbidden
	}
	if !validID(binding.AccountID) || !launchTargetHash.MatchString(binding.TargetHash) {
		return actor, contract.ErrInvalid
	}
	if err = s.allowed(ctx, actorID, binding.AccountID, true); err != nil {
		return actor, err
	}
	account, err := s.deps.Store.Account(ctx, binding.AccountID)
	if err != nil {
		return actor, err
	}
	if !account.Active || account.DeletedAt != nil {
		return actor, contract.ErrDisabled
	}
	return actor, nil
}
func (s *Service) SaveLaunchSecret(ctx context.Context, actorID int64, binding contract.LaunchBinding, hash string) (string, error) {
	actor, err := s.launchAccess(ctx, actorID, binding)
	if err != nil {
		return "", err
	}
	if !unixpassword.ValidHash(hash) {
		return "", contract.ErrInvalid
	}
	id := uuid.NewString()
	err = s.deps.Store.InAccountsTx(ctx, func(tx repository.AccountsTx) error {
		if err := tx.LockAccounts(ctx); err != nil {
			return err
		}
		account, err := tx.Account(ctx, binding.AccountID)
		if err != nil {
			return err
		}
		if !account.Active || account.DeletedAt != nil {
			return contract.ErrDisabled
		}
		if err := s.seal(ctx, tx, id, "instance_launch", binding.AccountID, "root_password_hash/"+id, launchPayload{ActorID: actorID, Binding: binding, PasswordHash: hash}); err != nil {
			return err
		}
		return tx.AppendAudit(ctx, audit(auth.Meta(ctx), actor, "accounts.launch_secret.created", "instance_launch", id, map[string]string{"account_id": binding.AccountID, "source": "instance.root_password"}))
	})
	return id, err
}
func (s *Service) LaunchSecret(ctx context.Context, actorID int64, id string, binding contract.LaunchBinding) (string, error) {
	if _, err := s.launchAccess(ctx, actorID, binding); err != nil {
		return "", err
	}
	if !validID(id) {
		return "", contract.ErrInvalid
	}
	row, err := s.deps.Store.Secret(ctx, id)
	if err != nil {
		return "", err
	}
	if row.OwnerType != "instance_launch" || row.OwnerID != binding.AccountID || row.Purpose != "root_password_hash/"+id {
		return "", contract.ErrForbidden
	}
	ring, err := vault.Load(s.deps.KeyringFile)
	if err != nil {
		return "", contract.ErrVault
	}
	raw, err := ring.Open(row.Binding(), row.Encrypted())
	if err != nil {
		return "", contract.ErrVault
	}
	defer clear(raw)
	var value launchPayload
	if json.Unmarshal(raw, &value) != nil || !unixpassword.ValidHash(value.PasswordHash) {
		return "", contract.ErrVault
	}
	if value.ActorID != actorID || value.Binding != binding {
		return "", contract.ErrForbidden
	}
	return value.PasswordHash, nil
}
func (s *Service) DeleteLaunchSecret(ctx context.Context, actorID int64, id string, binding contract.LaunchBinding) error {
	_, err := s.LaunchSecret(ctx, actorID, id, binding)
	if errors.Is(err, contract.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	actor, err := s.actor(ctx, actorID, false)
	if err != nil {
		return err
	}
	return s.deps.Store.InAccountsTx(ctx, func(tx repository.AccountsTx) error {
		if err := tx.LockAccounts(ctx); err != nil {
			return err
		}
		if err := tx.DeleteSecret(ctx, id); err != nil {
			return err
		}
		return tx.AppendAudit(ctx, audit(auth.Meta(ctx), actor, "accounts.launch_secret.deleted", "instance_launch", id, map[string]string{"account_id": binding.AccountID, "reason": "launch_secret_released"}))
	})
}
