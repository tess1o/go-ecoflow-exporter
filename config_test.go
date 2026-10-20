package main

import (
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func lookupFrom(vars map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := vars[k]; return v, ok }
}

func withVars(base, extra map[string]string) map[string]string {
	vars := maps.Clone(base)
	maps.Copy(vars, extra)
	return vars
}

var (
	restVars = map[string]string{"ECOFLOW_ACCESS_KEY": "a", "ECOFLOW_SECRET_KEY": "s"}
	mqttVars = map[string]string{"ECOFLOW_EMAIL": "e", "ECOFLOW_PASSWORD": "p", "ECOFLOW_DEVICES": "SN1"}
)

func TestLoadConfigDefaults(t *testing.T) {
	t.Run("rest", func(t *testing.T) {
		cfg, err := LoadConfig(lookupFrom(restVars))
		require.NoError(t, err)
		assert.Equal(t, "rest", cfg.ExporterType, "detected from the access keys")
		assert.Equal(t, "ecoflow", cfg.MetricPrefix)
		assert.Equal(t, "2112", cfg.Port)
		assert.Equal(t, 30*time.Second, cfg.Rest.Interval)
		assert.False(t, cfg.ProtobufDebug)
		assert.Empty(t, cfg.Devices, "devices are optional for the REST API")
	})

	t.Run("mqtt", func(t *testing.T) {
		cfg, err := LoadConfig(lookupFrom(mqttVars))
		require.NoError(t, err)
		assert.Equal(t, "mqtt", cfg.ExporterType, "detected from email and password")
		assert.Equal(t, MqttConfig{
			Email:                "e",
			Password:             "p",
			OfflineThreshold:     300 * time.Second,
			PingInterval:         20 * time.Second,
			StateRefreshInterval: 300 * time.Second,
		}, cfg.Mqtt)
	})

	t.Run("empty compose variables are unset", func(t *testing.T) {
		cfg, err := LoadConfig(lookupFrom(withVars(mqttVars, map[string]string{
			"EXPORTER_TYPE": "", "METRIC_PREFIX": "", "PROMETHEUS_PORT": " ", "PROTOBUF_DEBUG": "",
			"MQTT_DEVICE_OFFLINE_THRESHOLD_SECONDS": "", "ECOFLOW_ACCESS_KEY": "",
		})))
		require.NoError(t, err)
		assert.Equal(t, "mqtt", cfg.ExporterType)
		assert.Equal(t, "ecoflow", cfg.MetricPrefix)
		assert.Equal(t, "2112", cfg.Port)
		assert.Equal(t, 300*time.Second, cfg.Mqtt.OfflineThreshold)
	})

	t.Run("rest wins when both credentials are set", func(t *testing.T) {
		cfg, err := LoadConfig(lookupFrom(withVars(restVars, mqttVars)))
		require.NoError(t, err)
		assert.Equal(t, "rest", cfg.ExporterType)

		cfg, err = LoadConfig(lookupFrom(withVars(restVars, withVars(mqttVars, map[string]string{"EXPORTER_TYPE": "MQTT"}))))
		require.NoError(t, err)
		assert.Equal(t, "mqtt", cfg.ExporterType, "an explicit type wins")
	})
}

func TestLoadConfigSettings(t *testing.T) {
	cfg, err := LoadConfig(lookupFrom(withVars(mqttVars, map[string]string{
		"METRIC_PREFIX":                         "home",
		"PROMETHEUS_PORT":                       "9100",
		"PROTOBUF_DEBUG":                        "1",
		"MQTT_DEVICE_OFFLINE_THRESHOLD_SECONDS": "120",
		"MQTT_PING_INTERVAL_SECONDS":            "10",
		"MQTT_STATE_REFRESH_INTERVAL_SECONDS":   "60",
		"ECOFLOW_DEVICES_PRETTY_NAMES":          `{"SN1":"My Delta", " ":"skipped"}`,
		"ECOFLOW_DEVICES":                       " SN1, SN2 ,,",
	})))
	require.NoError(t, err)
	assert.Equal(t, "home", cfg.MetricPrefix)
	assert.Equal(t, "9100", cfg.Port)
	assert.True(t, cfg.ProtobufDebug)
	assert.Equal(t, 120*time.Second, cfg.Mqtt.OfflineThreshold)
	assert.Equal(t, 10*time.Second, cfg.Mqtt.PingInterval)
	assert.Equal(t, time.Minute, cfg.Mqtt.StateRefreshInterval)
	assert.Equal(t, map[string]string{"SN1": "My Delta", "SN2": "SN2"}, cfg.Devices)

	cfg, err = LoadConfig(lookupFrom(withVars(restVars, map[string]string{"SCRAPING_INTERVAL": "15"})))
	require.NoError(t, err)
	assert.Equal(t, 15*time.Second, cfg.Rest.Interval)
}

