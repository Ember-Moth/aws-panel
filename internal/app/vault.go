package app

import (
	"context"
	"github.com/herta-network/aws-panel/internal/app/migration/schema"
	"github.com/herta-network/aws-panel/internal/config"
	"github.com/herta-network/aws-panel/internal/infra/postgres"
	"github.com/herta-network/aws-panel/internal/module/accounts"
	"github.com/herta-network/aws-panel/internal/module/platform"
	"github.com/herta-network/aws-panel/internal/repository/kernel"
	"time"
)

func MaintainVault(ctx context.Context, cfg config.Config, operation, keyID string) (accounts.MaintenanceResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	db, pool, err := postgres.Open(ctx, cfg.Database)
	if err != nil {
		return accounts.MaintenanceResult{}, err
	}
	defer pool.Close()
	if err := schema.Check(ctx, pool); err != nil {
		return accounts.MaintenanceResult{}, err
	}
	store := accounts.NewRepository(kernel.Connection{DB: db}, platform.NewAuditAppender)
	return accounts.NewMaintenance(store, cfg.Secrets.KeyringFile).Maintain(ctx, operation, keyID)
}
