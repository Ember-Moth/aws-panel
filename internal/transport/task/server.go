package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/Ember-Moth/aws-panel/internal/config"
	"github.com/Ember-Moth/aws-panel/internal/infra/taskqueue"
	"github.com/Ember-Moth/aws-panel/internal/module/operations/contract"
	"github.com/hibiken/asynq"
)

type HeartbeatRecorder interface{ RecordHeartbeat(context.Context) error }
type OperationRunner interface {
	Run(context.Context, contract.Message) error
}

type Service struct {
	server  *asynq.Server
	mux     *asynq.ServeMux
	healthy atomic.Bool
}

func New(cfg config.Config, recorder HeartbeatRecorder, runner OperationRunner, logger *slog.Logger) *Service {
	service := &Service{mux: asynq.NewServeMux()}
	service.server = asynq.NewServer(taskqueue.RedisOption(cfg.Redis), asynq.Config{
		Concurrency:     cfg.Worker.Concurrency,
		ShutdownTimeout: cfg.Worker.ShutdownTimeout,
		Logger:          taskqueue.Logger{Log: logger},
		HealthCheckFunc: func(err error) { service.healthy.Store(err == nil) },
	})
	service.mux.Use(taskqueue.TraceMiddleware)
	service.mux.HandleFunc(taskqueue.HeartbeatType, func(ctx context.Context, _ *asynq.Task) error {
		return recorder.RecordHeartbeat(ctx)
	})
	service.mux.HandleFunc(taskqueue.OperationType, OperationHandler(runner))
	return service
}
func OperationHandler(runner OperationRunner) asynq.HandlerFunc {
	return func(ctx context.Context, task *asynq.Task) error {
		var message contract.Message
		if len(task.Payload()) > 4096 || json.Unmarshal(task.Payload(), &message) != nil {
			return fmt.Errorf("invalid step message: %w", asynq.SkipRetry)
		}
		err := runner.Run(ctx, message)
		if errors.Is(err, contract.ErrInvalid) {
			return fmt.Errorf("invalid step reference: %w", asynq.SkipRetry)
		}
		return err
	}
}

func (s *Service) Start() error {
	if err := s.server.Start(s.mux); err != nil {
		return err
	}
	s.healthy.Store(true)
	return nil
}
func (s *Service) Ready() bool  { return s.healthy.Load() }
func (s *Service) StopPolling() { s.healthy.Store(false); s.server.Stop() }
func (s *Service) Shutdown()    { s.healthy.Store(false); s.server.Shutdown() }
