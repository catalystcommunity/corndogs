package cmd

import (
	"os"

	api "github.com/CatalystCommunity/corndogs/corndogs/server/csilapi"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var submitTaskCmd = NewSubmitTaskCmd()

func NewSubmitTaskCmd() *cobra.Command {
	var address, port string
	var queue, currentState, autoTargetState, payload string
	var timeout, priority int64
	cmd := &cobra.Command{
		Use:   "submit-task",
		Short: "creates a corndogs task",
		Run: func(cmd *cobra.Command, args []string) {
			req := api.SubmitTaskRequest{
				Queue:           queue,
				CurrentState:    currentState,
				AutoTargetState: autoTargetState,
				Timeout:         timeout,
				Payload:         []byte(payload),
				Priority:        priority,
			}
			var resp api.SubmitTaskResponse
			if err := cborCall(baseURL(address, port), "SubmitTask", &req, &resp); err != nil {
				log.Err(err).Msg("failed to submit task")
				os.Exit(1)
			}
			log.Info().Msgf("response: %+v", resp)
		},
	}
	cmd.Flags().StringVarP(&address, "address", "a", "127.0.0.1", "The address to connect to the corndogs service")
	cmd.Flags().StringVarP(&port, "port", "p", "5080", "The port to connect to the corndogs service")
	cmd.Flags().StringVarP(&queue, "queue", "q", "", "The queue to submit the task to")
	cmd.Flags().StringVarP(&currentState, "current-state", "c", "", "The current state of the task")
	cmd.Flags().StringVarP(&autoTargetState, "auto-target-state", "t", "", "The target state of the task")
	cmd.Flags().Int64VarP(&timeout, "timeout", "o", 0, "The timeout of the task")
	cmd.Flags().StringVarP(&payload, "payload", "l", "", "The payload of the task")
	cmd.Flags().Int64VarP(&priority, "priority", "r", 0, "The priority of the task")
	rootCmd.AddCommand(cmd)
	return cmd
}
