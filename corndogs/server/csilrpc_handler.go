package server

import (
	"context"
	"errors"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
)

// rpcServiceName is the service name declared in the CSIL contract.
const rpcServiceName = "CorndogsService"

// rpcHandlerFunc returns the declared reply variant and its encoded payload.
type rpcHandlerFunc func(ctx context.Context, payload []byte) (variant string, out []byte, err error)

// rpcRoute uses the generated codecs to decode requests and encode reply arms.
func rpcRoute[Req any, Resp any](
	fn func(context.Context, Req) (Resp, error),
	decode func([]byte) (Req, error),
	encodeReply func(Resp, error) (string, []byte, error),
) rpcHandlerFunc {
	return func(ctx context.Context, payload []byte) (string, []byte, error) {
		req, err := decode(payload)
		if err != nil {
			return "", nil, err
		}
		resp, err := fn(ctx, req)
		var se *api.ServiceError
		if errors.As(err, &se) {
			err = &api.RPCApplicationError[api.ServiceError]{Value: *se}
		}
		return encodeReply(resp, err)
	}
}

// buildRPCRoutes maps each CSIL operation name to its handler. The op names are
// exactly what generated clients send (PascalCase method names).
func buildRPCRoutes(svc api.CorndogsService) map[string]rpcHandlerFunc {
	return map[string]rpcHandlerFunc{
		"SubmitTask":             rpcRoute(svc.SubmitTask, api.DecodeSubmitTaskRequest, api.EncodeCorndogsSubmitTaskReply),
		"GetTaskStateByID":       rpcRoute(svc.GetTaskStateByID, api.DecodeGetTaskStateByIDRequest, api.EncodeCorndogsGetTaskStateByIDReply),
		"GetNextTask":            rpcRoute(svc.GetNextTask, api.DecodeGetNextTaskRequest, api.EncodeCorndogsGetNextTaskReply),
		"GetNextTaskGroup":       rpcRoute(svc.GetNextTaskGroup, api.DecodeGetNextTaskGroupRequest, api.EncodeCorndogsGetNextTaskGroupReply),
		"UpdateTask":             rpcRoute(svc.UpdateTask, api.DecodeUpdateTaskRequest, api.EncodeCorndogsUpdateTaskReply),
		"CompleteTask":           rpcRoute(svc.CompleteTask, api.DecodeCompleteTaskRequest, api.EncodeCorndogsCompleteTaskReply),
		"CancelTask":             rpcRoute(svc.CancelTask, api.DecodeCancelTaskRequest, api.EncodeCorndogsCancelTaskReply),
		"CleanUpTimedOut":        rpcRoute(svc.CleanUpTimedOut, api.DecodeCleanUpTimedOutRequest, api.EncodeCorndogsCleanUpTimedOutReply),
		"GetQueues":              rpcRoute(svc.GetQueues, api.DecodeGetQueuesRequest, api.EncodeCorndogsGetQueuesReply),
		"GetQueueTaskCounts":     rpcRoute(svc.GetQueueTaskCounts, api.DecodeGetQueueTaskCountsRequest, api.EncodeCorndogsGetQueueTaskCountsReply),
		"GetTaskStateCounts":     rpcRoute(svc.GetTaskStateCounts, api.DecodeGetTaskStateCountsRequest, api.EncodeCorndogsGetTaskStateCountsReply),
		"GetQueueAndStateCounts": rpcRoute(svc.GetQueueAndStateCounts, api.DecodeGetQueueAndStateCountsRequest, api.EncodeCorndogsGetQueueAndStateCountsReply),
		// Resilience contract (0.8.0).
		"GetServerInfo":         rpcRoute(svc.GetServerInfo, api.DecodeGetServerInfoRequest, api.EncodeCorndogsGetServerInfoReply),
		"SubmitKeyedTask":       rpcRoute(svc.SubmitKeyedTask, api.DecodeSubmitKeyedTaskRequest, api.EncodeCorndogsSubmitKeyedTaskReply),
		"LookupSubmission":      rpcRoute(svc.LookupSubmission, api.DecodeLookupSubmissionRequest, api.EncodeCorndogsLookupSubmissionReply),
		"ClaimGuardedTask":      rpcRoute(svc.ClaimGuardedTask, api.DecodeClaimGuardedTaskRequest, api.EncodeCorndogsClaimGuardedTaskReply),
		"ClaimGuardedTaskGroup": rpcRoute(svc.ClaimGuardedTaskGroup, api.DecodeClaimGuardedTaskGroupRequest, api.EncodeCorndogsClaimGuardedTaskGroupReply),
		"UpdateGuardedTask":     rpcRoute(svc.UpdateGuardedTask, api.DecodeUpdateGuardedTaskRequest, api.EncodeCorndogsUpdateGuardedTaskReply),
		"CompleteGuardedTask":   rpcRoute(svc.CompleteGuardedTask, api.DecodeCompleteGuardedTaskRequest, api.EncodeCorndogsCompleteGuardedTaskReply),
		"CancelGuardedTask":     rpcRoute(svc.CancelGuardedTask, api.DecodeCancelGuardedTaskRequest, api.EncodeCorndogsCancelGuardedTaskReply),
		"GetGuardedTask":        rpcRoute(svc.GetGuardedTask, api.DecodeGetGuardedTaskRequest, api.EncodeCorndogsGetGuardedTaskReply),
		"LookupOperation":       rpcRoute(svc.LookupOperation, api.DecodeLookupOperationRequest, api.EncodeCorndogsLookupOperationReply),
	}
}
