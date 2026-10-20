package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-ecoflow-exporter/internal/protobuf"
	"google.golang.org/protobuf/proto"
)

// The tests in this file pin what dashboards, alert rules and SQL queries depend on: Prometheus metric
// names and labels, Timescale JSONB keys and Redis keys. They must keep passing unchanged.

const compatJSONPayload = `{"params":{"pd.soc":77,"inv.outTemp":41.5,"bms_bmsStatus.f32ShowSoc":76.3,
"inv.acInVol":0,"pd.wifiVer":"1.0.1","pd.list":[1,2],"pd.beepState":true}}`

// compatStates runs a JSON and a protobuf MQTT message through the MQTT exporter and returns the state
// it passes to the handlers, by serial number
func compatStates(t *testing.T) map[string]map[string]any {
	t.Helper()
	handler := &MockMetricHandler{}
	exporter := &MqttMetricsExporter{
		devices:         map[string]string{"JSON1": "Json Device", "DELTA3": "Delta 3"},
		handlers:        []MetricHandler{handler},
		deviceState:     make(map[string]map[string]any),
		protobufDecoder: protobuf.NewProtobufDecoder(false),
	}

	exporter.MessageHandler(nil, &mockMQTTMessage{topic: "/app/device/property/JSON1", payload: []byte(compatJSONPayload)})

	pdata, err := hex.DecodeString("1d00006b432500006b43b50300006b43851700006bc3")
	require.NoError(t, err)
	payload, err := proto.Marshal(&protobuf.HeaderMessage{
		Header: []*protobuf.Header{{Pdata: pdata, Src: 2, Dest: 32, CmdFunc: 254, CmdId: 21}},
	})
	require.NoError(t, err)
	exporter.MessageHandler(nil, &mockMQTTMessage{topic: "/app/device/property/DELTA3", payload: payload})

	handler.mu.Lock()
	defer handler.mu.Unlock()
	require.Len(t, handler.receivedDevice, 2)
	states := make(map[string]map[string]any)
	for i, d := range handler.receivedDevice {
		states[d.SN] = handler.receivedParams[i]
	}
	return states
}

var compatDevices = []EcoflowDevice{
	{SN: "JSON1", Name: "Json Device", Online: 1},
	{SN: "DELTA3", Name: "Delta 3", Online: 1},
}

func TestCompat_PrometheusSeries(t *testing.T) {
	states := compatStates(t)
	prom := newTestPrometheusExporter(t, &PrometheusConfig{Prefix: "ecoflow"})
	for _, d := range compatDevices {
		handle(context.Background(), prom, d, states[d.SN])
	}
	handle(context.Background(), prom, EcoflowDevice{SN: "OFF", Name: "Offline", Online: 0}, map[string]any{})

	reg := prometheus.NewPedanticRegistry()
	require.NoError(t, reg.Register(prom))
	families, err := reg.Gather()
	require.NoError(t, err)
	var got []string
	for _, f := range families {
		for _, m := range f.GetMetric() {
			var labels []string
			for _, l := range m.GetLabel() {
				labels = append(labels, fmt.Sprintf("%s=%q", l.GetName(), l.GetValue()))
			}
			got = append(got, fmt.Sprintf("%s{%s} %g | %s", f.GetName(), strings.Join(labels, ","), m.GetGauge().GetValue(), f.GetHelp()))
		}
	}
	slices.Sort(got)

	assert.Equal(t, []string{
		`ecoflow_bms_bms_status_f32_show_soc{device="Json Device",serial_number="JSON1"} 76.3 | EcoFlow device parameter "bms_bmsStatus.f32ShowSoc".`,
		`ecoflow_inv_ac_in_vol{device="Json Device",serial_number="JSON1"} 0 | EcoFlow device parameter "inv.acInVol".`,
		`ecoflow_inv_out_temp{device="Json Device",serial_number="JSON1"} 41.5 | EcoFlow device parameter "inv.outTemp".`,
		`ecoflow_online{device="Delta 3",serial_number="DELTA3"} 1 | EcoFlow device parameter "online".`,
		`ecoflow_online{device="Json Device",serial_number="JSON1"} 1 | EcoFlow device parameter "online".`,
		`ecoflow_online{device="Offline",serial_number="OFF"} 0 | EcoFlow device parameter "online".`,
		`ecoflow_pd_beep_state{device="Json Device",serial_number="JSON1"} 1 | EcoFlow device parameter "pd.beepState".`,
		`ecoflow_pd_soc{device="Json Device",serial_number="JSON1"} 77 | EcoFlow device parameter "pd.soc".`,
		`ecoflow_pow_get_ac_in{device="Delta 3",serial_number="DELTA3"} 235 | EcoFlow device parameter "powGetAcIn".`,
		`ecoflow_pow_get_ac_out{device="Delta 3",serial_number="DELTA3"} -235 | EcoFlow device parameter "powGetAcOut".`,
		`ecoflow_pow_in_sum_w{device="Delta 3",serial_number="DELTA3"} 235 | EcoFlow device parameter "powInSumW".`,
		`ecoflow_pow_out_sum_w{device="Delta 3",serial_number="DELTA3"} 235 | EcoFlow device parameter "powOutSumW".`,
	}, got)

	// an offline device keeps its series, set to 0
	handle(context.Background(), prom, EcoflowDevice{SN: "JSON1", Name: "Json Device", Online: 0}, map[string]any{})
	prom.mu.RLock()
	defer prom.mu.RUnlock()
	assert.Equal(t, 0.0, prom.metrics["JSON1_ecoflow_pd_soc"].value)
	assert.Equal(t, 0.0, prom.metrics["JSON1_ecoflow_online"].value)
}

