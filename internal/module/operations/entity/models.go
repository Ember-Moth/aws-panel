package entity

import "time"

// JSON columns contain validated, non-secret snapshots only.
type Plan struct {
	ID                            string
	ActorID                       int64
	Operation                     string
	ExecutorVersion, InputVersion int
	Input, Items                  string
	InputHash                     string
	CreatedAt, ExpiresAt          time.Time
}
type Job struct {
	ID, PlanID                       string
	ActorID                          int64
	ActorName, Operation, Title      string
	ExecutorVersion, InputVersion    int
	Input, InputHash, IdempotencyKey string
	WritesCloud, Diagnostic          bool
	Status                           string
	CancelRequested                  bool
	EventSeq, Version                int64
	CreatedAt, UpdatedAt             time.Time
	CompletedAt                      *time.Time
}
type Item struct {
	ID, JobID                             string
	Ordinal                               int
	TargetKey, Label, LockKey             string
	LockKeys                              string
	AccountID                             *string
	ConnectionID, Region, Binding, Status string
}
type Step struct {
	ID, JobID, ItemID              string
	Ordinal                        int
	Kind, Label, Input             string
	WritesCloud                    bool
	Status, Mode, IdempotencyToken string
	Uncertain                      bool
	Generation                     int64
	AttemptCount, AttemptLimit     int
	DeadlineAt                     time.Time
	NextRunAt, ScheduledAt         *time.Time
	LeaseOwner                     string
	LeaseEpoch                     int64
	LeaseExpiresAt                 *time.Time
	Code, Result                   string
	UpdatedAt                      time.Time
}
type Attempt struct {
	ID                    int64
	JobID, StepID         string
	Number                int
	LeaseEpoch            int64
	Mode, Binding         string
	StartedAt             time.Time
	FinishedAt            *time.Time
	Outcome, Code, Result string
}
type Event struct {
	JobID          string
	Seq            int64
	Type           string
	StepID, ItemID *string
	Code, Message  string
	OccurredAt     time.Time
}
type Outbox struct {
	ID, JobID, StepID      string
	Generation             int64
	AvailableAt, CreatedAt time.Time
	PublishedAt            *time.Time
	PublishOwner           string
	PublishUntil           *time.Time
	Attempts               int
	LastError              string
}
type Scope struct {
	Admin      bool
	AccountIDs []string
}
type TargetState struct {
	AccountID, TargetKey, JobID, StepID, Status, Code string
	CreatedAt, UpdatedAt                              time.Time
	NextRunAt                                         *time.Time
}
