package cmd

import (
	"fmt"
	"time"

	api "github.com/CatalystCommunity/corndogs/corndogs/server/csilapi"
	"github.com/spf13/cobra"
)

var timeoutCommand = NewTimeoutCommand()

func NewTimeoutCommand() *cobra.Command {
	var address string
	var port string
	var queue string
	timeoutCommand := &cobra.Command{
		Use:   "timeout",
		Short: "Send a CleanUpTimedOut request at the current time to a corndogs service",
		Long:  "Send a CleanUpTimedOut request at the current time to a corndogs service",
		Run: func(cmd *cobra.Command, args []string) {
			SendCleanUpTimedOut(address, port, queue)
		},
	}

	timeoutCommand.Flags().StringVarP(&address, "address", "a", "127.0.0.1", "The address to connect to the corndogs service")
	timeoutCommand.Flags().StringVarP(&port, "port", "p", "5080", "The port to connect to the corndogs service")
	timeoutCommand.Flags().StringVarP(&queue, "queue", "q", "", "The queue to limit the timeout to. If left blank the timeout will affect all tasks.")
	rootCmd.AddCommand(timeoutCommand)
	return timeoutCommand
}

func SendCleanUpTimedOut(address, port, queue string) {
	base := baseURL(address, port)
	fmt.Println("Connecting to:", base)

	nowUTC := time.Now().Add(time.Duration(7) * time.Second).UTC()
	if queue != "" {
		fmt.Printf("Sending for queue '%s' at time: %s\n", queue, nowUTC)
	} else {
		fmt.Println("Sending at time:", nowUTC)
	}

	req := api.CleanUpTimedOutRequest{
		AtTime: nowUTC.UnixNano(),
		Queue:  queue,
	}
	var resp api.CleanUpTimedOutResponse
	if err := cborCall(base, "CleanUpTimedOut", &req, &resp); err != nil {
		panic(err)
	}
	fmt.Printf("Timed out: %d\n", resp.TimedOut)
}
