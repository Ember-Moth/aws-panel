package app

import (
	"context"
	"time"

	"github.com/Ember-Moth/aws-panel/internal/app/migration/schema"
	"github.com/Ember-Moth/aws-panel/internal/auth"
	"github.com/Ember-Moth/aws-panel/internal/auth/ratelimit"
	"github.com/Ember-Moth/aws-panel/internal/config"
	"github.com/Ember-Moth/aws-panel/internal/infra/postgres"
	"github.com/Ember-Moth/aws-panel/internal/module/identity"
	"github.com/Ember-Moth/aws-panel/internal/module/identity/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/platform"
	"github.com/Ember-Moth/aws-panel/internal/repository/kernel"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type accountCatalog interface {
	Exists(context.Context, string) (bool, error)
	Label(context.Context, string) (string, error)
}

func newIdentity(cfg config.Config, db *gorm.DB, client *redis.Client, lookup ...accountCatalog) identity.Service {
	var limiter identity.LoginLimiter
	if client != nil {
		limiter = ratelimit.New(client, cfg.Auth.LoginWindow, cfg.Auth.LoginIPLimit, cfg.Auth.LoginAccountLimit)
	}
	var exists func(context.Context, string) (bool, error)
	var label func(context.Context, string) (string, error)
	if len(lookup) > 0 {
		exists = lookup[0].Exists
		label = lookup[0].Label
	}
	return identity.New(identity.Deps{
		Store:   identity.NewRepository(kernel.Connection{DB: db}, platform.NewAuditAppender),
		Limiter: limiter, SessionTTL: cfg.Auth.SessionTTL,
		AccountExists: exists,
		AccountLabel:  label,
	})
}

func InitializeAdmin(ctx context.Context, cfg config.Config, username, displayName, password string) (contract.Member, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	db, pool, err := postgres.Open(ctx, cfg.Database)
	if err != nil {
		return contract.Member{}, err
	}
	defer pool.Close()
	if err := schema.Check(ctx, pool); err != nil {
		return contract.Member{}, err
	}
	return newIdentity(cfg, db, nil).Bootstrap(ctx, contract.CreateMemberRequest{Username: username, DisplayName: displayName, Password: password},
		auth.RequestMeta{RequestID: "cli-admin-init"})
}
