package cmd

import (
	"fmt"
	"net"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	"github.com/spf13/cobra"
)

func newSubmitTaskCommand() *cobra.Command {
	var address, port string
	var queue, currentState, autoTargetState, payload string
	var timeout, priority int64
	cmd := &cobra.Command{
		Use:   "submit-task",
		Short: "Submit a task",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			req := api.SubmitTaskRequest{
				Queue:           queue,
				CurrentState:    currentState,
				AutoTargetState: autoTargetState,
				Timeout:         timeout,
				Payload:         []byte(payload),
				Priority:        priority,
			}
			resp, err := api.New(net.JoinHostPort(address, port)).SubmitTask(cmd.Context(), req)
			if err != nil {
				return fmt.Errorf("submit task: %w", err)
			}
			if resp.Task == nil {
				return fmt.Errorf("submit task: server response did not contain a task")
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Submitted task %s\n", resp.Task.Uuid)
			return err
		},
	}
	cmd.Flags().StringVarP(&address, "address", "a", "127.0.0.1", "RPC host name or IP address")
	cmd.Flags().StringVarP(&port, "port", "p", "5080", "RPC port")
	cmd.Flags().StringVarP(&queue, "queue", "q", "", "Queue name (server default if empty)")
	cmd.Flags().StringVarP(&currentState, "current-state", "c", "", "Initial state (server default if empty)")
	cmd.Flags().StringVarP(&autoTargetState, "auto-target-state", "t", "", "State to use when a worker claims the task")
	cmd.Flags().Int64VarP(&timeout, "timeout", "o", 0, "Timeout in seconds (server default if 0)")
	cmd.Flags().StringVarP(&payload, "payload", "l", "", "Task payload as text")
	cmd.Flags().Int64VarP(&priority, "priority", "r", 0, "Priority; higher values are claimed first")
	return cmd
}
