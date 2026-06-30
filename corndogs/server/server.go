package server

import (
	"fmt"
	"net/http"

	"github.com/CatalystCommunity/corndogs/corndogs/server/config"
	"github.com/CatalystCommunity/corndogs/corndogs/server/implementations"
	"github.com/CatalystCommunity/corndogs/corndogs/server/logging"
	"github.com/CatalystCommunity/corndogs/corndogs/server/metrics"
	"github.com/CatalystCommunity/corndogs/corndogs/server/store"
	"github.com/CatalystCommunity/corndogs/corndogs/server/store/filestore"
	"github.com/CatalystCommunity/corndogs/corndogs/server/store/postgresstore"
	zlog "github.com/rs/zerolog/log"
)

const listenOn = "0.0.0.0:5080"

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

	mux := http.NewServeMux()
	// Liveness/readiness for k8s.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	// CSIL-RPC transport (envelope-in-body HTTP profile).
	mux.HandleFunc(rpcPath, newCSILRPCHandler(srv))

	zlog.Info().Str("addr", listenOn).Str("rpc", rpcPath).Msg("corndogs listening (CSIL-RPC over HTTP)")
	return http.ListenAndServe(listenOn, mux)
}
