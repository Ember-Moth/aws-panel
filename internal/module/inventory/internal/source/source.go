package source

import (
	"context"
	"time"

	"github.com/Ember-Moth/aws-panel/internal/module/inventory/contract"
	"github.com/aws/aws-sdk-go-v2/aws"
)

type Request struct{ Kind, Region, AWSAccountID, Partition string }
type Resource struct {
	ID, Name, Region, State string
	Details                 contract.Details
	ObservedAt              time.Time
}
type Snapshot struct {
	Resources []Resource
	Traffic   []TrafficRow
	Pages     int
	RequestID string
}

// TrafficRow is one instance's network bytes for one UTC day.
type TrafficRow struct {
	ResourceID, Name, BundleID string
	AllowanceBytes             int64
	Day                        time.Time
	BytesIn, BytesOut          int64
	ObservedAt                 time.Time
}
type Cloud interface {
	Fetch(context.Context, aws.Config, Request) (Snapshot, error)
}
