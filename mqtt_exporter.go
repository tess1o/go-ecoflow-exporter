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
	"maps"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/tess1o/go-ecoflow"
	"go-ecoflow-exporter/internal/protobuf"
)

type MqttExporterConfig struct {
	Email    string
	Password string
	// Devices maps a serial number to its display name
	Devices              map[string]string
	OfflineThreshold     time.Duration
	PingInterval         time.Duration
	StateRefreshInterval time.Duration
	// ProtobufDebug dumps protobuf messages to the log
	ProtobufDebug bool
	Telemetry     *Telemetry
}

type MqttMetricsExporter struct {
	email                string
	password             string
	client               atomic.Pointer[ecoflow.MqttClient] // nil until logged in
	devices              map[string]string
	handlers             []MetricHandler
	offlineThreshold     time.Duration
	userId               string
	pingInterval         time.Duration
	stateRefreshInterval time.Duration
	deviceState          map[string]map[string]any // SN -> full device state
	stateMu              sync.RWMutex
	protobufDecoder      *protobuf.ProtobufDecoder // Persists to track seen devices
	telemetry            *Telemetry

	statusMu       sync.Mutex
	lastReceived   map[string]time.Time // SN -> time of the last message
	offlineSent    map[string]time.Time // SN -> time of the last offline update, present while the device is offline
	reconnectDelay time.Duration        // grows while forced reconnects do not bring data back
	nextReconnect  time.Time

	pingMu     sync.Mutex
	pingOK     map[string]int // SN -> successful keepalive pings
	pingFailed map[string]int // SN -> failed keepalive pings
}

const (
	maxReconnectDelay = 10 * time.Minute
	loginRetryDelay   = 5 * time.Second
	maxLoginDelay     = 5 * time.Minute
)

func NewMqttMetricsExporter(cfg MqttExporterConfig, handlers ...MetricHandler) (*MqttMetricsExporter, error) {
	// Validate inputs
	if cfg.Email == "" || cfg.Password == "" {
		return nil, errors.New("email and password are required")
	}
	if len(cfg.Devices) == 0 {
		return nil, errors.New("at least one device must be configured")
	}
	if len(handlers) == 0 {
		return nil, errors.New("at least one metric handler is required")
	}
	if cfg.OfflineThreshold <= 0 {
		return nil, fmt.Errorf("offlineThreshold must be positive, got %v", cfg.OfflineThreshold)
	}
	if cfg.PingInterval <= 0 {
		return nil, fmt.Errorf("pingInterval must be positive, got %v", cfg.PingInterval)
	}
	if cfg.StateRefreshInterval <= 0 {
		return nil, fmt.Errorf("stateRefreshInterval must be positive, got %v", cfg.StateRefreshInterval)
	}
	if cfg.PingInterval > cfg.OfflineThreshold {
		slog.Warn("pingInterval is greater than offlineThreshold, devices may be marked offline between pings",
			"pingInterval", cfg.PingInterval, "offlineThreshold", cfg.OfflineThreshold)
	}

	return &MqttMetricsExporter{
		email:                cfg.Email,
		password:             cfg.Password,
		devices:              cfg.Devices,
		handlers:             handlers,
		offlineThreshold:     cfg.OfflineThreshold,
		pingInterval:         cfg.PingInterval,
		stateRefreshInterval: cfg.StateRefreshInterval,
		deviceState:          make(map[string]map[string]any),
		protobufDecoder:      protobuf.NewProtobufDecoder(cfg.ProtobufDebug),
		telemetry:            cfg.Telemetry,
	}, nil
}

// login gets the user id (needed for the ping mechanism) and the MQTT connection settings
func (m *MqttMetricsExporter) login(ctx context.Context) (*ecoflow.MqttClient, error) {
	userId, err := getUserId(ctx, m.email, m.password)
	if err != nil {
		return nil, fmt.Errorf("failed to get user ID: %w", err)
	}
	m.userId = userId
	configuration := ecoflow.MqttClientConfiguration{
		Email:            m.email,
		Password:         m.password,
		OnConnect:        m.OnConnect,
		OnConnectionLost: m.OnConnectionLost,
		OnReconnect:      m.OnReconnect,
	}
	return ecoflow.NewMqttClient(ctx, configuration)
}

