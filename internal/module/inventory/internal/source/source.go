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
	Pages     int
	RequestID string
}
type Cloud interface {
	Fetch(context.Context, aws.Config, Request) (Snapshot, error)
}
