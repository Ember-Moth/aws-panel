package member

import (
	"context"
	"crypto/subtle"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/herta-network/aws-panel/internal/auth"
	"github.com/herta-network/aws-panel/internal/auth/password"
	"github.com/herta-network/aws-panel/internal/auth/token"
	"github.com/herta-network/aws-panel/internal/infra/clock"
	"github.com/herta-network/aws-panel/internal/module/identity/contract"
	"github.com/herta-network/aws-panel/internal/module/identity/entity"
	"github.com/herta-network/aws-panel/internal/repository"
	"github.com/herta-network/aws-panel/internal/repository/kernel"
)

type Limiter interface {
	Allow(context.Context, string, string) (bool, error)
}
type Deps struct {
	Store         repository.IdentityStore
	Limiter       Limiter
	SessionTTL    time.Duration
	AccountExists func(context.Context, string) (bool, error)
	AccountLabel  func(context.Context, string) (string, error)
}
type Service struct{ deps Deps }

func New(deps Deps) *Service { return &Service{deps: deps} }

var usernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{2,63}$`)

func Allowed(role, action string) bool {
	switch action {
	case contract.ActionSystemRead, contract.ActionAccountsRead, contract.ActionJobsRead:
		return role == contract.RoleAdmin || role == contract.RoleOperator || role == contract.RoleViewer
	case contract.ActionMembersManage, contract.ActionAuditRead, contract.ActionConnectionsManage, contract.ActionJobsDiagnose:
		return role == contract.RoleAdmin
	case contract.ActionCloudOperate:
		return role == contract.RoleAdmin || role == contract.RoleOperator
	default:
		return false
	}
}
func validRole(role string) bool {
	return role == contract.RoleAdmin || role == contract.RoleOperator || role == contract.RoleViewer
}
func validName(value string) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) > 0 && utf8.RuneCountInString(value) <= 80 && strings.IndexFunc(value, unicode.IsControl) < 0
}
func normalizeCreate(req contract.CreateMemberRequest) (contract.CreateMemberRequest, error) {
	req.Username = strings.ToLower(strings.TrimSpace(req.Username))
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	if !usernamePattern.MatchString(req.Username) {
		return req, contract.Invalid("用户名需要 3 至 64 位小写字母、数字、点、下划线或短横线")
	}
	if !validName(req.DisplayName) {
		return req, contract.Invalid("显示名称需要 1 至 80 个字符")
	}
	if !validRole(req.Role) {
		return req, contract.Invalid("请选择有效角色")
	}
	if err := password.Validate(req.Password); err != nil {
		return req, contract.Invalid(err.Error())
	}
	return req, nil
}
func view(m entity.Member) contract.Member {
	return contract.Member{ID: m.ID, Username: m.Username, DisplayName: m.DisplayName, Role: m.Role, Active: m.Active, Version: m.Version,
		CreatedAt: clock.In(m.CreatedAt), UpdatedAt: clock.In(m.UpdatedAt)}
}
func event(meta auth.RequestMeta, actor *entity.Member, action string, targetID int64, outcome string, details map[string]string) kernel.AuditEntry {
	value := kernel.AuditEntry{Action: action, TargetType: "member", Outcome: outcome, RequestID: meta.RequestID, ClientIP: meta.ClientIP,
		Metadata: details, OccurredAt: clock.Now()}
	if targetID > 0 {
		value.TargetID = strconv.FormatInt(targetID, 10)
	}
	if actor != nil {
		value.ActorID = &actor.ID
		value.ActorName = actor.Username
	}
	return value
}
func requireAdmin(ctx context.Context, tx repository.IdentityTx, id int64) (entity.Member, error) {
	actor, err := tx.MemberForUpdate(ctx, id)
	if err != nil {
		return actor, err
	}
	if !actor.Active || !Allowed(actor.Role, contract.ActionMembersManage) {
		return actor, contract.ErrForbidden
	}
	return actor, nil
}
func (s *Service) admin(ctx context.Context, id int64) error {
	actor, err := s.deps.Store.MemberByID(ctx, id)
	if err != nil {
		return err
	}
	if !actor.Active || !Allowed(actor.Role, contract.ActionMembersManage) {
		return contract.ErrForbidden
	}
	return nil
}

func (s *Service) Bootstrap(ctx context.Context, request contract.CreateMemberRequest, meta auth.RequestMeta) (contract.Member, error) {
	request.Role = contract.RoleAdmin
	req, err := normalizeCreate(request)
	if err != nil {
		return contract.Member{}, err
	}
	hash, err := password.Hash(ctx, req.Password)
	if err != nil {
		return contract.Member{}, contract.ErrUnavailable
	}
	now := clock.Now()
	member := entity.Member{Username: req.Username, DisplayName: req.DisplayName, PasswordHash: hash, Role: contract.RoleAdmin, Active: true, Version: 1, CreatedAt: now, UpdatedAt: now}
	err = s.deps.Store.InIdentityTx(ctx, func(tx repository.IdentityTx) error {
		if err := tx.LockAdministration(ctx); err != nil {
			return err
		}
		count, err := tx.MemberCount(ctx)
		if err != nil {
			return err
		}
		if count != 0 {
			return contract.ErrInitialized
		}
		if err := tx.CreateMember(ctx, &member); err != nil {
			return err
		}
		return tx.AppendAudit(ctx, event(meta, nil, "identity.member.initialized", member.ID, "success", map[string]string{"source": "cli", "role": member.Role}))
	})
	return view(member), err
}

func (s *Service) Login(ctx context.Context, req contract.LoginRequest, meta auth.RequestMeta) (contract.LoginGrant, error) {
	req.Username = strings.ToLower(strings.TrimSpace(req.Username))
	if len(req.Username) > 64 || len(req.Password) > 4*password.MaxLength || !utf8.ValidString(req.Password) {
		return contract.LoginGrant{}, contract.ErrInvalidCredentials
	}
	if s.deps.Limiter == nil {
		return contract.LoginGrant{}, contract.ErrUnavailable
	}
	allowed, err := s.deps.Limiter.Allow(ctx, req.Username, meta.ClientIP)
	if err != nil {
		return contract.LoginGrant{}, contract.ErrUnavailable
	}
	if !allowed {
		// The HTTP access log records 429. Do not let blocked requests amplify
		// a login flood into unbounded persistent audit writes.
		return contract.LoginGrant{}, contract.ErrRateLimited
	}
	member, err := s.deps.Store.MemberByUsername(ctx, req.Username)
	if errors.Is(err, contract.ErrNotFound) {
		if err := password.DummyCompare(ctx, req.Password); err != nil {
			return contract.LoginGrant{}, contract.ErrUnavailable
		}
		if err := s.failedLogin(ctx, meta, nil, "invalid_credentials"); err != nil {
			return contract.LoginGrant{}, err
		}
		return contract.LoginGrant{}, contract.ErrInvalidCredentials
	}
	if err != nil {
		return contract.LoginGrant{}, err
	}
	ok, err := password.Verify(ctx, member.PasswordHash, req.Password)
	if err != nil {
		return contract.LoginGrant{}, contract.ErrUnavailable
	}
	if !ok || !member.Active {
		if err := s.failedLogin(ctx, meta, &member, "invalid_credentials"); err != nil {
			return contract.LoginGrant{}, err
		}
		return contract.LoginGrant{}, contract.ErrInvalidCredentials
	}
	raw, err := token.New()
	if err != nil {
		return contract.LoginGrant{}, contract.ErrUnavailable
	}
	now := clock.Now()
	session := entity.Session{TokenHash: token.Hash(raw), MemberID: member.ID, CreatedAt: now, ExpiresAt: now.Add(s.deps.SessionTTL)}
	err = s.deps.Store.InIdentityTx(ctx, func(tx repository.IdentityTx) error {
		current, err := tx.MemberForUpdate(ctx, member.ID)
		if err != nil {
			return err
		}
		if !current.Active || subtle.ConstantTimeCompare([]byte(current.PasswordHash), []byte(member.PasswordHash)) != 1 {
			return contract.ErrInvalidCredentials
		}
		member = current
		if err := tx.CreateSession(ctx, session); err != nil {
			return err
		}
		return tx.AppendAudit(ctx, event(meta, &member, "identity.login", member.ID, "success", nil))
	})
	if err != nil {
		return contract.LoginGrant{}, err
	}
	return contract.LoginGrant{Token: raw, Session: contract.Session{Member: view(member), ExpiresAt: session.ExpiresAt}}, nil
}

func (s *Service) failedLogin(ctx context.Context, meta auth.RequestMeta, member *entity.Member, reason string) error {
	var id int64
	if member != nil {
		id = member.ID
	}
	return s.deps.Store.InIdentityTx(ctx, func(tx repository.IdentityTx) error {
		return tx.AppendAudit(ctx, event(meta, member, "identity.login", id, "failure", map[string]string{"reason": reason}))
	})
}

func (s *Service) Authenticate(ctx context.Context, raw string) (contract.Session, error) {
	if !token.Valid(raw) {
		return contract.Session{}, contract.ErrUnauthenticated
	}
	session, member, err := s.deps.Store.Session(ctx, token.Hash(raw), clock.Now())
	if errors.Is(err, contract.ErrNotFound) {
		return contract.Session{}, contract.ErrUnauthenticated
	}
	if err != nil {
		return contract.Session{}, err
	}
	return contract.Session{Member: view(member), ExpiresAt: clock.In(session.ExpiresAt)}, nil
}

func (s *Service) Logout(ctx context.Context, raw string, meta auth.RequestMeta) error {
	session, err := s.Authenticate(ctx, raw)
	if err != nil {
		return err
	}
	return s.deps.Store.InIdentityTx(ctx, func(tx repository.IdentityTx) error {
		actor, err := tx.MemberForUpdate(ctx, session.Member.ID)
		if err != nil {
			return err
		}
		if err := tx.RevokeSession(ctx, token.Hash(raw), clock.Now()); err != nil {
			return err
		}
		return tx.AppendAudit(ctx, event(meta, &actor, "identity.logout", actor.ID, "success", nil))
	})
}

func (s *Service) ChangePassword(ctx context.Context, actorID int64, req contract.ChangePasswordRequest, meta auth.RequestMeta) error {
	if err := password.Validate(req.NewPassword); err != nil {
		return contract.Invalid(err.Error())
	}
	member, err := s.deps.Store.MemberByID(ctx, actorID)
	if err != nil {
		return err
	}
	if !member.Active {
		return contract.ErrUnauthenticated
	}
	ok, err := password.Verify(ctx, member.PasswordHash, req.CurrentPassword)
	if err != nil {
		return contract.ErrUnavailable
	}
	if !ok {
		return contract.Invalid("当前密码不正确")
	}
	hash, err := password.Hash(ctx, req.NewPassword)
	if err != nil {
		return contract.ErrUnavailable
	}
	return s.deps.Store.InIdentityTx(ctx, func(tx repository.IdentityTx) error {
		current, err := tx.MemberForUpdate(ctx, actorID)
		if err != nil {
			return err
		}
		if !current.Active {
			return contract.ErrUnauthenticated
		}
		if subtle.ConstantTimeCompare([]byte(current.PasswordHash), []byte(member.PasswordHash)) != 1 {
			return contract.ErrConflict
		}
		current.PasswordHash = hash
		current.Version++
		current.UpdatedAt = clock.Now()
		if err := tx.UpdateMember(ctx, current); err != nil {
			return err
		}
		if err := tx.RevokeMemberSessions(ctx, actorID, clock.Now()); err != nil {
			return err
		}
		return tx.AppendAudit(ctx, event(meta, &current, "identity.password.changed", actorID, "success", nil))
	})
}

func (s *Service) ListMembers(ctx context.Context, actorID int64, page int) (*contract.MemberList, error) {
	if err := s.admin(ctx, actorID); err != nil {
		return nil, err
	}
	if page < 1 || page > 100000 {
		return nil, contract.Invalid("页码不正确")
	}
	members, total, err := s.deps.Store.ListMembers(ctx, page, 20)
	if err != nil {
		return nil, err
	}
	list := &contract.MemberList{Items: make([]contract.Member, 0, len(members)), Total: total, Page: page, PageSize: 20}
	for _, member := range members {
		list.Items = append(list.Items, view(member))
	}
	return list, nil
}
func (s *Service) Member(ctx context.Context, actorID, targetID int64) (contract.Member, error) {
	if err := s.admin(ctx, actorID); err != nil {
		return contract.Member{}, err
	}
	member, err := s.deps.Store.MemberByID(ctx, targetID)
	return view(member), err
}

func (s *Service) CreateMember(ctx context.Context, actorID int64, request contract.CreateMemberRequest, meta auth.RequestMeta) (contract.Member, error) {
	if err := s.admin(ctx, actorID); err != nil {
		return contract.Member{}, err
	}
	req, err := normalizeCreate(request)
	if err != nil {
		return contract.Member{}, err
	}
	hash, err := password.Hash(ctx, req.Password)
	if err != nil {
		return contract.Member{}, contract.ErrUnavailable
	}
	now := clock.Now()
	member := entity.Member{Username: req.Username, DisplayName: req.DisplayName, PasswordHash: hash, Role: req.Role, Active: true, Version: 1, CreatedAt: now, UpdatedAt: now}
	err = s.deps.Store.InIdentityTx(ctx, func(tx repository.IdentityTx) error {
		if err := tx.LockAdministration(ctx); err != nil {
			return err
		}
		actor, err := requireAdmin(ctx, tx, actorID)
		if err != nil {
			return err
		}
		if err := tx.CreateMember(ctx, &member); err != nil {
			return err
		}
		return tx.AppendAudit(ctx, event(meta, &actor, "identity.member.created", member.ID, "success", map[string]string{"role": member.Role}))
	})
	return view(member), err
}

func (s *Service) UpdateMember(ctx context.Context, actorID, targetID int64, req contract.UpdateMemberRequest, meta auth.RequestMeta) (contract.Member, error) {
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	if !validName(req.DisplayName) || !validRole(req.Role) || req.Active == nil || req.Version < 1 {
		return contract.Member{}, contract.ErrValidation
	}
	var target entity.Member
	err := s.deps.Store.InIdentityTx(ctx, func(tx repository.IdentityTx) error {
		if err := tx.LockAdministration(ctx); err != nil {
			return err
		}
		actor, err := requireAdmin(ctx, tx, actorID)
		if err != nil {
			return err
		}
		if targetID == actorID {
			target = actor
		} else {
			target, err = tx.MemberForUpdate(ctx, targetID)
			if err != nil {
				return err
			}
		}
		if target.Version != req.Version {
			return contract.ErrConflict
		}
		if target.Active && target.Role == contract.RoleAdmin && (!*req.Active || req.Role != contract.RoleAdmin) {
			count, err := tx.ActiveAdminCount(ctx)
			if err != nil {
				return err
			}
			if count <= 1 {
				return contract.ErrLastAdmin
			}
		}
		before := target
		target.DisplayName = req.DisplayName
		target.Role = req.Role
		target.Active = *req.Active
		target.Version++
		target.UpdatedAt = clock.Now()
		if err := tx.UpdateMember(ctx, target); err != nil {
			return err
		}
		if target.Role != before.Role || target.Active != before.Active {
			if err := tx.RevokeMemberSessions(ctx, targetID, clock.Now()); err != nil {
				return err
			}
		}
		return tx.AppendAudit(ctx, event(meta, &actor, "identity.member.updated", targetID, "success", map[string]string{
			"role_before": before.Role, "role_after": target.Role, "active_before": strconv.FormatBool(before.Active), "active_after": strconv.FormatBool(target.Active),
		}))
	})
	return view(target), err
}
