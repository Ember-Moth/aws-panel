// Package app is the composition root. Only cmd imports this root package.
package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/herta-network/aws-panel/internal/app/bootstrap"
	"github.com/herta-network/aws-panel/internal/app/buildinfo"
	"github.com/herta-network/aws-panel/internal/app/lifecycle"
	"github.com/herta-network/aws-panel/internal/app/migration/schema"
	"github.com/herta-network/aws-panel/internal/app/scheduler"
	"github.com/herta-network/aws-panel/internal/app/state"
	"github.com/herta-network/aws-panel/internal/config"
	"github.com/herta-network/aws-panel/internal/infra/postgres"
	"github.com/herta-network/aws-panel/internal/infra/taskqueue"
	"github.com/herta-network/aws-panel/internal/module/platform"
	"github.com/herta-network/aws-panel/internal/repository/kernel"
	"github.com/herta-network/aws-panel/internal/transport/http/routes"
	httpserver "github.com/herta-network/aws-panel/internal/transport/http/server"
	"github.com/herta-network/aws-panel/internal/transport/task"
	paneltrace "github.com/herta-network/aws-panel/pkg/trace"
	"github.com/herta-network/aws-panel/web"
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
	renderer, err := web.NewRenderer()
	if err != nil {
		return errors.New("cannot load embedded page templates")
	}
	bootCancel()

	ready := &lifecycle.Readiness{}
	worker := task.New(cfg, platformService, logger)
	scheduled, err := scheduler.New(cfg, logger)
	if err != nil {
		return errors.New("cannot configure task scheduler")
	}
	httpService, err := httpserver.New(cfg.HTTP, routes.Dependencies{
		Platform: platformService, Renderer: renderer,
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

	client := asynq.NewClient(taskqueue.RedisOption(cfg.Redis))
	defer client.Close()
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
