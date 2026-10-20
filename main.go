package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/tess1o/go-ecoflow"
)

const shutdownTimeout = 10 * time.Second

// Exporter collects device parameters and passes them to the metric handlers
type Exporter interface {
	// Run blocks until ctx is cancelled or the exporter cannot continue
	Run(ctx context.Context) error
	// Health returns an error when the exporter is not able to collect data
	Health() error
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(runHealthcheck())
	}

	setupLogger(nil)
	if err := run(); err != nil {
		slog.Error("Exporter failed", "error", err)
		os.Exit(1)
	}
	slog.Info("Application has been stopped")
}

func run() error {
	slog.Info("Starting the exporter...")
	cfg, err := LoadConfig(nil)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var telemetry *Telemetry
	if cfg.Prometheus.Enabled {
		telemetry = NewTelemetry(cfg.MetricPrefix, prometheus.DefaultRegisterer)
	}

	handlers, prom, err := createHandlers(ctx, cfg, telemetry)
	defer closeHandlers(handlers)
	if err != nil {
		return err
	}

	server, err := startHTTPServer(cfg, prom)
	if err != nil {
		return err
	}
	if server != nil {
		defer closeWithTimeout(server)
	}

	exporter, err := createExporter(cfg, telemetry, handlers)
	if err != nil {
		return err
	}
	if server != nil {
		server.SetHealthCheck(func() error { return health(exporter, handlers) })
	}

	err = exporter.Run(ctx)
	if ctx.Err() != nil {
		slog.Info("Received shutdown signal")
	}
	return err
}

// startHTTPServer serves /healthz, and /metrics when Prometheus is enabled. Without Prometheus the server
// only provides the health check, so a busy port is reported but does not stop the exporter.
func startHTTPServer(cfg *Config, prom *PrometheusExporter) (*HTTPServer, error) {
	var metrics http.Handler
	if prom != nil {
		metrics = prom.Handler()
	}
	server, err := NewHTTPServer(cfg.Prometheus.Port, metrics)
	if err != nil {
		if cfg.Prometheus.Enabled {
			return nil, fmt.Errorf("unable to start prometheus exporter: %w", err)
		}
		slog.Warn("Unable to start the health check server", "error", err)
		return nil, nil
	}
	return server, nil
}

// health combines the health of the exporter and the metric handlers
func health(exporter Exporter, handlers []MetricHandler) error {
	errs := []error{exporter.Health()}
	for _, h := range handlers {
		if c, ok := h.(HealthChecker); ok {
			errs = append(errs, c.Health())
		}
	}
	return errors.Join(errs...)
}

// createHandlers creates the enabled metric handlers, each behind its own queue
func createHandlers(ctx context.Context, cfg *Config, telemetry *Telemetry) ([]MetricHandler, *PrometheusExporter, error) {
	var handlers []MetricHandler
	queue := func(name string, h MetricHandler, minWriteInterval time.Duration) {
		var handler MetricHandler = NewQueuedHandler(name, h, QueueOptions{
			Size:           cfg.HandlerQueueSize,
			Timeout:        cfg.HandlerTimeout,
			Telemetry:      telemetry,
			UnhealthyAfter: cfg.HandlerUnhealthyAfter,
		})
		if minWriteInterval > 0 {
			slog.Info("Writes are throttled", "handler", name, "min_write_interval", minWriteInterval.String())
			handler = NewThrottledHandler(handler, minWriteInterval)
		}
		handlers = append(handlers, handler)
	}

	var prom *PrometheusExporter
	if cfg.Prometheus.Enabled {
		var err error
		prom, err = NewPrometheusExporter(&PrometheusConfig{
			Prefix:    cfg.MetricPrefix,
			MetricTTL: cfg.Prometheus.MetricTTL,
		})
		if err != nil {
			return handlers, nil, fmt.Errorf("unable to start prometheus exporter: %w", err)
		}
		queue("prometheus", prom, 0)
	}

	if cfg.Timescale.Enabled {
		timescale, err := NewTimescaleExporter(ctx, &TimescaleExporterConfig{
			Prefix:             cfg.MetricPrefix,
			TimescaleUrl:       cfg.Timescale.Url,
			MigrationSourceUrl: cfg.Timescale.MigrationsUrl,
			CompressionAfter:   cfg.Timescale.CompressionAfter,
			Retention:          cfg.Timescale.Retention,
		})
		if err != nil {
			return handlers, prom, fmt.Errorf("unable to start timescale exporter: %w", err)
		}
		queue("timescale", timescale, cfg.Timescale.MinWriteInterval)
	}

	if cfg.Redis.Enabled {
		queue("redis", NewRedisExporter(&RedisExporterConfig{
			Prefix:      cfg.MetricPrefix,
			RedisConfig: cfg.Redis.Options,
			Retention:   cfg.Redis.Retention,
			TimestampMs: cfg.Redis.TimestampMs,
		}), cfg.Redis.MinWriteInterval)
	}
	return handlers, prom, nil
}

func closeHandlers(handlers []MetricHandler) {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	for _, h := range handlers {
		if s, ok := h.(Shutdownable); ok {
			s.Close(ctx)
		}
	}
}

func closeWithTimeout(s Shutdownable) {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	s.Close(ctx)
}

func createExporter(cfg *Config, telemetry *Telemetry, handlers []MetricHandler) (Exporter, error) {
	switch cfg.ExporterType {
	case "mqtt":
		exporter, err := NewMqttMetricsExporter(MqttExporterConfig{
			Email:                cfg.Mqtt.Email,
			Password:             cfg.Mqtt.Password,
			Devices:              cfg.Devices,
			OfflineThreshold:     cfg.Mqtt.OfflineThreshold,
			PingInterval:         cfg.Mqtt.PingInterval,
			StateRefreshInterval: cfg.Mqtt.StateRefreshInterval,
			ProtobufDebug:        cfg.ProtobufDebug,
			Telemetry:            telemetry,
		}, handlers...)
		if err != nil {
			return nil, fmt.Errorf("unable to start mqtt exporter: %w", err)
		}
		slog.Info("MQTT exporter configuration",
			"offline_threshold", cfg.Mqtt.OfflineThreshold,
			"ping_interval", cfg.Mqtt.PingInterval,
			"state_refresh_interval", cfg.Mqtt.StateRefreshInterval)
		return exporter, nil
	default:
		client := ecoflow.NewEcoflowClient(cfg.Rest.AccessKey, cfg.Rest.SecretKey,
			ecoflow.WithHttpClient(&http.Client{Timeout: restRequestTimeout}))
		return NewRestMetricsExporter(client, RestExporterConfig{
			Interval:  cfg.Rest.Interval,
			Devices:   cfg.Devices,
			Telemetry: telemetry,
		}, handlers...), nil
	}
}

// runHealthcheck is used by the Docker HEALTHCHECK and queries /healthz. Without Prometheus the health
// server is optional: when nothing listens, there is nothing to ask and the check succeeds.
func runHealthcheck() int {
	e := newEnv(nil)
	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + e.str("PROMETHEUS_PORT", defaultMetricsPort) + "/healthz")
	if err != nil {
		if !e.bool("PROMETHEUS_ENABLED") && errors.Is(err, syscall.ECONNREFUSED) {
			return 0
		}
		fmt.Fprintln(os.Stderr, "healthcheck failed:", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "healthcheck failed: status", resp.Status)
		return 1
	}
	return 0
}
