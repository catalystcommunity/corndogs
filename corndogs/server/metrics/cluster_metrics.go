package metrics

import (
	"time"

	"github.com/CatalystCommunity/corndogs/corndogs/server/clustering"
	"github.com/CatalystCommunity/corndogs/corndogs/server/config"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Cluster metrics are deliberately low-cardinality: node-level gauges labeled only
// by the bounded node_id (never by queue, task, or peer), so a large cluster or a
// high queue count can't blow up the series count.
var (
	clusterIsLeader     *prometheus.GaugeVec
	clusterEpoch        *prometheus.GaugeVec
	clusterAppliedLSN   *prometheus.GaugeVec
	clusterCommittedLSN *prometheus.GaugeVec
	clusterAckCount     *prometheus.GaugeVec
	clusterLeaderChng   *prometheus.CounterVec
)

func initClusterMetrics() {
	labels := []string{"node_id"}
	clusterIsLeader = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: config.PrometheusNamespace, Subsystem: "cluster",
		Name: "is_leader", Help: "1 if this node currently leads, else 0",
	}, labels)
	clusterEpoch = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: config.PrometheusNamespace, Subsystem: "cluster",
		Name: "epoch", Help: "Current leadership epoch (increments each election)",
	}, labels)
	clusterAppliedLSN = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: config.PrometheusNamespace, Subsystem: "cluster",
		Name: "applied_lsn", Help: "Highest replication LSN applied locally",
	}, labels)
	clusterCommittedLSN = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: config.PrometheusNamespace, Subsystem: "cluster",
		Name: "committed_lsn", Help: "Semi-sync commit point (LSN durable on the ack quorum)",
	}, labels)
	clusterAckCount = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: config.PrometheusNamespace, Subsystem: "cluster",
		Name: "ack_count", Help: "Configured semi-sync durability quorum (follower acks per write)",
	}, labels)
	clusterLeaderChng = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: config.PrometheusNamespace, Subsystem: "cluster",
		Name: "leader_changes_total", Help: "Number of observed leadership changes",
	}, labels)
}

// StartClusterMetrics registers cluster gauges and starts a goroutine that samples
// the engine's stats every few seconds and updates them. Called only when
// clustering + Prometheus are both enabled.
func StartClusterMetrics(engine *clustering.Engine) {
	initClusterMetrics()
	go func() {
		var lastLeader string
		var seenLeader bool
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for range t.C {
			s := engine.Stats()
			id := s.NodeID
			b := 0.0
			if s.IsLeader {
				b = 1
			}
			clusterIsLeader.WithLabelValues(id).Set(b)
			clusterEpoch.WithLabelValues(id).Set(float64(s.Epoch))
			clusterAppliedLSN.WithLabelValues(id).Set(float64(s.AppliedLSN))
			clusterCommittedLSN.WithLabelValues(id).Set(float64(s.CommittedLSN))
			clusterAckCount.WithLabelValues(id).Set(float64(s.AckCount))
			if s.LeaderID != "" && (!seenLeader || s.LeaderID != lastLeader) {
				if seenLeader {
					clusterLeaderChng.WithLabelValues(id).Inc()
				}
				lastLeader, seenLeader = s.LeaderID, true
			}
		}
	}()
}
