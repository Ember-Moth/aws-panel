package kernel

import (
	"context"
	"time"
)

type AuditEntry struct {
	ActorID    *int64
	ActorName  string
	Action     string
	TargetType string
	TargetID   string
	Outcome    string
	RequestID  string
	ClientIP   string
	Metadata   map[string]string
	OccurredAt time.Time
}

type AuditRecord struct {
	ID         int64
	ActorName  string
	Action     string
	TargetType string
	TargetID   string
	Outcome    string
	RequestID  string
	ClientIP   string
	Metadata   string
	OccurredAt time.Time
}

type AuditAppender interface {
	AppendAudit(context.Context, AuditEntry) error
}
type AuditFactory func(Connection) AuditAppender
