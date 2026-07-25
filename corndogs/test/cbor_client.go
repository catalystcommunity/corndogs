package test

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	"github.com/CatalystCommunity/corndogs/corndogs/server/csilrpc"
)

// CorndogsClient is a CSIL-RPC test client over a persistent TCP StreamCarrier
// connection, matching the call shape the integration tests use:
// Method(ctx, *Request) (*Response, error).
type CorndogsClient struct {
	addr string
	mu   sync.Mutex // serializes calls on the one-in-flight connection + guards rpc
	rpc  *csilrpc.RpcClient
	conn net.Conn
}

// GetCorndogsClient returns a client pointed at the local server's TCP CSIL-RPC
// port (:5080).
func GetCorndogsClient() *CorndogsClient {
	return &CorndogsClient{addr: "127.0.0.1:5080"}
}

// rpcClient returns the connected RpcClient, dialing on first use. Caller holds mu.
func (c *CorndogsClient) rpcClient() (*csilrpc.RpcClient, error) {
	if c.rpc != nil {
		return c.rpc, nil
	}
	conn, err := net.DialTimeout("tcp", c.addr, 5*time.Second)
	if err != nil {
		return nil, err
	}
	c.conn = conn
	c.rpc = csilrpc.NewRpcClient(csilrpc.NewStreamCarrier(conn), true)
	return c.rpc, nil
}

func (c *CorndogsClient) reset() {
	if c.conn != nil {
		_ = c.conn.Close()
	}
	c.conn, c.rpc = nil, nil
}

// cborDo performs one CSIL-RPC call over the persistent TCP connection. Calls are
// serialized (the connection is one-in-flight); a transport failure resets the
// connection so the next call re-dials. A context deadline is honored by bounding
// the socket with SetDeadline, so a call against a hung server fails fast instead of
// blocking forever.
func cborDo[Req any, Resp any](
	ctx context.Context, c *CorndogsClient, op string, req *Req,
	encode func(Req) []byte, decode func([]byte) (Resp, error),
) (*Resp, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	rpc, err := c.rpcClient()
	if err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = c.conn.SetDeadline(dl)
		defer c.conn.SetDeadline(time.Time{})
	}
	rr, err := rpc.Call("CorndogsService", op, encode(*req), nil)
	if err != nil {
		c.reset()
		return nil, err
	}
	if rr.Variant != nil && *rr.Variant == "ServiceError" {
		if serr, derr := api.DecodeServiceError(rr.Payload); derr == nil {
			return nil, fmt.Errorf("service error %d: %s", serr.Code, serr.Message)
		}
	}
	resp, err := decode(rr.Payload)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *CorndogsClient) SubmitTask(ctx context.Context, req *api.SubmitTaskRequest) (*api.SubmitTaskResponse, error) {
	return cborDo(ctx, c, "SubmitTask", req, api.EncodeSubmitTaskRequest, api.DecodeSubmitTaskResponse)
}
func (c *CorndogsClient) GetTaskStateByID(ctx context.Context, req *api.GetTaskStateByIDRequest) (*api.GetTaskStateByIDResponse, error) {
	return cborDo(ctx, c, "GetTaskStateByID", req, api.EncodeGetTaskStateByIDRequest, api.DecodeGetTaskStateByIDResponse)
}
func (c *CorndogsClient) GetNextTask(ctx context.Context, req *api.GetNextTaskRequest) (*api.GetNextTaskResponse, error) {
	return cborDo(ctx, c, "GetNextTask", req, api.EncodeGetNextTaskRequest, api.DecodeGetNextTaskResponse)
}
func (c *CorndogsClient) GetNextTaskGroup(ctx context.Context, req *api.GetNextTaskGroupRequest) (*api.GetNextTaskGroupResponse, error) {
	return cborDo(ctx, c, "GetNextTaskGroup", req, api.EncodeGetNextTaskGroupRequest, api.DecodeGetNextTaskGroupResponse)
}
func (c *CorndogsClient) UpdateTask(ctx context.Context, req *api.UpdateTaskRequest) (*api.UpdateTaskResponse, error) {
	return cborDo(ctx, c, "UpdateTask", req, api.EncodeUpdateTaskRequest, api.DecodeUpdateTaskResponse)
}
func (c *CorndogsClient) CompleteTask(ctx context.Context, req *api.CompleteTaskRequest) (*api.CompleteTaskResponse, error) {
	return cborDo(ctx, c, "CompleteTask", req, api.EncodeCompleteTaskRequest, api.DecodeCompleteTaskResponse)
}
func (c *CorndogsClient) CancelTask(ctx context.Context, req *api.CancelTaskRequest) (*api.CancelTaskResponse, error) {
	return cborDo(ctx, c, "CancelTask", req, api.EncodeCancelTaskRequest, api.DecodeCancelTaskResponse)
}
func (c *CorndogsClient) CleanUpTimedOut(ctx context.Context, req *api.CleanUpTimedOutRequest) (*api.CleanUpTimedOutResponse, error) {
	return cborDo(ctx, c, "CleanUpTimedOut", req, api.EncodeCleanUpTimedOutRequest, api.DecodeCleanUpTimedOutResponse)
}
func (c *CorndogsClient) GetQueues(ctx context.Context, req *api.GetQueuesRequest) (*api.GetQueuesResponse, error) {
	return cborDo(ctx, c, "GetQueues", req, api.EncodeGetQueuesRequest, api.DecodeGetQueuesResponse)
}
func (c *CorndogsClient) GetQueueTaskCounts(ctx context.Context, req *api.GetQueueTaskCountsRequest) (*api.GetQueueTaskCountsResponse, error) {
	return cborDo(ctx, c, "GetQueueTaskCounts", req, api.EncodeGetQueueTaskCountsRequest, api.DecodeGetQueueTaskCountsResponse)
}
func (c *CorndogsClient) GetTaskStateCounts(ctx context.Context, req *api.GetTaskStateCountsRequest) (*api.GetTaskStateCountsResponse, error) {
	return cborDo(ctx, c, "GetTaskStateCounts", req, api.EncodeGetTaskStateCountsRequest, api.DecodeGetTaskStateCountsResponse)
}
func (c *CorndogsClient) GetQueueAndStateCounts(ctx context.Context, req *api.GetQueueAndStateCountsRequest) (*api.GetQueueAndStateCountsResponse, error) {
	return cborDo(ctx, c, "GetQueueAndStateCounts", req, api.EncodeGetQueueAndStateCountsRequest, api.DecodeGetQueueAndStateCountsResponse)
}
