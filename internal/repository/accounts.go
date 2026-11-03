package repository

import (
	"context"
	"github.com/herta-network/aws-panel/internal/module/accounts/entity"
	"github.com/herta-network/aws-panel/internal/repository/kernel"
)

type AccountsStore interface {
	Account(context.Context, string) (entity.Account, error)
	Accounts(context.Context, []string, bool) ([]entity.Account, error)
	Proxy(context.Context, string) (entity.Proxy, error)
	Proxies(context.Context) ([]entity.Proxy, error)
	ProxyUsage(context.Context, string) (int64, error)
	ProxyAccountUsage(context.Context, string) (int64, error)
	ProxyImpact(context.Context, string) ([]entity.Connection, error)
	Connection(context.Context, string) (entity.Connection, error)
	Connections(context.Context, string) ([]entity.Connection, error)
	Graph(context.Context, string) ([]entity.Node, error)
	Secrets(context.Context) ([]entity.Secret, error)
	Secret(context.Context, string) (entity.Secret, error)
	InAccountsTx(context.Context, func(AccountsTx) error) error
}
type AccountsTx interface {
	AccountsStore
	LockAccounts(context.Context) error
	CheckRemoval(context.Context, string, string) error
	SaveAccount(context.Context, entity.Account, int64) error
	SaveProxy(context.Context, entity.Proxy, int64) error
	SaveConnection(context.Context, entity.Connection, int64) error
	SaveSecret(context.Context, entity.Secret, int64) error
	DeleteSecret(context.Context, string) error
	SaveVerification(context.Context, entity.Connection) error
	DeleteConnection(context.Context, string) error
	DeleteProxy(context.Context, string) error
	SourceUsage(context.Context, string) (int64, error)
	kernel.AuditAppender
}
