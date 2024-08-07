package main

import (
	"context"
	"fmt"
	"github.com/go-redis/redis/v8"
	"log/slog"
	"strings"
	"time"
)

// check that RedisExporter implements MetricHandler
var _ MetricHandler = (*RedisExporter)(nil)

type RedisExporterConfig struct {
	Prefix      string
	RedisConfig *redis.Options
}

type RedisExporter struct {
	prefix string
	client *redis.Client
}

func NewRedisExporter(config *RedisExporterConfig) *RedisExporter {
	slog.Info("Creating redis exporter...")

	client := redis.NewClient(config.RedisConfig)

	return &RedisExporter{
		prefix: config.Prefix,
		client: client,
	}
}

func (r *RedisExporter) Handle(ctx context.Context, device EcoflowDevice, rawParameters map[string]interface{}) {
	if device.Online == 0 {
		slog.Info("Device is offline. Setting all metrics to 0", "SN", device.SN)
		rawParameters = r.handleOfflineDevice(rawParameters, device)
	}
	rawParameters["online"] = float64(device.Online)
	r.handleTimeScaleMetrics(ctx, rawParameters, device)
}

func (r *RedisExporter) handleOfflineDevice(metrics map[string]interface{}, dev EcoflowDevice) map[string]interface{} {
	for k := range metrics {
		if strings.Contains(k, dev.SN) {
			metrics[k] = 0
		}
	}
	return metrics
}

func (r *RedisExporter) handleTimeScaleMetrics(ctx context.Context, metrics map[string]interface{}, dev EcoflowDevice) {
	slog.Info("Handling metrics for device", "dev", dev.SN)
	timestamp := time.Now().Unix()
	pipe := r.client.Pipeline()
	for field, val := range metrics {
		metricName, _, err := generateMetricName(field, r.prefix, dev.SN)
		if err != nil {
			slog.Error("Unable to generate metric name", "metric", field)
			continue
		}
		slog.Debug("Updating metric", "metric", metricName, "value", val, "device", dev.SN)
		_, ok := val.([]interface{})
		if ok {
			slog.Debug("The value is an array, skipping it", "metric", metricName)
			continue
		}
		floatVal, ok := val.(float64)
		if ok {
			tsKey := fmt.Sprintf("ts:%s:%s", dev.SN, metricName)
			pipe.Do(ctx, "TS.ADD", tsKey, timestamp, floatVal)
		} else {
			slog.Error("Unable to convert value to float, skipping metric", "value", val, "metric", metricName)
		}
	}

	// adding device and last
	key := fmt.Sprintf("device:last_access:%s", dev.SN)
	pipe.Set(ctx, key, timestamp, 0)

	_, err := pipe.Exec(ctx)

	if err != nil {
		slog.Error("Unable to insert metrics", "redis_error", err)
	} else {
		slog.Debug("Inserted metrics", "device", dev.SN)
	}
}
