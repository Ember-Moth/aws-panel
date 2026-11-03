package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/herta-network/aws-panel/internal/auth/unixpassword"
	ac "github.com/herta-network/aws-panel/internal/module/accounts/contract"
	"github.com/herta-network/aws-panel/internal/module/compute/contract"
)

type LaunchSecrets interface {
	SaveLaunchSecret(context.Context, int64, ac.LaunchBinding, string) (string, error)
	LaunchSecret(context.Context, int64, string, ac.LaunchBinding) (string, error)
	DeleteLaunchSecret(context.Context, int64, string, ac.LaunchBinding) error
}

func launchBinding(target contract.Target, account string) ac.LaunchBinding {
	target.LaunchSecretID = ""
	raw, _ := json.Marshal(target)
	sum := sha256.Sum256(raw)
	return ac.LaunchBinding{AccountID: account, TargetHash: hex.EncodeToString(sum[:])}
}

type preparedLogin struct {
	id      string
	binding ac.LaunchBinding
}

func (s *Service) prepareLogins(ctx context.Context, actorID int64, req contract.Request) ([]contract.Target, func(), error) {
	stored := []preparedLogin{}
	cleanup := func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		for _, value := range stored {
			_ = s.deps.Accounts.DeleteLaunchSecret(cleanupCtx, actorID, value.id, value.binding)
		}
	}
	values := make([]contract.Target, 0, len(req.Targets))
	// Validate the entire batch before storing a secret. Passwords never enter
	// operations.Validate/Plan, their persisted input, or the preview DTO.
	for _, input := range req.Targets {
		value := input.Target
		if value.Action != "create" {
			if input.RootPassword != "" {
				return nil, cleanup, contract.ErrLogin
			}
			values = append(values, value)
			continue
		}
		if value.LaunchSecretID != "" {
			return nil, cleanup, contract.ErrLogin
		}
		method := value.LoginMethod
		if method == "" {
			method = "ssh_key"
		}
		if method != "ssh_key" && method != "root_password" {
			return nil, cleanup, contract.ErrLogin
		}
		if method == "root_password" {
			if unixpassword.Validate(input.RootPassword) != nil {
				return nil, cleanup, contract.ErrLogin
			}
			value.KeyPairName = ""
		} else if input.RootPassword != "" {
			return nil, cleanup, contract.ErrLogin
		}
		value.LoginMethod = method
		if method == "root_password" {
			value.LaunchSecretID = "00000000-0000-4000-8000-000000000001"
		}
		normalized, err := normalize([]contract.Target{value})
		if err != nil {
			return nil, cleanup, err
		}
		value = normalized[0]
		value.LaunchSecretID = ""
		value.LoginMethod = method
		values = append(values, value)
	}
	if len(values) == 0 || len(values) > 50 {
		return nil, cleanup, contract.ErrInvalid
	}
	hashes := map[string]string{}
	for i := range values {
		value := &values[i]
		if value.LoginMethod != "root_password" {
			continue
		}
		snapshot, err := s.deps.Accounts.Inspect(ctx, actorID, value.ConnectionID, value.Region, true)
		if err != nil {
			return nil, cleanup, publicError(err)
		}
		password := req.Targets[i].RootPassword
		hash := hashes[password]
		if hash == "" {
			hash, err = unixpassword.Hash(ctx, password)
			if err != nil {
				return nil, cleanup, contract.ErrLogin
			}
			hashes[password] = hash
		}
		binding := launchBinding(*value, snapshot.AccountID)
		id, err := s.deps.Accounts.SaveLaunchSecret(ctx, actorID, binding, hash)
		if err != nil {
			return nil, cleanup, publicError(err)
		}
		value.LaunchSecretID = id
		stored = append(stored, preparedLogin{id, binding})
	}
	result, err := normalize(values)
	return result, cleanup, err
}
