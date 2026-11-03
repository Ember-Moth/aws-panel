package cmd

import (
	"fmt"

	"github.com/Ember-Moth/aws-panel/internal/app/buildinfo"
	"github.com/spf13/cobra"
)

func versionCommand() *cobra.Command {
	return &cobra.Command{
		Use: "version", Short: "Print build information", Args: cobra.NoArgs,
		Run: func(command *cobra.Command, _ []string) {
			fmt.Fprintf(command.OutOrStdout(), "AWS Panel %s (%s) %s\n", buildinfo.Version, buildinfo.Revision, buildinfo.BuildTime)
		},
	}
}
