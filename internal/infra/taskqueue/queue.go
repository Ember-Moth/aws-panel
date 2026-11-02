package taskqueue

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/herta-network/aws-panel/internal/config"
	"github.com/hibiken/asynq"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

const HeartbeatType = "platform:heartbeat"

type envelope struct {
	Version int               `json:"version"`
	Trace   map[string]string `json:"trace,omitempty"`
	Body    json.RawMessage   `json:"body"`
}

func RedisOption(cfg config.Redis) asynq.RedisClientOpt {
	return asynq.RedisClientOpt{Addr: cfg.Address, Password: cfg.Password, DB: cfg.DB}
}

func NewTask(ctx context.Context, kind string, body any) (*asynq.Task, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	data, err := json.Marshal(envelope{Version: 1, Trace: carrier, Body: payload})
	if err != nil {
		return nil, err
	}
	return asynq.NewTask(kind, data), nil
}

func TraceMiddleware(next asynq.Handler) asynq.Handler {
	return asynq.HandlerFunc(func(ctx context.Context, task *asynq.Task) error {
		var payload envelope
		if err := json.Unmarshal(task.Payload(), &payload); err != nil || payload.Version != 1 {
			return fmt.Errorf("unsupported task envelope: %w", asynq.SkipRetry)
		}
		ctx = otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier(payload.Trace))
		ctx, span := otel.Tracer("aws-panel/tasks").Start(ctx, task.Type())
		defer span.End()
		err := next.ProcessTask(ctx, asynq.NewTask(task.Type(), payload.Body))
		if err != nil {
			span.RecordError(err)
		}
		return err
	})
}

type Logger struct{ Log *slog.Logger }

func (l Logger) Debug(args ...interface{}) { l.Log.Debug(fmt.Sprint(args...)) }
func (l Logger) Info(args ...interface{})  { l.Log.Info(fmt.Sprint(args...)) }
func (l Logger) Warn(args ...interface{})  { l.Log.Warn(fmt.Sprint(args...)) }
func (l Logger) Error(args ...interface{}) { l.Log.Error(fmt.Sprint(args...)) }
func (l Logger) Fatal(args ...interface{}) {
	l.Log.Error(fmt.Sprint(args...))
	panic("task queue fatal error")
}
