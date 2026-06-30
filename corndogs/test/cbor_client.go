package test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	"github.com/CatalystCommunity/corndogs/corndogs/server/csilrpc"
)

// CorndogsClient is a CSIL-RPC test client matching the call shape the integration
// tests use: Method(ctx, *Request) (*Response, error).
type CorndogsClient struct{ base string }

// GetCorndogsClient returns a client pointed at the local server on :5080.
func GetCorndogsClient() *CorndogsClient {
	return &CorndogsClient{base: "http://127.0.0.1:5080"}
}

// cborDo performs one CSIL-RPC call over the envelope-in-body HTTP profile. The
// request/response payloads use the csilgen-generated per-type codecs (encode/
// decode passed in); the carrier only moves bytes + the envelope.
func cborDo[Req any, Resp any](
	ctx context.Context, c *CorndogsClient, op string, req *Req,
	encode func(Req) []byte, decode func([]byte) (Resp, error),
) (*Resp, error) {
	env, err := csilrpc.NewRpcRequest("corndogs", op, encode(*req)).Encode()
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/csil/v1/rpc", bytes.NewReader(env))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/cbor")
	httpResp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()
	raw, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, err
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return nil, fmt.Errorf("http %d", httpResp.StatusCode)
	}
	rr, err := csilrpc.DecodeRpcResponse(raw)
	if err != nil {
		return nil, err
	}
	if terr := rr.AsTransportError(); terr != nil {
		return nil, terr
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
