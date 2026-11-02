package kernel

import (
	"context"
	"time"

	"github.com/herta-network/aws-panel/internal/module/platform/entity"
)

// PlatformStore grants only the platform module's persistence capabilities.
type PlatformStore interface {
	Runtime(context.Context) (entity.Runtime, error)
	TouchHeartbeat(context.Context, string, time.Time) error
	Heartbeat(context.Context, string) (*time.Time, error)
}
