package server

import (
	"context"
	"fmt"
	"io"
	"net/http"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	"github.com/CatalystCommunity/corndogs/corndogs/server/csilrpc"
	zlog "github.com/rs/zerolog/log"
)

// CSIL-RPC transport for corndogs. The canonical mount is the envelope-in-body
// HTTP profile: clients POST a CsilRpcRequest envelope to rpcPath and get a
// CsilRpcResponse envelope back (application/cbor, HTTP 200). The application
// payload is tag-24-wrapped CBOR of the request/response type. See csilgen
// docs/csil-rpc-transport.md.
const (
	rpcServiceName = "corndogs"      // wire service name (lowercased, "Service"-stripped)
	rpcPath        = "/csil/v1/rpc"  // canonical envelope-in-body mount
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
		"UpdateTask":             rpcRoute(svc.UpdateTask, api.DecodeUpdateTaskRequest, api.EncodeUpdateTaskResponse, "UpdateTaskResponse"),
		"CompleteTask":           rpcRoute(svc.CompleteTask, api.DecodeCompleteTaskRequest, api.EncodeCompleteTaskResponse, "CompleteTaskResponse"),
		"CancelTask":             rpcRoute(svc.CancelTask, api.DecodeCancelTaskRequest, api.EncodeCancelTaskResponse, "CancelTaskResponse"),
		"CleanUpTimedOut":        rpcRoute(svc.CleanUpTimedOut, api.DecodeCleanUpTimedOutRequest, api.EncodeCleanUpTimedOutResponse, "CleanUpTimedOutResponse"),
		"GetQueues":              rpcRoute(svc.GetQueues, api.DecodeGetQueuesRequest, api.EncodeGetQueuesResponse, "GetQueuesResponse"),
		"GetQueueTaskCounts":     rpcRoute(svc.GetQueueTaskCounts, api.DecodeGetQueueTaskCountsRequest, api.EncodeGetQueueTaskCountsResponse, "GetQueueTaskCountsResponse"),
		"GetTaskStateCounts":     rpcRoute(svc.GetTaskStateCounts, api.DecodeGetTaskStateCountsRequest, api.EncodeGetTaskStateCountsResponse, "GetTaskStateCountsResponse"),
		"GetQueueAndStateCounts": rpcRoute(svc.GetQueueAndStateCounts, api.DecodeGetQueueAndStateCountsRequest, api.EncodeGetQueueAndStateCountsResponse, "GetQueueAndStateCountsResponse"),
	}
}

// newCSILRPCHandler returns the HTTP handler for the envelope-in-body profile.
func newCSILRPCHandler(svc api.CorndogsService) http.HandlerFunc {
	routes := buildRPCRoutes(svc)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeRPC(w, csilrpc.NewRpcResponseTransportError(csilrpc.StatusMalformedEnvelope, "method not allowed"))
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeRPC(w, csilrpc.NewRpcResponseTransportError(csilrpc.StatusMalformedEnvelope, "read body: "+err.Error()))
			return
		}
		req, err := csilrpc.DecodeRpcRequest(body)
		if err != nil {
			writeRPC(w, csilrpc.NewRpcResponseTransportError(csilrpc.StatusMalformedEnvelope, err.Error()))
			return
		}
		if req.Service != rpcServiceName {
			writeRPC(w, csilrpc.NewRpcResponseTransportError(csilrpc.StatusUnknownServiceOrOp, "unknown service: "+req.Service).WithID(req.ID))
			return
		}
		route, ok := routes[req.Op]
		if !ok {
			writeRPC(w, csilrpc.NewRpcResponseTransportError(csilrpc.StatusUnknownServiceOrOp, "unknown op: "+req.Op).WithID(req.ID))
			return
		}
		// Store methods panic on internal failure; recover so the connection isn't
		// dropped and the caller gets a transport "internal" status instead.
		variant, out, herr := func() (v string, o []byte, e error) {
			defer func() {
				if p := recover(); p != nil {
					e = fmt.Errorf("panic: %v", p)
				}
			}()
			return route(r.Context(), req.Payload)
		}()
		if herr != nil {
			zlog.Error().Err(herr).Str("op", req.Op).Msg("csil-rpc handler error")
			writeRPC(w, csilrpc.NewRpcResponseTransportError(csilrpc.StatusInternal, herr.Error()).WithID(req.ID))
			return
		}
		writeRPC(w, csilrpc.NewRpcResponseOk(variant, out).WithID(req.ID))
	}
}

// writeRPC encodes a CsilRpcResponse envelope and writes it. Per the spec the
// HTTP status is 200 whenever an envelope is returned, even one carrying a
// non-zero transport status.
func writeRPC(w http.ResponseWriter, resp csilrpc.RpcResponse) {
	b, err := resp.Encode()
	if err != nil {
		http.Error(w, "encode error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/cbor")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}
