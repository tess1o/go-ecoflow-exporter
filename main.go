package main

import (
	"context"
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

const (
	shutdownTimeout = 10 * time.Second
	// unhealthyExitAfter stops the exporter when it cannot recover by itself, so that the container is
	// restarted (Docker restarts stopped containers, but not running unhealthy ones)
	unhealthyExitAfter = 30 * time.Minute
	watchdogInterval   = time.Minute
)

// version is set at build time with -ldflags "-X main.version=..."
var version = "dev"

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
	slog.Info("Starting the exporter...", "version", version)
	cfg, err := LoadConfig(nil)
	if err != nil {
		return err
	}
	slog.Info("Configuration", "config", cfg)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	telemetry := NewTelemetry(cfg.MetricPrefix, version, prometheus.DefaultRegisterer)
	prom, err := NewPrometheusExporter(&PrometheusConfig{Prefix: cfg.MetricPrefix})
	if err != nil {
		return fmt.Errorf("unable to create prometheus exporter: %w", err)
	}

	exporter, err := createExporter(cfg, telemetry, prom)
	if err != nil {
		return err
	}

	server, err := NewHTTPServer(cfg.Port, prom.Handler())
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		server.Close(closeCtx)
	}()
	server.SetHealthCheck(exporter.Health)

	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	go func() {
		if err := watchdog(runCtx, exporter.Health, unhealthyExitAfter, watchdogInterval); err != nil {
			cancel(err)
		}
	}()

	err = exporter.Run(runCtx)
	if ctx.Err() != nil {
		slog.Info("Received shutdown signal")
		return err
	}
	if cause := context.Cause(runCtx); cause != nil {
		return cause
	}
	return err
}

// watchdog checks the health every interval and returns an error once it has been failing for longer than limit.
// It returns nil when ctx is cancelled.
func watchdog(ctx context.Context, check func() error, limit, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var failingSince time.Time
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		err := check()
		switch {
		case err == nil:
			if !failingSince.IsZero() {
				slog.Info("Exporter is healthy again")
			}
			failingSince = time.Time{}
		case failingSince.IsZero():
			failingSince = time.Now()
			slog.Warn("Exporter is unhealthy", "error", err, "exit_after", limit)
		case time.Since(failingSince) > limit:
			return fmt.Errorf("unhealthy for %s, stopping so that the exporter is restarted: %w",
				time.Since(failingSince).Round(time.Second), err)
		}
	}
}

func createExporter(cfg *Config, telemetry *Telemetry, handlers ...MetricHandler) (Exporter, error) {
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

// runHealthcheck is used by the Docker HEALTHCHECK and queries /healthz
func runHealthcheck() int {
	e := newEnv(nil)
	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + e.str("PROMETHEUS_PORT", defaultPort) + "/healthz")
	if err != nil {
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
