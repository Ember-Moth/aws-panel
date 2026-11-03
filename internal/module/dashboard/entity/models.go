package entity

import "time"

type Snapshot struct {
	AccountID, Region, Kind, ConnectionID, Fingerprint, Status, Code, Data string
	LastAttemptAt                                                          time.Time
	LastSuccessAt                                                          *time.Time
}
type Run struct {
	StepID, JobID, AccountID, ConnectionID, Fingerprint, SpecHash, Catalog string
	NextRegion, Failures                                                   int
	StartedAt, UpdatedAt                                                   time.Time
	FinishedAt                                                             *time.Time
}
type Lease struct {
	StepID, Owner string
	Epoch         int64
}
