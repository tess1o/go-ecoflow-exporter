package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tess1o/go-ecoflow"
	"go-ecoflow-exporter/internal/protobuf"
)

func scrape(t *testing.T, port string) string {
	t.Helper()
	resp, err := http.Get("http://127.0.0.1:" + port + "/metrics")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(body)
}

// Handlers receive the same map concurrently; writing into it (as handlers once did with "online") can crash
// the process with "concurrent map read and map write". Run with -race.
func TestHandlersDoNotMutateSharedParams(t *testing.T) {
	prom := newTestPrometheusExporter(t, &PrometheusConfig{Prefix: "reg_shared"})

	params := map[string]any{"battery.level": 50.0, "ac.voltage": 230.0}
	device := EcoflowDevice{SN: "REGSHARED", Name: "dev", Online: 1}

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { handle(context.Background(), prom, device, params) })
	}
	wg.Wait()

	assert.Len(t, params, 2)
	assert.NotContains(t, params, "online")
}

func TestMetricNames(t *testing.T) {
	tests := map[string]string{
		"pd.wireUsedTime":       "pd_wire_used_time",
		"inv.acInVol":           "inv_ac_in_vol",
		"bms_bmsStatus.minCell": "bms_bms_status_min_cell",
		"bms.0.soc":             "bms_0_soc",
		"Pd.X":                  "pd_x",
		"ABC":                   "a_b_c",
		"0abc":                  "0abc",
		"inv-name x":            "inv_name_x",
		"temp°C":                "temp_c",
	}
	for raw, want := range tests {
		got, err := ecoflowParamToPrometheusMetric(raw)
		require.NoError(t, err, raw)
		assert.Equal(t, want, got, raw)
	}
	_, err := ecoflowParamToPrometheusMetric("")
	assert.Error(t, err)
}

// Invalid characters used to panic in prometheus.MustRegister and kill the process
func TestPrometheusInvalidNamesDoNotPanic(t *testing.T) {
	prom := newTestPrometheusExporter(t, &PrometheusConfig{Prefix: "reg_invalid"})
	servePrometheus(t, prom, "29972")

	require.NotPanics(t, func() {
		handle(context.Background(), prom, EcoflowDevice{SN: "REGINV", Name: "dev", Online: 1},
			map[string]any{"inv-name x": 1.0, "": 2.0, "ok.value": 3.0})
	})
	body := scrape(t, "29972")
	assert.Contains(t, body, `reg_invalid_inv_name_x{device="dev",serial_number="REGINV"} 1`)
	assert.Contains(t, body, `reg_invalid_ok_value{device="dev",serial_number="REGINV"} 3`)
}

// Arrays and strings used to be registered as gauges stuck at 0
func TestPrometheusOnlyNumbersBecomeMetrics(t *testing.T) {
	prom := newTestPrometheusExporter(t, &PrometheusConfig{Prefix: "reg_types"})
	servePrometheus(t, prom, "29973")

	handle(context.Background(), prom, EcoflowDevice{SN: "REGTYPES", Name: "dev", Online: 1}, map[string]any{
		"num":  4.5,
		"flag": true,
		"list": []any{1.0},
		"str":  "abc",
		"obj":  map[string]any{"a": 1.0},
		"nil":  nil,
	})

	body := scrape(t, "29973")
	assert.Contains(t, body, `reg_types_num{device="dev",serial_number="REGTYPES"} 4.5`)
	assert.Contains(t, body, `reg_types_flag{device="dev",serial_number="REGTYPES"} 1`)
	assert.Contains(t, body, `reg_types_online{device="dev",serial_number="REGTYPES"} 1`)
	assert.Contains(t, body, `# HELP reg_types_num EcoFlow device parameter "num".`)
	assert.Contains(t, body, `# TYPE reg_types_num gauge`)
	for _, name := range []string{"list", "str", "obj", "nil"} {
		assert.NotContains(t, body, "reg_types_"+name+"{")
	}
}

