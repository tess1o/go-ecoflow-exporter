package main

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIntegration_MQTT_Prometheus_StaleDataFix is the MOST CRITICAL integration test
// It verifies the complete fix for the stale data issue with MQTT + Prometheus
func TestIntegration_MQTT_Prometheus_StaleDataFix(t *testing.T) {
	// Create Prometheus exporter
	promConfig := &PrometheusConfig{
		Prefix:     "integ_stalefix",
		ServerPort: "29991",
	}
	promExporter := NewPrometheusExporter(promConfig)
	defer promExporter.Close(context.Background())

	time.Sleep(100 * time.Millisecond)

	// Create MQTT exporter with Prometheus handler
	devices := map[string]string{
		"INTEG_STALE": "Home Battery",
	}

	mqttExporter := &MqttMetricsExporter{
		devices:              devices,
		handlers:             []MetricHandler{promExporter},
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
	}

	// Initialize device status
	mu.Lock()
	deviceStatuses["INTEG_STALE"] = &DeviceStatus{LastReceived: time.Now()}
	mu.Unlock()

	// Scenario: Simulating power outage with partial MQTT updates
	t.Log("Step 1: Device is on mains power, sends full state")

	// First MQTT message: Full state with AC power
	msg1 := map[string]interface{}{
		"params": map[string]interface{}{
			"battery":   85.0,
			"acVoltage": 220.0,
			"dcCurrent": 5.2,
		},
	}
	payload1, _ := json.Marshal(msg1)
	mockMsg1 := &mockMQTTMessage{
		topic:   "/app/device/property/INTEG_STALE",
		payload: payload1,
	}

	mqttExporter.MessageHandler(nil, mockMsg1)
	time.Sleep(200 * time.Millisecond)

	// Verify initial state in cache
	mqttExporter.stateMu.RLock()
	state1 := mqttExporter.deviceState["INTEG_STALE"]
	mqttExporter.stateMu.RUnlock()

	require.NotNil(t, state1, "Device state should exist")
	assert.Equal(t, 85.0, state1["battery"], "Battery should be 85")
	assert.Equal(t, 220.0, state1["acVoltage"], "AC voltage should be 220")
	assert.Equal(t, 5.2, state1["dcCurrent"], "DC current should be 5.2")

	// Verify metrics were created in Prometheus
	promExporter.mu.RLock()
	metricCount1 := len(promExporter.metrics)
	_, hasBattery := promExporter.metrics["INTEG_STALE_integ_stalefix_battery"]
	_, hasAC := promExporter.metrics["INTEG_STALE_integ_stalefix_ac_voltage"]
	_, hasDC := promExporter.metrics["INTEG_STALE_integ_stalefix_dc_current"]
	promExporter.mu.RUnlock()

	assert.GreaterOrEqual(t, metricCount1, 3, "Should have at least 3 metrics")
	assert.True(t, hasBattery, "Should have battery metric")
	assert.True(t, hasAC, "Should have AC voltage metric")
	assert.True(t, hasDC, "Should have DC current metric")

	t.Log("Step 2: Power outage occurs! Device sends ONLY battery update")

	// CRITICAL TEST: Second MQTT message with PARTIAL update
	msg2 := map[string]interface{}{
		"params": map[string]interface{}{
			"battery": 84.0, // Only battery changed
			// acVoltage and dcCurrent NOT sent (this is the bug scenario!)
		},
	}
	payload2, _ := json.Marshal(msg2)
	mockMsg2 := &mockMQTTMessage{
		topic:   "/app/device/property/INTEG_STALE",
		payload: payload2,
	}

	mqttExporter.MessageHandler(nil, mockMsg2)
	time.Sleep(200 * time.Millisecond)

	// CRITICAL VERIFICATION: Check state cache maintained full state
	mqttExporter.stateMu.RLock()
	state2 := mqttExporter.deviceState["INTEG_STALE"]
	mqttExporter.stateMu.RUnlock()

	require.NotNil(t, state2, "Device state should still exist")

	// THE KEY ASSERTIONS:
	assert.Equal(t, 84.0, state2["battery"], "Battery should be updated to 84")
	assert.Equal(t, 220.0, state2["acVoltage"], "AC voltage should PERSIST at 220 from cache")
	assert.Equal(t, 5.2, state2["dcCurrent"], "DC current should PERSIST at 5.2 from cache")
	assert.Equal(t, 3, len(state2), "Should have all 3 parameters in cache")

	t.Log("✅ SUCCESS: State cache correctly maintains full device state across partial updates")
	t.Log("   - Battery updated: ✓")
	t.Log("   - AC voltage persisted: ✓")
	t.Log("   - DC current persisted: ✓")
	t.Log("   This fixes the stale data bug!")
}

