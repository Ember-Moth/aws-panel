package cmd

import (
	"os"

	"github.com/Ember-Moth/aws-panel/internal/app"
	"github.com/Ember-Moth/aws-panel/internal/config"
	"github.com/Ember-Moth/aws-panel/pkg/logger"
	"github.com/spf13/cobra"
)

func runCommand(path *string) *cobra.Command {
	return &cobra.Command{
		Use: "run", Short: "Run HTTP, task worker and scheduler in one process",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			cfg, err := config.Load(*path)
			if err != nil {
				return err
			}
			return app.Run(command.Context(), cfg, logger.New(os.Stdout, cfg.Log.Level))
		},
	}
}
