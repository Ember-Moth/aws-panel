package member

import (
	"context"
	"errors"

	"github.com/Ember-Moth/aws-panel/internal/auth"
	"github.com/Ember-Moth/aws-panel/internal/infra/clock"
	"github.com/Ember-Moth/aws-panel/internal/module/identity/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/identity/entity"
	"github.com/Ember-Moth/aws-panel/internal/repository"
	"github.com/google/uuid"
)

func (s *Service) Principal(ctx context.Context, id int64) (auth.Principal, error) {
	member, err := s.deps.Store.MemberByID(ctx, id)
	if errors.Is(err, contract.ErrNotFound) || err == nil && !member.Active {
		return auth.Principal{}, auth.ErrDenied
	}
	if err != nil {
		return auth.Principal{}, err
	}
	return auth.Principal{ID: member.ID, Username: member.Username, DisplayName: member.DisplayName, Role: member.Role}, nil
}
func (s *Service) AccountScope(ctx context.Context, id int64) ([]string, error) {
	if _, err := s.Principal(ctx, id); err != nil {
		return nil, err
	}
	rows, err := s.deps.Store.AccountGrants(ctx, id)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(rows))
	for _, row := range rows {
		result = append(result, row.AccountID)
	}
	return result, nil
}
func (s *Service) AccountAccess(ctx context.Context, id int64, account string, write bool) (bool, error) {
	actor, err := s.Principal(ctx, id)
	if err != nil {
		return false, err
	}
	if actor.Role == contract.RoleAdmin {
		return true, nil
	}
	if write && actor.Role != contract.RoleOperator {
		return false, nil
	}
	if actor.Role != contract.RoleOperator && actor.Role != contract.RoleViewer {
		return false, nil
	}
	rows, err := s.deps.Store.AccountGrants(ctx, id)
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		if row.AccountID == account {
			return !write || row.Access == "operate", nil
		}
	}
	return false, nil
}
func (s *Service) ListAccountGrants(ctx context.Context, actorID, memberID int64) ([]contract.AccountGrant, error) {
	if err := s.admin(ctx, actorID); err != nil {
		return nil, err
	}
	if _, err := s.deps.Store.MemberByID(ctx, memberID); err != nil {
		return nil, err
	}
	rows, err := s.deps.Store.AccountGrants(ctx, memberID)
	if err != nil {
		return nil, err
	}
	result := make([]contract.AccountGrant, 0, len(rows))
	for _, row := range rows {
		result = append(result, contract.AccountGrant{AccountID: row.AccountID, Access: row.Access, UpdatedAt: clock.In(row.UpdatedAt)})
	}
	return result, nil
}
func (s *Service) SetAccountGrant(ctx context.Context, actorID, memberID int64, req contract.GrantRequest, meta auth.RequestMeta) error {
	if err := s.admin(ctx, actorID); err != nil {
		return err
	}
	if _, err := uuid.Parse(req.AccountID); err != nil {
		return contract.ErrValidation
	}
	if req.Access != "read" && req.Access != "operate" && req.Access != "none" {
		return contract.ErrValidation
	}
	if req.Access != "none" {
		if s.deps.AccountExists == nil {
			return contract.ErrUnavailable
		}
		exists, err := s.deps.AccountExists(ctx, req.AccountID)
		if err != nil {
			return contract.ErrUnavailable
		}
		if !exists {
			return contract.ErrNotFound
		}
	}
	return s.deps.Store.InIdentityTx(ctx, func(tx repository.IdentityTx) error {
		if err := tx.LockAdministration(ctx); err != nil {
			return err
		}
		actor, err := requireAdmin(ctx, tx, actorID)
		if err != nil {
			return err
		}
		target, err := tx.MemberForUpdate(ctx, memberID)
		if err != nil {
			return err
		}
		if target.Role == contract.RoleAdmin && req.Access != "none" {
			return contract.Invalid("管理员无需单独分配账号权限")
		}
		value := entity.AccountGrant{MemberID: memberID, AccountID: req.AccountID, Access: req.Access, UpdatedAt: clock.Now()}
		if err := tx.SetAccountGrant(ctx, value); err != nil {
			return err
		}
		return tx.AppendAudit(ctx, event(meta, &actor, "identity.account_grant.changed", memberID, "success", map[string]string{"account_id": req.AccountID, "access": req.Access}))
	})
}

func (s *Service) AccountGrantPage(ctx context.Context, actorID int64, accountID string, page int) (*contract.GrantPage, error) {
	if err := s.admin(ctx, actorID); err != nil {
		return nil, err
	}
	if s.deps.AccountLabel == nil {
		return nil, contract.ErrUnavailable
	}
	label, err := s.deps.AccountLabel(ctx, accountID)
	if err != nil {
		return nil, contract.ErrNotFound
	}
	members, err := s.ListMembers(ctx, actorID, page)
	if err != nil {
		return nil, err
	}
	result := &contract.GrantPage{AccountID: accountID, Label: label, Page: page, Items: []contract.GrantMember{}}
	if page > 1 {
		result.Previous = page - 1
	}
	if int64(page*members.PageSize) < members.Total {
		result.Next = page + 1
	}
	for _, member := range members.Items {
		entry := contract.GrantMember{Member: member, Access: "none"}
		grants, err := s.deps.Store.AccountGrants(ctx, member.ID)
		if err != nil {
			return nil, err
		}
		for _, grant := range grants {
			if grant.AccountID == accountID {
				entry.Access = grant.Access
				break
			}
		}
		result.Items = append(result.Items, entry)
	}
	return result, nil
}