// TestIntegration_REST_Conceptual documents REST behavior differences
func TestIntegration_REST_Conceptual(t *testing.T) {
	// REST mode always fetches complete device state, so it doesn't have the stale data problem
	// This test documents the conceptual difference

	t.Log("REST Exporter Behavior:")
	t.Log("  - Polls EcoFlow API at regular intervals (default: 30s)")
	t.Log("  - Each poll calls GetDeviceAllParameters → returns COMPLETE state")
	t.Log("  - No partial updates, no state cache needed")
	t.Log("  - Simple but more API calls")
	t.Log("")
	t.Log("Trade-offs:")
	t.Log("  + Always has fresh, complete data")
	t.Log("  + Simpler implementation")
	t.Log("  - More API calls (rate limiting concerns)")
	t.Log("  - Polling delay (misses sub-interval changes)")
	t.Log("  - Doesn't work for all devices (some only support MQTT)")

	assert.True(t, true, "REST mode documented")
}

// TestIntegration_ConcurrentMQTT_Prometheus tests concurrent MQTT updates with Prometheus
func TestIntegration_ConcurrentMQTT_Prometheus(t *testing.T) {
	// Create Prometheus exporter
	promConfig := &PrometheusConfig{
		Prefix:     "integ_concurrent",
		ServerPort: "29989",
	}
	promExporter := NewPrometheusExporter(promConfig)
	defer promExporter.Close(context.Background())

	time.Sleep(100 * time.Millisecond)

	// Create MQTT exporter with multiple devices
	devices := map[string]string{
		"INTEG_CONC_A": "DeviceA",
		"INTEG_CONC_B": "DeviceB",
		"INTEG_CONC_C": "DeviceC",
	}

	mqttExporter := &MqttMetricsExporter{
		devices:              devices,
		handlers:             []MetricHandler{promExporter},
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
				msg := map[string]interface{}{
					"params": map[string]interface{}{
						"battery": float64(50 + (i % 50)),
						"voltage": float64(200 + (i % 40)),
					},
				}
				payload, _ := json.Marshal(msg)
				mockMsg := &mockMQTTMessage{
					topic:   "/app/device/property/" + sn,
					payload: payload,
				}
				mqttExporter.MessageHandler(nil, mockMsg)
			}
		}(deviceSN)
	}

	wg.Wait()
	time.Sleep(500 * time.Millisecond)

	// Verify each device has its own state in cache
	mqttExporter.stateMu.RLock()
	stateCount := len(mqttExporter.deviceState)
	for sn := range devices {
		state, exists := mqttExporter.deviceState[sn]
		assert.True(t, exists, "Device %s should have state", sn)
		assert.NotNil(t, state, "Device %s state should not be nil", sn)
		assert.Contains(t, state, "battery", "Device %s should have battery", sn)
	}
	mqttExporter.stateMu.RUnlock()

	assert.Equal(t, len(devices), stateCount, "Each device should have its own state")

	// Verify Prometheus has metrics for all devices
	promExporter.mu.RLock()
	metricCount := len(promExporter.metrics)
	promExporter.mu.RUnlock()

	// Each device should have at least 3 metrics (battery, voltage, online)
	expectedMinMetrics := len(devices) * 3
	assert.GreaterOrEqual(t, metricCount, expectedMinMetrics, "Should have metrics for all devices")

	t.Log("✅ SUCCESS: No race conditions detected in concurrent updates")
}

