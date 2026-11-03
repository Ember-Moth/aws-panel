package repository

import (
	"context"
	oc "github.com/herta-network/aws-panel/internal/module/operations/contract"
)

type EffectStore interface {
	Effect(context.Context, string) (*oc.Effect, error)
	PrepareEffect(context.Context, oc.Effect, oc.EffectLease) (oc.Effect, error)
	DispatchEffect(context.Context, string, oc.EffectLease) (oc.Effect, error)
	RecordEffect(context.Context, oc.Effect, oc.EffectLease) error
}
