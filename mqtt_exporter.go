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
	devices          []string
	handlers         []MetricHandler
	offlineThreshold time.Duration
}

func NewMqttMetricsExporter(email, password string, devices []string, offlineThreshold time.Duration, handlers ...MetricHandler) (*MqttMetricsExporter, error) {
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

func (e *MqttMetricsExporter) ExportMetrics() error {
	err := e.c.Connect()
	if err != nil {
		slog.Error("Unable to connect to ")
		return err
	}
	go e.monitorDeviceStatus()
	return nil
}

func (e *MqttMetricsExporter) MessageHandler(_ mqtt.Client, msg mqtt.Message) {
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
		for _, handler := range e.handlers {
			hh := handler
			go hh.Handle(context.Background(), ecoflow.DeviceInfo{
				SN:     serialNumber,
				Online: 1,
			}, params.Params)
		}
	}
}

func (e *MqttMetricsExporter) OnConnect(_ mqtt.Client) {
	slog.Info("Connected to the broker, trying to subscribe to the topics")
	e.initDeviceStatuses()
	for _, d := range e.devices {
		err := e.c.SubscribeForParameters(d, e.MessageHandler)
		if err != nil {
			slog.Error("Unable to subscribe for parameters", "error", err, "device", d)
		} else {
			slog.Info("Subscribed to receive parameters", "device", d)
		}
	}
}

func (e *MqttMetricsExporter) OnConnectionLost(_ mqtt.Client, err error) {
	slog.Error("Lost connection to the broker", "error", err)
}

func (e *MqttMetricsExporter) OnReconnect(_ mqtt.Client, _ *mqtt.ClientOptions) {
	slog.Info("Trying to reconnect to the broker...")
}

func (e *MqttMetricsExporter) monitorDeviceStatus() {
	for {
		time.Sleep(e.offlineThreshold)
		if !e.c.Client.IsConnected() {
			slog.Debug("MQTT client is not connected to the broker, we don't know the devices statuses...")
			continue
		}
		var offlineDevicesCount = 0
		for sn, status := range deviceStatuses {
			if time.Since(status.LastReceived) > e.offlineThreshold {
				offlineDevicesCount = offlineDevicesCount + 1
				for _, handler := range e.handlers {
					hh := handler
					go hh.Handle(context.Background(), ecoflow.DeviceInfo{
						SN:     sn,
						Online: 0,
					}, map[string]interface{}{})
				}
			}
		}
		//if true it means that either all devices are offline or we don't receive any messages from MQTT broker.
		//either way we need to reconnect the client
		if len(e.devices) == offlineDevicesCount {
			slog.Error("All devices are either offline or we don't receive messages from MQTT topic, we'll try to reconnect")
			e.c.Client.Disconnect(250)
			time.Sleep(5 * time.Second)
			e.c.Client.Connect()
		}
	}
}

func (e *MqttMetricsExporter) initDeviceStatuses() {
	mu.Lock()
	defer mu.Unlock()
	pastTime := time.Now().Add(-e.offlineThreshold * 2) // Set to a time far in the past
	for _, sn := range e.devices {
		deviceStatuses[sn] = &DeviceStatus{LastReceived: pastTime}
	}
}

// assuming that SN is the last part of the topic ("/app/device/property/${sn}")
func getSnFromTopic(topic string) string {
	topicStr := strings.Split(topic, "/")
	return topicStr[len(topicStr)-1]
}