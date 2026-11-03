package repository

import (
	"context"
	"time"

	"github.com/Ember-Moth/aws-panel/internal/module/identity/entity"
	"github.com/Ember-Moth/aws-panel/internal/repository/kernel"
)

type IdentityStore interface {
	MemberByUsername(context.Context, string) (entity.Member, error)
	MemberByID(context.Context, int64) (entity.Member, error)
	Session(context.Context, string, time.Time) (entity.Session, entity.Member, error)
	ListMembers(context.Context, int, int) ([]entity.Member, int64, error)
	AccountGrants(context.Context, int64) ([]entity.AccountGrant, error)
	InIdentityTx(context.Context, func(IdentityTx) error) error
}

type IdentityTx interface {
	LockAdministration(context.Context) error
	MemberForUpdate(context.Context, int64) (entity.Member, error)
	MemberCount(context.Context) (int64, error)
	ActiveAdminCount(context.Context) (int64, error)
	CreateMember(context.Context, *entity.Member) error
	UpdateMember(context.Context, entity.Member) error
	CreateSession(context.Context, entity.Session) error
	RevokeSession(context.Context, string, time.Time) error
	RevokeMemberSessions(context.Context, int64, time.Time) error
	SetAccountGrant(context.Context, entity.AccountGrant) error
	kernel.AuditAppender
}
