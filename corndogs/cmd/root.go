package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

func NewRootCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "corndogs",
		Short:        "Run and manage the Corndogs service",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
	}
	cmd.AddCommand(newRunCommand(), newSubmitTaskCommand(), newTimeoutCommand())
	return cmd
}

func Execute() {
	if err := NewRootCommand().Execute(); err != nil {
		os.Exit(1)
	}
}