func TestLoadConfigErrors(t *testing.T) {
	tests := []struct {
		name string
		vars map[string]string
		want []string
	}{
		{"no credentials", map[string]string{}, []string{"no EcoFlow credentials"}},
		{"unknown type", withVars(restVars, map[string]string{"EXPORTER_TYPE": "soap"}), []string{"unknown EXPORTER_TYPE"}},
		{"half of the rest credentials", map[string]string{"ECOFLOW_ACCESS_KEY": "a"}, []string{"ECOFLOW_SECRET_KEY"}},
		{"mqtt without devices", map[string]string{"ECOFLOW_EMAIL": "e", "ECOFLOW_PASSWORD": "p"}, []string{"ECOFLOW_DEVICES"}},
		{"invalid values are not ignored", withVars(mqttVars, map[string]string{
			"PROTOBUF_DEBUG":                        "yes please",
			"PROMETHEUS_PORT":                       "abc",
			"MQTT_PING_INTERVAL_SECONDS":            "20s",
			"MQTT_DEVICE_OFFLINE_THRESHOLD_SECONDS": "0",
			"METRIC_PREFIX":                         "eco-flow",
		}), []string{"PROTOBUF_DEBUG must be true or false", "PROMETHEUS_PORT must be a port number",
			"MQTT_PING_INTERVAL_SECONDS must be a whole number", "MQTT_DEVICE_OFFLINE_THRESHOLD_SECONDS must be a positive",
			"METRIC_PREFIX must match"}},
		{"port out of range", withVars(restVars, map[string]string{"PROMETHEUS_PORT": "70000"}), []string{"PROMETHEUS_PORT"}},
		{"invalid pretty names", withVars(restVars, map[string]string{"ECOFLOW_DEVICES_PRETTY_NAMES": "{"}), []string{"unable to parse device mapping"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadConfig(lookupFrom(tt.vars))
			require.Error(t, err)
			for _, want := range tt.want {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}

func TestLoadConfigSecretsFromFiles(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret")
	require.NoError(t, os.WriteFile(secret, []byte("from-file\n"), 0o600))

	vars := withVars(mqttVars, map[string]string{"ECOFLOW_PASSWORD": "", "ECOFLOW_PASSWORD_FILE": secret})
	cfg, err := LoadConfig(lookupFrom(vars))
	require.NoError(t, err)
	assert.Equal(t, "from-file", cfg.Mqtt.Password)

	vars["ECOFLOW_PASSWORD"] = "direct"
	cfg, err = LoadConfig(lookupFrom(vars))
	require.NoError(t, err)
	assert.Equal(t, "direct", cfg.Mqtt.Password, "the variable wins over the file")

	vars = withVars(restVars, map[string]string{"ECOFLOW_SECRET_KEY": "", "ECOFLOW_SECRET_KEY_FILE": filepath.Join(dir, "missing")})
	_, err = LoadConfig(lookupFrom(vars))
	assert.ErrorContains(t, err, "cannot read ECOFLOW_SECRET_KEY_FILE")
}

func TestConfigLogValueHidesCredentials(t *testing.T) {
	cfg, err := LoadConfig(lookupFrom(withVars(mqttVars, map[string]string{"ECOFLOW_PASSWORD": "hunter2"})))
	require.NoError(t, err)
	var b strings.Builder
	slog.New(slog.NewTextHandler(&b, nil)).Info("Configuration", "config", cfg)
	assert.Contains(t, b.String(), "config.exporter_type=mqtt")
	assert.Contains(t, b.String(), "config.offline_threshold=5m0s")
	assert.NotContains(t, b.String(), "hunter2")
}
