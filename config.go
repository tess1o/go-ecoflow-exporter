package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// generic
const (
	defaultMetricPrefix = "ecoflow"
	defaultInterval     = 30
	defaultExporterType = "rest"
)

// prometheus
const (
	defaultMetricsPort             = "2112"
	defaultOfflineThresholdSeconds = 60
)

// mqtt
const (
	defaultPingIntervalSeconds         = 20
	defaultStateRefreshIntervalSeconds = 300 // 5 minutes
)

// redis
const (
	defaultRedisUrl = "localhost:6379"
	defaultRedisDb  = 0
)

// handlers
const (
	defaultHandlerQueueSize      = 256
	defaultHandlerTimeoutSeconds = 30
)

var (
	errAccessKeyMandatory     = errors.New("ECOFLOW_ACCESS_KEY and ECOFLOW_SECRET_KEY are mandatory")
	errEmailPasswordMandatory = errors.New("ECOFLOW_EMAIL and ECOFLOW_PASSWORD are mandatory")
	errDevicesMandatory       = errors.New("either ECOFLOW_DEVICES_PRETTY_NAMES or ECOFLOW_DEVICES must be provided")
)

// Config holds the whole exporter configuration, read from environment variables
type Config struct {
	MetricPrefix string
	ExporterType string
	// Devices maps a serial number to its display name
	Devices map[string]string

	HandlerQueueSize int
	HandlerTimeout   time.Duration
	// HandlerUnhealthyAfter makes /healthz fail when a handler keeps failing for this long. Zero disables it.
	HandlerUnhealthyAfter time.Duration

	ProtobufDebug bool

	Rest       RestConfig
	Mqtt       MqttConfig
	Prometheus PrometheusEnvConfig
	Timescale  TimescaleEnvConfig
	Redis      RedisEnvConfig
}

type RestConfig struct {
	AccessKey string
	SecretKey string
	Interval  time.Duration
}

type MqttConfig struct {
	Email                string
	Password             string
	OfflineThreshold     time.Duration
	PingInterval         time.Duration
	StateRefreshInterval time.Duration
}

type PrometheusEnvConfig struct {
	Enabled   bool
	Port      string
	MetricTTL time.Duration
}

type TimescaleEnvConfig struct {
	Enabled          bool
	Url              string
	MigrationsUrl    string
	CompressionAfter time.Duration
	Retention        time.Duration
	MinWriteInterval time.Duration
}

type RedisEnvConfig struct {
	Enabled          bool
	Options          *redis.Options
	Retention        time.Duration
	MinWriteInterval time.Duration
	TimestampMs      bool
}

// env reads configuration values. Empty values are treated as unset, because docker compose
// passes `VAR: ${VAR}` through as an empty string when VAR is missing from .env
type env struct {
	lookup func(string) (string, bool)
	errs   []error
}

func newEnv(lookup func(string) (string, bool)) *env {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	return &env{lookup: lookup}
}

func (e *env) raw(key string) (string, bool) {
	val, ok := e.lookup(key)
	val = strings.TrimSpace(val)
	return val, ok && val != ""
}

func (e *env) str(key, def string) string {
	if val, ok := e.raw(key); ok {
		return val
	}
	return def
}

func (e *env) bool(key string) bool {
	val, ok := e.raw(key)
	if !ok {
		return false
	}
	b, err := strconv.ParseBool(val)
	if err != nil {
		slog.Warn("Invalid boolean value, treating it as false", "variable", key, "value", val)
		return false
	}
	return b
}

func (e *env) int(key string, def int) int {
	val, ok := e.raw(key)
	if !ok {
		return def
	}
	i, err := strconv.Atoi(val)
	if err != nil {
		slog.Warn("Invalid integer value, using default", "variable", key, "value", val, "default", def)
		return def
	}
	return i
}

// seconds reads a positive number of seconds
func (e *env) seconds(key string, def int) time.Duration {
	s := e.int(key, def)
	if s <= 0 {
		e.errs = append(e.errs, fmt.Errorf("%s must be a positive number of seconds, got %d", key, s))
	}
	return time.Duration(s) * time.Second
}

// optionalDuration reads a non-negative number of units. Zero (the default) means the feature is disabled.
func (e *env) optionalDuration(key string, unit time.Duration) time.Duration {
	n := e.int(key, 0)
	if n < 0 {
		e.errs = append(e.errs, fmt.Errorf("%s must not be negative, got %d", key, n))
		return 0
	}
	return time.Duration(n) * unit
}

