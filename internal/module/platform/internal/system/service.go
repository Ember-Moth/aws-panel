package system

import (
	"context"
	"time"

	"github.com/Ember-Moth/aws-panel/internal/config"
	"github.com/Ember-Moth/aws-panel/internal/infra/clock"
	"github.com/Ember-Moth/aws-panel/internal/module/platform/contract"
	"github.com/Ember-Moth/aws-panel/internal/repository/kernel"
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

func (s *Service) ListAudit(ctx context.Context, before int64) (*contract.AuditList, error) {
	if before < 0 {
		before = 0
	}
	rows, err := s.deps.Store.ListAudit(ctx, before, 21)
	if err != nil {
		return nil, err
	}
	result := &contract.AuditList{Items: make([]contract.AuditEvent, 0, 20)}
	if len(rows) > 20 {
		result.NextBefore = rows[19].ID
		rows = rows[:20]
	}
	for _, row := range rows {
		result.Items = append(result.Items, contract.AuditEvent{ID: row.ID, Actor: row.ActorName, Action: row.Action,
			TargetType: row.TargetType, TargetID: row.TargetID, Outcome: row.Outcome, RequestID: row.RequestID,
			ClientIP: row.ClientIP, Metadata: row.Metadata, OccurredAt: clock.In(row.OccurredAt)})
	}
	return result, nil
}
