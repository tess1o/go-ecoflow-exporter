package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/tess1o/go-ecoflow"
)

type DeviceStatus struct {
	LastReceived time.Time
}

var (
	deviceStatuses = make(map[string]*DeviceStatus)
	mu             sync.Mutex
)

type PingStats struct {
	sync.Mutex
	successful  map[string]int       // SN -> success count
	failed      map[string]int       // SN -> failure count
	lastAttempt map[string]time.Time // SN -> last attempt time
	lastSuccess map[string]time.Time // SN -> last success time
}

type MqttMetricsExporter struct {
	c                    *ecoflow.MqttClient
	devices              map[string]string
	handlers             []MetricHandler
	offlineThreshold     time.Duration
	userId               string
	pingInterval         time.Duration
	stateRefreshInterval time.Duration
	deviceState          map[string]map[string]interface{} // SN -> full device state
	stateMu              sync.RWMutex
	pingStats            *PingStats
}

func NewMqttMetricsExporter(email, password string, devices map[string]string, offlineThreshold, pingInterval, stateRefreshInterval time.Duration, handlers ...MetricHandler) (*MqttMetricsExporter, error) {
	// Validate inputs
	if email == "" || password == "" {
		return nil, errors.New("email and password are required")
	}
	if len(devices) == 0 {
		return nil, errors.New("at least one device must be configured")
	}
	if len(handlers) == 0 {
		return nil, errors.New("at least one metric handler is required")
	}
	if offlineThreshold <= 0 {
		return nil, fmt.Errorf("offlineThreshold must be positive, got %v", offlineThreshold)
	}
	if pingInterval <= 0 {
		return nil, fmt.Errorf("pingInterval must be positive, got %v", pingInterval)
	}
	if stateRefreshInterval <= 0 {
		return nil, fmt.Errorf("stateRefreshInterval must be positive, got %v", stateRefreshInterval)
	}
	if pingInterval > offlineThreshold {
		slog.Warn("pingInterval is greater than offlineThreshold, devices may be marked offline between pings",
			"pingInterval", pingInterval, "offlineThreshold", offlineThreshold)
	}

	// Get userId first (needed for ping mechanism)
	userId, err := getUserId(context.Background(), email, password)
	if err != nil {
		return nil, fmt.Errorf("failed to get user ID: %w", err)
	}

	exporter := &MqttMetricsExporter{
		devices:              devices,
		handlers:             handlers,
		offlineThreshold:     offlineThreshold,
		pingInterval:         pingInterval,
		stateRefreshInterval: stateRefreshInterval,
		userId:               userId,
		deviceState:          make(map[string]map[string]interface{}),
		pingStats: &PingStats{
			successful:  make(map[string]int),
			failed:      make(map[string]int),
			lastAttempt: make(map[string]time.Time),
			lastSuccess: make(map[string]time.Time),
		},
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
		slog.Error("Unable to connect to MQTT broker")
		return err
	}
	go m.monitorDeviceStatus()
	// Start periodic ping to keep connection alive
	m.startPingRoutine()
	slog.Info("Started RTC time ping routine to keep devices active")
	return nil
}

