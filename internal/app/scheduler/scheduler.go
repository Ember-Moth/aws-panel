package scheduler

import (
	"context"
	"log/slog"
	"time"

	"github.com/Ember-Moth/aws-panel/internal/config"
	"github.com/Ember-Moth/aws-panel/internal/infra/clock"
	"github.com/Ember-Moth/aws-panel/internal/infra/taskqueue"
	"github.com/hibiken/asynq"
)

func New(cfg config.Config, logger *slog.Logger) (*asynq.Scheduler, error) {
	scheduler := asynq.NewScheduler(taskqueue.RedisOption(cfg.Redis), &asynq.SchedulerOpts{
		Location: clock.Location(),
		Logger:   taskqueue.Logger{Log: logger},
	})
	task, err := taskqueue.NewTask(context.Background(), taskqueue.HeartbeatType, struct{}{})
	if err != nil {
		return nil, err
	}
	_, err = scheduler.Register("@every "+cfg.Worker.HeartbeatInterval.String(), task,
		asynq.MaxRetry(3), asynq.Timeout(10*time.Second))
	if err != nil {
		return nil, err
	}
	return scheduler, nil
}
