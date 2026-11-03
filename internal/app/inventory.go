package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/Ember-Moth/aws-panel/internal/module/inventory"
)

func runInventory(ctx context.Context, service inventory.Service, logger *slog.Logger) func() {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		var lastWarning time.Time
		for {
			work, stop := context.WithTimeout(ctx, 45*time.Second)
			err := service.Tick(work)
			stop()
			if err != nil && ctx.Err() == nil && time.Since(lastWarning) > 30*time.Second {
				logger.Warn("inventory scheduling temporarily unavailable")
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
