package main

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tess1o/go-ecoflow"
	"go-ecoflow-exporter/internal/protobuf"
	"google.golang.org/protobuf/proto"
)

// MockMetricHandler is a test handler that captures received metrics
type MockMetricHandler struct {
	mu             sync.Mutex
	receivedParams []map[string]interface{}
	receivedDevice []EcoflowDevice
}

func (m *MockMetricHandler) Handle(ctx context.Context, device EcoflowDevice, rawParameters map[string]interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Deep copy the params to avoid race conditions
	paramsCopy := make(map[string]interface{})
	for k, v := range rawParameters {
		paramsCopy[k] = v
	}

	m.receivedParams = append(m.receivedParams, paramsCopy)
	m.receivedDevice = append(m.receivedDevice, device)
}

func (m *MockMetricHandler) GetReceivedParams() []map[string]interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.receivedParams
}

func (m *MockMetricHandler) GetLastParams() map[string]interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.receivedParams) == 0 {
		return nil
	}
	return m.receivedParams[len(m.receivedParams)-1]
}

func (m *MockMetricHandler) GetReceivedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.receivedParams)
}

func (m *MockMetricHandler) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.receivedParams = nil
	m.receivedDevice = nil
}

// TestMqttExporter_StateCache_PartialUpdates tests the critical bug fix for stale data
// This is the MOST IMPORTANT test - verifies that partial MQTT updates don't cause stale metrics
func TestMqttExporter_StateCache_PartialUpdates(t *testing.T) {
	handler := &MockMetricHandler{}

	devices := map[string]string{
		"TEST123": "TestDevice",
	}

	exporter := &MqttMetricsExporter{
		devices:              devices,
		handlers:             []MetricHandler{handler},
		offlineThreshold:     60 * time.Second,
		userId:               "testuser",
		pingInterval:         20 * time.Second,
		stateRefreshInterval: 300 * time.Second,
		deviceState:          make(map[string]map[string]interface{}),
		stateMu:              sync.RWMutex{},
		pingStats: &PingStats{
			successful:  make(map[string]int),
			failed:      make(map[string]int),
			lastAttempt: make(map[string]time.Time),
			lastSuccess: make(map[string]time.Time),
		},
		protobufDecoder: protobuf.NewProtobufDecoder(),
	}

	// Initialize device statuses
	mu.Lock()
	deviceStatuses["TEST123"] = &DeviceStatus{LastReceived: time.Now()}
	mu.Unlock()

	// Simulate first MQTT message with full state
	msg1 := ecoflow.MqttDeviceParams{
		Params: map[string]interface{}{
			"battery.level": 85.5,
			"ac.voltage":    220.0,
			"dc.current":    5.2,
		},
	}
	payload1, _ := json.Marshal(msg1)

	mockMsg1 := &mockMQTTMessage{
		topic:   "/app/device/property/TEST123",
		payload: payload1,
	}

	exporter.MessageHandler(nil, mockMsg1)

	// Wait for handler to process
	time.Sleep(100 * time.Millisecond)

	// Verify first update has all parameters
	firstParams := handler.GetLastParams()
	require.NotNil(t, firstParams)
	assert.Equal(t, 85.5, firstParams["battery.level"], "Battery level should be 85.5")
	assert.Equal(t, 220.0, firstParams["ac.voltage"], "AC voltage should be 220.0")
	assert.Equal(t, 5.2, firstParams["dc.current"], "DC current should be 5.2")

	// CRITICAL TEST: Simulate second MQTT message with PARTIAL update (only battery changed)
	// This simulates the real-world scenario: power outage occurs, AC voltage drops to 0,
	// but device only sends battery update
	msg2 := ecoflow.MqttDeviceParams{
		Params: map[string]interface{}{
			"battery.level": 84.0, // Only battery level updated
		},
	}
	payload2, _ := json.Marshal(msg2)

	mockMsg2 := &mockMQTTMessage{
		topic:   "/app/device/property/TEST123",
		payload: payload2,
	}

	handler.Reset()
	exporter.MessageHandler(nil, mockMsg2)

	// Wait for handler to process
	time.Sleep(100 * time.Millisecond)

	// CRITICAL ASSERTION: Handler should receive FULL state, not just the delta
	secondParams := handler.GetLastParams()
	require.NotNil(t, secondParams)

	assert.Equal(t, 84.0, secondParams["battery.level"], "Battery level should be updated to 84.0")
	assert.Equal(t, 220.0, secondParams["ac.voltage"], "AC voltage should STILL be 220.0 (from cache)")
	assert.Equal(t, 5.2, secondParams["dc.current"], "DC current should STILL be 5.2 (from cache)")

	// Verify we have all 3 parameters (not just the 1 that was sent)
	assert.Equal(t, 3, len(secondParams), "Should have full state with 3 parameters, not just the delta")
}

