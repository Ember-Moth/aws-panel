package entity

import "time"

type Scope struct {
	ID, AccountID, Kind, Region     string
	Generation                      int64
	ActiveRunID                     *string
	Status                          string
	LastAttemptAt, LastSuccessAt    *time.Time
	LastErrorCode, LastConnectionID string
	ResourceCount                   int
	CreatedAt                       time.Time
}
type Run struct {
	ID, ScopeID, JobID, StepID              string
	LeaseEpoch, Generation                  int64
	ConnectionID, Fingerprint, Status, Code string
	PageCount, ResourceCount                int
	AWSRequestID                            string
	StartedAt                               time.Time
	FinishedAt                              *time.Time
}
type Snapshot struct {
	ID, ScopeID, ResourceID, Name, Region, State, Details string
	ObservedAt                                            time.Time
	Present                                               bool
	MissingSince                                          *time.Time
	Generation                                            int64
}
type Resource struct {
	Snapshot
	AccountID, Kind, ScopeRegion string
}
type Query struct {
	AccountIDs                []string
	Kind, Region, State, Text string
	IncludeMissing            bool
	Page, Limit               int
}
type Lease struct {
	JobID, StepID, Owner string
	Epoch                int64
}
type Ticket struct {
	Run   Run
	Scope Scope
	Lease Lease
}
type Schedule struct {
	ID                   string
	ActorID              int64
	Name, Request        string
	IntervalSeconds      int
	Active               bool
	Version              int64
	NextRunAt            time.Time
	LeaseOwner           string
	LeaseUntil           *time.Time
	LastJobID            *string
	LastErrorCode        string
	LastRunAt            *time.Time
	CreatedAt, UpdatedAt time.Time
}

type Overview struct {
	Scope
	ObservedCount, RunningCount int64
	CarrierCount                int64
	CarrierKnown                bool
	IPJSON, RegionJSON          string
}
