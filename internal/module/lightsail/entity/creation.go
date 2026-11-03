package entity

import "time"

type Creation struct {
	StepID, JobID, AccountID, Region, Name, Token, SpecHash, Spec, State string
	SendCount                                                            int
	FirstSentAt, LastSentAt                                              *time.Time
	ResourceARN, OperationIDs, AWSRequestID, Code                        string
	CreatedAt, UpdatedAt                                                 time.Time
}
type Lease struct {
	StepID, Owner string
	Epoch         int64
}
