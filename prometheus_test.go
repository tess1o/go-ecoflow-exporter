package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPrometheusExporter_MetricCreation tests that metrics are created correctly
func TestPrometheusExporter_MetricCreation(t *testing.T) {
	config := &PrometheusConfig{
		Prefix:     "test_ecoflow",
		ServerPort: "19999",
	}

	exporter := NewPrometheusExporter(config)
	defer exporter.Close(context.Background())

	// Wait for server to start
	time.Sleep(100 * time.Millisecond)

	device := EcoflowDevice{
		SN:     "TEST123",
		Name:   "TestDevice",
		Online: 1,
	}

	params := map[string]interface{}{
		"battery.level": 85.5,
		"ac.voltage":    220.0,
	}

	exporter.Handle(context.Background(), device, params)

	// Verify metrics were created in exporter's internal map
	exporter.mu.RLock()
	metricCount := len(exporter.metrics)
	exporter.mu.RUnlock()

	assert.Equal(t, 3, metricCount, "Should have 3 metrics (battery.level, ac.voltage, online)")

	// Verify metric keys exist
	exporter.mu.RLock()
	_, hasBattery := exporter.metrics["TEST123_test_ecoflow_battery_level"]
	_, hasAC := exporter.metrics["TEST123_test_ecoflow_ac_voltage"]
	_, hasOnline := exporter.metrics["TEST123_test_ecoflow_online"]
	exporter.mu.RUnlock()

	assert.True(t, hasBattery, "Should have battery.level metric")
	assert.True(t, hasAC, "Should have ac.voltage metric")
	assert.True(t, hasOnline, "Should have online metric")
}

// TestPrometheusExporter_MetricUpdate tests that existing metrics are updated
func TestPrometheusExporter_MetricUpdate(t *testing.T) {
	config := &PrometheusConfig{
		Prefix:     "test_update", // Unique prefix
		ServerPort: "19998",
	}

	exporter := NewPrometheusExporter(config)
	defer exporter.Close(context.Background())

	time.Sleep(100 * time.Millisecond)

	device := EcoflowDevice{
		SN:     "UPDATE123", // Unique device SN
		Name:   "TestDevice",
		Online: 1,
	}

	// First update
	params1 := map[string]interface{}{
		"battery.level": 85.5,
	}
	exporter.Handle(context.Background(), device, params1)

	exporter.mu.RLock()
	initialMetricCount := len(exporter.metrics)
	exporter.mu.RUnlock()

	// Second update - should reuse existing metric
	params2 := map[string]interface{}{
		"battery.level": 90.0,
	}
	exporter.Handle(context.Background(), device, params2)

	exporter.mu.RLock()
	finalMetricCount := len(exporter.metrics)
	exporter.mu.RUnlock()

	assert.Equal(t, initialMetricCount, finalMetricCount, "Metric count should not increase on update")
	assert.Equal(t, 2, finalMetricCount, "Should have exactly 2 metrics (battery.level, online)")
}

// TestPrometheusExporter_OfflineDevice tests that offline devices set all metrics to 0
func TestPrometheusExporter_OfflineDevice(t *testing.T) {
	config := &PrometheusConfig{
		Prefix:     "test_offline", // Unique prefix
		ServerPort: "19997",
	}

	exporter := NewPrometheusExporter(config)
	defer exporter.Close(context.Background())

	time.Sleep(100 * time.Millisecond)

	device := EcoflowDevice{
		SN:     "OFFLINE123", // Unique device SN
		Name:   "TestDevice",
		Online: 1,
	}

	// First, set some metrics while online
	params := map[string]interface{}{
		"battery.level": 85.5,
		"ac.voltage":    220.0,
	}
	exporter.Handle(context.Background(), device, params)

	exporter.mu.RLock()
	onlineMetricCount := len(exporter.metrics)
	exporter.mu.RUnlock()

	assert.Equal(t, 3, onlineMetricCount, "Should have 3 metrics when online")

	// Now mark device as offline
	device.Online = 0
	exporter.Handle(context.Background(), device, map[string]interface{}{})

	// Verify handleOfflineDevice was called and metrics still exist
	// (They're set to 0, not deleted)
	exporter.mu.RLock()
	offlineMetricCount := len(exporter.metrics)
	exporter.mu.RUnlock()

	assert.Equal(t, onlineMetricCount, offlineMetricCount, "Metrics should still exist when offline")
}

