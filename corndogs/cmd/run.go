package cmd

import (
	"github.com/CatalystCommunity/corndogs/corndogs/server"
	"github.com/spf13/cobra"
)

func newRunCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Run the Corndogs service",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			server.SetupAndRun()
		},
	}
}