func TestPrometheusOfflineDevice(t *testing.T) {
	prom := newTestPrometheusExporter(t, &PrometheusConfig{Prefix: "reg_off"})
	servePrometheus(t, prom, "29974")

	ctx := context.Background()
	handle(ctx, prom, EcoflowDevice{SN: "OFF1", Name: "a", Online: 1}, map[string]any{"soc": 80.0})
	handle(ctx, prom, EcoflowDevice{SN: "OFF10", Name: "b", Online: 1}, map[string]any{"soc": 70.0})
	handle(ctx, prom, EcoflowDevice{SN: "OFF1", Name: "a", Online: 0}, nil)
	// a device that was never online still reports online=0
	handle(ctx, prom, EcoflowDevice{SN: "NEVER", Name: "c", Online: 0}, nil)

	body := scrape(t, "29974")
	assert.NotContains(t, body, `reg_off_soc{device="a",serial_number="OFF1"}`, "series of an offline device are removed")
	assert.Contains(t, body, `reg_off_online{device="a",serial_number="OFF1"} 0`)
	// "OFF1" is a substring of "OFF10", which must not be affected
	assert.Contains(t, body, `reg_off_soc{device="b",serial_number="OFF10"} 70`)
	assert.Contains(t, body, `reg_off_online{device="c",serial_number="NEVER"} 0`)
}

func TestHTTPServerHealthz(t *testing.T) {
	server, err := NewHTTPServer("29976", http.NotFoundHandler())
	require.NoError(t, err)
	defer server.Close(context.Background())

	get := func(path string) int {
		resp, err := http.Get("http://127.0.0.1:29976" + path)
		require.NoError(t, err)
		resp.Body.Close()
		return resp.StatusCode
	}
	assert.Equal(t, http.StatusOK, get("/healthz"))
	server.SetHealthCheck(func() error { return errors.New("broken") })
	assert.Equal(t, http.StatusServiceUnavailable, get("/healthz"))
}

func TestHTTPServerPortInUse(t *testing.T) {
	server, err := NewHTTPServer("29977", http.NotFoundHandler())
	require.NoError(t, err)
	defer server.Close(context.Background())
	_, err = NewHTTPServer("29977", http.NotFoundHandler())
	assert.Error(t, err)
}

func TestTelemetryIsExposed(t *testing.T) {
	reg := prometheus.NewRegistry()
	tel := NewTelemetry("reg_tel", "1.2.3", reg)
	prom := newTestPrometheusExporter(t, &PrometheusConfig{Prefix: "reg_tel", Gatherer: reg})
	servePrometheus(t, prom, "29978")

	tel.SetMqttConnected(true)
	tel.RestRequest("device_list", nil)
	tel.MqttPing("SN", "keepalive", errors.New("x"))

	body := scrape(t, "29978")
	assert.Contains(t, body, "reg_tel_exporter_mqtt_connected 1")
	assert.Contains(t, body, `reg_tel_exporter_build_info{version="1.2.3"} 1`)
	assert.Contains(t, body, `reg_tel_exporter_rest_requests_total{endpoint="device_list",result="success"} 1`)
	assert.Contains(t, body, `reg_tel_exporter_mqtt_pings_total{result="error",serial_number="SN",type="keepalive"} 1`)

	var nilTelemetry *Telemetry
	assert.NotPanics(t, func() { nilTelemetry.SetMqttConnected(true) })
}

type fakeRestClient struct {
	listErr error
	devices []ecoflow.DeviceInfo
	params  map[string]map[string]any
}

func (f *fakeRestClient) GetDeviceList(context.Context) (*ecoflow.DeviceListResponse, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return &ecoflow.DeviceListResponse{Devices: f.devices}, nil
}