func TestCompat_TimescaleJSONKeys(t *testing.T) {
	states := compatStates(t)
	assert.Equal(t, map[string]any{
		"ecoflow_pd_soc":                      77.0,
		"ecoflow_inv_out_temp":                41.5,
		"ecoflow_bms_bms_status_f32_show_soc": 76.3,
		"ecoflow_inv_ac_in_vol":               0.0,
		"ecoflow_pd_wifi_ver":                 "1.0.1",
		"ecoflow_pd_beep_state":               true,
		"ecoflow_online":                      1.0,
	}, timescaleMetrics("ecoflow", NewDeviceUpdate(compatDevices[0], states["JSON1"])))
	assert.Equal(t, map[string]any{
		"ecoflow_pow_in_sum_w":   235.0,
		"ecoflow_pow_out_sum_w":  235.0,
		"ecoflow_pow_get_ac_in":  235.0,
		"ecoflow_pow_get_ac_out": -235.0,
		"ecoflow_online":         1.0,
	}, timescaleMetrics("ecoflow", NewDeviceUpdate(compatDevices[1], states["DELTA3"])))
}

// redisCapture records the commands of a pipeline instead of sending them
type redisCapture struct{ cmds [][]any }

func (c *redisCapture) DialHook(next redis.DialHook) redis.DialHook { return next }
func (c *redisCapture) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(_ context.Context, cmd redis.Cmder) error {
		c.cmds = append(c.cmds, cmd.Args())
		return nil
	}
}
func (c *redisCapture) ProcessPipelineHook(redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(_ context.Context, cmds []redis.Cmder) error {
		for _, cmd := range cmds {
			c.cmds = append(c.cmds, cmd.Args())
		}
		return nil
	}
}

func TestCompat_RedisKeys(t *testing.T) {
	states := compatStates(t)
	r := NewRedisExporter(&RedisExporterConfig{Prefix: "ecoflow", RedisConfig: &redis.Options{Addr: "127.0.0.1:1"}})
	defer r.Close(context.Background())
	capture := &redisCapture{}
	r.client.AddHook(capture)

	before := time.Now().Unix()
	handle(context.Background(), r, compatDevices[0], states["JSON1"])

	values := map[string]any{}
	var lastAccess []any
	for _, args := range capture.cmds {
		switch strings.ToUpper(fmt.Sprint(args[0])) {
		case "TS.ADD":
			values[args[1].(string)] = args[3]
			ts, ok := args[2].(int64)
			require.True(t, ok, "timestamp is an integer")
			assert.GreaterOrEqual(t, ts, before, "timestamp is Unix seconds")
			assert.Less(t, ts, before+5, "timestamp is Unix seconds")
		case "SET":
			lastAccess = args
		}
	}
	assert.Equal(t, map[string]any{
		"ts:JSON1:ecoflow_pd_soc":                      77.0,
		"ts:JSON1:ecoflow_inv_out_temp":                41.5,
		"ts:JSON1:ecoflow_bms_bms_status_f32_show_soc": 76.3,
		"ts:JSON1:ecoflow_inv_ac_in_vol":               0.0,
		"ts:JSON1:ecoflow_pd_beep_state":               1.0,
		"ts:JSON1:ecoflow_online":                      1.0,
	}, values)
	require.Len(t, lastAccess, 3)
	assert.Equal(t, "device:last_access:JSON1", lastAccess[1])
	assert.GreaterOrEqual(t, lastAccess[2].(int64), before, "last access is Unix seconds")
}
