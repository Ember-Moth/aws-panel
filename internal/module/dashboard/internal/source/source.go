package source

import (
	"context"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/herta-network/aws-panel/internal/module/dashboard/contract"
)

type Catalogue struct {
	Regions          []string `json:"regions"`
	LightsailRegions []string `json:"lightsail_regions"`
	LightsailKnown   bool     `json:"lightsail_known"`
}
type Observation struct {
	Kind string
	Data contract.Data
	Code string
	OK   bool
}
type Cloud interface {
	Catalogue(context.Context, aws.Config) (Catalogue, error)
	ReadRegion(context.Context, aws.Config, string, bool) ([]Observation, error)
}