func (f *fakeRestClient) GetDeviceAllParameters(_ context.Context, sn string) (map[string]any, error) {
	p, ok := f.params[sn]
	if !ok {
		return nil, fmt.Errorf("no params for %s", sn)
	}
	return p, nil
}

func TestRestExporterScrape(t *testing.T) {
	handler := &MockMetricHandler{}
	client := &fakeRestClient{
		devices: []ecoflow.DeviceInfo{{SN: "A", Online: 1}, {SN: "B", Online: 0}, {SN: "BROKEN", Online: 1}},
		params:  map[string]map[string]any{"A": {"soc": 90.0}, "B": {"soc": 10.0}},
	}
	exporter := NewRestMetricsExporter(client, RestExporterConfig{Interval: time.Minute, Devices: map[string]string{"A": "Alpha"}}, handler)
	exporter.scrape(context.Background())

	require.Equal(t, 2, handler.GetReceivedCount())
	handler.mu.Lock()
	defer handler.mu.Unlock()
	assert.Equal(t, EcoflowDevice{SN: "A", Name: "Alpha", Online: 1}, handler.receivedDevice[0])
	assert.Equal(t, EcoflowDevice{SN: "B", Name: "B", Online: 0}, handler.receivedDevice[1])
}

func TestRestExporterReportsOfflineDeviceWithoutQuota(t *testing.T) {
	handler := &MockMetricHandler{}
	client := &fakeRestClient{
		devices: []ecoflow.DeviceInfo{{SN: "OFF", Online: 0}, {SN: "ON", Online: 1}, {SN: "BROKEN", Online: 1}},
		params:  map[string]map[string]any{"ON": {"soc": 50.0}},
	}
	NewRestMetricsExporter(client, RestExporterConfig{Interval: time.Minute}, handler).scrape(context.Background())

	require.Equal(t, 2, handler.GetReceivedCount(), "an online device without parameters is skipped")
	handler.mu.Lock()
	defer handler.mu.Unlock()
	assert.Equal(t, EcoflowDevice{SN: "OFF", Name: "OFF", Online: 0}, handler.receivedDevice[0], "updates keep the device list order")
	assert.Empty(t, handler.receivedParams[0])
	assert.Equal(t, "ON", handler.receivedDevice[1].SN)
}

func TestRestExporterRunStopsAndReportsHealth(t *testing.T) {
	exporter := NewRestMetricsExporter(&fakeRestClient{listErr: errors.New("down")}, RestExporterConfig{Interval: 10 * time.Millisecond}, &MockMetricHandler{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- exporter.Run(ctx) }()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Run did not stop")
	}

	assert.NoError(t, exporter.Health(), "recently started")
	exporter.mu.Lock()
	exporter.started = time.Now().Add(-time.Hour)
	exporter.mu.Unlock()
	assert.Error(t, exporter.Health(), "no successful call for an hour")
}

func TestMqttReconnectBackoff(t *testing.T) {
	m := &MqttMetricsExporter{offlineThreshold: time.Minute}
	assert.True(t, m.reconnectDue())
	assert.False(t, m.reconnectDue(), "next attempt is delayed")
	assert.Equal(t, time.Minute, m.reconnectDelay)

	m.nextReconnect = time.Now().Add(-time.Second)
	assert.True(t, m.reconnectDue())
	assert.Equal(t, 2*time.Minute, m.reconnectDelay, "delay doubles")

	m.markReceived("SN")
	assert.True(t, m.reconnectDue(), "data from any device resets the backoff")
	assert.Equal(t, time.Minute, m.reconnectDelay)

	m.reconnectDelay = maxReconnectDelay
	m.nextReconnect = time.Time{}
	m.reconnectDue()
	assert.Equal(t, maxReconnectDelay, m.reconnectDelay, "delay is capped")
}

