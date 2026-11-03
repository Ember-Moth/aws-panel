package service

import (
	"context"
	"github.com/herta-network/aws-panel/internal/auth"
	"github.com/herta-network/aws-panel/internal/infra/clock"
	"github.com/herta-network/aws-panel/internal/module/accounts/contract"
	"github.com/herta-network/aws-panel/internal/repository"
)

func (s *Service) Delete(ctx context.Context, actorID int64, kind, id string, version int64, meta auth.RequestMeta) error {
	actor, err := s.actor(ctx, actorID, true)
	if err != nil {
		return err
	}
	if !validID(id) || version < 1 {
		return contract.ErrInvalid
	}
	return s.deps.Store.InAccountsTx(ctx, func(tx repository.AccountsTx) error {
		if err := tx.LockAccounts(ctx); err != nil {
			return err
		}
		if err := tx.CheckRemoval(ctx, kind, id); err != nil {
			return err
		}
		switch kind {
		case "connection":
			row, err := tx.Connection(ctx, id)
			if err != nil {
				return err
			}
			if row.Version != version {
				return contract.ErrConflict
			}
			count, err := tx.SourceUsage(ctx, id)
			if err != nil {
				return err
			}
			if count > 0 {
				return contract.Invalid("此连接仍是其他角色的来源，请先修改或删除依赖连接")
			}
			if err := tx.DeleteConnection(ctx, id); err != nil {
				return err
			}
			if err := tx.DeleteSecret(ctx, row.SecretID); err != nil {
				return err
			}
		case "proxy":
			row, err := tx.Proxy(ctx, id)
			if err != nil {
				return err
			}
			if row.Version != version {
				return contract.ErrConflict
			}
			count, err := tx.ProxyUsage(ctx, id)
			if err != nil {
				return err
			}
			if count > 0 {
				return contract.Invalid("代理仍被连接引用，请先修改这些连接的出口")
			}
			if err := tx.DeleteProxy(ctx, id); err != nil {
				return err
			}
			if row.SecretID != nil {
				if err := tx.DeleteSecret(ctx, *row.SecretID); err != nil {
					return err
				}
			}
		case "aws_account":
			row, err := tx.Account(ctx, id)
			if err != nil {
				return err
			}
			if row.DeletedAt != nil {
				return contract.ErrNotFound
			}
			if row.Version != version {
				return contract.ErrConflict
			}
			children, err := tx.Connections(ctx, id)
			if err != nil {
				return err
			}
			if len(children) > 0 {
				return contract.Invalid("请先删除此账号的访问连接")
			}
			secrets, err := tx.Secrets(ctx)
			if err != nil {
				return err
			}
			for _, secret := range secrets {
				if secret.OwnerType == "instance_launch" && secret.OwnerID == id {
					if err := tx.DeleteSecret(ctx, secret.ID); err != nil {
						return err
					}
				}
			}
			now := clock.Now()
			row.DeletedAt = &now
			row.Active = false
			row.Version++
			row.UpdatedAt = now
			if err := tx.SaveAccount(ctx, row, version); err != nil {
				return err
			}
		default:
			return contract.ErrInvalid
		}
		return tx.AppendAudit(ctx, audit(meta, actor, "accounts."+kind+".deleted", kind, id, nil))
	})
}
