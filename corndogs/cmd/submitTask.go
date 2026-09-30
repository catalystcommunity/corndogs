package cmd

import (
	"fmt"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	"github.com/spf13/cobra"
)

func newSubmitTaskCommand() *cobra.Command {
	var conn rpcFlags
	var queue, currentState, autoTargetState, payload, submissionKey string
	var timeout, priority int64
	var guarded bool
	cmd := &cobra.Command{
		Use:   "submit-task",
		Short: "Submit a task",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if submissionKey != "" {
				return submitKeyed(cmd, &conn, api.SubmitKeyedTaskRequest{
					SubmissionKey:   submissionKey,
					Guarded:         guarded,
					Queue:           queue,
					CurrentState:    currentState,
					AutoTargetState: autoTargetState,
					Timeout:         timeout,
					Payload:         []byte(payload),
					Priority:        priority,
				})
			}
			if guarded {
				return fmt.Errorf("--guarded needs --submission-key")
			}
			req := api.SubmitTaskRequest{
				Queue:           queue,
				CurrentState:    currentState,
				AutoTargetState: autoTargetState,
				Timeout:         timeout,
				Payload:         []byte(payload),
				Priority:        priority,
			}
			client, err := conn.client()
			if err != nil {
				return err
			}
			resp, err := client.SubmitTask(cmd.Context(), req)
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
	conn.register(cmd)
	cmd.Flags().StringVarP(&queue, "queue", "q", "", "Queue name (server default if empty)")
	cmd.Flags().StringVarP(&currentState, "current-state", "c", "", "Initial state (server default if empty)")
	cmd.Flags().StringVarP(&autoTargetState, "auto-target-state", "t", "", "State to use when a worker claims the task")
	cmd.Flags().Int64VarP(&timeout, "timeout", "o", 0, "Timeout in seconds (server default if 0)")
	cmd.Flags().StringVarP(&payload, "payload", "l", "", "Task payload as text")
	cmd.Flags().Int64VarP(&priority, "priority", "r", 0, "Priority; higher values are claimed first")
	cmd.Flags().StringVar(&submissionKey, "submission-key", "", "Submit with SubmitKeyedTask: a repeated call with the same key and flags returns the first task (needs --queue)")
	cmd.Flags().BoolVar(&guarded, "guarded", false, "Put the task under task guards (needs --submission-key)")
	return cmd
}

// submitKeyed sends a keyed submission. A retry with the same key is safe.
func submitKeyed(cmd *cobra.Command, conn *rpcFlags, req api.SubmitKeyedTaskRequest) error {
	client, err := conn.client()
	if err != nil {
		return err
	}
	resp, err := client.SubmitKeyedTask(cmd.Context(), req)
	if err != nil {
		return fmt.Errorf("submit keyed task: %w", err)
	}
	verb := "Submitted"
	if resp.Replayed {
		verb = "Already submitted"
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s task %s (key %q)\n", verb, resp.Receipt.TaskUuid, resp.Receipt.SubmissionKey)
	return err
}
