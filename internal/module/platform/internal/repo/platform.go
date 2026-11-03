package repo

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Ember-Moth/aws-panel/internal/module/platform/entity"
	"github.com/Ember-Moth/aws-panel/internal/repository/kernel"
	"gorm.io/gorm"
)

type Store struct{ db *gorm.DB }

func New(connection kernel.Connection) *Store { return &Store{db: connection.DB} }

func (s *Store) Runtime(ctx context.Context) (entity.Runtime, error) {
	var raw []byte
	err := s.db.WithContext(ctx).Raw("SELECT value FROM platform_settings WHERE key = ?", "runtime").Row().Scan(&raw)
	if err != nil {
		return entity.Runtime{}, errors.New("cannot load platform settings")
	}
	var settings entity.Runtime
	if err := json.Unmarshal(raw, &settings); err != nil || settings.Name == "" {
		return entity.Runtime{}, errors.New("invalid platform runtime settings")
	}
	return settings, nil
}

func (s *Store) TouchHeartbeat(ctx context.Context, component string, now time.Time) error {
	result := s.db.WithContext(ctx).Exec(
		"INSERT INTO platform_heartbeats (component, observed_at) VALUES (?, ?) ON CONFLICT (component) DO UPDATE SET observed_at = EXCLUDED.observed_at",
		component, now,
	)
	if result.Error != nil {
		return errors.New("cannot record worker heartbeat")
	}
	return nil
}

func (s *Store) Heartbeat(ctx context.Context, component string) (*time.Time, error) {
	var rows []struct{ ObservedAt time.Time }
	if err := s.db.WithContext(ctx).Raw("SELECT observed_at FROM platform_heartbeats WHERE component = ?", component).Scan(&rows).Error; err != nil {
		return nil, errors.New("cannot read worker heartbeat")
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0].ObservedAt, nil
}
