package main

import (
	"context"
	"github.com/tess1o/go-ecoflow"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

const (
	defaultMetricPrefix = "ecoflow"
	defaultInterval     = 30
	defaultMetricsPort  = "2112"
)

const (
	timescaleDbSource = "file://timescaledb/migration"
)

type Shutdownable interface {
	Close(ctx context.Context)
}

func main() {
	setLoggerLevel()

	accessKey := os.Getenv("ECOFLOW_ACCESS_KEY")
	secretKey := os.Getenv("ECOFLOW_SECRET_KEY")

	if accessKey == "" || secretKey == "" {
		slog.Error("ECOFLOW_ACCESS_KEY and ECOFLOW_SECRET_KEY are mandatory")
		return
	}

	client := ecoflow.NewEcoflowClient(accessKey, secretKey)
	metricPrefix := getStringOrDefault("METRIC_PREFIX", defaultMetricPrefix)

	var handlers []MetricHandler

	handlers = enablePrometheus(metricPrefix, handlers)
	handlers = enableTimescaleDb(metricPrefix, handlers)

	done := make(chan bool, 1)

	setupGracefulShutdown(handlers, done)

	interval := getIntOrDefault("SCRAPING_INTERVAL", defaultInterval)
	exporter := NewMetricsExporter(client, time.Duration(interval)*time.Second, handlers...)
	go exporter.ExportMetrics()

	<-done
	slog.Info("Application has been stopped")
}

func enableTimescaleDb(metricPrefix string, handlers []MetricHandler) []MetricHandler {
	if isOptionEnabled("TIMESCALE_ENABLED") {
		timescaleUrl := os.Getenv("TIMESCALE_URL")
		if timescaleUrl == "" {
			log.Fatal("TIMESCALE_URL is mandatory if TIMESCALE_ENABLED is true")
		}

		timescaleExporter := NewTimescaleExporter(&TimescaleExporterConfig{
			Prefix:             metricPrefix,
			TimescaleUrl:       timescaleUrl,
			MigrationSourceUrl: timescaleDbSource,
		})

		handlers = append(handlers, timescaleExporter)
	}
	return handlers
}

func enablePrometheus(metricPrefix string, handlers []MetricHandler) []MetricHandler {
	if isOptionEnabled("PROMETHEUS_ENABLED") {
		port := getStringOrDefault("PROMETHEUS_PORT", defaultMetricsPort)
		promExp := NewPrometheusExporter(&PrometheusConfig{
			Prefix:     metricPrefix,
			ServerPort: port,
		})
		handlers = append(handlers, promExp)
	}
	return handlers
}

func setupGracefulShutdown(handlers []MetricHandler, done chan bool) {
	// Create a channel to listen for OS signals
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)

	// Goroutine to handle shutdown
	go func() {
		<-signalChan
		slog.Info("Received shutdown signal")

		// Create a context with a timeout to allow outstanding requests to complete
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		for _, shutdownable := range handlers {
			s, ok := shutdownable.(Shutdownable)
			if ok {
				s.Close(ctx)
			}
		}
		done <- true
	}()
}

func setLoggerLevel() {
	debugEnabled := os.Getenv("DEBUG_ENABLED")
	if debugEnabled == "true" || debugEnabled == "1" {
		slog.SetLogLoggerLevel(slog.LevelDebug)
	} else {
		slog.SetLogLoggerLevel(slog.LevelInfo)
	}
}

func getStringOrDefault(key, def string) string {
	val, exists := os.LookupEnv(key)
	if exists {
		return val
	}
	return def
}

func getIntOrDefault(key string, def int) int {
	val, exists := os.LookupEnv(key)
	if exists {
		intVal, err := strconv.Atoi(val)
		if err != nil {
			return def
		}
		return intVal
	}
	return def
}

func isOptionEnabled(key string) bool {
	val, exists := os.LookupEnv(key)
	if exists && (val == "1" || val == "true") {
		return true
	}
	return false
}
