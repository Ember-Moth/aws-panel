package contract

import "time"

// Effect stores a non-secret external-write receipt. A dispatched receipt is
// reconciled before another send; a confirmed receipt cannot dispatch again.
type Effect struct {
	StepID, JobID, AccountID, Region, Purpose, SpecHash, Token, State string
	SendCount                                                         int
	FirstSentAt, LastSentAt                                           *time.Time
	ResourceID, RequestID, Code, Proof                                string
	CreatedAt, UpdatedAt                                              time.Time
	// Additional raw resource keys claimed atomically after AWS reveals IDs.
	ResourceLocks []string `json:"-" gorm:"-"`
}
type EffectLease struct {
	StepID, Owner string
	Epoch         int64
}
