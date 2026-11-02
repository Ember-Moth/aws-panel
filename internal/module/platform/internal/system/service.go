package system

import (
	"context"
	"time"

	"github.com/herta-network/aws-panel/internal/config"
	"github.com/herta-network/aws-panel/internal/infra/clock"
	"github.com/herta-network/aws-panel/internal/module/platform/contract"
	"github.com/herta-network/aws-panel/internal/repository/kernel"
)

type Deps struct {
	Store             kernel.PlatformStore
	Runtime           func() contract.Runtime
	Version           string
	Revision          string
	HeartbeatInterval time.Duration
}

type Service struct{ deps Deps }

func New(deps Deps) *Service { return &Service{deps: deps} }

func (s *Service) LoadRuntime(ctx context.Context) (contract.Runtime, error) {
	value, err := s.deps.Store.Runtime(ctx)
	if err != nil {
		return contract.Runtime{}, err
	}
	return contract.Runtime{Name: value.Name}, nil
}

func (s *Service) Status(ctx context.Context) (*contract.SystemStatus, error) {
	heartbeat, err := s.deps.Store.Heartbeat(ctx, "worker")
	if err != nil {
		return nil, err
	}
	if heartbeat != nil {
		local := clock.In(*heartbeat)
		heartbeat = &local
	}
	now := clock.Now()
	healthy := heartbeat != nil && !heartbeat.After(now) && now.Sub(*heartbeat) < 3*s.deps.HeartbeatInterval
	return &contract.SystemStatus{
		Name:            s.deps.Runtime().Name,
		Version:         s.deps.Version,
		Revision:        s.deps.Revision,
		Timezone:        config.Timezone,
		ServerTime:      now,
		WorkerHeartbeat: heartbeat,
		WorkerHealthy:   healthy,
	}, nil
}

func (s *Service) RecordHeartbeat(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return s.deps.Store.TouchHeartbeat(ctx, "worker", clock.Now())
}