// secret reads key, or the contents of the file named by key_FILE (Docker secrets). key wins when both are set.
func (e *env) secret(key string) string {
	if val, ok := e.raw(key); ok {
		return val
	}
	path, ok := e.raw(key + "_FILE")
	if !ok {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("cannot read %s_FILE: %w", key, err))
		return ""
	}
	return strings.TrimSpace(string(data))
}

// LoadConfig reads and validates the configuration from environment variables
func LoadConfig(lookup func(string) (string, bool)) (*Config, error) {
	e := newEnv(lookup)

	cfg := &Config{
		// an explicitly empty prefix is honored (it was before), so it is not read through e.str
		MetricPrefix:     defaultMetricPrefix,
		ExporterType:     strings.ToLower(e.str("EXPORTER_TYPE", defaultExporterType)),
		HandlerQueueSize: e.int("HANDLER_QUEUE_SIZE", defaultHandlerQueueSize),
		HandlerTimeout:   e.seconds("HANDLER_TIMEOUT_SECONDS", defaultHandlerTimeoutSeconds),

		HandlerUnhealthyAfter: e.optionalDuration("HANDLER_UNHEALTHY_AFTER_SECONDS", time.Second),
		ProtobufDebug:         e.bool("PROTOBUF_DEBUG"),
	}
	if prefix, ok := e.lookup("METRIC_PREFIX"); ok {
		cfg.MetricPrefix = strings.TrimSpace(prefix)
	}
	if cfg.HandlerQueueSize <= 0 {
		e.errs = append(e.errs, fmt.Errorf("HANDLER_QUEUE_SIZE must be positive, got %d", cfg.HandlerQueueSize))
	}

	devices, err := parseDeviceMapping(e.str("ECOFLOW_DEVICES_PRETTY_NAMES", ""), e.str("ECOFLOW_DEVICES", ""))
	if err != nil {
		e.errs = append(e.errs, err)
	}
	cfg.Devices = devices

	switch cfg.ExporterType {
	case "rest":
		cfg.Rest = RestConfig{
			AccessKey: e.str("ECOFLOW_ACCESS_KEY", ""),
			SecretKey: e.secret("ECOFLOW_SECRET_KEY"),
			Interval:  e.seconds("SCRAPING_INTERVAL", defaultInterval),
		}
		if cfg.Rest.AccessKey == "" || cfg.Rest.SecretKey == "" {
			e.errs = append(e.errs, errAccessKeyMandatory)
		}
	case "mqtt":
		cfg.Mqtt = MqttConfig{
			Email:                e.str("ECOFLOW_EMAIL", ""),
			Password:             e.secret("ECOFLOW_PASSWORD"),
			OfflineThreshold:     e.seconds("MQTT_DEVICE_OFFLINE_THRESHOLD_SECONDS", defaultOfflineThresholdSeconds),
			PingInterval:         e.seconds("MQTT_PING_INTERVAL_SECONDS", defaultPingIntervalSeconds),
			StateRefreshInterval: e.seconds("MQTT_STATE_REFRESH_INTERVAL_SECONDS", defaultStateRefreshIntervalSeconds),
		}
		if cfg.Mqtt.Email == "" || cfg.Mqtt.Password == "" {
			e.errs = append(e.errs, errEmailPasswordMandatory)
		}
		if len(cfg.Devices) == 0 {
			e.errs = append(e.errs, errDevicesMandatory)
		}
	default:
		e.errs = append(e.errs, fmt.Errorf("unknown EXPORTER_TYPE %q, supported types: rest, mqtt", cfg.ExporterType))
	}

	cfg.Prometheus = PrometheusEnvConfig{
		Enabled: e.bool("PROMETHEUS_ENABLED"),
		Port:    e.str("PROMETHEUS_PORT", defaultMetricsPort),
	}
	if ttl := e.int("PROMETHEUS_METRIC_TTL_SECONDS", 0); ttl > 0 {
		cfg.Prometheus.MetricTTL = time.Duration(ttl) * time.Second
	}

	cfg.Timescale = TimescaleEnvConfig{
		Enabled:          e.bool("TIMESCALE_ENABLED"),
		Url:              e.secret("TIMESCALE_URL"),
		MigrationsUrl:    e.str("TIMESCALE_MIGRATIONS_URL", ""),
		CompressionAfter: e.optionalDuration("TIMESCALE_COMPRESSION_AFTER_DAYS", 24*time.Hour),
		Retention:        e.optionalDuration("TIMESCALE_RETENTION_DAYS", 24*time.Hour),
		MinWriteInterval: e.optionalDuration("TIMESCALE_MIN_WRITE_INTERVAL_SECONDS", time.Second),
	}
	if cfg.Timescale.Enabled && cfg.Timescale.Url == "" {
		e.errs = append(e.errs, errors.New("TIMESCALE_URL is mandatory if TIMESCALE_ENABLED is true"))
	}

	cfg.Redis = RedisEnvConfig{Enabled: e.bool("REDIS_ENABLED")}
	if cfg.Redis.Enabled {
		cfg.Redis.Options = redisOptions(e)
		cfg.Redis.Retention = e.optionalDuration("REDIS_RETENTION_DAYS", 24*time.Hour)
		cfg.Redis.MinWriteInterval = e.optionalDuration("REDIS_MIN_WRITE_INTERVAL_SECONDS", time.Second)
		cfg.Redis.TimestampMs = e.bool("REDIS_TIMESTAMP_MS")
	}

	if !cfg.Prometheus.Enabled && !cfg.Timescale.Enabled && !cfg.Redis.Enabled {
		e.errs = append(e.errs, errors.New("no metric handlers enabled. Make sure at least one metric handler is enabled: "+
			"PROMETHEUS_ENABLED=true or TIMESCALE_ENABLED=true or REDIS_ENABLED=true"))
	}

	if err := errors.Join(e.errs...); err != nil {
		return nil, err
	}
	return cfg, nil
}