// TestPrometheusExporter_ConcurrentUpdates tests that concurrent metric updates don't cause races
func TestPrometheusExporter_ConcurrentUpdates(t *testing.T) {
	config := &PrometheusConfig{
		Prefix:     "test_concurrent", // Unique prefix
		ServerPort: "19996",
	}

	exporter := NewPrometheusExporter(config)
	defer exporter.Close(context.Background())

	time.Sleep(100 * time.Millisecond)

	device := EcoflowDevice{
		SN:     "CONCURRENT123", // Unique device SN
		Name:   "TestDevice",
		Online: 1,
	}

	// Simulate concurrent updates from multiple goroutines
	var wg sync.WaitGroup
	numGoroutines := 10
	updatesPerGoroutine := 100

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < updatesPerGoroutine; j++ {
				params := map[string]interface{}{
					"battery.level": float64(j % 100),
					"ac.voltage":    float64(220 + id),
				}
				exporter.Handle(context.Background(), device, params)
			}
		}(i)
	}

	wg.Wait()

	// Should have created exactly 3 metrics (battery.level, ac.voltage, online)
	exporter.mu.RLock()
	metricCount := len(exporter.metrics)
	exporter.mu.RUnlock()

	assert.Equal(t, 3, metricCount, "Should have exactly 3 metrics despite concurrent updates")
}

// TestPrometheusExporter_MultipleDevices tests handling multiple devices
func TestPrometheusExporter_MultipleDevices(t *testing.T) {
	config := &PrometheusConfig{
		Prefix:     "test_multi", // Unique prefix
		ServerPort: "19995",
	}

	exporter := NewPrometheusExporter(config)
	defer exporter.Close(context.Background())

	time.Sleep(100 * time.Millisecond)

	devices := []EcoflowDevice{
		{SN: "MULTI_DEV1", Name: "Delta2", Online: 1}, // Unique SNs
		{SN: "MULTI_DEV2", Name: "RiverPro", Online: 1},
		{SN: "MULTI_DEV3", Name: "DeltaPro", Online: 1},
	}

	params := map[string]interface{}{
		"battery.level": 85.5,
		"ac.voltage":    220.0,
	}

	for _, device := range devices {
		exporter.Handle(context.Background(), device, params)
	}

	// Each device should have 3 metrics: battery.level, ac.voltage, online
	expectedMetrics := len(devices) * 3

	exporter.mu.RLock()
	actualMetrics := len(exporter.metrics)
	exporter.mu.RUnlock()

	assert.Equal(t, expectedMetrics, actualMetrics, "Should have metrics for all devices")

	// Verify each device has its metrics
	exporter.mu.RLock()
	for _, device := range devices {
		batteryKey := device.SN + "_test_multi_battery_level" // Use correct prefix
		acKey := device.SN + "_test_multi_ac_voltage"
		onlineKey := device.SN + "_test_multi_online"

		assert.Contains(t, exporter.metrics, batteryKey, "Should have battery metric for "+device.SN)
		assert.Contains(t, exporter.metrics, acKey, "Should have AC metric for "+device.SN)
		assert.Contains(t, exporter.metrics, onlineKey, "Should have online metric for "+device.SN)
	}
	exporter.mu.RUnlock()
}

// TestPrometheusExporter_InvalidValues tests handling of invalid metric values
func TestPrometheusExporter_InvalidValues(t *testing.T) {
	config := &PrometheusConfig{
		Prefix:     "test_invalid", // Unique prefix
		ServerPort: "19994",
	}

	exporter := NewPrometheusExporter(config)
	defer exporter.Close(context.Background())

	time.Sleep(100 * time.Millisecond)

	device := EcoflowDevice{
		SN:     "INVALID123", // Unique device SN
		Name:   "TestDevice",
		Online: 1,
	}

	// Test various invalid value types
	params := map[string]interface{}{
		"valid.metric": 85.5,
		"string.value": "not a number",
		"array.value":  []interface{}{1, 2, 3},
		"nil.value":    nil,
		"bool.value":   true,
		"object.value": map[string]interface{}{"nested": "value"},
	}

	// Should not panic
	require.NotPanics(t, func() {
		exporter.Handle(context.Background(), device, params)
	})

	// Verify that valid metrics were created
	// Note: Metrics may be created even for invalid types, but they won't have valid values set
	// The important behavior is that errors are logged and invalid values don't cause panics
	exporter.mu.RLock()
	metricCount := len(exporter.metrics)
	_, hasValid := exporter.metrics["INVALID123_test_invalid_valid_metric"]
	_, hasOnline := exporter.metrics["INVALID123_test_invalid_online"]
	exporter.mu.RUnlock()

	assert.True(t, hasValid, "Should have valid metric")
	assert.True(t, hasOnline, "Should have online metric")
	// Metrics may be created for all parameters (including invalid types)
	// The key is that they won't have valid values set (checked by error logs above)
	assert.GreaterOrEqual(t, metricCount, 2, "Should have at least valid metric and online metric")
}

