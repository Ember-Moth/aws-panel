package source

import (
	"context"
	"github.com/Ember-Moth/aws-panel/internal/module/addresses/contract"
	"github.com/aws/aws-sdk-go-v2/aws"
	"time"
)

type Operation struct {
	ID, Name, Type, State, Region string
	CreatedAt                     time.Time
	Terminal                      bool
}
type Accepted struct {
	ResourceID, RequestID string
	Operations            []Operation
}
type Cloud interface {
	Catalogue(context.Context, aws.Config, string, string) (contract.Catalogue, error)
	Address(context.Context, aws.Config, string, string, string) (*contract.Address, error)
	Instance(context.Context, aws.Config, string, string, string) (*contract.Instance, error)
	FindAllocated(context.Context, aws.Config, string, string, string, map[string]string) ([]contract.Address, error)
	History(context.Context, aws.Config, string, string) ([]Operation, error)
	Operation(context.Context, aws.Config, string, string) (*Operation, error)
	Allocate(context.Context, aws.Config, string, string, string, map[string]string) (Accepted, error)
	Attach(context.Context, aws.Config, string, contract.Address, contract.Instance) (Accepted, error)
	Detach(context.Context, aws.Config, string, contract.Address) (Accepted, error)
	Release(context.Context, aws.Config, string, contract.Address) (Accepted, error)
	Quota(context.Context, aws.Config, string, string) (contract.Quota, error)
}
