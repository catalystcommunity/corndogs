package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/CatalystCommunity/corndogs/corndogs/server/config"
	api "github.com/CatalystCommunity/corndogs/corndogs/server/csilapi"
	"github.com/CatalystCommunity/corndogs/corndogs/server/implementations"
	"github.com/CatalystCommunity/corndogs/corndogs/server/logging"
	"github.com/CatalystCommunity/corndogs/corndogs/server/metrics"
	"github.com/CatalystCommunity/corndogs/corndogs/server/store"
	"github.com/CatalystCommunity/corndogs/corndogs/server/store/filestore"
	"github.com/CatalystCommunity/corndogs/corndogs/server/store/postgresstore"
	"github.com/fxamacker/cbor/v2"
	zlog "github.com/rs/zerolog/log"
)

const listenOn = "0.0.0.0:5080"

// servicePath is the URL prefix for unary CSIL calls: /v1alpha1/corndogs/{Method}.
const servicePath = "/v1alpha1/corndogs/"

func SetupAndRun() {
	logging.InitLogging()
	if err := selectStore(); err != nil {
		zlog.Fatal().Err(err).Msg("store selection failed")
	}
	if err := run(); err != nil {
		zlog.Fatal().Err(err).Msg("server exited")
	}
}

// selectStore chooses the storage backend from STORAGE_BACKEND:
//
//	postgres (default) — the shared, horizontally-scalable backend.
//	file               — an embedded, single-process backend (bolt or bunt,
//	                     chosen via CORNDOGS_FILESTORE_BACKEND). No extra system
//	                     to operate; trades horizontal scale-out / HA for it.
func selectStore() error {
	switch config.GetEnvOrDefault("STORAGE_BACKEND", "postgres") {
	case "postgres":
		store.SetStore(postgresstore.PostgresStore{})
	case "file":
		s, err := filestore.New(filestore.LoadConfig())
		if err != nil {
			return err
		}
		store.SetStore(s)
	default:
		return fmt.Errorf("unknown STORAGE_BACKEND %q (want \"postgres\" or \"file\")",
			config.GetEnvOrDefault("STORAGE_BACKEND", "postgres"))
	}
	return nil
}

// methodHandler decodes a CBOR request body, invokes the service, and returns the
// CBOR-encoded response.
type methodHandler func(ctx context.Context, body []byte) ([]byte, error)

// wrap adapts a typed unary service method into a CBOR byte handler.
func wrap[Req any, Resp any](fn func(context.Context, Req) (Resp, error)) methodHandler {
	return func(ctx context.Context, body []byte) ([]byte, error) {
		var req Req
		if len(body) > 0 {
			if err := cbor.Unmarshal(body, &req); err != nil {
				return nil, err
			}
		}
		resp, err := fn(ctx, req)
		if err != nil {
			return nil, err
		}
		return cbor.Marshal(resp)
	}
}

func run() error {
	deferredFunc, err := store.AppStore.Initialize()
	if err != nil {
		zlog.Error().Err(err).Msg("store initialize failed")
		panic(err)
	}
	if deferredFunc != nil {
		defer deferredFunc()
	}

	if config.PrometheusEnabled {
		metrics.StartMetricsEndpoint()
		metrics.InitializeMetrics()
		if config.PrometheusQueueSizeEnabled {
			metrics.StartQueueSizeMetric(config.PrometheusQueueSizeInterval, config.PrometheusMetricQueryTimeout)
		}
	}

	srv := &implementations.V1Alpha1Server{}
	handlers := map[string]methodHandler{
		"SubmitTask":             wrap(srv.SubmitTask),
		"GetTaskStateByID":       wrap(srv.GetTaskStateByID),
		"GetNextTask":            wrap(srv.GetNextTask),
		"UpdateTask":             wrap(srv.UpdateTask),
		"CompleteTask":           wrap(srv.CompleteTask),
		"CancelTask":             wrap(srv.CancelTask),
		"CleanUpTimedOut":        wrap(srv.CleanUpTimedOut),
		"GetQueues":              wrap(srv.GetQueues),
		"GetQueueTaskCounts":     wrap(srv.GetQueueTaskCounts),
		"GetTaskStateCounts":     wrap(srv.GetTaskStateCounts),
		"GetQueueAndStateCounts": wrap(srv.GetQueueAndStateCounts),
	}

	mux := http.NewServeMux()
	// Liveness/readiness for k8s (replaces the old gRPC health service).
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc(servicePath, func(w http.ResponseWriter, r *http.Request) {
		dispatch(w, r, handlers)
	})

	zlog.Info().Str("addr", listenOn).Msg("corndogs listening (CBOR over HTTP)")
	return http.ListenAndServe(listenOn, mux)
}

func dispatch(w http.ResponseWriter, r *http.Request, handlers map[string]methodHandler) {
	defer func() {
		if p := recover(); p != nil {
			zlog.Error().Interface("panic", p).Msg("handler panicked")
			writeError(w, http.StatusInternalServerError, 13, "internal error")
		}
	}()

	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, 12, "method not allowed")
		return
	}
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	h, ok := handlers[method]
	if !ok {
		writeError(w, http.StatusNotFound, 12, "unknown method: "+method)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, 3, "read body: "+err.Error())
		return
	}
	respBytes, err := h(r.Context(), body)
	if err != nil {
		zlog.Error().Err(err).Str("method", method).Msg("handler error")
		writeError(w, http.StatusInternalServerError, 13, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/cbor")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(respBytes)
}

// writeError sends a CBOR-encoded ServiceError with the given HTTP status.
func writeError(w http.ResponseWriter, status int, code uint64, msg string) {
	b, err := cbor.Marshal(api.ServiceError{Code: code, Message: msg})
	if err != nil {
		http.Error(w, msg, status)
		return
	}
	w.Header().Set("Content-Type", "application/cbor")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}