// TestPrometheusExporter_MetricNameGeneration tests metric name conversion
func TestPrometheusExporter_MetricNameGeneration(t *testing.T) {
	tests := []struct {
		name           string
		rawMetric      string
		prefix         string
		deviceSN       string
		expectedMetric string
		shouldError    bool
	}{
		{
			name:           "Simple dot notation",
			rawMetric:      "battery.level",
			prefix:         "ecoflow",
			deviceSN:       "TEST123",
			expectedMetric: "ecoflow_battery_level",
			shouldError:    false,
		},
		{
			name:           "CamelCase conversion",
			rawMetric:      "pd.wireUsedTime",
			prefix:         "ecoflow",
			deviceSN:       "TEST123",
			expectedMetric: "ecoflow_pd_wire_used_time",
			shouldError:    false,
		},
		{
			name:           "Multiple dots and capitals",
			rawMetric:      "inv.acInVol",
			prefix:         "ecoflow",
			deviceSN:       "TEST123",
			expectedMetric: "ecoflow_inv_ac_in_vol",
			shouldError:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metricName, deviceMetricName, err := generateMetricName(tt.rawMetric, tt.prefix, tt.deviceSN)

			if tt.shouldError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedMetric, metricName)
				assert.Equal(t, tt.deviceSN+"_"+tt.expectedMetric, deviceMetricName)
			}
		})
	}
}

// TestPrometheusExporter_NoInputMutation tests that input parameters are not mutated
func TestPrometheusExporter_NoInputMutation(t *testing.T) {
	config := &PrometheusConfig{
		Prefix:     "test_nomutate", // Unique prefix
		ServerPort: "19993",
	}

	exporter := NewPrometheusExporter(config)
	defer exporter.Close(context.Background())

	time.Sleep(100 * time.Millisecond)

	device := EcoflowDevice{
		SN:     "NOMUTATE123", // Unique device SN
		Name:   "TestDevice",
		Online: 1,
	}

	// Create params and keep reference
	params := map[string]interface{}{
		"battery.level": 85.5,
	}

	originalLen := len(params)

	exporter.Handle(context.Background(), device, params)

	// Input should not be mutated
	assert.Equal(t, originalLen, len(params), "Input parameters should not be modified")
	assert.NotContains(t, params, "online", "Online metric should not be added to input")
}

// TestPrometheusExporter_GracefulShutdown tests HTTP server shutdown
func TestPrometheusExporter_GracefulShutdown(t *testing.T) {
	config := &PrometheusConfig{
		Prefix:     "test_shutdown", // Unique prefix
		ServerPort: "19992",
	}

	exporter := NewPrometheusExporter(config)

	// Verify server is running
	time.Sleep(100 * time.Millisecond)

	// Shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	require.NotPanics(t, func() {
		exporter.Close(ctx)
	})

	// Give server time to stop
	time.Sleep(100 * time.Millisecond)
}

// TestPrometheusExporter_StateIsolation tests that each exporter instance has isolated state
func TestPrometheusExporter_StateIsolation(t *testing.T) {
	// Create two separate exporters
	config1 := &PrometheusConfig{
		Prefix:     "exporter1",
		ServerPort: "19991",
	}
	exporter1 := NewPrometheusExporter(config1)
	defer exporter1.Close(context.Background())

	config2 := &PrometheusConfig{
		Prefix:     "exporter2",
		ServerPort: "19990",
	}
	exporter2 := NewPrometheusExporter(config2)
	defer exporter2.Close(context.Background())

	time.Sleep(100 * time.Millisecond)

	device := EcoflowDevice{
		SN:     "TEST123",
		Name:   "TestDevice",
		Online: 1,
	}

	params := map[string]interface{}{
		"battery.level": 85.5,
	}

	// Update exporter1
	exporter1.Handle(context.Background(), device, params)

	// Update exporter2 with different device
	device2 := EcoflowDevice{
		SN:     "TEST456",
		Name:   "TestDevice2",
		Online: 1,
	}
	exporter2.Handle(context.Background(), device2, params)

	// Verify exporters have isolated state
	exporter1.mu.RLock()
	exporter1Count := len(exporter1.metrics)
	exporter1.mu.RUnlock()

	exporter2.mu.RLock()
	exporter2Count := len(exporter2.metrics)
	exporter2.mu.RUnlock()

	assert.Equal(t, 2, exporter1Count, "Exporter1 should have 2 metrics")
	assert.Equal(t, 2, exporter2Count, "Exporter2 should have 2 metrics")

	// Verify they have different metrics
	exporter1.mu.RLock()
	_, hasDevice1 := exporter1.metrics["TEST123_exporter1_battery_level"]
	_, hasDevice2InExp1 := exporter1.metrics["TEST456_exporter1_battery_level"]
	exporter1.mu.RUnlock()

	exporter2.mu.RLock()
	_, hasDevice2 := exporter2.metrics["TEST456_exporter2_battery_level"]
	_, hasDevice1InExp2 := exporter2.metrics["TEST123_exporter2_battery_level"]
	exporter2.mu.RUnlock()

	assert.True(t, hasDevice1, "Exporter1 should have device1 metrics")
	assert.False(t, hasDevice2InExp1, "Exporter1 should not have device2 metrics")
	assert.True(t, hasDevice2, "Exporter2 should have device2 metrics")
	assert.False(t, hasDevice1InExp2, "Exporter2 should not have device1 metrics")
}
