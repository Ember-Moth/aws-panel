package service

import (
	"context"
	"errors"
	"strconv"

	"github.com/Ember-Moth/aws-panel/internal/auth"
	"github.com/Ember-Moth/aws-panel/internal/auth/vault"
	"github.com/Ember-Moth/aws-panel/internal/infra/clock"
	"github.com/Ember-Moth/aws-panel/internal/repository"
)

type MaintenanceResult struct {
	ActiveKey   string
	Reencrypted int
	KeyUsage    map[string]int
}

// Maintain is only wired to the local CLI, never to a network handler.
func (s *Service) Maintain(ctx context.Context, operation, keyID string) (MaintenanceResult, error) {
	result := MaintenanceResult{KeyUsage: make(map[string]int)}
	err := s.deps.Store.InAccountsTx(ctx, func(tx repository.AccountsTx) error {
		if err := tx.LockAccounts(ctx); err != nil {
			return err
		}
		ring, err := vault.Load(s.deps.KeyringFile)
		if err != nil {
			return err
		}
		rows, err := tx.Secrets(ctx)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if _, err := ring.Open(row.Binding(), row.Encrypted()); err != nil {
				return vault.ErrInvalid
			}
			result.KeyUsage[row.KeyID]++
		}
		if operation == "status" {
			result.ActiveKey = ring.Active
			return nil
		}
		if operation == "retire" {
			if keyID == ring.Active || result.KeyUsage[keyID] > 0 {
				return errors.New("key is active or still referenced by ciphertext")
			}
			if _, ok := ring.Keys[keyID]; !ok {
				return errors.New("key ID does not exist")
			}
			delete(ring.Keys, keyID)
			if err := ring.Write(s.deps.KeyringFile, false); err != nil {
				return err
			}
		} else {
			if operation == "rotate" {
				if _, err := ring.Add(); err != nil {
					return err
				}
				// Persist the new key before changing ciphertext. On any later
				// failure, both old and new ciphertext remain decryptable.
				if err := ring.Write(s.deps.KeyringFile, false); err != nil {
					return err
				}
			} else if operation != "reencrypt" {
				return errors.New("unsupported vault operation")
			}
			for _, row := range rows {
				if row.KeyID == ring.Active {
					continue
				}
				raw, err := ring.Open(row.Binding(), row.Encrypted())
				if err != nil {
					return vault.ErrInvalid
				}
				encrypted, err := ring.Seal(row.Binding(), raw)
				if err != nil {
					return err
				}
				revision := row.Revision
				row.Revision++
				row.Set(encrypted)
				row.UpdatedAt = clock.Now()
				if err := tx.SaveSecret(ctx, row, revision); err != nil {
					return err
				}
				result.Reencrypted++
			}
		}
		result.ActiveKey = ring.Active
		current, err := tx.Secrets(ctx)
		if err != nil {
			return err
		}
		result.KeyUsage = make(map[string]int)
		for _, row := range current {
			result.KeyUsage[row.KeyID]++
		}
		return tx.AppendAudit(ctx, audit(auth.RequestMeta{RequestID: "cli-vault-" + operation}, auth.Principal{}, "accounts.vault."+operation, "vault", "", map[string]string{
			"source": "cli", "key_id": ring.Active, "count": strconv.Itoa(result.Reencrypted),
		}))
	})
	return result, err
}