// TestMqttExporter_StateCache_MultipleDevices tests state cache isolation between devices
func TestMqttExporter_StateCache_MultipleDevices(t *testing.T) {
	handler := &MockMetricHandler{}

	devices := map[string]string{
		"DEVICE1": "Delta2",
		"DEVICE2": "RiverPro",
	}

	exporter := &MqttMetricsExporter{
		devices:              devices,
		handlers:             []MetricHandler{handler},
		offlineThreshold:     60 * time.Second,
		userId:               "testuser",
		pingInterval:         20 * time.Second,
		stateRefreshInterval: 300 * time.Second,
		deviceState:          make(map[string]map[string]interface{}),
		stateMu:              sync.RWMutex{},
		pingStats: &PingStats{
			successful:  make(map[string]int),
			failed:      make(map[string]int),
			lastAttempt: make(map[string]time.Time),
			lastSuccess: make(map[string]time.Time),
		},
		protobufDecoder: protobuf.NewProtobufDecoder(),
	}

	// Initialize device statuses
	mu.Lock()
	deviceStatuses["DEVICE1"] = &DeviceStatus{LastReceived: time.Now()}
	deviceStatuses["DEVICE2"] = &DeviceStatus{LastReceived: time.Now()}
	mu.Unlock()

	// Send data for DEVICE1
	msg1 := ecoflow.MqttDeviceParams{
		Params: map[string]interface{}{
			"battery.level": 85.5,
			"ac.voltage":    220.0,
		},
	}
	payload1, _ := json.Marshal(msg1)
	mockMsg1 := &mockMQTTMessage{
		topic:   "/app/device/property/DEVICE1",
		payload: payload1,
	}
	exporter.MessageHandler(nil, mockMsg1)

	// Send DIFFERENT data for DEVICE2
	msg2 := ecoflow.MqttDeviceParams{
		Params: map[string]interface{}{
			"battery.level": 45.0,
			"ac.voltage":    110.0,
		},
	}
	payload2, _ := json.Marshal(msg2)
	mockMsg2 := &mockMQTTMessage{
		topic:   "/app/device/property/DEVICE2",
		payload: payload2,
	}
	exporter.MessageHandler(nil, mockMsg2)

	time.Sleep(100 * time.Millisecond)

	// Verify device states are isolated
	exporter.stateMu.RLock()
	device1State := exporter.deviceState["DEVICE1"]
	device2State := exporter.deviceState["DEVICE2"]
	exporter.stateMu.RUnlock()

	assert.Equal(t, 85.5, device1State["battery.level"])
	assert.Equal(t, 220.0, device1State["ac.voltage"])

	assert.Equal(t, 45.0, device2State["battery.level"])
	assert.Equal(t, 110.0, device2State["ac.voltage"])

	// States should not interfere with each other
	assert.NotEqual(t, device1State["battery.level"], device2State["battery.level"])
}

