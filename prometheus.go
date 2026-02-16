package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// check that PrometheusExporter implements MetricHandler
var _ MetricHandler = (*PrometheusExporter)(nil)
var _ Shutdownable = (*PrometheusExporter)(nil)

// PrometheusConfig represents the configuration for recording Prometheus metrics.
type PrometheusConfig struct {
	// Prefix represents the metric prefix to be used when recording Prometheus metrics. It's a good idea to use value "ecoflow"
	Prefix     string
	ServerPort string
}

type PrometheusExporter struct {
	Config  *PrometheusConfig
	metrics map[string]prometheus.Gauge
	mu      sync.RWMutex
	Server  *http.Server
}

func NewPrometheusExporter(config *PrometheusConfig) *PrometheusExporter {
	slog.Debug("Creating prometheus exporter")

	// Set up HTTP server for Prometheus metrics
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())

	server := &http.Server{
		Addr:    ":" + config.ServerPort,
		Handler: mux,
	}

	go func() {
		// Start the HTTP server
		slog.Debug("Starting HTTP server", "port", config.ServerPort)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("HTTP server ListenAndServe error", "error", err)
		}
	}()
	return &PrometheusExporter{
		Config:  config,
		Server:  server,
		mu:      sync.RWMutex{},
		metrics: make(map[string]prometheus.Gauge),
	}
}

func (p *PrometheusExporter) Handle(_ context.Context, device EcoflowDevice, rawParameters map[string]interface{}) {
	slog.Debug("Handling prometheus metrics for device", "sn", device.SN)
	if device.Online == 0 {
		slog.Info("Device is offline. Setting all metrics to 0", "SN", device.SN, "device name", device.Name)
		p.handleOfflineDevice(device)
		return
	}

	// Create a copy of parameters and add online status (don't mutate input)
	parameters := make(map[string]interface{}, len(rawParameters)+1)
	for k, v := range rawParameters {
		parameters[k] = v
	}
	parameters["online"] = float64(device.Online)

	p.handleMetrics(device, parameters)
}

func (p *PrometheusExporter) handleOfflineDevice(device EcoflowDevice) {
	// Hold read lock while iterating over metrics map
	p.mu.RLock()
	defer p.mu.RUnlock()

	for k, v := range p.metrics {
		if strings.Contains(k, device.SN) {
			v.Set(0)
		}
	}
}

func (p *PrometheusExporter) handleMetrics(device EcoflowDevice, parameters map[string]interface{}) {
	for field, val := range parameters {
		p.handleOneMetric(device, field, val)
	}
}

func (p *PrometheusExporter) handleOneMetric(device EcoflowDevice, field string, val interface{}) {
	metricName, deviceMetricName, err := generateMetricName(field, p.Config.Prefix, device.SN)
	if err != nil {
		slog.Error("Unable to generate metric name", "metric", field)
		return
	}

	// Hold lock for entire check-and-register operation to prevent race condition
	p.mu.Lock()
	gauge, ok := p.metrics[deviceMetricName]
	if !ok {
		slog.Debug("Adding new metric", "metric", metricName, "device", device.SN, "device_name", device.Name)
		gauge = prometheus.NewGauge(prometheus.GaugeOpts{
			Name: metricName,
			ConstLabels: map[string]string{
				"device":        device.Name,
				"serial_number": device.SN,
			},
		})
		prometheus.MustRegister(gauge)
		p.metrics[deviceMetricName] = gauge
	} else {
		slog.Debug("Updating metric", "metric", metricName, "value", val, "device", device.SN, "device_name", device.Name)
	}
	p.mu.Unlock()

	// Check if value is an array
	_, ok = val.([]interface{})
	if ok {
		slog.Debug("The value is an array, skipping it", "metric", metricName)
		return
	}

	// Try to convert to float and set value
	floatVal, ok := val.(float64)
	if ok {
		gauge.Set(floatVal)
	}
}

func (p *PrometheusExporter) Close(ctx context.Context) {
	// Shutdown HTTP server
	slog.Debug("Shutting down HTTP server...")
	if err := p.Server.Shutdown(ctx); err != nil {
		slog.Error("HTTP server shutdown error", "error", err)
	} else {
		slog.Debug("HTTP server gracefully stopped")
	}
}
