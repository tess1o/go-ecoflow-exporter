package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// check that RedisExporter implements MetricHandler
var _ MetricHandler = (*RedisExporter)(nil)
var _ Shutdownable = (*RedisExporter)(nil)

type RedisExporterConfig struct {
	Prefix      string
	RedisConfig *redis.Options
	// Retention is set on time series when they are created. Zero keeps samples forever.
	Retention time.Duration
	// TimestampMs stores samples with millisecond timestamps, as RedisTimeSeries expects.
	// The default (seconds) is kept for existing setups.
	TimestampMs bool
}

// RedisExporter writes every numeric parameter to a RedisTimeSeries key "ts:<serial number>:<metric name>"
// and the time of the last update to "device:last_access:<serial number>"
type RedisExporter struct {
	config *RedisExporterConfig
	client *redis.Client
}

func NewRedisExporter(config *RedisExporterConfig) *RedisExporter {
	slog.Info("Creating redis exporter...")
	return &RedisExporter{
		config: config,
		client: redis.NewClient(config.RedisConfig),
	}
}

func (r *RedisExporter) Handle(ctx context.Context, u *DeviceUpdate) error {
	device := u.Device
	if device.Online == 0 {
		slog.Info("Device is offline", "SN", device.SN)
	}
	slog.Debug("Handling metrics for device", "dev", device.SN)

	timestamp := u.Time.Unix()
	if r.config.TimestampMs {
		timestamp = u.Time.UnixMilli()
	}
	pipe := r.client.Pipeline()
	add := func(metricName string, value float64) {
		slog.Debug("Updating metric", "metric", metricName, "value", value, "device", device.SN)
		args := []any{"TS.ADD", fmt.Sprintf("ts:%s:%s", device.SN, metricName), timestamp, value}
		if r.config.Retention > 0 {
			args = append(args, "RETENTION", r.config.Retention.Milliseconds())
		}
		// several MQTT updates can arrive within one timestamp, the last one wins
		args = append(args, "ON_DUPLICATE", "LAST")
		// labels are only used when the key is created, they allow TS.MRANGE ... FILTER device=<name>
		args = append(args, "LABELS", "serial_number", device.SN, "metric", metricName)
		if device.Name != "" {
			args = append(args, "device", device.Name)
		}
		pipe.Do(ctx, args...)
	}

	for _, s := range u.Samples(r.config.Prefix) {
		if s.Param == "online" {
			continue
		}
		if !s.Numeric {
			slog.Debug("The value is not a number, skipping it", "metric", s.Param, "device", device.SN)
			continue
		}
		add(s.Name, s.Value)
	}
	onlineName, _, _ := generateMetricName("online", r.config.Prefix, device.SN)
	add(onlineName, float64(device.Online))
	pipe.Set(ctx, fmt.Sprintf("device:last_access:%s", device.SN), u.Time.Unix(), 0)

	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("unable to insert metrics: %w", err)
	}
	slog.Debug("Inserted metrics", "device", device.SN)
	return nil
}

func (r *RedisExporter) Close(_ context.Context) {
	if err := r.client.Close(); err != nil {
		slog.Error("Unable to close redis client", "error", err)
	}
}
