package repo

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/herta-network/aws-panel/internal/repository/kernel"
)

func (s *Store) AppendAudit(ctx context.Context, event kernel.AuditEntry) error {
	if event.Metadata == nil {
		event.Metadata = map[string]string{}
	}
	for key := range event.Metadata {
		switch key {
		case "source", "reason", "role", "role_before", "role_after", "active_before", "active_after",
			"version", "result", "stage", "key_id", "count", "account_id", "access", "operation", "targets", "steps", "refresh_connections", "interval_minutes":
		default:
			return errors.New("unsupported audit metadata")
		}
	}
	data, err := json.Marshal(event.Metadata)
	if err != nil {
		return errors.New("invalid audit metadata")
	}
	result := s.db.WithContext(ctx).Exec(
		"INSERT INTO audit_events (actor_id, actor_name, action, target_type, target_id, outcome, request_id, client_ip, metadata, occurred_at) VALUES (?, ?, ?, ?, ?, ?, ?, NULLIF(?, '')::inet, ?::jsonb, ?)",
		event.ActorID, event.ActorName, event.Action, event.TargetType, event.TargetID, event.Outcome, event.RequestID, event.ClientIP, string(data), event.OccurredAt)
	if result.Error != nil {
		return errors.New("cannot append audit event")
	}
	return nil
}

func (s *Store) ListAudit(ctx context.Context, before int64, limit int) ([]kernel.AuditRecord, error) {
	query := s.db.WithContext(ctx).Table("audit_events").
		Select("id, actor_name, action, target_type, target_id, outcome, request_id, COALESCE(host(client_ip), '') AS client_ip, metadata::text AS metadata, occurred_at")
	if before > 0 {
		query = query.Where("id < ?", before)
	}
	var records []kernel.AuditRecord
	if err := query.Order("id DESC").Limit(limit).Scan(&records).Error; err != nil {
		return nil, errors.New("cannot query audit events")
	}
	return records, nil
}