// TestMqttExporter_UnconfiguredDevice tests that unconfigured devices are ignored
func TestMqttExporter_UnconfiguredDevice(t *testing.T) {
	handler := &MockMetricHandler{}

	devices := map[string]string{
		"CONFIGURED": "ConfiguredDevice",
	}

	exporter := &MqttMetricsExporter{
		devices:              devices,
		handlers:             []MetricHandler{handler},
		offlineThreshold:     60 * time.Second,
		userId:               "testuser",
		pingInterval:         20 * time.Second,
		stateRefreshInterval: 300 * time.Second,
		deviceState:          make(map[string]map[string]interface{}),
		stateMu:              sync.RWMutex{},
		pingStats: &PingStats{
			successful:  make(map[string]int),
			failed:      make(map[string]int),
			lastAttempt: make(map[string]time.Time),
			lastSuccess: make(map[string]time.Time),
		},
		protobufDecoder: protobuf.NewProtobufDecoder(),
	}

	// Send message for unconfigured device
	msg := ecoflow.MqttDeviceParams{
		Params: map[string]interface{}{
			"battery.level": 85.5,
		},
	}
	payload, _ := json.Marshal(msg)
	mockMsg := &mockMQTTMessage{
		topic:   "/app/device/property/UNCONFIGURED",
		payload: payload,
	}

	exporter.MessageHandler(nil, mockMsg)
	time.Sleep(100 * time.Millisecond)

	// Handler should not have been called
	assert.Equal(t, 0, handler.GetReceivedCount(), "Unconfigured device should be ignored")

	// State should not be created for unconfigured device
	exporter.stateMu.RLock()
	_, exists := exporter.deviceState["UNCONFIGURED"]
	exporter.stateMu.RUnlock()

	assert.False(t, exists, "State should not be created for unconfigured device")
}

// TestMqttExporter_InvalidTopic tests handling of malformed MQTT topics
func TestMqttExporter_InvalidTopic(t *testing.T) {
	handler := &MockMetricHandler{}

	devices := map[string]string{
		"TEST123": "TestDevice",
	}

	exporter := &MqttMetricsExporter{
		devices:              devices,
		handlers:             []MetricHandler{handler},
		offlineThreshold:     60 * time.Second,
		userId:               "testuser",
		pingInterval:         20 * time.Second,
		stateRefreshInterval: 300 * time.Second,
		deviceState:          make(map[string]map[string]interface{}),
		stateMu:              sync.RWMutex{},
		pingStats: &PingStats{
			successful:  make(map[string]int),
			failed:      make(map[string]int),
			lastAttempt: make(map[string]time.Time),
			lastSuccess: make(map[string]time.Time),
		},
		protobufDecoder: protobuf.NewProtobufDecoder(),
	}

	testCases := []struct {
		name  string
		topic string
	}{
		{"Empty topic", ""},
		{"Topic with no slash", "notopic"},
		{"Topic ending with slash", "/app/device/property/"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			handler.Reset()

			msg := ecoflow.MqttDeviceParams{
				Params: map[string]interface{}{
					"battery.level": 85.5,
				},
			}
			payload, _ := json.Marshal(msg)
			mockMsg := &mockMQTTMessage{
				topic:   tc.topic,
				payload: payload,
			}

			// Should not panic
			require.NotPanics(t, func() {
				exporter.MessageHandler(nil, mockMsg)
			})

			time.Sleep(50 * time.Millisecond)

			// Handler should not have been called
			assert.Equal(t, 0, handler.GetReceivedCount(), "Invalid topic should be ignored")
		})
	}
}

// TestMqttExporter_EmptyParameters tests handling of empty parameter updates
func TestMqttExporter_EmptyParameters(t *testing.T) {
	handler := &MockMetricHandler{}

	devices := map[string]string{
		"TEST123": "TestDevice",
	}

	exporter := &MqttMetricsExporter{
		devices:              devices,
		handlers:             []MetricHandler{handler},
		offlineThreshold:     60 * time.Second,
		userId:               "testuser",
		pingInterval:         20 * time.Second,
		stateRefreshInterval: 300 * time.Second,
		deviceState:          make(map[string]map[string]interface{}),
		stateMu:              sync.RWMutex{},
		pingStats: &PingStats{
			successful:  make(map[string]int),
			failed:      make(map[string]int),
			lastAttempt: make(map[string]time.Time),
			lastSuccess: make(map[string]time.Time),
		},
		protobufDecoder: protobuf.NewProtobufDecoder(),
	}

	mu.Lock()
	deviceStatuses["TEST123"] = &DeviceStatus{LastReceived: time.Now()}
	mu.Unlock()

	// Send message with empty parameters
	msg := ecoflow.MqttDeviceParams{
		Params: map[string]interface{}{},
	}
	payload, _ := json.Marshal(msg)
	mockMsg := &mockMQTTMessage{
		topic:   "/app/device/property/TEST123",
		payload: payload,
	}

	exporter.MessageHandler(nil, mockMsg)
	time.Sleep(100 * time.Millisecond)

	// Handler should not have been called (empty params are skipped)
	assert.Equal(t, 0, handler.GetReceivedCount(), "Empty parameters should be skipped")
}

