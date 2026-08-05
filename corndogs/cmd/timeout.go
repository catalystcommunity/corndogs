package cmd

import (
	"fmt"
	"net"
	"time"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	"github.com/spf13/cobra"
)

func newTimeoutCommand() *cobra.Command {
	var address string
	var port string
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
			resp, err := api.New(net.JoinHostPort(address, port)).CleanUpTimedOut(cmd.Context(), req)
			if err != nil {
				return fmt.Errorf("process timed-out tasks: %w", err)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Timed out %d tasks\n", resp.TimedOut)
			return err
		},
	}

	cmd.Flags().StringVarP(&address, "address", "a", "127.0.0.1", "RPC host name or IP address")
	cmd.Flags().StringVarP(&port, "port", "p", "5080", "RPC port")
	cmd.Flags().StringVarP(&queue, "queue", "q", "", "Process only this queue (all queues if empty)")
	return cmd
}
