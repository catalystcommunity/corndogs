package metrics

import (
	"context"
	"net/http"
	"time"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	"github.com/CatalystCommunity/corndogs/corndogs/server/config"
	"github.com/CatalystCommunity/corndogs/corndogs/server/store"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog/log"
)

var TasksTotal prometheus.Counter
var CompletedTasksTotal prometheus.Counter
var CanceledTasksTotal prometheus.Counter
var TimedOutTasksTotal prometheus.Counter
var TasksInQueue *prometheus.GaugeVec

// StartMetricsEndpoint registers /metrics on the default mux and serves it (with
// anything else on the default mux, e.g. /healthz) on addr.
func StartMetricsEndpoint(addr string) {
	http.Handle("/metrics", promhttp.Handler())
	go func() { _ = http.ListenAndServe(addr, nil) }()
}

func InitializeMetrics() {
	TasksTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: config.PrometheusNamespace,
		Name:      "tasks_total",
		Help:      "The total tasks that have ever been in the system",
	})
	CompletedTasksTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: config.PrometheusNamespace,
		Name:      "completed_tasks_total",
		Help:      "The total tasks that have been completed",
	})
	CanceledTasksTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: config.PrometheusNamespace,
		Name:      "canceled_tasks_total",
		Help:      "The total tasks that have been canceled",
	})
	TimedOutTasksTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: config.PrometheusNamespace,
		Name:      "timed_out_tasks_total",
		Help:      "The total tasks that have been timed out",
	})

	TasksInQueue = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: config.PrometheusNamespace,
		Name:      "tasks_in_queue",
		Help:      "The total tasks that are currently in the queue",
	}, []string{"queue", "current_state"})
}

// StartQueueSizeMetric starts a goroutine that will periodically query the
// database for the number of tasks in each queue and in each state, and update
// the tasks_in_queue metric
func StartQueueSizeMetric(interval time.Duration, queryTimeout time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		for range ticker.C {
			response, err := getQueueAndStateCounts(queryTimeout)
			if err != nil {
				log.Err(err).Msg("failed to get queue and state counts for metrics")
				continue
			}

			for queue, stateCounts := range response.QueueAndStateCounts {
				for state, count := range stateCounts.StateCounts {
					TasksInQueue.With(prometheus.Labels{
						"queue":         queue,
						"current_state": state,
					}).Set(float64(count))
				}
			}
		}
	}()
}

func getQueueAndStateCounts(timeout time.Duration) (*api.GetQueueAndStateCountsResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return store.AppStore.GetQueueAndStateCounts(ctx, &api.GetQueueAndStateCountsRequest{})
}