// TestMqttExporter_InvalidJSON tests handling of malformed JSON
func TestMqttExporter_InvalidJSON(t *testing.T) {
	handler := &MockMetricHandler{}

	devices := map[string]string{
		"TEST123": "TestDevice",
	}

	exporter := &MqttMetricsExporter{
		devices:              devices,
		handlers:             []MetricHandler{handler},
		offlineThreshold:     60 * time.Second,
		userId:               "testuser",
		pingInterval:         20 * time.Second,
		stateRefreshInterval: 300 * time.Second,
		deviceState:          make(map[string]map[string]interface{}),
		stateMu:              sync.RWMutex{},
		pingStats: &PingStats{
			successful:  make(map[string]int),
			failed:      make(map[string]int),
			lastAttempt: make(map[string]time.Time),
			lastSuccess: make(map[string]time.Time),
		},
		protobufDecoder: protobuf.NewProtobufDecoder(),
	}

	mu.Lock()
	deviceStatuses["TEST123"] = &DeviceStatus{LastReceived: time.Now()}
	mu.Unlock()

	// Send invalid JSON
	mockMsg := &mockMQTTMessage{
		topic:   "/app/device/property/TEST123",
		payload: []byte("not valid json{{{"),
	}

	// Should not panic
	require.NotPanics(t, func() {
		exporter.MessageHandler(nil, mockMsg)
	})

	time.Sleep(50 * time.Millisecond)

	// Handler should not have been called
	assert.Equal(t, 0, handler.GetReceivedCount(), "Invalid JSON should be ignored")
}

// TestMqttExporter_ConcurrentMessageHandling tests concurrent MQTT message processing
func TestMqttExporter_ConcurrentMessageHandling(t *testing.T) {
	handler := &MockMetricHandler{}

	devices := map[string]string{
		"DEVICE1": "Device1",
		"DEVICE2": "Device2",
		"DEVICE3": "Device3",
	}

	exporter := &MqttMetricsExporter{
		devices:              devices,
		handlers:             []MetricHandler{handler},
		offlineThreshold:     60 * time.Second,
		userId:               "testuser",
		pingInterval:         20 * time.Second,
		stateRefreshInterval: 300 * time.Second,
		deviceState:          make(map[string]map[string]interface{}),
		stateMu:              sync.RWMutex{},
		pingStats: &PingStats{
			successful:  make(map[string]int),
			failed:      make(map[string]int),
			lastAttempt: make(map[string]time.Time),
			lastSuccess: make(map[string]time.Time),
		},
		protobufDecoder: protobuf.NewProtobufDecoder(),
	}

	// Initialize device statuses
	mu.Lock()
	for sn := range devices {
		deviceStatuses[sn] = &DeviceStatus{LastReceived: time.Now()}
	}
	mu.Unlock()

	// Simulate concurrent MQTT messages from multiple devices
	var wg sync.WaitGroup
	messagesPerDevice := 50

	for deviceSN := range devices {
		wg.Add(1)
		go func(sn string) {
			defer wg.Done()
			for i := 0; i < messagesPerDevice; i++ {
				msg := ecoflow.MqttDeviceParams{
					Params: map[string]interface{}{
						"battery.level": float64(i % 100),
						"iteration":     float64(i),
					},
				}
				payload, _ := json.Marshal(msg)
				mockMsg := &mockMQTTMessage{
					topic:   "/app/device/property/" + sn,
					payload: payload,
				}
				exporter.MessageHandler(nil, mockMsg)
			}
		}(deviceSN)
	}

	wg.Wait()
	time.Sleep(200 * time.Millisecond)

	// Verify each device has its own state
	exporter.stateMu.RLock()
	assert.Equal(t, len(devices), len(exporter.deviceState), "Each device should have its own state")

	for sn := range devices {
		state, exists := exporter.deviceState[sn]
		assert.True(t, exists, "Device %s should have state", sn)
		assert.NotNil(t, state, "Device %s state should not be nil", sn)
		assert.Contains(t, state, "battery.level", "Device %s should have battery.level", sn)
	}
	exporter.stateMu.RUnlock()
}

