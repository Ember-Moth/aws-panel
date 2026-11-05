// Package app is the composition root. Only cmd imports this root package.
package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/Ember-Moth/aws-panel/internal/app/bootstrap"
	"github.com/Ember-Moth/aws-panel/internal/app/buildinfo"
	"github.com/Ember-Moth/aws-panel/internal/app/lifecycle"
	"github.com/Ember-Moth/aws-panel/internal/app/migration/schema"
	"github.com/Ember-Moth/aws-panel/internal/app/scheduler"
	"github.com/Ember-Moth/aws-panel/internal/app/state"
	"github.com/Ember-Moth/aws-panel/internal/config"
	"github.com/Ember-Moth/aws-panel/internal/infra/catalogcache"
	"github.com/Ember-Moth/aws-panel/internal/infra/postgres"
	"github.com/Ember-Moth/aws-panel/internal/infra/taskqueue"
	"github.com/Ember-Moth/aws-panel/internal/module/accounts"
	"github.com/Ember-Moth/aws-panel/internal/module/addresses"
	"github.com/Ember-Moth/aws-panel/internal/module/compute"
	"github.com/Ember-Moth/aws-panel/internal/module/dashboard"
	"github.com/Ember-Moth/aws-panel/internal/module/inventory"
	"github.com/Ember-Moth/aws-panel/internal/module/lightsail"
	"github.com/Ember-Moth/aws-panel/internal/module/operations"
	oc "github.com/Ember-Moth/aws-panel/internal/module/operations/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/platform"
	"github.com/Ember-Moth/aws-panel/internal/module/quotas"
	"github.com/Ember-Moth/aws-panel/internal/module/regions"
	"github.com/Ember-Moth/aws-panel/internal/repository/kernel"
	"github.com/Ember-Moth/aws-panel/internal/transport/http/routes"
	"github.com/Ember-Moth/aws-panel/internal/transport/http/security"
	httpserver "github.com/Ember-Moth/aws-panel/internal/transport/http/server"
	"github.com/Ember-Moth/aws-panel/internal/transport/task"
	paneltrace "github.com/Ember-Moth/aws-panel/pkg/trace"
	"github.com/Ember-Moth/aws-panel/web"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
)

