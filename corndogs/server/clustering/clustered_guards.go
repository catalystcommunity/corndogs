package clustering

import (
	"context"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	"github.com/CatalystCommunity/corndogs/corndogs/server/store"
)

// The resilience operations go through the leader, reads included. A follower
// can lag the leader, so its missing receipt does not prove that a request did
// not run. The leader also waits until the data it reports is committed (see
// Engine.handleProposal).

func (c *ClusteredStore) SubmitKeyedTask(ctx context.Context, req *api.SubmitKeyedTaskRequest, ro store.ReceiptOptions) (*api.SubmitKeyedTaskResponse, error) {
	var out *api.SubmitKeyedTaskResponse
	err := c.propose(func() (e error) { out, e = c.local.SubmitKeyedTask(ctx, req, ro); return })
	return out, err
}

func (c *ClusteredStore) LookupSubmission(ctx context.Context, req *api.LookupSubmissionRequest) (*api.LookupSubmissionResponse, error) {
	var out *api.LookupSubmissionResponse
	err := c.propose(func() (e error) { out, e = c.local.LookupSubmission(ctx, req); return })
	return out, err
}

func (c *ClusteredStore) ClaimGuardedTask(ctx context.Context, req *api.ClaimGuardedTaskGroupRequest, ro store.ReceiptOptions) (*api.ClaimGuardedTaskGroupResponse, error) {
	var out *api.ClaimGuardedTaskGroupResponse
	err := c.propose(func() (e error) { out, e = c.local.ClaimGuardedTask(ctx, req, ro); return })
	return out, err
}

func (c *ClusteredStore) UpdateGuardedTask(ctx context.Context, req *api.UpdateGuardedTaskRequest, ro store.ReceiptOptions) (*api.UpdateGuardedTaskResponse, error) {
	var out *api.UpdateGuardedTaskResponse
	err := c.propose(func() (e error) { out, e = c.local.UpdateGuardedTask(ctx, req, ro); return })
	return out, err
}

func (c *ClusteredStore) FinishGuardedTask(ctx context.Context, req *api.CompleteGuardedTaskRequest, op string, ro store.ReceiptOptions) (*api.CompleteGuardedTaskResponse, error) {
	var out *api.CompleteGuardedTaskResponse
	err := c.propose(func() (e error) { out, e = c.local.FinishGuardedTask(ctx, req, op, ro); return })
	return out, err
}

func (c *ClusteredStore) GetGuardedTask(ctx context.Context, req *api.GetGuardedTaskRequest) (*api.GetGuardedTaskResponse, error) {
	var out *api.GetGuardedTaskResponse
	err := c.propose(func() (e error) { out, e = c.local.GetGuardedTask(ctx, req); return })
	return out, err
}

func (c *ClusteredStore) LookupOperation(ctx context.Context, req *api.LookupOperationRequest) (*api.LookupOperationResponse, error) {
	var out *api.LookupOperationResponse
	err := c.propose(func() (e error) { out, e = c.local.LookupOperation(ctx, req); return })
	return out, err
}

func (c *ClusteredStore) PurgeExpiredReceipts(ctx context.Context, now int64, limit int) (int, error) {
	var n int
	err := c.propose(func() (e error) { n, e = c.local.PurgeExpiredReceipts(ctx, now, limit); return })
	return n, err
}
