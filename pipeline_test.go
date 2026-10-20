package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeviceUpdateSamplesAreComputedOncePerPrefix(t *testing.T) {
	u := NewDeviceUpdate(EcoflowDevice{SN: "SN"}, map[string]any{"pd.soc": 50.0, "name": "x", "": 1.0})
	samples := u.Samples("ecoflow")
	require.Len(t, samples, 2, "a parameter without a name is skipped")
	byParam := map[string]Sample{}
	for _, s := range samples {
		byParam[s.Param] = s
	}
	assert.Equal(t, Sample{Param: "pd.soc", Name: "ecoflow_pd_soc", Raw: 50.0, Value: 50, Numeric: true}, byParam["pd.soc"])
	assert.Equal(t, Sample{Param: "name", Name: "ecoflow_name", Raw: "x"}, byParam["name"])

	again := u.Samples("ecoflow")
	assert.Same(t, &samples[0], &again[0], "cached")
	assert.Equal(t, "other_pd_soc", findSample(u.Samples("other"), "pd.soc").Name)
}

func findSample(samples []Sample, param string) Sample {
	for _, s := range samples {
		if s.Param == param {
			return s
		}
	}
	return Sample{}
}

// failingHandler fails with the queued errors, then succeeds
type failingHandler struct {
	mu    sync.Mutex
	errs  []error
	calls int
}

func (f *failingHandler) Handle(context.Context, *DeviceUpdate) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if len(f.errs) == 0 {
		return nil
	}
	err := f.errs[0]
	f.errs = f.errs[1:]
	return err
}

func (f *failingHandler) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestQueuedHandlerRetriesNetworkErrorsOnce(t *testing.T) {
	netErr := &net.OpError{Op: "dial", Err: errors.New("connection refused")}
	inner := &failingHandler{errs: []error{netErr, netErr, netErr}}
	q := NewQueuedHandler("retry", inner, QueueOptions{Size: 1, Timeout: 5 * time.Second})
	require.NoError(t, q.Handle(context.Background(), NewDeviceUpdate(EcoflowDevice{SN: "R"}, nil)))
	q.Close(context.Background())
	assert.Equal(t, 2, inner.callCount(), "one retry")

	inner = &failingHandler{errs: []error{errors.New("syntax error")}}
	q = NewQueuedHandler("noretry", inner, QueueOptions{Size: 1, Timeout: 5 * time.Second})
	require.NoError(t, q.Handle(context.Background(), NewDeviceUpdate(EcoflowDevice{SN: "R"}, nil)))
	q.Close(context.Background())
	assert.Equal(t, 1, inner.callCount(), "other errors are not retried")
}

func TestQueuedHandlerHealth(t *testing.T) {
	q := &QueuedHandler{name: "h", unhealthyAfter: time.Minute}
	assert.NoError(t, q.Health())

	q.record(errors.New("db down"))
	assert.NoError(t, q.Health(), "failing for less than a minute")
	q.failingSince = time.Now().Add(-2 * time.Minute)
	assert.ErrorContains(t, q.Health(), "db down")

	q.record(nil)
	assert.NoError(t, q.Health(), "a success resets it")

	disabled := &QueuedHandler{name: "h"}
	disabled.record(errors.New("db down"))
	disabled.failingSince = time.Now().Add(-time.Hour)
	assert.NoError(t, disabled.Health(), "disabled by default")
}

func TestQueuedHandlerRecoversFromPanics(t *testing.T) {
	q := NewQueuedHandler("panic", panicHandler{}, QueueOptions{Size: 1, Timeout: time.Second, UnhealthyAfter: time.Nanosecond})
	require.NoError(t, q.Handle(context.Background(), NewDeviceUpdate(EcoflowDevice{SN: "P"}, nil)))
	q.Close(context.Background())
	assert.ErrorContains(t, q.Health(), "panic: boom")
}

type panicHandler struct{}

func (panicHandler) Handle(context.Context, *DeviceUpdate) error { panic("boom") }

func TestThrottledHandlerCoalescesUpdates(t *testing.T) {
	inner := &MockMetricHandler{}
	th := NewThrottledHandler(inner, 200*time.Millisecond)
	online := EcoflowDevice{SN: "T", Online: 1}

	for i := range 5 {
		require.NoError(t, th.Handle(context.Background(), NewDeviceUpdate(online, map[string]any{"v": float64(i)})))
	}
	assert.Equal(t, 1, inner.GetReceivedCount(), "the first update passes, the rest waits")
	require.NoError(t, th.Handle(context.Background(), NewDeviceUpdate(EcoflowDevice{SN: "OTHER", Online: 1}, map[string]any{})))
	assert.Equal(t, 2, inner.GetReceivedCount(), "devices are throttled separately")

	require.Eventually(t, func() bool { return inner.GetReceivedCount() == 3 }, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, 4.0, inner.GetLastParams()["v"], "the latest pending update wins")

	// a status change is passed on immediately, after the pending state
	require.NoError(t, th.Handle(context.Background(), NewDeviceUpdate(online, map[string]any{"v": 5.0})))
	require.NoError(t, th.Handle(context.Background(), NewDeviceUpdate(EcoflowDevice{SN: "T", Online: 0}, map[string]any{})))
	params := inner.GetReceivedParams()
	require.Len(t, params, 5)
	assert.Equal(t, 5.0, params[3]["v"])
	inner.mu.Lock()
	assert.Equal(t, 0, inner.receivedDevice[4].Online)
	inner.mu.Unlock()

	// pending updates are passed on when closing
	require.NoError(t, th.Handle(context.Background(), NewDeviceUpdate(EcoflowDevice{SN: "T", Online: 0}, map[string]any{"v": 6.0})))
	th.Close(context.Background())
	assert.Equal(t, 6.0, inner.GetLastParams()["v"])
}

