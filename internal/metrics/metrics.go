// Package metrics defines Aperture's internal Prometheus metrics.
//
// Metrics are registered on a dedicated registry (not the global default) so
// tests can create isolated instances.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Metrics holds every metric Aperture exports.
type Metrics struct {
	Registry *prometheus.Registry

	// Ingestion (HTTP layer)
	Requests        *prometheus.CounterVec // by HTTP status code
	RequestDuration prometheus.Histogram
	SpansReceived   prometheus.Counter
	SpansRejected   *prometheus.CounterVec // by validation reason
	SpansAccepted   prometheus.Counter     // acknowledged to the client after commit

	// Pipeline
	QueueCapacity prometheus.Gauge
	Overloaded    prometheus.Counter
	BatchSpans    prometheus.Histogram
	BatchJobs     prometheus.Histogram
	FlushDuration prometheus.Histogram
	WriteErrors   prometheus.Counter
	SpansWritten  prometheus.Counter
}

// New creates and registers all metrics on a fresh registry.
func New() *Metrics {
	r := prometheus.NewRegistry()
	r.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	m := &Metrics{
		Registry: r,
		Requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "aperture_ingest_requests_total",
			Help: "OTLP export requests by HTTP status code.",
		}, []string{"code"}),
		RequestDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "aperture_ingest_request_duration_seconds",
			Help:    "End-to-end OTLP export request latency, including the wait for durable commit.",
			Buckets: prometheus.ExponentialBuckets(0.0005, 2, 16),
		}),
		SpansReceived: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "aperture_ingest_spans_received_total",
			Help: "Spans received in successfully decoded export requests.",
		}),
		SpansRejected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "aperture_ingest_spans_rejected_total",
			Help: "Spans rejected by validation, reported to clients via OTLP partial success.",
		}, []string{"reason"}),
		SpansAccepted: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "aperture_ingest_spans_accepted_total",
			Help: "Spans acknowledged to clients after their batch committed to PostgreSQL.",
		}),
		QueueCapacity: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "aperture_pipeline_queue_capacity",
			Help: "Configured pipeline queue capacity, in export requests.",
		}),
		Overloaded: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "aperture_pipeline_overload_rejections_total",
			Help: "Export requests refused because the pipeline queue was full.",
		}),
		BatchSpans: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "aperture_pipeline_batch_spans",
			Help:    "Spans per database write batch.",
			Buckets: prometheus.ExponentialBuckets(1, 2, 14),
		}),
		BatchJobs: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "aperture_pipeline_batch_requests",
			Help:    "Export requests coalesced into each database write batch.",
			Buckets: prometheus.ExponentialBuckets(1, 2, 10),
		}),
		FlushDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "aperture_pipeline_flush_duration_seconds",
			Help:    "Time to write and commit one batch.",
			Buckets: prometheus.ExponentialBuckets(0.0005, 2, 16),
		}),
		WriteErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "aperture_pipeline_write_errors_total",
			Help: "Failed database write attempts.",
		}),
		SpansWritten: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "aperture_pipeline_spans_written_total",
			Help: "Spans in committed write batches (includes duplicates ignored by ON CONFLICT).",
		}),
	}
	r.MustRegister(m.Requests, m.RequestDuration, m.SpansReceived, m.SpansRejected, m.SpansAccepted,
		m.QueueCapacity, m.Overloaded, m.BatchSpans, m.BatchJobs, m.FlushDuration, m.WriteErrors, m.SpansWritten)
	return m
}

// RegisterQueueDepth exposes the live queue depth via a callback.
func (m *Metrics) RegisterQueueDepth(depth func() float64) {
	m.Registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "aperture_pipeline_queue_depth",
		Help: "Export requests currently waiting in the pipeline queue.",
	}, depth))
}
