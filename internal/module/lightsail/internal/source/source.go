package source

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/herta-network/aws-panel/internal/module/lightsail/contract"
)

type Instance struct {
	ARN, Name, Region, Zone, BlueprintID, BundleID, IPType, KeyPairName, State, RequestID string
	CreatedAt                                                                             time.Time
	Tags                                                                                  map[string]string
}
type Operation struct {
	ID, Name, Region, Type, State, Code string
	Terminal                            bool
	CreatedAt                           time.Time
}
type Created struct {
	Operations []Operation
	RequestID  string
}
type Cloud interface {
	Regions(context.Context, aws.Config) ([]contract.Region, error)
	Catalogue(context.Context, aws.Config) (contract.Catalogue, error)
	Instance(context.Context, aws.Config, string) (*Instance, error)
	Create(context.Context, aws.Config, contract.Target, map[string]string, string) (Created, error)
	Operations(context.Context, aws.Config, string) ([]Operation, error)
}

// FirewallCloud changes the public ingress rules of the caller-owned instance.
type FirewallCloud interface {
	FirewallOpen(context.Context, aws.Config, string, string) (bool, error)
	OpenFirewall(context.Context, aws.Config, string, string) (Created, error)
}

// CatalogueParts separates slow-changing products from account state for display.
type CatalogueParts interface {
	Products(context.Context, aws.Config) (contract.Catalogue, error)
	Environment(context.Context, aws.Config) (contract.Catalogue, error)
}