func TestRedisCommandsAreSafeForExistingKeys(t *testing.T) {
	capture := &redisCapture{}
	r := NewRedisExporter(&RedisExporterConfig{Prefix: "ecoflow", RedisConfig: &redis.Options{Addr: "127.0.0.1:1"}})
	defer r.Close(context.Background())
	r.client.AddHook(capture)
	u := NewDeviceUpdate(EcoflowDevice{SN: "SN", Name: "My Delta", Online: 1}, map[string]any{"pd.soc": 50.0})
	require.NoError(t, r.Handle(context.Background(), u))

	assert.Equal(t, []any{"TS.ADD", "ts:SN:ecoflow_pd_soc", u.Time.Unix(), 50.0, "ON_DUPLICATE", "LAST",
		"LABELS", "serial_number", "SN", "metric", "ecoflow_pd_soc", "device", "My Delta"}, capture.cmds[0])

	capture.cmds = nil
	r.config.Retention = 30 * 24 * time.Hour
	r.config.TimestampMs = true
	require.NoError(t, r.Handle(context.Background(), u))
	assert.Equal(t, []any{"TS.ADD", "ts:SN:ecoflow_pd_soc", u.Time.UnixMilli(), 50.0, "RETENTION", int64(2592000000),
		"ON_DUPLICATE", "LAST", "LABELS", "serial_number", "SN", "metric", "ecoflow_pd_soc", "device", "My Delta"}, capture.cmds[0])
	last := capture.cmds[len(capture.cmds)-1]
	assert.Equal(t, u.Time.Unix(), last[2], "last access stays in seconds")
}

func TestRedisReturnsErrors(t *testing.T) {
	r := NewRedisExporter(&RedisExporterConfig{Prefix: "ecoflow",
		RedisConfig: &redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1, DialTimeout: 50 * time.Millisecond}})
	defer r.Close(context.Background())
	err := r.Handle(context.Background(), NewDeviceUpdate(EcoflowDevice{SN: "SN", Online: 1}, nil))
	assert.ErrorContains(t, err, "unable to insert metrics")
	assert.True(t, isTransient(err), "connection errors are retried by the queue")
}

