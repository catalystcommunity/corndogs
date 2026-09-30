package metrics

import (
	"github.com/CatalystCommunity/corndogs/corndogs/server/config"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Resilience metrics. Operators watch legacy_requests_total fall to zero before
// they set a policy to "required". Labels have a small, fixed set of values.
var (
	LegacyRequestsTotal   *prometheus.CounterVec
	PolicyRejectionsTotal *prometheus.CounterVec
	ReplayedRequestsTotal *prometheus.CounterVec
	ResiliencePolicy      *prometheus.GaugeVec
	ReceiptsPurgedTotal   prometheus.Counter
)

func initResilienceMetrics() {
	LegacyRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: config.PrometheusNamespace,
		Name:      "legacy_requests_total",
		Help:      "Accepted legacy mutation requests (no submission key or task guard), by operation",
	}, []string{"op"})
	PolicyRejectionsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: config.PrometheusNamespace,
		Name:      "policy_rejections_total",
		Help:      "Requests rejected by the resilience policy before they changed data, by operation and reason",
	}, []string{"op", "reason"})
	ReplayedRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: config.PrometheusNamespace,
		Name:      "replayed_requests_total",
		Help:      "Keyed or guarded requests answered from a stored receipt, by operation",
	}, []string{"op"})
	ResiliencePolicy = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: config.PrometheusNamespace,
		Name:      "resilience_policy",
		Help:      "Effective policy of each resilience feature (1 for the active policy)",
	}, []string{"feature", "policy"})
	ReceiptsPurgedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: config.PrometheusNamespace,
		Name:      "receipts_purged_total",
		Help:      "Expired submission and operation receipts deleted",
	})
	ResiliencePolicy.WithLabelValues("submission_keys", config.SubmissionKeyPolicy).Set(1)
	ResiliencePolicy.WithLabelValues("task_guards", config.TaskGuardPolicy).Set(1)
}

// CountLegacy records one accepted legacy mutation request.
func CountLegacy(op string) {
	if config.PrometheusEnabled && LegacyRequestsTotal != nil {
		LegacyRequestsTotal.WithLabelValues(op).Inc()
	}
}

// CountRejection records one policy rejection.
func CountRejection(op, reason string) {
	if config.PrometheusEnabled && PolicyRejectionsTotal != nil {
		PolicyRejectionsTotal.WithLabelValues(op, reason).Inc()
	}
}

// CountReplay records one request answered from a receipt.
func CountReplay(op string) {
	if config.PrometheusEnabled && ReplayedRequestsTotal != nil {
		ReplayedRequestsTotal.WithLabelValues(op).Inc()
	}
}

// CountPurged records deleted receipts.
func CountPurged(n int) {
	if config.PrometheusEnabled && ReceiptsPurgedTotal != nil {
		ReceiptsPurgedTotal.Add(float64(n))
	}
}
