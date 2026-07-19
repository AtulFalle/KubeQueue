// Package runtimemetrics owns KubeQueue's bounded Prometheus collectors.
package runtimemetrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	httpRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kubequeue_http_requests_total",
		Help: "HTTP requests completed by method, route, and status.",
	}, []string{"method", "route", "status"})
	httpDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "kubequeue_http_request_duration_seconds",
		Help:    "HTTP request latency by method and route.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "route"})
	httpInFlight = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "kubequeue_http_requests_in_flight",
		Help: "HTTP requests currently in flight.",
	})
	reconciliationDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "kubequeue_reconciliation_duration_seconds",
		Help:    "Worker reconciliation duration by outcome.",
		Buckets: prometheus.DefBuckets,
	}, []string{"outcome"})
	reconciliationFailures = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "kubequeue_reconciliation_failures_total",
		Help: "Worker reconciliation attempts that failed.",
	})
	queueDepth = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "kubequeue_queue_depth",
		Help: "Current number of queued managed jobs.",
	})
	admissionRejections = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kubequeue_admission_rejections_total",
		Help: "Scheduler admission rejections by bounded reason.",
	}, []string{"reason"})
	leadershipGeneration = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "kubequeue_leadership_generation",
		Help: "Current reconciliation leadership fencing generation.",
	})
	leadershipHeld = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "kubequeue_leadership_held",
		Help: "Whether this worker currently holds reconciliation leadership.",
	})
	workerReady = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "kubequeue_worker_ready",
		Help: "Whether the worker is ready to reconcile.",
	})
	informersSynced = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "kubequeue_worker_informers_synced",
		Help: "Number of namespace authorities with synchronized informers.",
	})
	informersTotal = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "kubequeue_worker_informers_total",
		Help: "Number of namespace authorities expected to synchronize.",
	})
	schemaHealthy = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "kubequeue_schema_healthy",
		Help: "Whether the local process verified a compatible database schema.",
	})
)

func init() {
	prometheus.MustRegister(
		httpRequests, httpDuration, httpInFlight,
		reconciliationDuration, reconciliationFailures,
		queueDepth, admissionRejections,
		leadershipGeneration, leadershipHeld,
		workerReady, informersSynced, informersTotal,
		schemaHealthy,
	)
}

func Handler() http.Handler {
	return promhttp.HandlerFor(
		prometheus.DefaultGatherer,
		promhttp.HandlerOpts{EnableOpenMetrics: true},
	)
}

func ObserveHTTP(method, route string, status int, elapsed time.Duration) {
	httpRequests.WithLabelValues(method, route, strconv.Itoa(status)).Inc()
	httpDuration.WithLabelValues(method, route).Observe(elapsed.Seconds())
}

func HTTPStarted() func() {
	httpInFlight.Inc()
	return httpInFlight.Dec
}

func ObserveReconciliation(elapsed time.Duration, failed bool) {
	outcome := "success"
	if failed {
		outcome = "failure"
		reconciliationFailures.Inc()
	}
	reconciliationDuration.WithLabelValues(outcome).Observe(elapsed.Seconds())
}

func SetQueueDepth(depth int) {
	queueDepth.Set(float64(depth))
}

func RecordAdmissionRejection(reason string) {
	switch reason {
	case "quota", "namespace_limit", "admission_error":
	default:
		reason = "other"
	}
	admissionRejections.WithLabelValues(reason).Inc()
}

func SetLeadership(generation uint64, held bool) {
	leadershipGeneration.Set(float64(generation))
	if held {
		leadershipHeld.Set(1)
		return
	}
	leadershipHeld.Set(0)
}

func SetWorkerReadiness(ready bool, synced, total int) {
	if ready {
		workerReady.Set(1)
	} else {
		workerReady.Set(0)
	}
	informersSynced.Set(float64(synced))
	informersTotal.Set(float64(total))
}

func SetSchemaHealthy(healthy bool) {
	if healthy {
		schemaHealthy.Set(1)
		return
	}
	schemaHealthy.Set(0)
}
