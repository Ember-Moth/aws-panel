package contract

import "time"

type AuditEvent struct {
	ID         int64     `json:"id"`
	Actor      string    `json:"actor"`
	Action     string    `json:"action"`
	TargetType string    `json:"target_type"`
	TargetID   string    `json:"target_id"`
	Outcome    string    `json:"outcome"`
	RequestID  string    `json:"request_id"`
	ClientIP   string    `json:"client_ip"`
	Metadata   string    `json:"metadata"`
	OccurredAt time.Time `json:"occurred_at"`
}
type AuditList struct {
	Items      []AuditEvent `json:"items"`
	NextBefore int64        `json:"next_before,omitempty"`
}
