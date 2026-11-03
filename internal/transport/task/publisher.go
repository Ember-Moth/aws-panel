package task

import (
	"context"
	"errors"
	"time"

	"github.com/herta-network/aws-panel/internal/infra/taskqueue"
	"github.com/herta-network/aws-panel/internal/module/operations/contract"
	"github.com/hibiken/asynq"
)

type Publisher struct {
	Client *asynq.Client
	Queue  string
}

func (p Publisher) Publish(ctx context.Context, id string, message contract.Message) error {
	task, err := taskqueue.NewTask(ctx, taskqueue.OperationType, message)
	if err != nil {
		return contract.ErrInvalid
	}
	options := []asynq.Option{asynq.TaskID("operations-" + id), asynq.MaxRetry(3), asynq.Timeout(3 * time.Minute)}
	if p.Queue != "" {
		options = append(options, asynq.Queue(p.Queue))
	}
	_, err = p.Client.EnqueueContext(ctx, task, options...)
	if errors.Is(err, asynq.ErrTaskIDConflict) {
		return nil
	}
	return err
}
