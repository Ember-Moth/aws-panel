package source

import (
	"context"
	"github.com/aws/aws-sdk-go-v2/aws"
)

type State struct{ Region, Status, RequestID string }
type Cloud interface {
	List(context.Context, aws.Config) ([]State, error)
	Status(context.Context, aws.Config, string) (State, error)
	Enable(context.Context, aws.Config, string) (string, error)
}
