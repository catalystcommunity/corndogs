package clustering

import (
	"context"
	"errors"
	"fmt"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	"github.com/CatalystCommunity/corndogs/corndogs/server/store"
)

// ClusteredStore adapts a local BoltStore + Engine to the store.Store interface so
// the existing RPC server uses clustering transparently. Writes (which mutate —
// submit, claim, group-claim, update, complete, cancel, cleanup) are proposed to
// the engine: they run on the leader and block until the semi-sync quorum commits,
// or return ErrNotLeader on a follower (the RPC layer redirects). Reads pass
// straight through to the local replica — followers serve reads (eventually
// consistent).
type ClusteredStore struct {
	local        store.Store
	engine       *Engine
	transport    *TCPTransport
	localCleanup func()
}

// NewClusteredStore wraps an already-opened local store and a built (not yet
// running) engine + transport. Initialize starts them.
func NewClusteredStore(local store.Store, engine *Engine, transport *TCPTransport, localCleanup func()) *ClusteredStore {
	return &ClusteredStore{local: local, engine: engine, transport: transport, localCleanup: localCleanup}
}

// Engine exposes the engine (for metrics polling).
func (c *ClusteredStore) Engine() *Engine { return c.engine }

// Initialize starts the engine loop and the TCP transport, returning the teardown
// (stop engine, close transport, close the local store).
func (c *ClusteredStore) Initialize() (func(), error) {
	go c.engine.Run()
	if c.transport != nil {
		if err := c.transport.Start(); err != nil {
			c.engine.Stop()
			if c.localCleanup != nil {
				c.localCleanup()
			}
			return nil, err
		}
	}
	return func() {
		c.engine.Stop()
		if c.transport != nil {
			c.transport.Close()
		}
		if c.localCleanup != nil {
			c.localCleanup()
		}
	}, nil
}

// propose runs a store write on the engine goroutine and waits for commit. On a
// follower it returns a parseable redirect ("not-leader leader=<addr>") so the
// client can retry against the leader — safe because a not-leader write is
// rejected before it executes (no partial effect).
func (c *ClusteredStore) propose(fn func() error) error {
	err := c.engine.Propose(fn)
	if errors.Is(err, ErrNotLeader) {
		addr := ""
		if c.transport != nil {
			addr = c.transport.CurrentLeaderRPCAddr()
		}
		return fmt.Errorf("not-leader leader=%s", addr)
	}
	return err
}

// --- writes (leader-only, semi-sync) ---------------------------------------

func (c *ClusteredStore) SubmitTask(ctx context.Context, req *api.SubmitTaskRequest) (*api.SubmitTaskResponse, error) {
	var out *api.SubmitTaskResponse
	err := c.propose(func() (e error) { out, e = c.local.SubmitTask(ctx, req); return })
	return out, err
}

func (c *ClusteredStore) GetNextTask(ctx context.Context, req *api.GetNextTaskRequest) (*api.GetNextTaskResponse, error) {
	var out *api.GetNextTaskResponse
	err := c.propose(func() (e error) { out, e = c.local.GetNextTask(ctx, req); return })
	return out, err
}

func (c *ClusteredStore) GetNextTaskGroup(ctx context.Context, req *api.GetNextTaskGroupRequest) (*api.GetNextTaskGroupResponse, error) {
	var out *api.GetNextTaskGroupResponse
	err := c.propose(func() (e error) { out, e = c.local.GetNextTaskGroup(ctx, req); return })
	return out, err
}

func (c *ClusteredStore) UpdateTask(ctx context.Context, req *api.UpdateTaskRequest) (*api.UpdateTaskResponse, error) {
	var out *api.UpdateTaskResponse
	err := c.propose(func() (e error) { out, e = c.local.UpdateTask(ctx, req); return })
	return out, err
}

func (c *ClusteredStore) CompleteTask(ctx context.Context, req *api.CompleteTaskRequest) (*api.CompleteTaskResponse, error) {
	var out *api.CompleteTaskResponse
	err := c.propose(func() (e error) { out, e = c.local.CompleteTask(ctx, req); return })
	return out, err
}

func (c *ClusteredStore) CancelTask(ctx context.Context, req *api.CancelTaskRequest) (*api.CancelTaskResponse, error) {
	var out *api.CancelTaskResponse
	err := c.propose(func() (e error) { out, e = c.local.CancelTask(ctx, req); return })
	return out, err
}

func (c *ClusteredStore) CleanUpTimedOut(ctx context.Context, req *api.CleanUpTimedOutRequest) (*api.CleanUpTimedOutResponse, error) {
	var out *api.CleanUpTimedOutResponse
	err := c.propose(func() (e error) { out, e = c.local.CleanUpTimedOut(ctx, req); return })
	return out, err
}

// --- reads (served locally by any replica) ---------------------------------

func (c *ClusteredStore) MustGetTaskStateByID(ctx context.Context, req *api.GetTaskStateByIDRequest) (*api.GetTaskStateByIDResponse, error) {
	return c.local.MustGetTaskStateByID(ctx, req)
}
func (c *ClusteredStore) GetQueues(ctx context.Context, req *api.GetQueuesRequest) (*api.GetQueuesResponse, error) {
	return c.local.GetQueues(ctx, req)
}
func (c *ClusteredStore) GetQueueTaskCounts(ctx context.Context, req *api.GetQueueTaskCountsRequest) (*api.GetQueueTaskCountsResponse, error) {
	return c.local.GetQueueTaskCounts(ctx, req)
}
func (c *ClusteredStore) GetTaskStateCounts(ctx context.Context, req *api.GetTaskStateCountsRequest) (*api.GetTaskStateCountsResponse, error) {
	return c.local.GetTaskStateCounts(ctx, req)
}
func (c *ClusteredStore) GetQueueAndStateCounts(ctx context.Context, req *api.GetQueueAndStateCountsRequest) (*api.GetQueueAndStateCountsResponse, error) {
	return c.local.GetQueueAndStateCounts(ctx, req)
}

var _ store.Store = (*ClusteredStore)(nil)
