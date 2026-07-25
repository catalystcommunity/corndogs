package server

import (
	"fmt"
	"net"
	"net/http"

	"github.com/CatalystCommunity/corndogs/corndogs/server/clustering"
	"github.com/CatalystCommunity/corndogs/corndogs/server/config"
	"github.com/CatalystCommunity/corndogs/corndogs/server/implementations"
	"github.com/CatalystCommunity/corndogs/corndogs/server/logging"
	"github.com/CatalystCommunity/corndogs/corndogs/server/metrics"
	"github.com/CatalystCommunity/corndogs/corndogs/server/store"
	"github.com/CatalystCommunity/corndogs/corndogs/server/store/filestore"
	"github.com/CatalystCommunity/corndogs/corndogs/server/store/postgresstore"
	zlog "github.com/rs/zerolog/log"
)

// listenOn is the CSIL-RPC TCP (StreamCarrier) listen address — the canonical
// client transport. Configurable so multiple nodes can run on one host.
var listenOn = config.GetEnvOrDefault("CORNDOGS_LISTEN", "0.0.0.0:5080")

// opsListenOn is the HTTP address for liveness/readiness and Prometheus only
// (never CSIL-RPC).
var opsListenOn = config.GetEnvOrDefault("CORNDOGS_HTTP_LISTEN", ":8080")

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
	// Clustering wraps the file backend for redundancy/failover (docs/clustering-tier1.md).
	// It takes precedence over STORAGE_BACKEND when CORNDOGS_CLUSTER_ENABLED=true.
	if cs := clustering.LoadSettings(); cs.Enabled {
		wrapped, err := clustering.Setup(cs, filestore.LoadConfig())
		if err != nil {
			return fmt.Errorf("cluster setup: %w", err)
		}
		zlog.Info().Str("node", cs.NodeID).Strs("peers", cs.Peers).Int("ack_count", cs.DurabilityAckCount()).
			Msg("clustering enabled (replicated file backend)")
		store.SetStore(wrapped)
		return nil
	}
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

	srv := &implementations.V1Alpha1Server{}

	// Ops HTTP (health + Prometheus) on a separate port. HTTP now serves ONLY
	// liveness/readiness and metrics; the CSIL-RPC service itself is TCP (below).
	http.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	if config.PrometheusEnabled {
		metrics.StartMetricsEndpoint(opsListenOn) // /metrics + /healthz on the ops port
		metrics.InitializeMetrics()
		if config.PrometheusQueueSizeEnabled {
			metrics.StartQueueSizeMetric(config.PrometheusQueueSizeInterval, config.PrometheusMetricQueryTimeout)
		}
		if cs, ok := store.AppStore.(*clustering.ClusteredStore); ok {
			metrics.StartClusterMetrics(cs.Engine())
		}
	} else {
		go func() { _ = http.ListenAndServe(opsListenOn, nil) }() // /healthz only
	}
	if _, ok := store.AppStore.(*clustering.ClusteredStore); ok {
		zlog.Info().Msg("clustering active (TCP StreamCarrier peer transport)")
	}

	// CSIL-RPC transport = TCP StreamCarrier (the canonical client transport). One
	// shared dispatch (buildRPCRoutes) as the HTTP profile used; only framing differs.
	ln, err := net.Listen("tcp", listenOn)
	if err != nil {
		return err
	}
	zlog.Info().Str("rpc_tcp", listenOn).Str("ops_http", opsListenOn).Msg("corndogs listening (CSIL-RPC over TCP)")
	serveCSILRPCTCP(ln, srv) // blocks until the listener closes
	return nil
}
