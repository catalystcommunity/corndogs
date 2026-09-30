package server

import (
	"context"
	"errors"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
)

// CSIL-RPC route table for corndogs. buildRPCRoutes maps each CSIL operation to
// a typed handler; the TCP serve loop (tcp_rpc.go) dispatches to it. The wire is
// CSIL-RPC (csilgen docs/csil-rpc-transport.md): the application payload is a
// tag-24-wrapped CBOR of the request/response type.
const (
	rpcServiceName      = "CorndogsService" // wire service name = the verbatim CSIL `service` block name (what generated clients send)
	serviceErrorVariant = "ServiceError"
)

// rpcHandlerFunc decodes a request payload, invokes a service method, and returns
// the success variant name + encoded response payload (or an error → transport
// status "internal").
type rpcHandlerFunc func(ctx context.Context, payload []byte) (variant string, out []byte, err error)

// rpcRoute adapts a typed CorndogsService method into an rpcHandlerFunc, using the
// csilgen-generated per-type codecs (api.Decode*/api.Encode*) for the tag-24
// payload. The reply payload is tagged with the success arm name (e.g.
// "SubmitTaskResponse"). No reflection / struct tags — the generated codec owns
// the wire bytes.
func rpcRoute[Req any, Resp any](
	fn func(context.Context, Req) (Resp, error),
	decode func([]byte) (Req, error),
	encode func(Resp) []byte,
	variant string,
) rpcHandlerFunc {
	return func(ctx context.Context, payload []byte) (string, []byte, error) {
		req, err := decode(payload)
		if err != nil {
			return "", nil, err
		}
		resp, err := fn(ctx, req)
		if err != nil {
			// A ServiceError is the operation's error arm: status 0 with the
			// ServiceError variant, which every generated client decodes.
			var se *api.ServiceError
			if errors.As(err, &se) {
				return serviceErrorVariant, api.EncodeServiceError(*se), nil
			}
			return "", nil, err
		}
		return variant, encode(resp), nil
	}
}

// buildRPCRoutes maps each CSIL operation name to its handler. The op names are
// exactly what generated clients send (PascalCase method names).
func buildRPCRoutes(svc api.CorndogsService) map[string]rpcHandlerFunc {
	return map[string]rpcHandlerFunc{
		"SubmitTask":             rpcRoute(svc.SubmitTask, api.DecodeSubmitTaskRequest, api.EncodeSubmitTaskResponse, "SubmitTaskResponse"),
		"GetTaskStateByID":       rpcRoute(svc.GetTaskStateByID, api.DecodeGetTaskStateByIDRequest, api.EncodeGetTaskStateByIDResponse, "GetTaskStateByIDResponse"),
		"GetNextTask":            rpcRoute(svc.GetNextTask, api.DecodeGetNextTaskRequest, api.EncodeGetNextTaskResponse, "GetNextTaskResponse"),
		"GetNextTaskGroup":       rpcRoute(svc.GetNextTaskGroup, api.DecodeGetNextTaskGroupRequest, api.EncodeGetNextTaskGroupResponse, "GetNextTaskGroupResponse"),
		"UpdateTask":             rpcRoute(svc.UpdateTask, api.DecodeUpdateTaskRequest, api.EncodeUpdateTaskResponse, "UpdateTaskResponse"),
		"CompleteTask":           rpcRoute(svc.CompleteTask, api.DecodeCompleteTaskRequest, api.EncodeCompleteTaskResponse, "CompleteTaskResponse"),
		"CancelTask":             rpcRoute(svc.CancelTask, api.DecodeCancelTaskRequest, api.EncodeCancelTaskResponse, "CancelTaskResponse"),
		"CleanUpTimedOut":        rpcRoute(svc.CleanUpTimedOut, api.DecodeCleanUpTimedOutRequest, api.EncodeCleanUpTimedOutResponse, "CleanUpTimedOutResponse"),
		"GetQueues":              rpcRoute(svc.GetQueues, api.DecodeGetQueuesRequest, api.EncodeGetQueuesResponse, "GetQueuesResponse"),
		"GetQueueTaskCounts":     rpcRoute(svc.GetQueueTaskCounts, api.DecodeGetQueueTaskCountsRequest, api.EncodeGetQueueTaskCountsResponse, "GetQueueTaskCountsResponse"),
		"GetTaskStateCounts":     rpcRoute(svc.GetTaskStateCounts, api.DecodeGetTaskStateCountsRequest, api.EncodeGetTaskStateCountsResponse, "GetTaskStateCountsResponse"),
		"GetQueueAndStateCounts": rpcRoute(svc.GetQueueAndStateCounts, api.DecodeGetQueueAndStateCountsRequest, api.EncodeGetQueueAndStateCountsResponse, "GetQueueAndStateCountsResponse"),
		// Resilience contract (0.8.0).
		"GetServerInfo":         rpcRoute(svc.GetServerInfo, api.DecodeGetServerInfoRequest, api.EncodeGetServerInfoResponse, "GetServerInfoResponse"),
		"SubmitKeyedTask":       rpcRoute(svc.SubmitKeyedTask, api.DecodeSubmitKeyedTaskRequest, api.EncodeSubmitKeyedTaskResponse, "SubmitKeyedTaskResponse"),
		"LookupSubmission":      rpcRoute(svc.LookupSubmission, api.DecodeLookupSubmissionRequest, api.EncodeLookupSubmissionResponse, "LookupSubmissionResponse"),
		"ClaimGuardedTask":      rpcRoute(svc.ClaimGuardedTask, api.DecodeClaimGuardedTaskRequest, api.EncodeClaimGuardedTaskResponse, "ClaimGuardedTaskResponse"),
		"ClaimGuardedTaskGroup": rpcRoute(svc.ClaimGuardedTaskGroup, api.DecodeClaimGuardedTaskGroupRequest, api.EncodeClaimGuardedTaskGroupResponse, "ClaimGuardedTaskGroupResponse"),
		"UpdateGuardedTask":     rpcRoute(svc.UpdateGuardedTask, api.DecodeUpdateGuardedTaskRequest, api.EncodeUpdateGuardedTaskResponse, "UpdateGuardedTaskResponse"),
		"CompleteGuardedTask":   rpcRoute(svc.CompleteGuardedTask, api.DecodeCompleteGuardedTaskRequest, api.EncodeCompleteGuardedTaskResponse, "CompleteGuardedTaskResponse"),
		"CancelGuardedTask":     rpcRoute(svc.CancelGuardedTask, api.DecodeCancelGuardedTaskRequest, api.EncodeCancelGuardedTaskResponse, "CancelGuardedTaskResponse"),
		"GetGuardedTask":        rpcRoute(svc.GetGuardedTask, api.DecodeGetGuardedTaskRequest, api.EncodeGetGuardedTaskResponse, "GetGuardedTaskResponse"),
		"LookupOperation":       rpcRoute(svc.LookupOperation, api.DecodeLookupOperationRequest, api.EncodeLookupOperationResponse, "LookupOperationResponse"),
	}
}
