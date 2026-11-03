package bootstrap

import (
	"context"

	"github.com/Ember-Moth/aws-panel/internal/app/state"
	"github.com/Ember-Moth/aws-panel/internal/module/platform/contract"
)

type SettingsLoader interface {
	LoadRuntime(context.Context) (contract.Runtime, error)
}

func Load(ctx context.Context, loader SettingsLoader, runtime *state.Runtime) error {
	settings, err := loader.LoadRuntime(ctx)
	if err != nil {
		return err
	}
	runtime.Publish(settings)
	return nil
}
