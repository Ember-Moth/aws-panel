package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/Ember-Moth/aws-panel/internal/app/migration/schema"
	"github.com/Ember-Moth/aws-panel/internal/config"
	"github.com/Ember-Moth/aws-panel/internal/infra/postgres"
	"github.com/spf13/cobra"
)

func migrateCommand(path *string) *cobra.Command {
	command := &cobra.Command{Use: "migrate", Short: "Manage the PostgreSQL schema"}
	command.AddCommand(&cobra.Command{
		Use: "up", Short: "Apply embedded PostgreSQL migrations", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			cfg, err := config.Load(*path)
			if err != nil {
				return err
			}
			if err := schema.Up(cfg.Database); err != nil {
				return err
			}
			fmt.Fprintln(command.OutOrStdout(), "PostgreSQL migrations applied.")
			return nil
		},
	})
	command.AddCommand(&cobra.Command{
		Use: "status", Short: "Verify schema version and session timezone", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			cfg, err := config.Load(*path)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(command.Context(), 10*time.Second)
			defer cancel()
			_, pool, err := postgres.Open(ctx, cfg.Database)
			if err != nil {
				return err
			}
			defer pool.Close()
			if err := schema.Check(ctx, pool); err != nil {
				return err
			}
			fmt.Fprintf(command.OutOrStdout(), "schema=%d timezone=%s\n", schema.Version, config.Timezone)
			return nil
		},
	})
	return command
}
