package test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	api "github.com/CatalystCommunity/corndogs/corndogs/server/csilapi"
	"github.com/fxamacker/cbor/v2"
)

// CorndogsClient is a CBOR-over-HTTP test client matching the call shape the
// integration tests use: Method(ctx, *Request) (*Response, error).
type CorndogsClient struct{ base string }

// GetCorndogsClient returns a client pointed at the local server on :5080.
func GetCorndogsClient() *CorndogsClient {
	return &CorndogsClient{base: "http://127.0.0.1:5080"}
}

func cborDo[Req any, Resp any](ctx context.Context, c *CorndogsClient, method string, req *Req) (*Resp, error) {
	body, err := cbor.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base+"/v1alpha1/corndogs/"+method, bytes.NewReader(body))
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
		var serr api.ServiceError
		if cbor.Unmarshal(raw, &serr) == nil && serr.Message != "" {
			return nil, fmt.Errorf("service error %d: %s", serr.Code, serr.Message)
		}
		return nil, fmt.Errorf("http %d", httpResp.StatusCode)
	}
	var resp Resp
	if err := cbor.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *CorndogsClient) SubmitTask(ctx context.Context, req *api.SubmitTaskRequest) (*api.SubmitTaskResponse, error) {
	return cborDo[api.SubmitTaskRequest, api.SubmitTaskResponse](ctx, c, "SubmitTask", req)
}
func (c *CorndogsClient) GetTaskStateByID(ctx context.Context, req *api.GetTaskStateByIDRequest) (*api.GetTaskStateByIDResponse, error) {
	return cborDo[api.GetTaskStateByIDRequest, api.GetTaskStateByIDResponse](ctx, c, "GetTaskStateByID", req)
}
func (c *CorndogsClient) GetNextTask(ctx context.Context, req *api.GetNextTaskRequest) (*api.GetNextTaskResponse, error) {
	return cborDo[api.GetNextTaskRequest, api.GetNextTaskResponse](ctx, c, "GetNextTask", req)
}
func (c *CorndogsClient) UpdateTask(ctx context.Context, req *api.UpdateTaskRequest) (*api.UpdateTaskResponse, error) {
	return cborDo[api.UpdateTaskRequest, api.UpdateTaskResponse](ctx, c, "UpdateTask", req)
}
func (c *CorndogsClient) CompleteTask(ctx context.Context, req *api.CompleteTaskRequest) (*api.CompleteTaskResponse, error) {
	return cborDo[api.CompleteTaskRequest, api.CompleteTaskResponse](ctx, c, "CompleteTask", req)
}
func (c *CorndogsClient) CancelTask(ctx context.Context, req *api.CancelTaskRequest) (*api.CancelTaskResponse, error) {
	return cborDo[api.CancelTaskRequest, api.CancelTaskResponse](ctx, c, "CancelTask", req)
}
func (c *CorndogsClient) CleanUpTimedOut(ctx context.Context, req *api.CleanUpTimedOutRequest) (*api.CleanUpTimedOutResponse, error) {
	return cborDo[api.CleanUpTimedOutRequest, api.CleanUpTimedOutResponse](ctx, c, "CleanUpTimedOut", req)
}
func (c *CorndogsClient) GetQueues(ctx context.Context, req *api.GetQueuesRequest) (*api.GetQueuesResponse, error) {
	return cborDo[api.GetQueuesRequest, api.GetQueuesResponse](ctx, c, "GetQueues", req)
}
func (c *CorndogsClient) GetQueueTaskCounts(ctx context.Context, req *api.GetQueueTaskCountsRequest) (*api.GetQueueTaskCountsResponse, error) {
	return cborDo[api.GetQueueTaskCountsRequest, api.GetQueueTaskCountsResponse](ctx, c, "GetQueueTaskCounts", req)
}
func (c *CorndogsClient) GetTaskStateCounts(ctx context.Context, req *api.GetTaskStateCountsRequest) (*api.GetTaskStateCountsResponse, error) {
	return cborDo[api.GetTaskStateCountsRequest, api.GetTaskStateCountsResponse](ctx, c, "GetTaskStateCounts", req)
}
func (c *CorndogsClient) GetQueueAndStateCounts(ctx context.Context, req *api.GetQueueAndStateCountsRequest) (*api.GetQueueAndStateCountsResponse, error) {
	return cborDo[api.GetQueueAndStateCountsRequest, api.GetQueueAndStateCountsResponse](ctx, c, "GetQueueAndStateCounts", req)
}
