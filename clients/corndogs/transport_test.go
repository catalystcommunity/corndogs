package corndogs

import (
	"context"
	"os"
	"testing"
)

// TestE2ECSILRPC drives the generated client + CSIL-RPC carrier against a live
// corndogs server. Gated on CORNDOGS_E2E (the base URL, e.g.
// http://127.0.0.1:5080) so it only runs when a server is up. This proves the
// generated client connects out of the box and can submit + request tasks.
func TestE2ECSILRPC(t *testing.T) {
	base := os.Getenv("CORNDOGS_E2E")
	if base == "" {
		t.Skip("set CORNDOGS_E2E=<base url> with a running corndogs server to run")
	}
	c := New(base)
	ctx := context.Background()
	const queue = "e2e-csil-rpc"

	sub, err := c.SubmitTask(ctx, SubmitTaskRequest{
		Queue:           queue,
		CurrentState:    "submitted",
		AutoTargetState: "wip",
		Timeout:         -1,
		Payload:         []byte("hello-csil-rpc"),
	})
	if err != nil {
		t.Fatalf("SubmitTask: %v", err)
	}
	if sub.Task == nil || sub.Task.Uuid == "" {
		t.Fatalf("SubmitTask returned no task")
	}
	t.Logf("submitted %s", sub.Task.Uuid)

	got, err := c.GetNextTask(ctx, GetNextTaskRequest{Queue: queue, CurrentState: "submitted"})
	if err != nil {
		t.Fatalf("GetNextTask: %v", err)
	}
	if got.Task == nil {
		t.Fatalf("GetNextTask returned nil task")
	}
	if got.Task.Uuid != sub.Task.Uuid {
		t.Fatalf("claimed uuid %q != submitted %q", got.Task.Uuid, sub.Task.Uuid)
	}
	if string(got.Task.Payload) != "hello-csil-rpc" {
		t.Fatalf("payload round-trip failed: %q", got.Task.Payload)
	}
	t.Logf("E2E OK: submitted + claimed %s over CSIL-RPC", got.Task.Uuid)
}