// TestMqttExporter_InputValidation tests constructor input validation
func TestMqttExporter_InputValidation(t *testing.T) {
	handler := &MockMetricHandler{}

	testCases := []struct {
		name                 string
		email                string
		password             string
		devices              map[string]string
		offlineThreshold     time.Duration
		pingInterval         time.Duration
		stateRefreshInterval time.Duration
		handlers             []MetricHandler
		shouldError          bool
		errorContains        string
	}{
		{
			name:                 "Valid configuration",
			email:                "test@example.com",
			password:             "password",
			devices:              map[string]string{"TEST": "Device"},
			offlineThreshold:     60 * time.Second,
			pingInterval:         20 * time.Second,
			stateRefreshInterval: 300 * time.Second,
			handlers:             []MetricHandler{handler},
			shouldError:          true, // Will fail on getUserId, but passes validation
			errorContains:        "",
		},
		{
			name:                 "Empty email",
			email:                "",
			password:             "password",
			devices:              map[string]string{"TEST": "Device"},
			offlineThreshold:     60 * time.Second,
			pingInterval:         20 * time.Second,
			stateRefreshInterval: 300 * time.Second,
			handlers:             []MetricHandler{handler},
			shouldError:          true,
			errorContains:        "email and password",
		},
		{
			name:                 "Empty password",
			email:                "test@example.com",
			password:             "",
			devices:              map[string]string{"TEST": "Device"},
			offlineThreshold:     60 * time.Second,
			pingInterval:         20 * time.Second,
			stateRefreshInterval: 300 * time.Second,
			handlers:             []MetricHandler{handler},
			shouldError:          true,
			errorContains:        "email and password",
		},
		{
			name:                 "Empty devices",
			email:                "test@example.com",
			password:             "password",
			devices:              map[string]string{},
			offlineThreshold:     60 * time.Second,
			pingInterval:         20 * time.Second,
			stateRefreshInterval: 300 * time.Second,
			handlers:             []MetricHandler{handler},
			shouldError:          true,
			errorContains:        "at least one device",
		},
		{
			name:                 "No handlers",
			email:                "test@example.com",
			password:             "password",
			devices:              map[string]string{"TEST": "Device"},
			offlineThreshold:     60 * time.Second,
			pingInterval:         20 * time.Second,
			stateRefreshInterval: 300 * time.Second,
			handlers:             []MetricHandler{},
			shouldError:          true,
			errorContains:        "at least one metric handler",
		},
		{
			name:                 "Negative offline threshold",
			email:                "test@example.com",
			password:             "password",
			devices:              map[string]string{"TEST": "Device"},
			offlineThreshold:     -60 * time.Second,
			pingInterval:         20 * time.Second,
			stateRefreshInterval: 300 * time.Second,
			handlers:             []MetricHandler{handler},
			shouldError:          true,
			errorContains:        "offlineThreshold must be positive",
		},
		{
			name:                 "Zero ping interval",
			email:                "test@example.com",
			password:             "password",
			devices:              map[string]string{"TEST": "Device"},
			offlineThreshold:     60 * time.Second,
			pingInterval:         0,
			stateRefreshInterval: 300 * time.Second,
			handlers:             []MetricHandler{handler},
			shouldError:          true,
			errorContains:        "pingInterval must be positive",
		},
		{
			name:                 "Negative state refresh interval",
			email:                "test@example.com",
			password:             "password",
			devices:              map[string]string{"TEST": "Device"},
			offlineThreshold:     60 * time.Second,
			pingInterval:         20 * time.Second,
			stateRefreshInterval: -300 * time.Second,
			handlers:             []MetricHandler{handler},
			shouldError:          true,
			errorContains:        "stateRefreshInterval must be positive",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewMqttMetricsExporter(
				tc.email,
				tc.password,
				tc.devices,
				tc.offlineThreshold,
				tc.pingInterval,
				tc.stateRefreshInterval,
				tc.handlers...,
			)

			if tc.shouldError {
				assert.Error(t, err)
				if tc.errorContains != "" {
					assert.Contains(t, err.Error(), tc.errorContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// Mock MQTT message for testing
type mockMQTTMessage struct {
	topic   string
	payload []byte
}

func (m *mockMQTTMessage) Duplicate() bool         { return false }
func (m *mockMQTTMessage) Qos() byte               { return 0 }
func (m *mockMQTTMessage) Retained() bool          { return false }
func (m *mockMQTTMessage) Topic() string           { return m.topic }
func (m *mockMQTTMessage) MessageID() uint16       { return 0 }
func (m *mockMQTTMessage) Payload() []byte         { return m.payload }
func (m *mockMQTTMessage) Ack()                    {}
func (m *mockMQTTMessage) AutoAckOff()             {}
func (m *mockMQTTMessage) SetAutoAck(autoAck bool) {}
func (m *mockMQTTMessage) AutoAck() bool           { return false }

var _ mqtt.Message = (*mockMQTTMessage)(nil)

// TestMessageHandler_ProtobufMessage tests basic protobuf message decoding
func TestMessageHandler_ProtobufMessage(t *testing.T) {
	handler := &MockMetricHandler{}

	devices := map[string]string{
		"PROTO123": "ProtobufDevice",
	}

	exporter := &MqttMetricsExporter{
		devices:              devices,
		handlers:             []MetricHandler{handler},
		offlineThreshold:     60 * time.Second,
		userId:               "testuser",
		pingInterval:         20 * time.Second,
		stateRefreshInterval: 300 * time.Second,
		deviceState:          make(map[string]map[string]interface{}),
		stateMu:              sync.RWMutex{},
		pingStats: &PingStats{
			successful:  make(map[string]int),
			failed:      make(map[string]int),
			lastAttempt: make(map[string]time.Time),
			lastSuccess: make(map[string]time.Time),
		},
		protobufDecoder: protobuf.NewProtobufDecoder(),
	}

	// Initialize device statuses
	mu.Lock()
	deviceStatuses["PROTO123"] = &DeviceStatus{LastReceived: time.Now()}
	mu.Unlock()

	// Create a protobuf message with unencrypted JSON data
	pdataJSON := map[string]interface{}{
		"battery.level": 75.5,
		"ac.voltage":    230.0,
		"dc.current":    3.8,
	}
	pdataBytes, _ := json.Marshal(pdataJSON)

	protoMsg := &protobuf.HeaderMessage{
		Header: []*protobuf.Header{
			{
				Pdata:   pdataBytes,
				EncType: 0, // No encryption
				CmdFunc: 1,
				CmdId:   2,
				Seq:     0,
			},
		},
	}

	payload, err := proto.Marshal(protoMsg)
	require.NoError(t, err)

	mockMsg := &mockMQTTMessage{
		topic:   "/app/device/property/PROTO123",
		payload: payload,
	}

	exporter.MessageHandler(nil, mockMsg)
	time.Sleep(100 * time.Millisecond)

	// Verify handler received the decoded params
	assert.Equal(t, 1, handler.GetReceivedCount(), "Handler should be called once")
	params := handler.GetLastParams()
	require.NotNil(t, params)

	assert.Equal(t, 75.5, params["battery.level"], "Battery level should match")
	assert.Equal(t, 230.0, params["ac.voltage"], "AC voltage should match")
	assert.Equal(t, 3.8, params["dc.current"], "DC current should match")
}

// TestMessageHandler_ProtobufWithEncryption tests protobuf with XOR encryption
func TestMessageHandler_ProtobufWithEncryption(t *testing.T) {
	handler := &MockMetricHandler{}

	devices := map[string]string{
		"PROTO456": "EncryptedDevice",
	}

	exporter := &MqttMetricsExporter{
		devices:              devices,
		handlers:             []MetricHandler{handler},
		offlineThreshold:     60 * time.Second,
		userId:               "testuser",
		pingInterval:         20 * time.Second,
		stateRefreshInterval: 300 * time.Second,
		deviceState:          make(map[string]map[string]interface{}),
		stateMu:              sync.RWMutex{},
		pingStats: &PingStats{
			successful:  make(map[string]int),
			failed:      make(map[string]int),
			lastAttempt: make(map[string]time.Time),
			lastSuccess: make(map[string]time.Time),
		},
		protobufDecoder: protobuf.NewProtobufDecoder(),
	}

	mu.Lock()
	deviceStatuses["PROTO456"] = &DeviceStatus{LastReceived: time.Now()}
	mu.Unlock()

	// Create JSON data and encrypt it
	pdataJSON := map[string]interface{}{
		"battery.level": 60.0,
		"temperature":   25.5,
	}
	pdataBytes, _ := json.Marshal(pdataJSON)

	// Encrypt with XOR using seq number
	seq := int32(42)
	encryptedPdata := make([]byte, len(pdataBytes))
	seqByte := byte(seq & 0xFF)
	for i, b := range pdataBytes {
		encryptedPdata[i] = b ^ seqByte
	}

	protoMsg := &protobuf.HeaderMessage{
		Header: []*protobuf.Header{
			{
				Pdata:   encryptedPdata,
				EncType: 1, // XOR encryption
				CmdFunc: 10,
				CmdId:   20,
				Seq:     seq,
			},
		},
	}

	payload, err := proto.Marshal(protoMsg)
	require.NoError(t, err)

	mockMsg := &mockMQTTMessage{
		topic:   "/app/device/property/PROTO456",
		payload: payload,
	}

	exporter.MessageHandler(nil, mockMsg)
	time.Sleep(100 * time.Millisecond)

	// Verify decryption worked
	assert.Equal(t, 1, handler.GetReceivedCount())
	params := handler.GetLastParams()
	require.NotNil(t, params)

	assert.Equal(t, 60.0, params["battery.level"])
	assert.Equal(t, 25.5, params["temperature"])
}

// TestMessageHandler_ProtobufPartialUpdates tests state cache with protobuf
func TestMessageHandler_ProtobufPartialUpdates(t *testing.T) {
	handler := &MockMetricHandler{}

	devices := map[string]string{
		"PROTO789": "StateCacheDevice",
	}

	exporter := &MqttMetricsExporter{
		devices:              devices,
		handlers:             []MetricHandler{handler},
		offlineThreshold:     60 * time.Second,
		userId:               "testuser",
		pingInterval:         20 * time.Second,
		stateRefreshInterval: 300 * time.Second,
		deviceState:          make(map[string]map[string]interface{}),
		stateMu:              sync.RWMutex{},
		pingStats: &PingStats{
			successful:  make(map[string]int),
			failed:      make(map[string]int),
			lastAttempt: make(map[string]time.Time),
			lastSuccess: make(map[string]time.Time),
		},
		protobufDecoder: protobuf.NewProtobufDecoder(),
	}

	mu.Lock()
	deviceStatuses["PROTO789"] = &DeviceStatus{LastReceived: time.Now()}
	mu.Unlock()

	// First protobuf message with full state
	pdata1 := map[string]interface{}{
		"battery.level": 90.0,
		"ac.voltage":    240.0,
		"dc.current":    4.5,
	}
	pdata1Bytes, _ := json.Marshal(pdata1)

	msg1 := &protobuf.HeaderMessage{
		Header: []*protobuf.Header{
			{
				Pdata:   pdata1Bytes,
				EncType: 0,
				CmdFunc: 1,
				CmdId:   1,
			},
		},
	}

	payload1, _ := proto.Marshal(msg1)
	mockMsg1 := &mockMQTTMessage{
		topic:   "/app/device/property/PROTO789",
		payload: payload1,
	}

	exporter.MessageHandler(nil, mockMsg1)
	time.Sleep(100 * time.Millisecond)

	// Verify first message
	assert.Equal(t, 1, handler.GetReceivedCount())
	params1 := handler.GetLastParams()
	assert.Equal(t, 90.0, params1["battery.level"])
	assert.Equal(t, 240.0, params1["ac.voltage"])
	assert.Equal(t, 4.5, params1["dc.current"])

	// Second protobuf message with PARTIAL update (only battery)
	pdata2 := map[string]interface{}{
		"battery.level": 85.0, // Only this changed
	}
	pdata2Bytes, _ := json.Marshal(pdata2)

	msg2 := &protobuf.HeaderMessage{
		Header: []*protobuf.Header{
			{
				Pdata:   pdata2Bytes,
				EncType: 0,
				CmdFunc: 1,
				CmdId:   1,
			},
		},
	}

	payload2, _ := proto.Marshal(msg2)
	mockMsg2 := &mockMQTTMessage{
		topic:   "/app/device/property/PROTO789",
		payload: payload2,
	}

	handler.Reset()
	exporter.MessageHandler(nil, mockMsg2)
	time.Sleep(100 * time.Millisecond)

	// CRITICAL: Verify state cache maintains full state
	assert.Equal(t, 1, handler.GetReceivedCount())
	params2 := handler.GetLastParams()
	assert.Equal(t, 85.0, params2["battery.level"], "Battery should be updated")
	assert.Equal(t, 240.0, params2["ac.voltage"], "AC voltage should persist from cache")
	assert.Equal(t, 4.5, params2["dc.current"], "DC current should persist from cache")
}

// TestMessageHandler_JSONStillWorks verifies backward compatibility with JSON
func TestMessageHandler_JSONStillWorks(t *testing.T) {
	handler := &MockMetricHandler{}

	devices := map[string]string{
		"JSON123": "JSONDevice",
	}

	exporter := &MqttMetricsExporter{
		devices:              devices,
		handlers:             []MetricHandler{handler},
		offlineThreshold:     60 * time.Second,
		userId:               "testuser",
		pingInterval:         20 * time.Second,
		stateRefreshInterval: 300 * time.Second,
		deviceState:          make(map[string]map[string]interface{}),
		stateMu:              sync.RWMutex{},
		pingStats: &PingStats{
			successful:  make(map[string]int),
			failed:      make(map[string]int),
			lastAttempt: make(map[string]time.Time),
			lastSuccess: make(map[string]time.Time),
		},
		protobufDecoder: protobuf.NewProtobufDecoder(),
	}

	mu.Lock()
	deviceStatuses["JSON123"] = &DeviceStatus{LastReceived: time.Now()}
	mu.Unlock()

	// Send a traditional JSON message
	msg := ecoflow.MqttDeviceParams{
		Params: map[string]interface{}{
			"battery.level": 55.5,
			"ac.voltage":    220.0,
		},
	}
	payload, _ := json.Marshal(msg)

	mockMsg := &mockMQTTMessage{
		topic:   "/app/device/property/JSON123",
		payload: payload,
	}

	exporter.MessageHandler(nil, mockMsg)
	time.Sleep(100 * time.Millisecond)

	// Verify JSON messages still work exactly as before
	assert.Equal(t, 1, handler.GetReceivedCount())
	params := handler.GetLastParams()
	assert.Equal(t, 55.5, params["battery.level"])
	assert.Equal(t, 220.0, params["ac.voltage"])
}

// TestMessageHandler_ProtobufMultipleHeaders tests merging multiple headers
func TestMessageHandler_ProtobufMultipleHeaders(t *testing.T) {
	handler := &MockMetricHandler{}

	devices := map[string]string{
		"MULTI123": "MultiHeaderDevice",
	}

	exporter := &MqttMetricsExporter{
		devices:              devices,
		handlers:             []MetricHandler{handler},
		offlineThreshold:     60 * time.Second,
		userId:               "testuser",
		pingInterval:         20 * time.Second,
		stateRefreshInterval: 300 * time.Second,
		deviceState:          make(map[string]map[string]interface{}),
		stateMu:              sync.RWMutex{},
		pingStats: &PingStats{
			successful:  make(map[string]int),
			failed:      make(map[string]int),
			lastAttempt: make(map[string]time.Time),
			lastSuccess: make(map[string]time.Time),
		},
		protobufDecoder: protobuf.NewProtobufDecoder(),
	}

	mu.Lock()
	deviceStatuses["MULTI123"] = &DeviceStatus{LastReceived: time.Now()}
	mu.Unlock()

	// Create protobuf message with multiple headers
	pdata1, _ := json.Marshal(map[string]interface{}{"battery.level": 70.0})
	pdata2, _ := json.Marshal(map[string]interface{}{"ac.voltage": 220.0})

	protoMsg := &protobuf.HeaderMessage{
		Header: []*protobuf.Header{
			{
				Pdata:   pdata1,
				EncType: 0,
				CmdFunc: 1,
				CmdId:   1,
			},
			{
				Pdata:   pdata2,
				EncType: 0,
				CmdFunc: 2,
				CmdId:   2,
			},
		},
	}

	payload, _ := proto.Marshal(protoMsg)
	mockMsg := &mockMQTTMessage{
		topic:   "/app/device/property/MULTI123",
		payload: payload,
	}

	exporter.MessageHandler(nil, mockMsg)
	time.Sleep(100 * time.Millisecond)

	// Verify both headers were merged
	assert.Equal(t, 1, handler.GetReceivedCount())
	params := handler.GetLastParams()
	assert.Equal(t, 70.0, params["battery.level"])
	assert.Equal(t, 220.0, params["ac.voltage"])
}
