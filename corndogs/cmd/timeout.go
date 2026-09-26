package cmd

import (
	"fmt"
	"time"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	"github.com/spf13/cobra"
)

func newTimeoutCommand() *cobra.Command {
	var conn rpcFlags
	var queue string
	cmd := &cobra.Command{
		Use:   "timeout",
		Short: "Process tasks that are timed out",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			req := api.CleanUpTimedOutRequest{
				AtTime: time.Now().UTC().UnixNano(),
				Queue:  queue,
			}
			client, err := conn.client()
			if err != nil {
				return err
			}
			resp, err := client.CleanUpTimedOut(cmd.Context(), req)
			if err != nil {
				return fmt.Errorf("process timed-out tasks: %w", err)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Timed out %d tasks\n", resp.TimedOut)
			return err
		},
	}

	conn.register(cmd)
	cmd.Flags().StringVarP(&queue, "queue", "q", "", "Process only this queue (all queues if empty)")
	return cmd
}
