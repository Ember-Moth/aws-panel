package source

import (
	"context"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/herta-network/aws-panel/internal/module/compute/contract"
)

type Accepted struct{ ResourceID, RequestID string }
type Cloud interface {
	Network(context.Context, aws.Config, string, contract.Network, map[string]string) (contract.Network, bool, error)
	Provision(context.Context, aws.Config, string, contract.Network, map[string]string) (Accepted, error)
	NetworkQuotas(context.Context, aws.Config, string) ([]contract.Quota, error)

	Catalogue(context.Context, aws.Config) (contract.Catalogue, error)
	Instances(context.Context, aws.Config, string) ([]contract.Instance, error)
	Instance(context.Context, aws.Config, string, string) (*contract.Instance, error)
	FindCreated(context.Context, aws.Config, string, string) ([]contract.Instance, error)
	Launch(context.Context, aws.Config, contract.Spec, string, string, map[string]string) (Accepted, error)
	Manage(context.Context, aws.Config, string, contract.Instance) (Accepted, error)
	Quota(context.Context, aws.Config, string, string) (contract.Quota, error)
}