func Run(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	stopTrace := paneltrace.Setup(buildinfo.Version)
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = stopTrace(stopCtx)
	}()

	bootCtx, bootCancel := context.WithTimeout(ctx, 15*time.Second)
	defer bootCancel()
	db, pool, err := postgres.Open(bootCtx, cfg.Database)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := schema.Check(bootCtx, pool); err != nil {
		return err
	}

	redisClient := redis.NewClient(&redis.Options{
		Addr: cfg.Redis.Address, Password: cfg.Redis.Password, DB: cfg.Redis.DB,
		DialTimeout: 5 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second,
	})
	defer redisClient.Close()
	if err := redisClient.Ping(bootCtx).Err(); err != nil {
		return errors.New("cannot connect to Redis; check configuration and permissions")
	}

	runtime := &state.Runtime{}
	platformService := platform.New(platform.Deps{
		Store:   platform.NewRepository(kernel.Connection{DB: db}),
		Runtime: runtime.Read, Version: buildinfo.Version, Revision: buildinfo.Revision,
		HeartbeatInterval: cfg.Worker.HeartbeatInterval,
	})
	if err := bootstrap.Load(bootCtx, platformService, runtime); err != nil {
		return err
	}
	accountStore := accounts.NewRepository(kernel.Connection{DB: db}, platform.NewAuditAppender, operations.NewReferenceGuard)
	catalog := accounts.NewCatalog(accountStore)
	identityService := newIdentity(cfg, db, redisClient, catalog)
	accountService := accounts.New(accounts.Deps{Store: accountStore, Access: identityService, KeyringFile: cfg.Secrets.KeyringFile})
	if err := accountService.CheckVault(bootCtx); err != nil {
		return err
	}
	defer accountService.Close()
	client := asynq.NewClient(taskqueue.RedisOption(cfg.Redis))
	defer client.Close()
	operationStore := operations.NewRepository(kernel.Connection{DB: db}, platform.NewAuditAppender, accounts.NewBindingGuard)
	inventoryStore := inventory.NewRepository(kernel.Connection{DB: db}, platform.NewAuditAppender, operations.NewExecutionGuard)
	dashboardStore := dashboard.NewRepository(kernel.Connection{DB: db}, operations.NewExecutionGuard)
	lightsailStore := lightsail.NewRepository(kernel.Connection{DB: db}, operations.NewExecutionGuard)
	operationService, err := operations.New(operations.Deps{Store: operationStore, Access: identityService, Connections: jobConnections{accounts: accountService}, Publisher: task.Publisher{Client: client}, CloudWritesEnabled: cfg.Cloud.WritesEnabled, Executors: []oc.Executor{operations.NewDiagnostic(operationStore), dashboard.NewExecutor(dashboard.ExecutorDeps{Clients: accountService, Store: dashboardStore}), inventory.NewExecutor(inventory.ExecutorDeps{Store: inventoryStore, Clients: accountService}), regions.NewExecutor(regions.ExecutorDeps{Clients: accountService}), lightsail.NewExecutor(lightsail.ExecutorDeps{Store: lightsailStore, Clients: accountService, LaunchSecrets: accountService, Effects: operations.NewEffectStore(kernel.Connection{DB: db})}), addresses.NewExecutor(addresses.ExecutorDeps{Clients: accountService, Effects: operations.NewEffectStore(kernel.Connection{DB: db})}), compute.NewExecutor(compute.ExecutorDeps{Clients: accountService, LaunchSecrets: accountService, Effects: operations.NewEffectStore(kernel.Connection{DB: db})})}})
	if err != nil {
		return err
	}
	inventoryService := inventory.New(inventory.Deps{Store: inventoryStore, Accounts: accountService, Access: identityService, Jobs: operationService, Observer: operations.NewObserver(operationStore)})
	dashboardService := dashboard.New(dashboard.Deps{Accounts: accountService, Access: identityService, Inventory: inventoryService, Store: dashboardStore, Jobs: operationService})
	directoryCache := catalogcache.New(ctx, redisClient)
	defer directoryCache.Close()
	regionService := regions.New(regions.Deps{Cache: directoryCache, Accounts: accountService, Access: identityService, Jobs: operationService, WritesEnabled: cfg.Cloud.WritesEnabled})
	lightsailService := lightsail.New(lightsail.Deps{Cache: directoryCache, Accounts: accountService, Access: identityService, Jobs: operationService, WritesEnabled: cfg.Cloud.WritesEnabled})
	addressService := addresses.New(addresses.Deps{Cache: directoryCache, Accounts: accountService, Access: identityService, Jobs: operationService, WritesEnabled: cfg.Cloud.WritesEnabled})
	computeService := compute.New(compute.Deps{Cache: directoryCache, Effects: operations.NewEffectStore(kernel.Connection{DB: db}), Accounts: accountService, Access: identityService, Jobs: operationService, WritesEnabled: cfg.Cloud.WritesEnabled})
	quotaService := quotas.New(quotas.Deps{Cache: directoryCache, Accounts: accountService, Access: identityService})
	policy, err := security.New(cfg)
	if err != nil {
		return err
	}
	renderer, err := web.NewRenderer()
	if err != nil {
		return errors.New("cannot load embedded page templates")
	}
	renderer.PreviewMode = !cfg.Cloud.WritesEnabled
	bootCancel()

	ready := &lifecycle.Readiness{}
	worker := task.New(cfg, platformService, operationService, logger)
	scheduled, err := scheduler.New(cfg, logger)
	if err != nil {
		return errors.New("cannot configure task scheduler")
	}
	httpService, err := httpserver.New(cfg.HTTP, routes.Dependencies{
		Platform: platformService, Identity: identityService, Accounts: accountService, Policy: policy, Renderer: renderer,
		Operations: operationService, Diagnostics: cfg.Worker.Diagnostics,
		Inventory: inventoryService,
		Regions:   regionService,
		Lightsail: lightsailService, Addresses: addressService, Quotas: quotaService, Compute: computeService, Dashboard: dashboardService,
		Shutdown: ctx.Done(),
		Ready: func(ctx context.Context) bool {
			if !ready.Ready() || !worker.Ready() {
				return false
			}
			ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			return pool.PingContext(ctx) == nil && redisClient.Ping(ctx).Err() == nil
		},
	}, logger)
	if err != nil {
		return err
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout)
		defer cancel()
		_ = httpService.Shutdown(stopCtx)
	}()

	if err := worker.Start(); err != nil {
		return errors.New("cannot start task worker")
	}
	defer worker.Shutdown()
	stopOperations := runOperations(ctx, operationService, logger)
	defer stopOperations()
	stopInventory := runInventory(ctx, inventoryService, logger)
	defer stopInventory()
	if err := scheduled.Start(); err != nil {
		return errors.New("cannot start task scheduler")
	}
	defer scheduled.Shutdown()
	startCtx, startCancel := context.WithTimeout(ctx, 5*time.Second)
	err = httpService.Start(startCtx)
	startCancel()
	if err != nil {
		return err
	}

	heartbeat, err := taskqueue.NewTask(ctx, taskqueue.HeartbeatType, struct{}{})
	if err != nil {
		return err
	}
	queueCtx, queueCancel := context.WithTimeout(ctx, 5*time.Second)
	_, err = client.EnqueueContext(queueCtx, heartbeat, asynq.MaxRetry(3), asynq.Timeout(10*time.Second))
	queueCancel()
	if err != nil {
		return errors.New("cannot enqueue worker startup check")
	}
	ready.Set(true)
	logger.Info("AWS Panel ready", "address", cfg.HTTP.Address, "timezone", config.Timezone, "version", buildinfo.Version)

	select {
	case <-ctx.Done():
		err = nil
	case runErr := <-httpService.Done():
		if runErr != nil {
			err = errors.New("HTTP service exited unexpectedly")
		} else {
			err = errors.New("HTTP service stopped unexpectedly")
		}
	}

	ready.Set(false)
	logger.Info("stopping AWS Panel")
	scheduled.Shutdown()
	stopInventory()
	stopOperations()
	worker.StopPolling()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout)
	if stopErr := httpService.Shutdown(stopCtx); stopErr != nil {
		logger.Warn("HTTP shutdown did not complete within its deadline")
	}
	stopCancel()
	worker.Shutdown()
	logger.Info("AWS Panel stopped")
	return err
}
