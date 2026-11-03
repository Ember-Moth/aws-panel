package identity

import (
	"context"
	"time"

	"github.com/herta-network/aws-panel/internal/auth"
	"github.com/herta-network/aws-panel/internal/module/identity/contract"
	"github.com/herta-network/aws-panel/internal/module/identity/internal/member"
	"github.com/herta-network/aws-panel/internal/module/identity/internal/repo"
	"github.com/herta-network/aws-panel/internal/repository"
	"github.com/herta-network/aws-panel/internal/repository/kernel"
)

type Service interface {
	Bootstrap(context.Context, contract.CreateMemberRequest, auth.RequestMeta) (contract.Member, error)
	Login(context.Context, contract.LoginRequest, auth.RequestMeta) (contract.LoginGrant, error)
	Authenticate(context.Context, string) (contract.Session, error)
	Logout(context.Context, string, auth.RequestMeta) error
	ChangePassword(context.Context, int64, contract.ChangePasswordRequest, auth.RequestMeta) error
	ListMembers(context.Context, int64, int) (*contract.MemberList, error)
	Member(context.Context, int64, int64) (contract.Member, error)
	CreateMember(context.Context, int64, contract.CreateMemberRequest, auth.RequestMeta) (contract.Member, error)
	UpdateMember(context.Context, int64, int64, contract.UpdateMemberRequest, auth.RequestMeta) (contract.Member, error)
	Principal(context.Context, int64) (auth.Principal, error)
	AccountScope(context.Context, int64) ([]string, error)
	AccountAccess(context.Context, int64, string, bool) (bool, error)
	ListAccountGrants(context.Context, int64, int64) ([]contract.AccountGrant, error)
	SetAccountGrant(context.Context, int64, int64, contract.GrantRequest, auth.RequestMeta) error
	AccountGrantPage(context.Context, int64, string, int) (*contract.GrantPage, error)
}
type LoginLimiter interface {
	Allow(context.Context, string, string) (bool, error)
}
type Deps struct {
	Store         repository.IdentityStore
	Limiter       LoginLimiter
	SessionTTL    time.Duration
	AccountExists func(context.Context, string) (bool, error)
	AccountLabel  func(context.Context, string) (string, error)
}

func New(deps Deps) Service {
	return member.New(member.Deps{Store: deps.Store, Limiter: deps.Limiter, SessionTTL: deps.SessionTTL, AccountExists: deps.AccountExists, AccountLabel: deps.AccountLabel})
}
func NewRepository(connection kernel.Connection, audit kernel.AuditFactory) repository.IdentityStore {
	return repo.New(connection, audit)
}
func Allowed(role, action string) bool { return member.Allowed(role, action) }
