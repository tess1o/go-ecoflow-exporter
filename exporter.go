package main

import (
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/tess1o/go-ecoflow"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"
)

const (
	defaultMetricPrefix = "ecoflow"
	defaultInterval     = 30
)

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
	interval := getIntOrDefault("PROMETHEUS_INTERVAL", defaultInterval)

	// configure prometheus scrap interval and metric prefix
	config := PrometheusConfig{
		Interval: time.Second * time.Duration(interval),
		Prefix:   metricPrefix,
	}

	RecordPrometheusMetrics(client, &config)

	// start server with metrics
	slog.Info("Starting server on port 2112. Metrics are available at http://localhost:2112/metrics")
	http.Handle("/metrics", promhttp.Handler())
	err := http.ListenAndServe(":2112", nil)
	if err != nil {
		slog.Error("Unable to start the server", "error", err)
	}
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
