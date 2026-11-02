package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/herta-network/aws-panel/internal/infra/clock"
	"github.com/spf13/cobra"
)

func Execute() int {
	clock.Configure()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	root := NewRoot()
	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "aws-panel:", err)
		return 1
	}
	return 0
}

func NewRoot() *cobra.Command {
	var path string
	root := &cobra.Command{
		Use: "aws-panel", Short: "AWS team management panel",
		SilenceUsage: true, SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&path, "config", "", "YAML configuration path (otherwise use defaults and environment)")
	root.AddCommand(runCommand(&path), migrateCommand(&path), versionCommand(), healthcheckCommand())
	return root
}
