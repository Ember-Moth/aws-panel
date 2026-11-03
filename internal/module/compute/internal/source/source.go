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

// WavelengthCloud is optional so existing ordinary EC2 adapters remain compatible.
type WavelengthCloud interface {
	WavelengthZones(context.Context, aws.Config) ([]contract.WavelengthZone, error)
	WavelengthCatalogue(context.Context, aws.Config, string) (contract.Catalogue, error)
	EnableWavelength(context.Context, aws.Config, string) (Accepted, error)
}

// IngressCloud configures the regional forwarding companion and its private trust boundary.
type IngressCloud interface {
	ObserveIngress(context.Context, aws.Config, string, contract.IngressState, map[string]string) (contract.IngressState, bool, error)
	ConfigureIngress(context.Context, aws.Config, string, contract.IngressState, map[string]string) (Accepted, error)
}

// FirewallCloud manages a dedicated group owned by a newly created instance.
type FirewallCloud interface {
	ObserveFirewall(context.Context, aws.Config, string, contract.FirewallState, map[string]string) (contract.FirewallState, bool, error)
	ConfigureFirewall(context.Context, aws.Config, string, contract.FirewallState, map[string]string) (Accepted, error)
}

// CatalogueParts separates display directories; Catalogue remains an uncached read.
type CatalogueParts interface {
	Images(context.Context, aws.Config) (contract.Catalogue, error)
	InstanceTypes(context.Context, aws.Config, string) (contract.Catalogue, error)
	Environment(context.Context, aws.Config, string) (contract.Catalogue, error)
}
