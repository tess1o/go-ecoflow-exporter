package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	defaultMetricPrefix                = "ecoflow"
	defaultPort                        = "2112"
	defaultRestIntervalSeconds         = 30
	defaultOfflineThresholdSeconds     = 300
	defaultPingIntervalSeconds         = 20
	defaultStateRefreshIntervalSeconds = 300 // 5 minutes
)

var (
	errAccessKeyMandatory     = errors.New("ECOFLOW_ACCESS_KEY and ECOFLOW_SECRET_KEY are mandatory")
	errEmailPasswordMandatory = errors.New("ECOFLOW_EMAIL and ECOFLOW_PASSWORD are mandatory")
	errDevicesMandatory       = errors.New("either ECOFLOW_DEVICES_PRETTY_NAMES or ECOFLOW_DEVICES must be provided")
	errNoCredentials          = errors.New("no EcoFlow credentials: set ECOFLOW_EMAIL and ECOFLOW_PASSWORD (MQTT), " +
		"or ECOFLOW_ACCESS_KEY and ECOFLOW_SECRET_KEY (REST API)")
)

var metricPrefixPattern = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)

// Config holds the whole exporter configuration, read from environment variables
type Config struct {
	MetricPrefix string
	// ExporterType is "rest" or "mqtt"
	ExporterType string
	// Devices maps a serial number to its display name
	Devices map[string]string
	// Port serves /metrics and /healthz
	Port          string
	ProtobufDebug bool

	Rest RestConfig
	Mqtt MqttConfig
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

// env reads configuration values. Empty values are treated as unset, because docker compose
// passes `VAR: ${VAR}` through as an empty string when VAR is not defined
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
		e.errs = append(e.errs, fmt.Errorf("%s must be true or false, got %q", key, val))
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
		e.errs = append(e.errs, fmt.Errorf("%s must be a whole number, got %q", key, val))
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

// port reads a TCP port number
func (e *env) port(key, def string) string {
	val := e.str(key, def)
	if p, err := strconv.Atoi(val); err != nil || p < 1 || p > 65535 {
		e.errs = append(e.errs, fmt.Errorf("%s must be a port number between 1 and 65535, got %q", key, val))
	}
	return val
}

// LoadConfig reads and validates the configuration from environment variables
func LoadConfig(lookup func(string) (string, bool)) (*Config, error) {
	e := newEnv(lookup)

	cfg := &Config{
		MetricPrefix:  e.str("METRIC_PREFIX", defaultMetricPrefix),
		Port:          e.port("PROMETHEUS_PORT", defaultPort),
		ProtobufDebug: e.bool("PROTOBUF_DEBUG"),
	}
	if !metricPrefixPattern.MatchString(cfg.MetricPrefix) {
		e.errs = append(e.errs, fmt.Errorf("METRIC_PREFIX must match %s, got %q", metricPrefixPattern, cfg.MetricPrefix))
	}

	devices, err := parseDeviceMapping(e.str("ECOFLOW_DEVICES_PRETTY_NAMES", ""), e.str("ECOFLOW_DEVICES", ""))
	if err != nil {
		e.errs = append(e.errs, err)
	}
	cfg.Devices = devices

	rest := RestConfig{AccessKey: e.str("ECOFLOW_ACCESS_KEY", ""), SecretKey: e.secret("ECOFLOW_SECRET_KEY")}
	mqtt := MqttConfig{Email: e.str("ECOFLOW_EMAIL", ""), Password: e.secret("ECOFLOW_PASSWORD")}

	cfg.ExporterType = strings.ToLower(e.str("EXPORTER_TYPE", ""))
	if cfg.ExporterType == "" {
		cfg.ExporterType = detectExporterType(rest, mqtt)
	}

	switch cfg.ExporterType {
	case "rest":
		rest.Interval = e.seconds("SCRAPING_INTERVAL", defaultRestIntervalSeconds)
		cfg.Rest = rest
		if rest.AccessKey == "" || rest.SecretKey == "" {
			e.errs = append(e.errs, errAccessKeyMandatory)
		}
	case "mqtt":
		mqtt.OfflineThreshold = e.seconds("MQTT_DEVICE_OFFLINE_THRESHOLD_SECONDS", defaultOfflineThresholdSeconds)
		mqtt.PingInterval = e.seconds("MQTT_PING_INTERVAL_SECONDS", defaultPingIntervalSeconds)
		mqtt.StateRefreshInterval = e.seconds("MQTT_STATE_REFRESH_INTERVAL_SECONDS", defaultStateRefreshIntervalSeconds)
		cfg.Mqtt = mqtt
		if mqtt.Email == "" || mqtt.Password == "" {
			e.errs = append(e.errs, errEmailPasswordMandatory)
		}
		if len(cfg.Devices) == 0 {
			e.errs = append(e.errs, errDevicesMandatory)
		}
	case "":
		e.errs = append(e.errs, errNoCredentials)
	default:
		e.errs = append(e.errs, fmt.Errorf("unknown EXPORTER_TYPE %q, supported types: rest, mqtt", cfg.ExporterType))
	}

	if err := errors.Join(e.errs...); err != nil {
		return nil, err
	}
	return cfg, nil
}

// detectExporterType picks the exporter type from the credentials that are set. REST wins when both are set,
// as it was the default before. An empty result means no credentials at all.
func detectExporterType(rest RestConfig, mqtt MqttConfig) string {
	switch {
	case rest.AccessKey != "" || rest.SecretKey != "":
		return "rest"
	case mqtt.Email != "" || mqtt.Password != "":
		return "mqtt"
	}
	return ""
}

// LogValue describes the effective configuration for the startup log, without credentials
func (c *Config) LogValue() slog.Value {
	devices := make([]string, 0, len(c.Devices))
	for sn, name := range c.Devices {
		devices = append(devices, sn+"="+name)
	}
	slices.Sort(devices)

	attrs := []slog.Attr{
		slog.String("exporter_type", c.ExporterType),
		slog.String("metric_prefix", c.MetricPrefix),
		slog.String("port", c.Port),
		slog.Any("devices", devices),
	}
	switch c.ExporterType {
	case "rest":
		attrs = append(attrs, slog.Duration("interval", c.Rest.Interval))
	case "mqtt":
		attrs = append(attrs,
			slog.Duration("offline_threshold", c.Mqtt.OfflineThreshold),
			slog.Duration("ping_interval", c.Mqtt.PingInterval),
			slog.Duration("state_refresh_interval", c.Mqtt.StateRefreshInterval),
			slog.Bool("protobuf_debug", c.ProtobufDebug))
	}
	return slog.GroupValue(attrs...)
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

// setupLogger configures the log level (LOG_LEVEL) and format (LOG_FORMAT=json)
func setupLogger(lookup func(string) (string, bool)) {
	e := newEnv(lookup)
	level := slog.LevelInfo
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
