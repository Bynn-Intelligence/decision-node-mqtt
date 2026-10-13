package bridge

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Metrics are the bridge's Prometheus series, in a registry of their own.
type Metrics struct {
	Registry  *prometheus.Registry
	messages  *prometheus.CounterVec
	inFlight  prometheus.Gauge
	latency   prometheus.Histogram
	connected prometheus.Gauge
	pubErrors prometheus.Counter
}

// NewMetrics registers the bridge's series and the Go and process ones.
func NewMetrics() *Metrics {
	m := &Metrics{
		Registry: prometheus.NewRegistry(),
		messages: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "decisionnode_mqtt_requests_total",
			Help: "Requests received, by how they ended: answered, api_error, invalid_payload, no_profile, payload_too_large, rate_limited, overloaded.",
		}, []string{"result"}),
		inFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "decisionnode_mqtt_in_flight",
			Help: "Requests being answered now.",
		}),
		latency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "decisionnode_mqtt_decision_seconds",
			Help:    "Time from a request's arrival to its answer being published.",
			Buckets: []float64{0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60},
		}),
		connected: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "decisionnode_mqtt_connected",
			Help: "1 while the bridge is connected to the broker.",
		}),
		pubErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "decisionnode_mqtt_publish_errors_total",
			Help: "Answers that could not be published.",
		}),
	}
	m.Registry.MustRegister(m.messages, m.inFlight, m.latency, m.connected, m.pubErrors,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}