func TestMqttOfflineDevices(t *testing.T) {
	m := &MqttMetricsExporter{offlineThreshold: time.Minute, devices: map[string]string{"A": "A", "B": "B"}}
	m.initDeviceStatuses()
	assert.Empty(t, m.offlineDevices(), "devices get the offline threshold to report after connecting")

	m.statusMu.Lock()
	for sn := range m.lastReceived {
		m.lastReceived[sn] = time.Now().Add(-2 * time.Minute)
	}
	m.statusMu.Unlock()
	assert.ElementsMatch(t, []string{"A", "B"}, m.offlineDevices(), "silent devices are offline")
	assert.False(t, m.markReceived("A"), "no offline update was sent yet")
	assert.Equal(t, []string{"B"}, m.offlineDevices())
}

func TestMqttOfflineUpdatesOncePerThreshold(t *testing.T) {
	m := &MqttMetricsExporter{offlineThreshold: time.Minute}
	assert.Equal(t, []string{"A"}, m.offlineUpdatesDue([]string{"A"}))
	assert.Empty(t, m.offlineUpdatesDue([]string{"A"}), "the monitor checks more often than it reports")

	m.statusMu.Lock()
	m.offlineSent["A"] = time.Now().Add(-time.Minute)
	m.statusMu.Unlock()
	assert.Equal(t, []string{"A"}, m.offlineUpdatesDue([]string{"A"}), "reported again after a threshold")

	assert.True(t, m.markReceived("A"), "the device is back online")
	assert.False(t, m.markReceived("A"))
	assert.Equal(t, []string{"A"}, m.offlineUpdatesDue([]string{"A"}), "a new outage is reported right away")
}

func TestMqttHealthBeforeLogin(t *testing.T) {
	m := &MqttMetricsExporter{}
	assert.ErrorContains(t, m.Health(), "not logged in")
	assert.False(t, m.connected())
	assert.ErrorContains(t, m.requestLatestQuotas("SN"), "not logged in")
	assert.ErrorContains(t, m.sendRtcTimePing("SN"), "not logged in")
}

func TestMqttReloginAfterFruitlessReconnects(t *testing.T) {
	m := &MqttMetricsExporter{relogin: make(chan struct{}, 1)}
	for i := 1; i < reloginAfterReconnects; i++ {
		assert.False(t, m.reloginDue(), "reconnect %d", i)
	}
	assert.True(t, m.reloginDue(), "log in again after %d reconnects without data", reloginAfterReconnects)
	assert.False(t, m.reloginDue(), "the count starts over")

	m.markReceived("SN")
	for i := 1; i < reloginAfterReconnects; i++ {
		assert.False(t, m.reloginDue(), "data from any device resets the count")
	}

	m.requestRelogin()
	m.requestRelogin() // a pending request is enough, this must not block
	assert.Len(t, m.relogin, 1)
}

func TestWatchdog(t *testing.T) {
	var mu sync.Mutex
	healthErr := errors.New("broken")
	check := func() error {
		mu.Lock()
		defer mu.Unlock()
		return healthErr
	}

	err := watchdog(context.Background(), check, 50*time.Millisecond, 10*time.Millisecond)
	assert.ErrorContains(t, err, "stopping so that the exporter is restarted")
	assert.ErrorIs(t, err, healthErr)

	mu.Lock()
	healthErr = nil
	mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	assert.NoError(t, watchdog(ctx, check, 50*time.Millisecond, 10*time.Millisecond), "a healthy exporter keeps running")
}

func TestProtobufDecoderConcurrentUse(t *testing.T) {
	d := protobuf.NewProtobufDecoder(true)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			d.LogFirstProtobufMessage(fmt.Sprintf("SN%d", i%3))
			_, _ = d.DecodeProtobufMessage([]byte{0x0a, 0x02, 0x10, 0x01})
		})
	}
	wg.Wait()
	assert.Equal(t, "protobuf", protobuf.FormatProtobuf.String())
	assert.True(t, strings.HasPrefix(protobuf.FormatUnknown.String(), "unknown"))
}
