package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeviceUpdateSamplesAreComputedOncePerPrefix(t *testing.T) {
	u := NewDeviceUpdate(EcoflowDevice{SN: "SN"}, map[string]any{"pd.soc": 50.0, "name": "x", "": 1.0})
	samples := u.Samples("ecoflow")
	require.Len(t, samples, 2, "a parameter without a name is skipped")
	byParam := map[string]Sample{}
	for _, s := range samples {
		byParam[s.Param] = s
	}
	assert.Equal(t, Sample{Param: "pd.soc", Name: "ecoflow_pd_soc", Raw: 50.0, Value: 50, Numeric: true}, byParam["pd.soc"])
	assert.Equal(t, Sample{Param: "name", Name: "ecoflow_name", Raw: "x"}, byParam["name"])

	again := u.Samples("ecoflow")
	assert.Same(t, &samples[0], &again[0], "cached")
	assert.Equal(t, "other_pd_soc", findSample(u.Samples("other"), "pd.soc").Name)
}

func findSample(samples []Sample, param string) Sample {
	for _, s := range samples {
		if s.Param == param {
			return s
		}
	}
	return Sample{}
}

type panicHandler struct{}

func (panicHandler) Handle(context.Context, *DeviceUpdate) error { panic("boom") }

func TestDispatchSurvivesPanickingHandler(t *testing.T) {
	next := &MockMetricHandler{}
	u := NewDeviceUpdate(EcoflowDevice{SN: "P"}, map[string]any{"v": 1.0})
	assert.NotPanics(t, func() { dispatch(context.Background(), []MetricHandler{panicHandler{}, next}, u) })
	assert.Equal(t, 1, next.GetReceivedCount(), "the next handler still gets the update")
	assert.ErrorContains(t, safeHandle(context.Background(), panicHandler{}, u), "panic: boom")
}