func TestLoadConfigOptionalSettings(t *testing.T) {
	lookup := func(vars map[string]string) func(string) (string, bool) {
		return func(k string) (string, bool) { v, ok := vars[k]; return v, ok }
	}
	base := map[string]string{"ECOFLOW_ACCESS_KEY": "a", "ECOFLOW_SECRET_KEY": "s", "REDIS_ENABLED": "true",
		"TIMESCALE_ENABLED": "true", "TIMESCALE_URL": "postgres://h/db"}
	with := func(extra map[string]string) map[string]string {
		vars := map[string]string{}
		for k, v := range base {
			vars[k] = v
		}
		for k, v := range extra {
			vars[k] = v
		}
		return vars
	}

	t.Run("empty values keep the previous behaviour", func(t *testing.T) {
		cfg, err := LoadConfig(lookup(with(map[string]string{
			"REDIS_URL": "", "REDIS_DB": "", "TIMESCALE_RETENTION_DAYS": "", "REDIS_TIMESTAMP_MS": "",
		})))
		require.NoError(t, err)
		assert.Equal(t, TimescaleEnvConfig{Enabled: true, Url: "postgres://h/db"}, cfg.Timescale)
		assert.Equal(t, "localhost:6379", cfg.Redis.Options.Addr)
		assert.Zero(t, cfg.Redis.Retention)
		assert.Zero(t, cfg.Redis.MinWriteInterval)
		assert.False(t, cfg.Redis.TimestampMs)
		assert.Zero(t, cfg.HandlerUnhealthyAfter)
	})

	t.Run("new settings", func(t *testing.T) {
		cfg, err := LoadConfig(lookup(with(map[string]string{
			"TIMESCALE_COMPRESSION_AFTER_DAYS":     "7",
			"TIMESCALE_RETENTION_DAYS":             "365",
			"TIMESCALE_MIN_WRITE_INTERVAL_SECONDS": "10",
			"REDIS_RETENTION_DAYS":                 "30",
			"REDIS_MIN_WRITE_INTERVAL_SECONDS":     "5",
			"REDIS_TIMESTAMP_MS":                   "true",
			"HANDLER_UNHEALTHY_AFTER_SECONDS":      "600",
			"PROTOBUF_DEBUG":                       "1",
		})))
		require.NoError(t, err)
		assert.Equal(t, 7*24*time.Hour, cfg.Timescale.CompressionAfter)
		assert.Equal(t, 365*24*time.Hour, cfg.Timescale.Retention)
		assert.Equal(t, 10*time.Second, cfg.Timescale.MinWriteInterval)
		assert.Equal(t, 30*24*time.Hour, cfg.Redis.Retention)
		assert.Equal(t, 5*time.Second, cfg.Redis.MinWriteInterval)
		assert.True(t, cfg.Redis.TimestampMs)
		assert.Equal(t, 10*time.Minute, cfg.HandlerUnhealthyAfter)
		assert.True(t, cfg.ProtobufDebug)
	})

	t.Run("negative values are rejected", func(t *testing.T) {
		_, err := LoadConfig(lookup(with(map[string]string{"TIMESCALE_RETENTION_DAYS": "-1"})))
		assert.ErrorContains(t, err, "TIMESCALE_RETENTION_DAYS must not be negative")
	})

	t.Run("redis url", func(t *testing.T) {
		cfg, err := LoadConfig(lookup(with(map[string]string{"REDIS_URL": "rediss://u:pw@redis.example:6380/2", "REDIS_DB": "5"})))
		require.NoError(t, err)
		o := cfg.Redis.Options
		assert.Equal(t, "redis.example:6380", o.Addr)
		assert.Equal(t, "u", o.Username)
		assert.Equal(t, "pw", o.Password)
		assert.Equal(t, 2, o.DB, "the database in the URL wins")
		assert.NotNil(t, o.TLSConfig)
		assert.True(t, o.DisableIdentity)

		cfg, err = LoadConfig(lookup(with(map[string]string{"REDIS_URL": "redis://redis:6379", "REDIS_DB": "3", "REDIS_PASSWORD": "x"})))
		require.NoError(t, err)
		assert.Equal(t, 3, cfg.Redis.Options.DB, "REDIS_DB applies when the URL has no database")
		assert.Equal(t, "x", cfg.Redis.Options.Password)

		cfg, err = LoadConfig(lookup(with(map[string]string{"REDIS_URL": "redis:6379", "REDIS_DB": "1", "REDIS_USER": "u"})))
		require.NoError(t, err)
		assert.Equal(t, "redis:6379", cfg.Redis.Options.Addr, "host:port works as before")
		assert.Equal(t, 1, cfg.Redis.Options.DB)
		assert.Equal(t, "u", cfg.Redis.Options.Username)

		_, err = LoadConfig(lookup(with(map[string]string{"REDIS_URL": "redis://h:notaport"})))
		assert.ErrorContains(t, err, "invalid REDIS_URL")
	})

	t.Run("secrets from files", func(t *testing.T) {
		dir := t.TempDir()
		secret := filepath.Join(dir, "secret")
		require.NoError(t, os.WriteFile(secret, []byte("from-file\n"), 0o600))
		vars := with(map[string]string{"ECOFLOW_SECRET_KEY": "", "ECOFLOW_SECRET_KEY_FILE": secret, "REDIS_PASSWORD_FILE": secret})
		cfg, err := LoadConfig(lookup(vars))
		require.NoError(t, err)
		assert.Equal(t, "from-file", cfg.Rest.SecretKey)
		assert.Equal(t, "from-file", cfg.Redis.Options.Password)

		vars["ECOFLOW_SECRET_KEY"] = "direct"
		cfg, err = LoadConfig(lookup(vars))
		require.NoError(t, err)
		assert.Equal(t, "direct", cfg.Rest.SecretKey, "the variable wins over the file")

		vars["ECOFLOW_SECRET_KEY"] = ""
		vars["ECOFLOW_SECRET_KEY_FILE"] = filepath.Join(dir, "missing")
		_, err = LoadConfig(lookup(vars))
		assert.ErrorContains(t, err, "cannot read ECOFLOW_SECRET_KEY_FILE")
	})
}

func TestHealthCombinesExporterAndHandlers(t *testing.T) {
	failing := &QueuedHandler{name: "timescale", unhealthyAfter: time.Second}
	failing.record(errors.New("db down"))
	failing.failingSince = time.Now().Add(-time.Minute)
	handlers := []MetricHandler{&MockMetricHandler{}, &ThrottledHandler{inner: failing}}

	err := health(&RestMetricsExporter{interval: time.Second, started: time.Now()}, handlers)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "timescale handler failing"), fmt.Sprint(err))
}