func (m *MqttMetricsExporter) MessageHandler(_ mqtt.Client, msg mqtt.Message) {
	serialNumber := getSnFromTopic(msg.Topic())

	// Skip if serial number extraction failed
	if serialNumber == "" {
		slog.Warn("Could not extract serial number from topic", "topic", msg.Topic())
		return
	}

	// Validate: only process configured devices
	if _, isConfigured := m.devices[serialNumber]; !isConfigured {
		slog.Warn("Received message for unconfigured device, ignoring", "sn", serialNumber, "topic", msg.Topic())
		return
	}

	mu.Lock()
	if _, exists := deviceStatuses[serialNumber]; !exists {
		deviceStatuses[serialNumber] = &DeviceStatus{}
	}
	deviceStatuses[serialNumber].LastReceived = time.Now()
	mu.Unlock()

	var params ecoflow.MqttDeviceParams
	err := json.Unmarshal(msg.Payload(), &params)
	if err != nil {
		slog.Error("Unable to parse message", "message", msg.Payload(), "topic", msg.Topic(), "error", err)
		return
	}

	slog.Debug("Received device parameters", "topic", msg.Topic(), "param_count", len(params.Params))

	// Skip if no parameters received
	if len(params.Params) == 0 {
		slog.Debug("Received empty parameters, skipping", "device", serialNumber)
		return
	}

	// Merge incoming params with cached state
	m.stateMu.Lock()
	if m.deviceState[serialNumber] == nil {
		m.deviceState[serialNumber] = make(map[string]interface{})
	}
	for k, v := range params.Params {
		m.deviceState[serialNumber][k] = v
	}
	// Create a copy of the full state to pass to handlers
	fullState := make(map[string]interface{})
	for k, v := range m.deviceState[serialNumber] {
		fullState[k] = v
	}
	m.stateMu.Unlock()

	// Skip calling handlers if we have no state (shouldn't happen but defensive)
	if len(fullState) == 0 {
		slog.Warn("No state available for device after merge", "device", serialNumber)
		return
	}

	// Pass full state (not just delta) to handlers
	for _, handler := range m.handlers {
		hh := handler
		go hh.Handle(context.Background(), EcoflowDevice{
			SN:     serialNumber,
			Name:   getDeviceName(m.devices, serialNumber),
			Online: 1,
		}, fullState)
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
			// Request initial data from device (like the official app does)
			if err := m.requestLatestQuotas(sn); err != nil {
				slog.Error("Failed to request initial quotas", "error", err, "device", sn, "device_name", name)
			}
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

		// Lock while reading deviceStatuses to prevent race condition
		mu.Lock()
		for sn, status := range deviceStatuses {
			if time.Since(status.LastReceived) > m.offlineThreshold {
				offlineDevicesCount++
				// Call handlers outside the critical section by copying needed data
				deviceSN := sn
				deviceName := getDeviceName(m.devices, sn)

				for _, handler := range m.handlers {
					hh := handler
					go hh.Handle(context.Background(), EcoflowDevice{
						SN:     deviceSN,
						Name:   deviceName,
						Online: 0,
					}, map[string]interface{}{})
				}
			}
		}
		mu.Unlock()

		// If true it means that either all devices are offline or we don't receive any messages from MQTT broker.
		// Either way we need to reconnect the client
		if len(m.devices) == offlineDevicesCount && offlineDevicesCount > 0 {
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
	// Iterate over keys (serial numbers), not values (device names)
	for sn := range m.devices {
		deviceStatuses[sn] = &DeviceStatus{LastReceived: pastTime}
	}
}

// getSnFromTopic extracts serial number from MQTT topic
// Assumes SN is the last part of the topic ("/app/device/property/${sn}")
// Returns empty string if topic format is invalid
func getSnFromTopic(topic string) string {
	if topic == "" {
		return ""
	}
	topicStr := strings.Split(topic, "/")
	if len(topicStr) == 0 {
		return ""
	}
	sn := topicStr[len(topicStr)-1]
	// Basic validation: SN should not be empty
	if sn == "" {
		slog.Warn("Invalid topic format: empty serial number", "topic", topic)
		return ""
	}
	return sn
}

// getUserId gets the userId by logging in to EcoFlow API
func getUserId(ctx context.Context, email, password string) (string, error) {
	params := map[string]string{
		"email":    email,
		"password": base64.StdEncoding.EncodeToString([]byte(password)),
		"scene":    "IOT_APP",
		"userType": "ECOFLOW",
	}
	jsonParams, err := json.Marshal(params)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.ecoflow.com/auth/login", bytes.NewReader(jsonParams))
	if err != nil {
		return "", fmt.Errorf("failed to create login request: %w", err)
	}

	req.Header.Add("lang", "en_US")
	req.Header.Add("content-type", "application/json")

	client := http.Client{
		Timeout: 30 * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to execute login request: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			slog.Warn("Failed to close response body", "error", closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("login failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	var loginResponse struct {
		Data struct {
			User struct {
				UserId string `json:"userId"`
			} `json:"user"`
		} `json:"data"`
	}

	err = json.Unmarshal(responseBody, &loginResponse)
	if err != nil {
		return "", fmt.Errorf("unable to unmarshal login response: %w", err)
	}

	if loginResponse.Data.User.UserId == "" {
		return "", fmt.Errorf("empty userId in login response: %s", string(responseBody))
	}

	return loginResponse.Data.User.UserId, nil
}

// requestLatestQuotas requests the device to start pushing data (initial data request)
func (m *MqttMetricsExporter) requestLatestQuotas(sn string) error {
	message := map[string]interface{}{
		"id":          strconv.FormatInt(time.Now().UnixMilli(), 10),
		"version":     "1.1",
		"from":        "Android",
		"operateType": "latestQuotas",
		"params":      map[string]interface{}{},
	}

	payload, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal latestQuotas message: %w", err)
	}

	topic := fmt.Sprintf("/app/%s/%s/thing/property/get", m.userId, sn)
	token := m.c.Client.Publish(topic, 1, false, payload)

	// Wait with timeout
	if !token.WaitTimeout(5 * time.Second) {
		return errors.New("publish timeout for latestQuotas request")
	}

	if token.Error() != nil {
		return fmt.Errorf("failed to request latest quotas: %w", token.Error())
	}

	slog.Info("Requested latest quotas from device", "device", sn, "topic", topic)
	return nil
}

// sendRtcTimePing sends a setRtcTime command to keep the connection alive
func (m *MqttMetricsExporter) sendRtcTimePing(sn string) error {
	now := time.Now()

	// Track attempt
	m.pingStats.Lock()
	m.pingStats.lastAttempt[sn] = now
	m.pingStats.Unlock()

	message := map[string]interface{}{
		"from":        "Android",
		"id":          strconv.FormatInt(time.Now().UnixMilli(), 10),
		"moduleType":  2,
		"operateType": "setRtcTime",
		"params": map[string]interface{}{
			"min":   now.Minute(),
			"day":   now.Day(),
			"week":  int(now.Weekday()),
			"sec":   now.Second(),
			"month": int(now.Month()),
			"hour":  now.Hour(),
			"year":  now.Year(),
		},
		"version": "1.0",
	}

	payload, err := json.Marshal(message)
	if err != nil {
		m.recordPingFailure(sn, err)
		return fmt.Errorf("failed to marshal ping: %w", err)
	}

	topic := fmt.Sprintf("/app/%s/%s/thing/property/set", m.userId, sn)
	token := m.c.Client.Publish(topic, 1, false, payload)

	// Wait with timeout
	if !token.WaitTimeout(5 * time.Second) {
		err := errors.New("publish timeout")
		m.recordPingFailure(sn, err)
		return err
	}

	if token.Error() != nil {
		m.recordPingFailure(sn, token.Error())
		return token.Error()
	}

	m.recordPingSuccess(sn)
	slog.Debug("Sent RTC time ping", "device", sn, "topic", topic)
	return nil
}

// recordPingSuccess records a successful ping for statistics
func (m *MqttMetricsExporter) recordPingSuccess(sn string) {
	m.pingStats.Lock()
	defer m.pingStats.Unlock()
	m.pingStats.successful[sn]++
	m.pingStats.lastSuccess[sn] = time.Now()
}

// recordPingFailure records a failed ping and logs warning if failure rate is high
func (m *MqttMetricsExporter) recordPingFailure(sn string, err error) {
	m.pingStats.Lock()
	defer m.pingStats.Unlock()
	m.pingStats.failed[sn]++

	total := m.pingStats.successful[sn] + m.pingStats.failed[sn]
	if total > 0 {
		failRate := float64(m.pingStats.failed[sn]) / float64(total)
		// Log only if failure rate is concerning (>10%)
		if failRate > 0.1 {
			slog.Warn("High ping failure rate detected",
				"device", sn,
				"failed", m.pingStats.failed[sn],
				"successful", m.pingStats.successful[sn],
				"failure_rate", fmt.Sprintf("%.1f%%", failRate*100),
				"error", err)
		}
	}
}

// startPingRoutine starts a goroutine that sends periodic pings to all devices
// and periodically requests full state refresh
func (m *MqttMetricsExporter) startPingRoutine() {
	go func() {
		ticker := time.NewTicker(m.pingInterval)
		defer ticker.Stop()

		tickCount := 0
		// Calculate how many pings between state refreshes
		ticksPerRefresh := int(m.stateRefreshInterval / m.pingInterval)
		if ticksPerRefresh < 1 {
			ticksPerRefresh = 1
		}

		for range ticker.C {
			if !m.c.Client.IsConnected() {
				slog.Debug("MQTT client not connected, skipping ping")
				continue
			}

			tickCount++

			// Every Nth tick, do a full state refresh instead of ping
			useStateRefresh := (tickCount % ticksPerRefresh) == 0

			// Ping all devices in parallel
			var wg sync.WaitGroup
			for sn, name := range m.devices {
				wg.Add(1)
				go func(serialNum, deviceName string) {
					defer wg.Done()
					if useStateRefresh {
						slog.Debug("Sending state refresh", "device", serialNum, "device_name", deviceName)
						if err := m.requestLatestQuotas(serialNum); err != nil {
							slog.Error("State refresh failed", "error", err, "device", serialNum, "device_name", deviceName)
						}
					} else {
						slog.Debug("Sending keepalive ping", "device", serialNum, "device_name", deviceName)
						if err := m.sendRtcTimePing(serialNum); err != nil {
							// Error already logged in sendRtcTimePing via recordPingFailure
							slog.Debug("Ping failed", "error", err, "device", serialNum)
						}
					}
				}(sn, name)
			}
			// Wait for all pings/refreshes to complete before next tick
			wg.Wait()
		}
	}()
}