// redisOptions reads the Redis connection settings. REDIS_URL is either "host:port" or a URL
// ("redis://user:password@host:6379/0", "rediss://..." for TLS). REDIS_USER, REDIS_PASSWORD and REDIS_DB
// apply when the URL does not specify them.
func redisOptions(e *env) *redis.Options {
	addr := e.str("REDIS_URL", defaultRedisUrl)
	opts := &redis.Options{Addr: addr}
	if strings.HasPrefix(addr, "redis://") || strings.HasPrefix(addr, "rediss://") {
		parsed, err := redis.ParseURL(addr)
		if err != nil {
			e.errs = append(e.errs, fmt.Errorf("invalid REDIS_URL: %w", err))
		} else {
			opts = parsed
		}
	}
	if opts.Username == "" {
		opts.Username = e.str("REDIS_USER", "")
	}
	if opts.Password == "" {
		opts.Password = e.secret("REDIS_PASSWORD")
	}
	if opts.DB == 0 {
		opts.DB = e.int("REDIS_DB", defaultRedisDb)
	}
	// older Redis versions reject CLIENT SETINFO, which go-redis v9 sends by default
	opts.DisableIdentity = true
	return opts
}

// parseDeviceMapping builds the device mapping, where the key is a device serial number and the value is
// either the pretty name (specified in ECOFLOW_DEVICES_PRETTY_NAMES) or SN itself taken from ECOFLOW_DEVICES
func parseDeviceMapping(prettyNames, devices string) (map[string]string, error) {
	mapping := make(map[string]string)
	if prettyNames != "" {
		var parsed map[string]string
		if err := json.Unmarshal([]byte(prettyNames), &parsed); err != nil {
			return nil, fmt.Errorf("unable to parse device mapping, make sure it has format {\"R33XXXXXXXXX\":\"My Delta 2\", \"R33YYYYY\":\"Delta Pro backup\"}. Original error: %w", err)
		}
		for sn, name := range parsed {
			sn = strings.TrimSpace(sn)
			if sn == "" {
				continue
			}
			mapping[sn] = name
		}
	}

	for _, sn := range strings.Split(devices, ",") {
		sn = strings.TrimSpace(sn)
		if sn == "" {
			continue
		}
		if _, exists := mapping[sn]; !exists {
			mapping[sn] = sn
		}
	}
	return mapping, nil
}

// setupLogger configures the log level (LOG_LEVEL, or DEBUG_ENABLED for debug) and format (LOG_FORMAT=json)
func setupLogger(lookup func(string) (string, bool)) {
	e := newEnv(lookup)
	level := slog.LevelInfo
	if e.bool("DEBUG_ENABLED") {
		level = slog.LevelDebug
	}
	if val, ok := e.raw("LOG_LEVEL"); ok {
		if err := level.UnmarshalText([]byte(val)); err != nil {
			slog.Warn("Invalid LOG_LEVEL, supported values: debug, info, warn, error", "value", val)
		}
	}

	switch format, _ := e.raw("LOG_FORMAT"); strings.ToLower(format) {
	case "json":
		slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
	case "", "text":
		slog.SetLogLoggerLevel(level)
	default:
		slog.SetLogLoggerLevel(level)
		slog.Warn("Invalid LOG_FORMAT, supported values: text, json", "value", format)
	}
}
