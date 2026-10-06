// Package obs holds the service's Prometheus metrics and OpenTelemetry tracer setup.
package obs

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Metrics are registered on their own registry, not the global default.
type Metrics struct {
	Reg        *prometheus.Registry
	Requests   *prometheus.HistogramVec // route, outcome
	Stages     *prometheus.HistogramVec // stage
	Inliers    prometheus.Histogram
	QueueDepth prometheus.GaugeFunc
	Inflight   prometheus.GaugeFunc
	Shed       *prometheus.CounterVec // reason
	MapEvents  *prometheus.CounterVec // map, event
	MapsLoaded prometheus.GaugeFunc
	SvcEst     prometheus.GaugeFunc
}

// Gauges are read at scrape time.
type Gauges struct {
	QueueDepth, Inflight, MapsLoaded, ServiceEstimateSec func() float64
}

func NewMetrics(g Gauges) *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	// 1 ms to ~16 s, factor 1.5: covers a fast reject and a slow localize.
	lat := prometheus.ExponentialBuckets(0.001, 1.5, 24)
	m := &Metrics{
		Reg: reg,
		Requests: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "vloc_http_request_duration_seconds", Help: "HTTP request latency by route and outcome.", Buckets: lat,
		}, []string{"route", "outcome"}),
		Stages: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "vloc_stage_duration_seconds", Help: "Per-stage time: queue (admission wait), decode, extract, match, pose (from the core), core (sum), overhead (service time outside the core).", Buckets: lat,
		}, []string{"stage"}),
		Inliers: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "vloc_localize_inliers", Help: "RANSAC inliers of returned poses.", Buckets: prometheus.ExponentialBuckets(4, 2, 10),
		}),
		Shed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "vloc_shed_total", Help: "Requests rejected by admission control, by reason.",
		}, []string{"reason"}),
		MapEvents: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "vloc_map_events_total", Help: "Map lifecycle events: loaded, swapped, freed, load_failed, rejected.",
		}, []string{"map", "event"}),
		QueueDepth: prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "vloc_queue_depth", Help: "Requests waiting for a worker."}, g.QueueDepth),
		Inflight:   prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "vloc_inflight", Help: "Requests running on a worker."}, g.Inflight),
		MapsLoaded: prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "vloc_maps_loaded", Help: "Maps currently loaded."}, g.MapsLoaded),
		SvcEst:     prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "vloc_service_time_estimate_seconds", Help: "EWMA of worker run time used for admission."}, g.ServiceEstimateSec),
	}
	reg.MustRegister(m.Requests, m.Stages, m.Inliers, m.Shed, m.MapEvents, m.QueueDepth, m.Inflight, m.MapsLoaded, m.SvcEst)
	return m
}