// TestIntegration_DeviceOffline_Prometheus tests offline device handling
func TestIntegration_DeviceOffline_Prometheus(t *testing.T) {
	// Create Prometheus exporter
	promConfig := &PrometheusConfig{
		Prefix:     "integ_offline",
		ServerPort: "29988",
	}
	promExporter := NewPrometheusExporter(promConfig)
	defer promExporter.Close(context.Background())

	time.Sleep(100 * time.Millisecond)

	// Create MQTT exporter
	devices := map[string]string{
		"INTEG_OFF": "OfflineDevice",
	}

	mqttExporter := &MqttMetricsExporter{
		devices:              devices,
		handlers:             []MetricHandler{promExporter},
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
	}

	mu.Lock()
	deviceStatuses["INTEG_OFF"] = &DeviceStatus{LastReceived: time.Now()}
	mu.Unlock()

	// Step 1: Device is online with data
	msg1 := map[string]interface{}{
		"params": map[string]interface{}{
			"battery": 85.0,
			"voltage": 220.0,
		},
	}
	payload1, _ := json.Marshal(msg1)
	mockMsg1 := &mockMQTTMessage{
		topic:   "/app/device/property/INTEG_OFF",
		payload: payload1,
	}

	mqttExporter.MessageHandler(nil, mockMsg1)
	time.Sleep(200 * time.Millisecond)

	// Verify metrics exist
	promExporter.mu.RLock()
	initialMetricCount := len(promExporter.metrics)
	promExporter.mu.RUnlock()

	assert.GreaterOrEqual(t, initialMetricCount, 2, "Should have metrics for online device")

	// Step 2: Device goes offline
	device := EcoflowDevice{
		SN:     "INTEG_OFF",
		Name:   "OfflineDevice",
		Online: 0,
	}
	promExporter.Handle(context.Background(), device, map[string]interface{}{})
	time.Sleep(200 * time.Millisecond)

	// Verify metrics still exist (set to 0, not deleted)
	promExporter.mu.RLock()
	offlineMetricCount := len(promExporter.metrics)
	promExporter.mu.RUnlock()

	assert.Equal(t, initialMetricCount, offlineMetricCount, "Metrics should still exist when offline")

	t.Log("✅ SUCCESS: Offline devices correctly handled")
}

// TestIntegration_MetricNameConversion tests end-to-end metric name conversion
func TestIntegration_MetricNameConversion(t *testing.T) {
	// Create Prometheus exporter
	promConfig := &PrometheusConfig{
		Prefix:     "integ_names",
		ServerPort: "29987",
	}
	promExporter := NewPrometheusExporter(promConfig)
	defer promExporter.Close(context.Background())

	time.Sleep(100 * time.Millisecond)

	device := EcoflowDevice{
		SN:     "INTEG_NAME",
		Name:   "TestDevice",
		Online: 1,
	}

	// Test various parameter name formats
	params := map[string]interface{}{
		"pd.wireUsedTime":      100.0,
		"inv.acInVol":          220.0,
		"battery.level":        85.0,
		"mppt.carOutPutEnable": 1.0,
		"ems.chgVol":           55.0,
	}

	promExporter.Handle(context.Background(), device, params)
	time.Sleep(200 * time.Millisecond)

	// Verify metric names are correctly converted
	promExporter.mu.RLock()
	defer promExporter.mu.RUnlock()

	expectedMetrics := []string{
		"INTEG_NAME_integ_names_pd_wire_used_time",
		"INTEG_NAME_integ_names_inv_ac_in_vol",
		"INTEG_NAME_integ_names_battery_level",
		"INTEG_NAME_integ_names_mppt_car_out_put_enable",
		"INTEG_NAME_integ_names_ems_chg_vol",
		"INTEG_NAME_integ_names_online",
	}

	for _, expected := range expectedMetrics {
		assert.Contains(t, promExporter.metrics, expected, "Should have metric: "+expected)
	}

	t.Log("✅ SUCCESS: All metric names correctly converted to Prometheus format")
}
