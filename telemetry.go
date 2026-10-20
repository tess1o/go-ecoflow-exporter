package main

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Telemetry holds metrics about the exporter itself (not the devices).
// A nil *Telemetry is valid and records nothing, which keeps tests short.
type Telemetry struct {
	mqttConnected  prometheus.Gauge
	lastMessage    *prometheus.GaugeVec
	mqttMessages   *prometheus.CounterVec
	decodeErrors   *prometheus.CounterVec
	mqttPings      *prometheus.CounterVec
	mqttReconnects prometheus.Counter
	mqttRelogins   prometheus.Counter
	restRequests   *prometheus.CounterVec
	buildInfo      *prometheus.GaugeVec
}

// NewTelemetry creates the exporter metrics and registers them with reg.
// Metric names are "<prefix>_exporter_*", for example "ecoflow_exporter_mqtt_connected".
func NewTelemetry(prefix, version string, reg prometheus.Registerer) *Telemetry {
	name := func(n string) string { return prefix + "_exporter_" + n }
	t := &Telemetry{
		mqttConnected: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: name("mqtt_connected"),
			Help: "1 if the exporter is connected to the EcoFlow MQTT broker, 0 otherwise.",
		}),
		lastMessage: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: name("last_message_timestamp_seconds"),
			Help: "Unix time of the last parameters received from the device.",
		}, []string{"device", "serial_number"}),
		mqttMessages: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: name("mqtt_messages_total"),
			Help: "MQTT messages received from devices, by payload format.",
		}, []string{"serial_number", "format"}),
		decodeErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: name("decode_errors_total"),
			Help: "MQTT messages that could not be decoded, by payload format.",
		}, []string{"serial_number", "format"}),
		mqttPings: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: name("mqtt_pings_total"),
			Help: "Keepalive pings and state refresh requests sent to devices, by type and result.",
		}, []string{"serial_number", "type", "result"}),
		mqttReconnects: prometheus.NewCounter(prometheus.CounterOpts{
			Name: name("mqtt_forced_reconnects_total"),
			Help: "Reconnects forced because no device sent data within the offline threshold.",
		}),
		mqttRelogins: prometheus.NewCounter(prometheus.CounterOpts{
			Name: name("mqtt_relogins_total"),
			Help: "Logins repeated to get fresh MQTT broker credentials, because reconnects did not bring data back.",
		}),
		restRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: name("rest_requests_total"),
			Help: "EcoFlow REST API requests, by endpoint and result.",
		}, []string{"endpoint", "result"}),
		buildInfo: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: name("build_info"),
			Help: "Always 1, labelled with the exporter version.",
		}, []string{"version"}),
	}
	if reg != nil {
		reg.MustRegister(t.mqttConnected, t.lastMessage, t.mqttMessages, t.decodeErrors, t.mqttPings,
			t.mqttReconnects, t.mqttRelogins, t.restRequests, t.buildInfo)
	}
	t.buildInfo.WithLabelValues(version).Set(1)
	return t
}

func result(err error) string {
	if err != nil {
		return "error"
	}
	return "success"
}

func (t *Telemetry) SetMqttConnected(connected bool) {
	if t == nil {
		return
	}
	if connected {
		t.mqttConnected.Set(1)
	} else {
		t.mqttConnected.Set(0)
	}
}

func (t *Telemetry) DeviceSeen(device EcoflowDevice) {
	if t == nil {
		return
	}
	t.lastMessage.WithLabelValues(device.Name, device.SN).Set(float64(time.Now().Unix()))
}

func (t *Telemetry) MqttMessage(sn, format string) {
	if t == nil {
		return
	}
	t.mqttMessages.WithLabelValues(sn, format).Inc()
}

func (t *Telemetry) DecodeError(sn, format string) {
	if t == nil {
		return
	}
	t.decodeErrors.WithLabelValues(sn, format).Inc()
}

func (t *Telemetry) MqttPing(sn, kind string, err error) {
	if t == nil {
		return
	}
	t.mqttPings.WithLabelValues(sn, kind, result(err)).Inc()
}

func (t *Telemetry) MqttForcedReconnect() {
	if t == nil {
		return
	}
	t.mqttReconnects.Inc()
}

func (t *Telemetry) MqttRelogin() {
	if t == nil {
		return
	}
	t.mqttRelogins.Inc()
}

func (t *Telemetry) RestRequest(endpoint string, err error) {
	if t == nil {
		return
	}
	t.restRequests.WithLabelValues(endpoint, result(err)).Inc()
}
