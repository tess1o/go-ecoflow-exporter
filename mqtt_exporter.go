package main

import (
	"context"
	"encoding/json"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/tess1o/go-ecoflow"
	"log/slog"
	"strings"
	"sync"
	"time"
)

type DeviceStatus struct {
	LastReceived time.Time
}

var (
	deviceStatuses = make(map[string]*DeviceStatus)
	mu             sync.Mutex
)

type MqttMetricsExporter struct {
	c                *ecoflow.MqttClient
	devices          map[string]string
	handlers         []MetricHandler
	offlineThreshold time.Duration
}

func NewMqttMetricsExporter(email, password string, devices map[string]string, offlineThreshold time.Duration, handlers ...MetricHandler) (*MqttMetricsExporter, error) {
	exporter := &MqttMetricsExporter{
		devices:          devices,
		handlers:         handlers,
		offlineThreshold: offlineThreshold,
	}
	configuration := ecoflow.MqttClientConfiguration{
		Email:            email,
		Password:         password,
		OnConnect:        exporter.OnConnect,
		OnConnectionLost: exporter.OnConnectionLost,
		OnReconnect:      exporter.OnReconnect,
	}
	client, err := ecoflow.NewMqttClient(context.Background(), configuration)
	if err != nil {
		return nil, err
	}

	exporter.c = client

	return exporter, nil
}

func (m *MqttMetricsExporter) ExportMetrics() error {
	err := m.c.Connect()
	if err != nil {
		slog.Error("Unable to connect to ")
		return err
	}
	go m.monitorDeviceStatus()
	return nil
}

func (m *MqttMetricsExporter) MessageHandler(_ mqtt.Client, msg mqtt.Message) {
	mu.Lock()
	defer mu.Unlock()

	serialNumber := getSnFromTopic(msg.Topic())

	if _, exists := deviceStatuses[serialNumber]; !exists {
		deviceStatuses[serialNumber] = &DeviceStatus{}
	}

	deviceStatuses[serialNumber].LastReceived = time.Now()

	var params ecoflow.MqttDeviceParams
	err := json.Unmarshal(msg.Payload(), &params)
	if err != nil {
		slog.Error("Unable to parse message", "message", msg.Payload(), "topic", msg.Topic(), "error", err)
	} else {
		slog.Debug("Received device parameters", "topic", msg.Topic(), "params", params)
		for _, handler := range m.handlers {
			hh := handler
			go hh.Handle(context.Background(), EcoflowDevice{
				SN:     serialNumber,
				Name:   getDeviceName(m.devices, serialNumber),
				Online: 1,
			}, params.Params)
		}
	}
}

func (m *MqttMetricsExporter) OnConnect(_ mqtt.Client) {
	slog.Info("Connected to the broker, trying to subscribe to the topics")
	m.initDeviceStatuses()
	for sn, name := range m.devices {
		err := m.c.SubscribeForParameters(sn, m.MessageHandler)
		if err != nil {
			slog.Error("Unable to subscribe for parameters", "error", err, "device", sn, "device_name", name)
		} else {
			slog.Info("Subscribed to receive parameters", "device", sn, "device_name", name)
		}
	}
}

func (m *MqttMetricsExporter) OnConnectionLost(_ mqtt.Client, err error) {
	slog.Error("Lost connection to the broker", "error", err)
}

func (m *MqttMetricsExporter) OnReconnect(_ mqtt.Client, _ *mqtt.ClientOptions) {
	slog.Info("Trying to reconnect to the broker...")
}

func (m *MqttMetricsExporter) monitorDeviceStatus() {
	for {
		time.Sleep(m.offlineThreshold)
		if !m.c.Client.IsConnected() {
			slog.Debug("MQTT client is not connected to the broker, we don't know the devices statuses...")
			continue
		}
		var offlineDevicesCount = 0
		for sn, status := range deviceStatuses {
			if time.Since(status.LastReceived) > m.offlineThreshold {
				offlineDevicesCount = offlineDevicesCount + 1
				for _, handler := range m.handlers {
					hh := handler
					go hh.Handle(context.Background(), EcoflowDevice{
						SN:     sn,
						Name:   getDeviceName(m.devices, sn),
						Online: 0,
					}, map[string]interface{}{})
				}
			}
		}
		//if true it means that either all devices are offline or we don't receive any messages from MQTT broker.
		//either way we need to reconnect the client
		if len(m.devices) == offlineDevicesCount {
			slog.Error("All devices are either offline or we don't receive messages from MQTT topic, we'll try to reconnect")
			m.c.Client.Disconnect(250)
			time.Sleep(5 * time.Second)
			m.c.Client.Connect()
		}
	}
}

func (m *MqttMetricsExporter) initDeviceStatuses() {
	mu.Lock()
	defer mu.Unlock()
	pastTime := time.Now().Add(-m.offlineThreshold * 2) // Set to a time far in the past
	for _, sn := range m.devices {
		deviceStatuses[sn] = &DeviceStatus{LastReceived: pastTime}
	}
}

// assuming that SN is the last part of the topic ("/app/device/property/${sn}")
func getSnFromTopic(topic string) string {
	topicStr := strings.Split(topic, "/")
	return topicStr[len(topicStr)-1]
}
