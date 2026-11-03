package accounts

import (
	"context"
	"github.com/Ember-Moth/aws-panel/internal/auth"
	"github.com/Ember-Moth/aws-panel/internal/module/accounts/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/accounts/internal/awsapi"
	"github.com/Ember-Moth/aws-panel/internal/module/accounts/internal/repo"
	"github.com/Ember-Moth/aws-panel/internal/module/accounts/internal/service"
	"github.com/Ember-Moth/aws-panel/internal/repository"
	"github.com/Ember-Moth/aws-panel/internal/repository/kernel"
	awssdk "github.com/aws/aws-sdk-go-v2/aws"
)

type Access = service.Access
type STS = service.STS

func NewSTS() STS { return awsapi.STS{} }

type IdentityResult = service.IdentityResult
type MaintenanceResult = service.MaintenanceResult
type Service interface {
	SaveLaunchSecret(context.Context, int64, contract.LaunchBinding, string) (string, error)
	LaunchSecret(context.Context, int64, string, contract.LaunchBinding) (string, error)
	DeleteLaunchSecret(context.Context, int64, string, contract.LaunchBinding) error
	Exists(context.Context, string) (bool, error)
	CheckVault(context.Context) error
	VaultReady() bool
	ListAccounts(context.Context, int64) ([]contract.Account, error)
	AccountDetail(context.Context, int64, string) (*contract.AccountDetail, error)
	SaveAccount(context.Context, int64, string, contract.AccountRequest, auth.RequestMeta) (contract.Account, error)
	ListProxies(context.Context, int64) ([]contract.Proxy, error)
	Proxy(context.Context, int64, string) (contract.Proxy, error)
	SaveProxy(context.Context, int64, string, contract.ProxyRequest, auth.RequestMeta) (contract.Proxy, error)
	Connection(context.Context, int64, string) (contract.Connection, error)
	SaveConnection(context.Context, int64, string, string, contract.ConnectionRequest, auth.RequestMeta) (contract.Connection, error)
	Verify(context.Context, int64, string, auth.RequestMeta) (contract.Verification, error)
	ProbeProxy(context.Context, int64, string, auth.RequestMeta) (contract.Verification, error)
	ConfigFor(context.Context, int64, string, string, bool) (awssdk.Config, error)
	Inspect(context.Context, int64, string, string, bool) (contract.Snapshot, error)
	ConfigForBound(context.Context, int64, string, string, bool, string, func(context.Context) error) (awssdk.Config, error)
	Delete(context.Context, int64, string, string, int64, auth.RequestMeta) error
	Close()
}
type Maintenance interface {
	Maintain(context.Context, string, string) (MaintenanceResult, error)
}
type Catalog interface {
	Exists(context.Context, string) (bool, error)
	Label(context.Context, string) (string, error)
}

func NewCatalog(store repository.AccountsStore) Catalog {
	return service.New(service.Deps{Store: store})
}

type Deps struct {
	Store       repository.AccountsStore
	Access      Access
	KeyringFile string
	STS         STS
}

func New(deps Deps) Service {
	if deps.STS == nil {
		deps.STS = awsapi.STS{}
	}
	return service.New(service.Deps{Store: deps.Store, Access: deps.Access, KeyringFile: deps.KeyringFile, STS: deps.STS})
}
func NewMaintenance(store repository.AccountsStore, path string) Maintenance {
	return service.New(service.Deps{Store: store, KeyringFile: path})
}
func NewRepository(connection kernel.Connection, audit kernel.AuditFactory, guards ...kernel.ReferenceGuardFactory) repository.AccountsStore {
	return repo.New(connection, audit, guards...)
}
func NewBindingGuard(connection kernel.Connection) kernel.BindingGuard {
	return service.NewBindingGuard(repo.New(connection, nil))
}