// Run logs in, connects to the broker and keeps devices reporting until ctx is cancelled.
// Login is retried, so a temporary EcoFlow outage at startup does not stop the exporter.
func (m *MqttMetricsExporter) Run(ctx context.Context) error {
	var client *ecoflow.MqttClient
	err := retryWithBackoff(ctx, "log in to EcoFlow", 0, loginRetryDelay, maxLoginDelay, func() error {
		var err error
		client, err = m.login(ctx)
		return err
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	m.client.Store(client)

	token := client.Client.Connect()
	select {
	case <-token.Done():
		if err := token.Error(); err != nil {
			return fmt.Errorf("unable to connect to MQTT broker: %w", err)
		}
	case <-ctx.Done():
		client.Client.Disconnect(250)
		return nil
	}

	go m.monitorDeviceStatus(ctx)
	go m.pingRoutine(ctx)
	slog.Info("Started RTC time ping routine to keep devices active")

	<-ctx.Done()
	client.Client.Disconnect(250)
	m.telemetry.SetMqttConnected(false)
	return nil
}

// mqtt returns the MQTT client, it must only be called once Run has logged in
func (m *MqttMetricsExporter) mqtt() *ecoflow.MqttClient {
	return m.client.Load()
}

// Health reports an error while the exporter is not connected to the MQTT broker
func (m *MqttMetricsExporter) Health() error {
	c := m.mqtt()
	if c == nil {
		return errors.New("not logged in to EcoFlow yet")
	}
	if !c.Client.IsConnectionOpen() {
		return errors.New("not connected to the EcoFlow MQTT broker")
	}
	return nil
}

// markReceived records that the device sent data and resets the forced reconnect backoff.
// It reports whether the device was considered offline.
func (m *MqttMetricsExporter) markReceived(sn string) (wasOffline bool) {
	m.statusMu.Lock()
	defer m.statusMu.Unlock()
	if m.lastReceived == nil {
		m.lastReceived = make(map[string]time.Time)
	}
	m.lastReceived[sn] = time.Now()
	m.reconnectDelay = 0
	m.nextReconnect = time.Time{}
	_, wasOffline = m.offlineSent[sn]
	delete(m.offlineSent, sn)
	return wasOffline
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

	if m.markReceived(serialNumber) {
		slog.Info("Device is back online, requesting its full state", "device", serialNumber)
		// not from this callback: paho must not be blocked while the request is published
		go func() {
			if err := m.requestLatestQuotas(serialNumber); err != nil {
				slog.Error("Failed to request latest quotas", "error", err, "device", serialNumber)
			}
		}()
	}

	// Detect message format and decode
	payload := msg.Payload()
	format := protobuf.DetectFormat(payload)
	m.telemetry.MqttMessage(serialNumber, format.String())

	var params map[string]any
	var err error

	switch format {
	case protobuf.FormatJSON:
		// Existing JSON path
		var deviceParams ecoflow.MqttDeviceParams
		err = json.Unmarshal(payload, &deviceParams)
		if err != nil {
			m.telemetry.DecodeError(serialNumber, format.String())
			slog.Error("Unable to parse JSON message", "topic", msg.Topic(), "error", err)
			return
		}
		params = deviceParams.Params
		slog.Debug("Received JSON device parameters", "topic", msg.Topic(), "param_count", len(params))

	case protobuf.FormatProtobuf:
		// New protobuf path - log first occurrence per device
		m.protobufDecoder.LogFirstProtobufMessage(serialNumber)

		params, err = m.protobufDecoder.DecodeProtobufMessage(payload)
		if err != nil {
			m.telemetry.DecodeError(serialNumber, format.String())
			slog.Error("Unable to parse protobuf message",
				"topic", msg.Topic(),
				"device", serialNumber,
				"error", err,
				"payload_len", len(payload))
			return
		}
		slog.Debug("Received protobuf device parameters", "topic", msg.Topic(), "param_count", len(params))

	case protobuf.FormatUnknown:
		m.telemetry.DecodeError(serialNumber, format.String())
		previewLen := min(32, len(payload))
		slog.Info("Unknown message format - not JSON or protobuf",
			"topic", msg.Topic(),
			"device", serialNumber,
			"payload_len", len(payload),
			"payload_preview", fmt.Sprintf("%x", payload[:previewLen]),
			"help", "Please report this to https://github.com/tess1o/go-ecoflow-exporter/issues")
		return
	}

	// Skip if no parameters received
	if len(params) == 0 {
		slog.Debug("Received empty parameters, skipping", "device", serialNumber)
		return
	}

	// Merge incoming params with cached state
	m.stateMu.Lock()
	if m.deviceState == nil {
		m.deviceState = make(map[string]map[string]any)
	}
	if m.deviceState[serialNumber] == nil {
		m.deviceState[serialNumber] = make(map[string]any)
	}
	maps.Copy(m.deviceState[serialNumber], params)
	// Handlers get a copy of the full state; it is shared between them and must not be modified
	fullState := maps.Clone(m.deviceState[serialNumber])
	m.stateMu.Unlock()

	// Skip calling handlers if we have no state (shouldn't happen but defensive)
	if len(fullState) == 0 {
		slog.Warn("No state available for device after merge", "device", serialNumber)
		return
	}

	// Pass full state (not just delta) to handlers
	device := EcoflowDevice{
		SN:     serialNumber,
		Name:   getDeviceName(m.devices, serialNumber),
		Online: 1,
	}
	m.telemetry.DeviceSeen(device)
	dispatch(context.Background(), m.handlers, NewDeviceUpdate(device, fullState))
}

func (m *MqttMetricsExporter) OnConnect(_ mqtt.Client) {
	slog.Info("Connected to the broker, trying to subscribe to the topics")
	m.telemetry.SetMqttConnected(true)
	m.initDeviceStatuses()
	for sn, name := range m.devices {
		err := m.mqtt().SubscribeForParameters(sn, m.MessageHandler)
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
	m.telemetry.SetMqttConnected(false)
}

func (m *MqttMetricsExporter) OnReconnect(_ mqtt.Client, _ *mqtt.ClientOptions) {
	slog.Info("Trying to reconnect to the broker...")
}

// monitorDeviceStatus reports devices that did not send data within the offline threshold as offline,
// and reconnects to the broker when no device sends data (the broker sometimes stops delivering messages,
// for example after the EcoFlow mobile app was opened and closed).
// It checks four times per threshold, but sends the offline update of a device once per threshold.
func (m *MqttMetricsExporter) monitorDeviceStatus(ctx context.Context) {
	ticker := time.NewTicker(max(m.offlineThreshold/4, 100*time.Millisecond))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if !m.mqtt().Client.IsConnected() {
			slog.Debug("MQTT client is not connected to the broker, we don't know the devices statuses...")
			continue
		}

		offline := m.offlineDevices()
		for _, sn := range m.offlineUpdatesDue(offline) {
			device := EcoflowDevice{SN: sn, Name: getDeviceName(m.devices, sn), Online: 0}
			dispatch(ctx, m.handlers, NewDeviceUpdate(device, map[string]any{}))
		}

		// If true it means that either all devices are offline or we don't receive any messages from MQTT broker.
		// Either way we need to reconnect the client
		if len(offline) > 0 && len(offline) == len(m.devices) && m.reconnectDue() {
			m.forceReconnect(ctx)
		}
	}
}

// offlineDevices returns the serial numbers of devices that did not send data within the offline threshold
func (m *MqttMetricsExporter) offlineDevices() []string {
	m.statusMu.Lock()
	defer m.statusMu.Unlock()
	var offline []string
	for sn, last := range m.lastReceived {
		if time.Since(last) > m.offlineThreshold {
			offline = append(offline, sn)
		}
	}
	return offline
}

// offlineUpdatesDue returns the offline devices whose last offline update is at least a threshold old,
// and records that an update is sent now
func (m *MqttMetricsExporter) offlineUpdatesDue(offline []string) []string {
	m.statusMu.Lock()
	defer m.statusMu.Unlock()
	if m.offlineSent == nil {
		m.offlineSent = make(map[string]time.Time)
	}
	now := time.Now()
	var due []string
	for _, sn := range offline {
		if last, sent := m.offlineSent[sn]; !sent || now.Sub(last) >= m.offlineThreshold {
			m.offlineSent[sn] = now
			due = append(due, sn)
		}
	}
	return due
}

// reconnectDue reports whether a forced reconnect may run now. Consecutive reconnects that do not bring
// any data back are spaced out exponentially (devices may simply be switched off).
func (m *MqttMetricsExporter) reconnectDue() bool {
	m.statusMu.Lock()
	defer m.statusMu.Unlock()
	now := time.Now()
	if now.Before(m.nextReconnect) {
		slog.Debug("All devices are offline, next reconnect attempt scheduled", "at", m.nextReconnect)
		return false
	}
	m.reconnectDelay = min(max(2*m.reconnectDelay, m.offlineThreshold), maxReconnectDelay)
	m.nextReconnect = now.Add(m.reconnectDelay)
	return true
}

func (m *MqttMetricsExporter) forceReconnect(ctx context.Context) {
	slog.Error("All devices are either offline or we don't receive messages from MQTT topic, we'll try to reconnect")
	m.telemetry.MqttForcedReconnect()
	m.mqtt().Client.Disconnect(250)
	m.telemetry.SetMqttConnected(false)

	select {
	case <-ctx.Done():
		return
	case <-time.After(5 * time.Second):
	}

	token := m.mqtt().Client.Connect()
	select {
	case <-ctx.Done():
	case <-token.Done():
		if err := token.Error(); err != nil {
			slog.Error("Unable to reconnect to the MQTT broker", "error", err)
		}
	case <-time.After(30 * time.Second):
		slog.Warn("Reconnect to the MQTT broker is taking long, it continues in the background")
	}
}

func (m *MqttMetricsExporter) initDeviceStatuses() {
	m.statusMu.Lock()
	defer m.statusMu.Unlock()
	if m.lastReceived == nil {
		m.lastReceived = make(map[string]time.Time)
	}
	// Unknown until the first message arrives: a device that sends nothing within the offline threshold
	// (counted from the connection) is offline
	now := time.Now()
	for sn := range m.devices {
		if _, seen := m.lastReceived[sn]; !seen {
			m.lastReceived[sn] = now
		}
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
	message := map[string]any{
		"id":          strconv.FormatInt(time.Now().UnixMilli(), 10),
		"version":     "1.1",
		"from":        "Android",
		"operateType": "latestQuotas",
		"params":      map[string]any{},
	}

	payload, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal latestQuotas message: %w", err)
	}

	topic := fmt.Sprintf("/app/%s/%s/thing/property/get", m.userId, sn)
	token := m.mqtt().Client.Publish(topic, 1, false, payload)

	// Wait with timeout
	if !token.WaitTimeout(5 * time.Second) {
		err = errors.New("publish timeout for latestQuotas request")
	} else if token.Error() != nil {
		err = fmt.Errorf("failed to request latest quotas: %w", token.Error())
	}
	m.telemetry.MqttPing(sn, "state_refresh", err)
	if err != nil {
		return err
	}

	slog.Info("Requested latest quotas from device", "device", sn, "topic", topic)
	return nil
}

// sendRtcTimePing sends a setRtcTime command to keep the connection alive
func (m *MqttMetricsExporter) sendRtcTimePing(sn string) error {
	now := time.Now()
	message := map[string]any{
		"from":        "Android",
		"id":          strconv.FormatInt(now.UnixMilli(), 10),
		"moduleType":  2,
		"operateType": "setRtcTime",
		"params": map[string]any{
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
		err = fmt.Errorf("failed to marshal ping: %w", err)
		m.recordPing(sn, err)
		return err
	}

	topic := fmt.Sprintf("/app/%s/%s/thing/property/set", m.userId, sn)
	token := m.mqtt().Client.Publish(topic, 1, false, payload)

	// Wait with timeout
	if !token.WaitTimeout(5 * time.Second) {
		err = errors.New("publish timeout")
	} else {
		err = token.Error()
	}
	m.recordPing(sn, err)
	if err != nil {
		return err
	}
	slog.Debug("Sent RTC time ping", "device", sn, "topic", topic)
	return nil
}

// recordPing counts the keepalive result and logs a warning when the failure rate is high
func (m *MqttMetricsExporter) recordPing(sn string, err error) {
	m.telemetry.MqttPing(sn, "keepalive", err)
	m.pingMu.Lock()
	defer m.pingMu.Unlock()
	if m.pingOK == nil {
		m.pingOK = make(map[string]int)
		m.pingFailed = make(map[string]int)
	}
	if err == nil {
		m.pingOK[sn]++
		return
	}
	m.pingFailed[sn]++

	failed, total := m.pingFailed[sn], m.pingOK[sn]+m.pingFailed[sn]
	// Log only if failure rate is concerning (>10%)
	if failRate := float64(failed) / float64(total); failRate > 0.1 {
		slog.Warn("High ping failure rate detected",
			"device", sn,
			"failed", failed,
			"successful", m.pingOK[sn],
			"failure_rate", fmt.Sprintf("%.1f%%", failRate*100),
			"error", err)
	}
}

// pingRoutine sends periodic pings to all devices and periodically requests a full state refresh
func (m *MqttMetricsExporter) pingRoutine(ctx context.Context) {
	ticker := time.NewTicker(m.pingInterval)
	defer ticker.Stop()

	tickCount := 0
	// Calculate how many pings between state refreshes
	ticksPerRefresh := max(int(m.stateRefreshInterval/m.pingInterval), 1)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if !m.mqtt().Client.IsConnected() {
			slog.Debug("MQTT client not connected, skipping ping")
			continue
		}

		tickCount++
		// Every Nth tick, do a full state refresh instead of ping
		useStateRefresh := tickCount%ticksPerRefresh == 0

		// Ping all devices in parallel and wait for all of them before the next tick
		var wg sync.WaitGroup
		for sn, name := range m.devices {
			wg.Go(func() {
				if useStateRefresh {
					slog.Debug("Sending state refresh", "device", sn, "device_name", name)
					if err := m.requestLatestQuotas(sn); err != nil {
						slog.Error("State refresh failed", "error", err, "device", sn, "device_name", name)
					}
				} else {
					slog.Debug("Sending keepalive ping", "device", sn, "device_name", name)
					if err := m.sendRtcTimePing(sn); err != nil {
						// Error already logged in sendRtcTimePing via recordPing
						slog.Debug("Ping failed", "error", err, "device", sn)
					}
				}
			})
		}
		wg.Wait()
	}
}
