package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode"
)

type EcoflowDevice struct {
	SN     string `json:"sn"`
	Name   string `json:"name"`
	Online int    `json:"online"`
}

// DeviceUpdate is one set of device parameters passed from an exporter to the metric handlers.
// The same update is shared by all handlers, so it must be treated as read-only.
type DeviceUpdate struct {
	Device EcoflowDevice
	// Time is when the parameters were received from EcoFlow
	Time   time.Time
	Params map[string]any

	samplesMu sync.Mutex
	samples   map[string][]Sample // by metric prefix
}

func NewDeviceUpdate(device EcoflowDevice, params map[string]any) *DeviceUpdate {
	return &DeviceUpdate{Device: device, Time: time.Now(), Params: params}
}

// Sample is one device parameter together with its metric name
type Sample struct {
	Param string // EcoFlow parameter name, for example "pd.wireUsedTime"
	Name  string // metric name, for example "ecoflow_pd_wire_used_time"
	Raw   any
	// Value is the numeric value of Raw, valid when Numeric is true
	Value   float64
	Numeric bool
}

// Samples returns the parameters with their metric names ("<prefix>_<parameter>"). They are computed once
// per prefix and shared by all handlers. Parameters whose name cannot be converted are skipped.
func (u *DeviceUpdate) Samples(prefix string) []Sample {
	u.samplesMu.Lock()
	defer u.samplesMu.Unlock()
	if s, ok := u.samples[prefix]; ok {
		return s
	}
	samples := make([]Sample, 0, len(u.Params))
	for param, raw := range u.Params {
		name, _, err := generateMetricName(param, prefix, u.Device.SN)
		if err != nil {
			slog.Error("Unable to generate metric name", "metric", param, "error", err)
			continue
		}
		value, numeric := toFloat(raw)
		samples = append(samples, Sample{Param: param, Name: name, Raw: raw, Value: value, Numeric: numeric})
	}
	if u.samples == nil {
		u.samples = make(map[string][]Sample)
	}
	u.samples[prefix] = samples
	return samples
}

// MetricHandler receives device updates and stores or exposes them
type MetricHandler interface {
	Handle(ctx context.Context, update *DeviceUpdate) error
}

// dispatch passes the update to every handler
func dispatch(ctx context.Context, handlers []MetricHandler, u *DeviceUpdate) {
	for _, h := range handlers {
		if err := h.Handle(ctx, u); err != nil {
			slog.Error("Metric handler failed", "device", u.Device.SN, "error", err)
		}
	}
}

type Shutdownable interface {
	Close(ctx context.Context)
}

// HealthChecker is implemented by components that can report that they are not working
type HealthChecker interface {
	Health() error
}

// generateMetricName takes a rawMetric string (for example "pd.wireUsedTime") received from Ecoflow Rest API.
// the function returns two values: metricName and deviceMetric name.
// metricName is used as a metric name in prometheus
// deviceMetricName is used as unique key from all metrics (device serial number + metric name). It's required to store all metrics in a map.
// The function converts rawMetric to prometheus compatible value (pd_wire_used_time)
// As a result it returns two values:
// metricName: prefix + prometheus compatible value (ecoflow_pd_wire_used_time)
// deviceMetricName: device serial number + metricName (R13124123123213_ecoflow_pd_wire_used_time)
// Example:
// rawMetric: pd.wireUsedTime
// prefix: ecoflow
// deviceSn: R13124123123213
//
// return values:
// metricName: ecoflow_pd_wire_used_time
// deviceMetricName: R13124123123213_ecoflow_pd_wire_used_time
func generateMetricName(rawMetric string, prefix string, deviceSn string) (string, string, error) {
	prometheusName, err := ecoflowParamToPrometheusMetric(rawMetric)
	if err != nil {
		return "", "", err
	}
	metricName := prefix + "_" + prometheusName
	deviceMetricName := deviceSn + "_" + metricName
	return metricName, deviceMetricName, nil
}

// ecoflowParamToPrometheusMetric converts an Ecoflow parameter name to a Prometheus compatible name.
// Dots are replaced with underscores, and every uppercase letter is lowercased and prefixed with an
// underscore unless the previous character already is one ("pd.wireUsedTime" -> "pd_wire_used_time").
// Any other character outside [a-zA-Z0-9_:] is replaced with an underscore, so the prefixed result
// always matches the Prometheus data model pattern [a-zA-Z_:][a-zA-Z0-9_:]*.
func ecoflowParamToPrometheusMetric(metricKey string) (string, error) {
	if metricKey == "" {
		return "", fmt.Errorf("ecoflow parameter `%s` can't be converted to prometheus name", metricKey)
	}

	var b strings.Builder
	b.Grow(len(metricKey) + 8)
	lastUnderscore := false
	for i, r := range metricKey {
		switch {
		case unicode.IsUpper(r):
			if i > 0 && !lastUnderscore {
				b.WriteByte('_')
			}
			r = unicode.ToLower(r)
		case r == '.':
			r = '_'
		}
		if !isMetricNameRune(r) {
			r = '_'
		}
		b.WriteRune(r)
		lastUnderscore = r == '_'
	}

	// a leading digit is fine: generateMetricName always puts "<prefix>_" in front
	return b.String(), nil
}

func isMetricNameRune(r rune) bool {
	return r == '_' || r == ':' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

// toFloat converts a parameter value to a float when it represents a number.
// Booleans are reported as 1/0; strings, arrays, objects and nil are not numbers.
func toFloat(val any) (float64, bool) {
	switch v := val.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case uint32:
		return float64(v), true
	case uint64:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	case bool:
		if v {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

func getDeviceName(mapping map[string]string, sn string) string {
	if name, exists := mapping[sn]; exists {
		return name
	}
	return sn
}
