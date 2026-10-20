package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// check that PrometheusExporter implements MetricHandler
var _ MetricHandler = (*PrometheusExporter)(nil)
var _ prometheus.Collector = (*PrometheusExporter)(nil)

// PrometheusConfig represents the configuration for recording Prometheus metrics.
type PrometheusConfig struct {
	// Prefix represents the metric prefix to be used when recording Prometheus metrics. It's a good idea to use value "ecoflow"
	Prefix string
	// MetricTTL drops device series that were not updated for this long. Zero keeps them forever.
	// The "online" series is refreshed by every update (including offline ones), so it never expires.
	MetricTTL time.Duration
	// Gatherer provides the non-device metrics served on /metrics (Go runtime, exporter telemetry).
	// Defaults to prometheus.DefaultGatherer.
	Gatherer prometheus.Gatherer
}

// promSample is the latest value of one device parameter
type promSample struct {
	sn      string
	device  string
	name    string
	value   float64
	updated time.Time
}

// PrometheusExporter keeps the latest value of every device parameter and exposes them as gauges
// named "<prefix>_<parameter>" with "device" and "serial_number" labels.
type PrometheusExporter struct {
	Config *PrometheusConfig

	handler http.Handler

	mu      sync.RWMutex
	metrics map[string]*promSample // key: <serial number>_<metric name>
	descs   map[string]*prometheus.Desc
}

func NewPrometheusExporter(config *PrometheusConfig) (*PrometheusExporter, error) {
	slog.Debug("Creating prometheus exporter")

	p := &PrometheusExporter{
		Config:  config,
		metrics: make(map[string]*promSample),
		descs:   make(map[string]*prometheus.Desc),
	}

	// device metrics live in their own registry, so names coming from devices can never clash with
	// (or break registration of) the default collectors
	devices := prometheus.NewRegistry()
	if err := devices.Register(p); err != nil {
		return nil, err
	}
	base := config.Gatherer
	if base == nil {
		base = prometheus.DefaultGatherer
	}
	gatherers := prometheus.Gatherers{base, devices}

	p.handler = promhttp.InstrumentMetricHandler(prometheus.DefaultRegisterer,
		promhttp.HandlerFor(gatherers, promhttp.HandlerOpts{
			// one inconsistent series must not fail the whole scrape
			ErrorHandling: promhttp.ContinueOnError,
			ErrorLog:      slog.NewLogLogger(slog.Default().Handler(), slog.LevelWarn),
		}))
	return p, nil
}

// Handler serves the device metrics together with the metrics of Config.Gatherer
func (p *PrometheusExporter) Handler() http.Handler {
	return p.handler
}

func (p *PrometheusExporter) Handle(_ context.Context, u *DeviceUpdate) error {
	device := u.Device
	slog.Debug("Handling prometheus metrics for device", "sn", device.SN)
	samples := u.Samples(p.Config.Prefix)

	p.mu.Lock()
	defer p.mu.Unlock()

	if device.Online == 0 {
		slog.Info("Device is offline. Setting all metrics to 0", "SN", device.SN, "device name", device.Name)
		for _, s := range p.metrics {
			if s.sn == device.SN {
				s.value = 0
			}
		}
	} else {
		for _, s := range samples {
			if !s.Numeric {
				slog.Debug("The value is not a number, skipping it", "metric", s.Param, "device", device.SN)
				continue
			}
			p.setLocked(device, s.Param, s.Name, s.Value, u.Time)
		}
	}
	// online is always reported, also for devices that were never seen online
	onlineName, _, _ := generateMetricName("online", p.Config.Prefix, device.SN)
	p.setLocked(device, "online", onlineName, float64(device.Online), u.Time)
	return nil
}

func (p *PrometheusExporter) setLocked(device EcoflowDevice, param, metricName string, value float64, updated time.Time) {
	deviceMetricName := device.SN + "_" + metricName
	s, ok := p.metrics[deviceMetricName]
	if !ok {
		slog.Debug("Adding new metric", "metric", metricName, "device", device.SN, "device_name", device.Name)
		if _, exists := p.descs[metricName]; !exists {
			p.descs[metricName] = prometheus.NewDesc(metricName,
				fmt.Sprintf("EcoFlow device parameter %q.", param),
				[]string{"device", "serial_number"}, nil)
		}
		s = &promSample{sn: device.SN, device: device.Name, name: metricName}
		p.metrics[deviceMetricName] = s
	}
	s.value = value
	s.updated = updated
}

// Describe sends no descriptors: the set of metrics depends on what devices report,
// so the collector is registered as unchecked
func (p *PrometheusExporter) Describe(chan<- *prometheus.Desc) {}

func (p *PrometheusExporter) Collect(ch chan<- prometheus.Metric) {
	p.pruneExpired()

	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, s := range p.metrics {
		desc := p.descs[s.name]
		m, err := prometheus.NewConstMetric(desc, prometheus.GaugeValue, s.value, s.device, s.sn)
		if err != nil {
			m = prometheus.NewInvalidMetric(desc, err)
		}
		ch <- m
	}
}

func (p *PrometheusExporter) pruneExpired() {
	if p.Config.MetricTTL <= 0 {
		return
	}
	deadline := time.Now().Add(-p.Config.MetricTTL)

	p.mu.Lock()
	defer p.mu.Unlock()
	for key, s := range p.metrics {
		if s.updated.Before(deadline) {
			slog.Debug("Dropping stale metric", "metric", s.name, "device", s.sn)
			delete(p.metrics, key)
		}
	}
}
