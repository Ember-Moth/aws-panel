package contract

import (
	"context"
	"encoding/json"
	"time"
)

// Executors are registered by the composition root and pinned by exact version.
// Input and results must be typed, non-secret data; never raw SDK errors or responses.
type Definition struct {
	ID                      string
	Version, InputVersion   int
	Title                   string
	WritesCloud, Diagnostic bool
	MaxAttempts             int
	Deadline, CallTimeout   time.Duration
}
type ConnectionVersion struct {
	ID           string `json:"id"`
	Version      int64  `json:"version"`
	ProxyID      string `json:"proxy_id,omitempty"`
	ProxyVersion int64  `json:"proxy_version,omitempty"`
}
type Binding struct {
	AccountID    string              `json:"account_id,omitempty"`
	ConnectionID string              `json:"connection_id,omitempty"`
	AWSAccountID string              `json:"aws_account_id,omitempty"`
	Partition    string              `json:"partition,omitempty"`
	Region       string              `json:"region,omitempty"`
	Fingerprint  string              `json:"fingerprint,omitempty"`
	Chain        []ConnectionVersion `json:"chain,omitempty"`
}
type Target struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	// Write plans provide a stable cloud-target key shared across operations.
	// The engine adds account and region before acquiring the target lock.
	LockKey string  `json:"lock_key,omitempty"`
	Binding Binding `json:"binding"`
}
type StepPlan struct {
	Kind        string          `json:"kind"`
	Label       string          `json:"label"`
	Input       json.RawMessage `json:"input" swaggertype:"object"`
	WritesCloud bool            `json:"writes_cloud"`
}
type ItemPlan struct {
	Target Target     `json:"target"`
	Steps  []StepPlan `json:"steps"`
}
type Call struct {
	JobID, ItemID, StepID string
	ActorID               int64
	Target                Target
	Kind                  string
	Input                 json.RawMessage
	Token                 string
	Attempt               int
	LeaseEpoch            int64
	LeaseOwner            string
	Uncertain             bool
	Previous              Output
	Completed             []StepOutput
	// Check must run before every external request, including retries. AWS
	// adapters attach this guard to their connection-scoped HTTP client.
	Check func(context.Context) error
}
type StepOutput struct {
	ID      string
	Kind    string
	Ordinal int
	Output  Output
}

const (
	Succeeded      = "succeeded"
	Failed         = "failed"
	Waiting        = "waiting"
	Attention      = "needs_attention"
	Cancelled      = "cancelled"
	Queued         = "queued"
	Running        = "running"
	PartialSuccess = "partial_success"
	// Retry is only valid when the executor has established that repeating
	// Execute with the same token is safe. Ambiguous calls first use Waiting;
	// uncertainty survives a safe retry until success or confirmed absence.
	Retry = "retry"
)

type Output struct {
	ResourceID string `json:"resource_id,omitempty"`
	RequestID  string `json:"aws_request_id,omitempty"`
	Summary    string `json:"summary,omitempty"`
}
type Result struct {
	State  string
	Code   string
	Output Output
	After  time.Duration
	// ConfirmedAbsent may clear uncertainty only when reconciliation has
	// established that there is no outstanding accepted effect.
	ConfirmedAbsent bool
}
type Executor interface {
	Definition() Definition
	Validate(json.RawMessage) (json.RawMessage, error)
	Plan(context.Context, json.RawMessage, []Target) ([]ItemPlan, error)
	Execute(context.Context, Call) Result
	Reconcile(context.Context, Call) Result
}

type Message struct {
	JobID      string `json:"job_id"`
	StepID     string `json:"step_id"`
	Generation int64  `json:"generation"`
}
