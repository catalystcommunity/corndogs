package server

import (
	"context"
	"fmt"
	"net"
	"sync"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	"github.com/CatalystCommunity/corndogs/corndogs/server/config"
	"github.com/CatalystCommunity/corndogs/corndogs/server/csilrpc"
	zlog "github.com/rs/zerolog/log"
)

// Requests on one connection run concurrently so that one slow operation does
// not block other multiplexed requests. A mutex keeps response frames intact.
const (
	opHello = "$hello"
	opPing  = "$ping"
)

// This limit gives backpressure before one connection creates too many goroutines.
const maxConcurrentPerConn = 256

// serveCSILRPCTCP accepts connections on ln and serves CSIL-RPC on each until ln
// is closed.
func serveCSILRPCTCP(ln net.Listener, svc api.CorndogsService) {
	routes := buildRPCRoutes(svc)
	for {
		conn, err := ln.Accept()
		if err != nil {
			return // listener closed
		}
		go serveConn(conn, routes)
	}
}

func serveConn(conn net.Conn, routes map[string]rpcHandlerFunc) {
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetKeepAlive(true)
	}
	defer conn.Close()
	// Close of the connection cancels its active store operations.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	carrier := csilrpc.NewStreamCarrierWithMaxFrame(conn, config.MaxRPCFrameBytes)
	var writeMu sync.Mutex // serializes response frames onto the shared connection
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxConcurrentPerConn)
	for {
		frame, err := carrier.RecvFrame()
		if err != nil || frame == nil {
			break // connection closed or errored
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(frame []byte) {
			defer wg.Done()
			defer func() { <-sem }()
			out := handleRPCFrame(ctx, routes, frame)
			if out == nil {
				return
			}
			writeMu.Lock()
			_ = carrier.SendFrame(out)
			writeMu.Unlock()
		}(frame)
	}
	// The connection is gone: cancel any still-running handlers' store ops now (their
	// responses can't be delivered anyway) rather than waiting them out — this is the
	// disconnect-cancels-the-query behavior the HTTP handler had via r.Context().
	cancel()
	wg.Wait()
}

// handleRPCFrame decodes one request frame, dispatches it, and returns the encoded
// response frame (echoing the request's correlation id so a multiplexing client can
// match the reply to its call).
func handleRPCFrame(ctx context.Context, routes map[string]rpcHandlerFunc, frame []byte) []byte {
	req, derr := csilrpc.DecodeRpcRequest(frame)
	if derr != nil {
		out, _ := csilrpc.NewRpcResponseTransportError(csilrpc.StatusMalformedEnvelope, derr.Error()).Encode()
		return out
	}
	id := req.ID
	outcome := dispatchRPC(ctx, routes, &req)
	var resp csilrpc.RpcResponse
	if outcome.IsReply {
		resp = csilrpc.NewRpcResponseOk(outcome.Variant, outcome.Payload).WithID(id)
	} else {
		resp = csilrpc.NewRpcResponseTransportError(outcome.Status, outcome.Message).WithID(id)
	}
	out, err := resp.Encode()
	if err != nil {
		out, _ = csilrpc.NewRpcResponseTransportError(csilrpc.StatusInternal, err.Error()).WithID(id).Encode()
	}
	return out
}

// dispatchRPC handles one request: control ops (`$hello`/`$ping`) get a control
// reply; everything else routes to the application handler exactly as the HTTP
// profile did, threading the connection context so a dropped client cancels the
// underlying store operation.
func dispatchRPC(ctx context.Context, routes map[string]rpcHandlerFunc, req *csilrpc.RpcRequest) csilrpc.HandlerOutcome {
	switch req.Op {
	case opHello:
		return csilrpc.Reply("$hello-ack", nil)
	case opPing:
		return csilrpc.Reply("$pong", nil)
	}
	if req.Service != rpcServiceName {
		return csilrpc.Transport(csilrpc.StatusUnknownServiceOrOp, "unknown service: "+req.Service)
	}
	route, ok := routes[req.Op]
	if !ok {
		return csilrpc.Transport(csilrpc.StatusUnknownServiceOrOp, "unknown op: "+req.Op)
	}
	// Store methods panic on internal failure; recover so one bad call doesn't drop
	// the whole connection.
	variant, out, herr := func() (v string, o []byte, e error) {
		defer func() {
			if p := recover(); p != nil {
				e = fmt.Errorf("panic: %v", p)
			}
		}()
		return route(ctx, req.Payload)
	}()
	if herr != nil {
		zlog.Error().Err(herr).Str("op", req.Op).Msg("csil-rpc/tcp handler error")
		return csilrpc.Transport(csilrpc.StatusInternal, herr.Error())
	}
	return csilrpc.Reply(variant, out)
}
