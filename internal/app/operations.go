package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/herta-network/aws-panel/internal/module/accounts"
	ac "github.com/herta-network/aws-panel/internal/module/accounts/contract"
	"github.com/herta-network/aws-panel/internal/module/operations"
	oc "github.com/herta-network/aws-panel/internal/module/operations/contract"
)

type jobConnections struct{ accounts accounts.Service }

func (c jobConnections) Inspect(ctx context.Context, actorID int64, id, region string, write bool) (oc.Binding, error) {
	value, err := c.accounts.Inspect(ctx, actorID, id, region, write)
	if errors.Is(err, ac.ErrForbidden) {
		return oc.Binding{}, oc.ErrForbidden
	}
	if errors.Is(err, ac.ErrUnavailable) {
		return oc.Binding{}, oc.ErrUnavailable
	}
	if err != nil {
		return oc.Binding{}, oc.ErrBinding
	}
	result := oc.Binding{AccountID: value.AccountID, ConnectionID: value.ConnectionID, AWSAccountID: value.AWSAccountID, Partition: value.Partition, Region: value.Region, Fingerprint: value.Fingerprint}
	for _, node := range value.Chain {
		result.Chain = append(result.Chain, oc.ConnectionVersion{ID: node.ID, Version: node.Version, ProxyID: node.ProxyID, ProxyVersion: node.ProxyVersion})
	}
	return result, nil
}

func runOperations(ctx context.Context, service operations.Service, logger *slog.Logger) func() {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		var lastWarning time.Time
		for {
			work, stop := context.WithTimeout(ctx, 15*time.Second)
			err := service.Recover(work)
			if err == nil {
				err = service.Publish(work)
			}
			stop()
			if err != nil && ctx.Err() == nil && time.Since(lastWarning) > 30*time.Second {
				logger.Warn("task recovery or outbox temporarily unavailable")
				lastWarning = time.Now()
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { cancel(); <-done }
}
