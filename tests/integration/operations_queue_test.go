package integration

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/herta-network/aws-panel/internal/infra/taskqueue"
	"github.com/herta-network/aws-panel/internal/module/operations"
	oc "github.com/herta-network/aws-panel/internal/module/operations/contract"
	tasktransport "github.com/herta-network/aws-panel/internal/transport/task"
	"github.com/hibiken/asynq"
)

func TestOperationsRealRedisOutboxAndLostMessages(t *testing.T) {
	address := os.Getenv("AWS_PANEL_TEST_REDIS_ADDRESS")
	if address == "" {
		t.Skip("isolated Redis not configured")
	}
	h := newOperations(t)
	ctx := context.Background()
	option := asynq.RedisClientOpt{Addr: address}
	queueName := "operations-test-" + uuid.NewString()
	client := asynq.NewClient(option)
	t.Cleanup(func() { client.Close() })
	inspector := asynq.NewInspector(option)
	t.Cleanup(func() { inspector.DeleteQueue(queueName, true); inspector.Close() })
	failedMark := &failedPublishMark{OperationsStore: h.store}
	service, err := operations.New(operations.Deps{Store: failedMark, Access: h.members, Publisher: tasktransport.Publisher{Client: client, Queue: queueName}, Executors: []oc.Executor{operations.NewDiagnostic(h.store)}, RedeliveryAfter: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	h.service = service
	job := h.submit(t, "response_lost", 1)
	failedMark.fail.Store(true)
	if service.Publish(ctx) == nil {
		t.Fatal("post-enqueue marker failure not injected")
	}
	info, err := inspector.GetQueueInfo(queueName)
	if err != nil || info.Pending != 1 {
		t.Fatal("Redis did not accept the outbox message")
	}
	h.expedite(t, job.ID)
	if err := service.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	info, err = inspector.GetQueueInfo(queueName)
	if err != nil || info.Pending != 1 {
		t.Fatal("outbox ID did not deduplicate Redis delivery")
	}
	removed, err := inspector.DeleteAllPendingTasks(queueName)
	if err != nil || removed != 1 {
		t.Fatal("could not inject lost Redis message")
	}
	h.expedite(t, job.ID)
	if err := service.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if err := service.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	info, err = inspector.GetQueueInfo(queueName)
	if err != nil || info.Pending != 1 {
		t.Fatal("database recovery did not recreate the lost Redis delivery")
	}
	worker := asynq.NewServer(option, asynq.Config{Concurrency: 2, Queues: map[string]int{queueName: 1}, ShutdownTimeout: time.Second, LogLevel: asynq.ErrorLevel})
	mux := asynq.NewServeMux()
	mux.Use(taskqueue.TraceMiddleware)
	mux.HandleFunc(taskqueue.OperationType, tasktransport.OperationHandler(service))
	if err := worker.Start(mux); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(worker.Shutdown)
	deadline := time.Now().Add(12 * time.Second)
	for {
		if err := service.Publish(ctx); err != nil {
			t.Fatal(err)
		}
		detail, err := service.Detail(ctx, h.admin, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if detail.Job.Status == oc.Succeeded {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("real Asynq consumer did not finish the recovered job")
		}
		time.Sleep(100 * time.Millisecond)
	}
	worker.Stop()
	worker.Shutdown()
	var calls int
	if err := h.owner.QueryRow("SELECT sum(request_count) FROM operations_diagnostic_effects").Scan(&calls); err != nil || calls != 1 {
		t.Fatal("queue recovery duplicated the accepted effect")
	}
	if err := inspector.DeleteQueue(queueName, true); err != nil && !errors.Is(err, asynq.ErrQueueNotFound) {
		t.Fatal(err)
	}
}
