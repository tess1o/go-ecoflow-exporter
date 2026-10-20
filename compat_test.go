package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-ecoflow-exporter/internal/protobuf"
	"google.golang.org/protobuf/proto"
)

// The tests in this file pin what dashboards and alert rules depend on: Prometheus metric names and labels.
// They must keep passing unchanged.

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

	// an offline device keeps only its online series, set to 0
	handle(context.Background(), prom, EcoflowDevice{SN: "JSON1", Name: "Json Device", Online: 0}, map[string]any{})
	prom.mu.RLock()
	defer prom.mu.RUnlock()
	assert.NotContains(t, prom.metrics, "JSON1_ecoflow_pd_soc")
	assert.Equal(t, 0.0, prom.metrics["JSON1_ecoflow_online"].value)
}
