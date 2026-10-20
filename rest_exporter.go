package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/tess1o/go-ecoflow"
)

// restRequestTimeout bounds every single call to the EcoFlow REST API
const restRequestTimeout = 30 * time.Second

// restConcurrency limits how many device parameter requests run at the same time
const restConcurrency = 4

// ecoflowRestClient is the part of *ecoflow.Client used by the REST exporter
type ecoflowRestClient interface {
	GetDeviceList(ctx context.Context) (*ecoflow.DeviceListResponse, error)
	GetDeviceAllParameters(ctx context.Context, deviceSn string) (map[string]any, error)
}

type RestExporterConfig struct {
	Interval time.Duration
	// Devices maps a serial number to its display name
	Devices   map[string]string
	Telemetry *Telemetry
}

type RestMetricsExporter struct {
	c         ecoflowRestClient
	interval  time.Duration
	handlers  []MetricHandler
	mapping   map[string]string
	telemetry *Telemetry

	mu          sync.Mutex
	started     time.Time
	lastSuccess time.Time
}

func NewRestMetricsExporter(c ecoflowRestClient, cfg RestExporterConfig, handlers ...MetricHandler) *RestMetricsExporter {
	return &RestMetricsExporter{
		c:         c,
		interval:  cfg.Interval,
		handlers:  handlers,
		mapping:   cfg.Devices,
		telemetry: cfg.Telemetry,
	}
}

// Run polls the REST API every interval until ctx is cancelled
func (e *RestMetricsExporter) Run(ctx context.Context) error {
	e.mu.Lock()
	e.started = time.Now()
	e.mu.Unlock()

	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()
	for {
		e.scrape(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// scrape fetches all devices linked to the account and passes their parameters to the handlers
func (e *RestMetricsExporter) scrape(ctx context.Context) {
	slog.Info("Getting ecoflow devices")
	listCtx, cancel := context.WithTimeout(ctx, restRequestTimeout)
	devices, err := e.c.GetDeviceList(listCtx)
	cancel()
	e.telemetry.RestRequest("device_list", err)
	if err != nil {
		slog.Error("Cannot get devices list", "error", err)
		return
	}

	e.mu.Lock()
	e.lastSuccess = time.Now()
	e.mu.Unlock()

	// fetch in parallel, but pass the updates on in the order of the device list
	updates := make([]*DeviceUpdate, len(devices.Devices))
	sem := make(chan struct{}, restConcurrency)
	var wg sync.WaitGroup
	for i, dev := range devices.Devices {
		wg.Go(func() {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			updates[i] = e.fetchDevice(ctx, dev)
		})
	}
	wg.Wait()

	for _, u := range updates {
		if u != nil && ctx.Err() == nil {
			dispatch(ctx, e.handlers, u)
		}
	}
}

// fetchDevice gets the parameters of one device. It returns nil when an online device's parameters cannot
// be fetched. For an offline device the status is still reported, the API may not return its parameters.
func (e *RestMetricsExporter) fetchDevice(ctx context.Context, dev ecoflow.DeviceInfo) *DeviceUpdate {
	slog.Info("Getting ecoflow parameters", "sn", dev.SN)
	paramsCtx, cancel := context.WithTimeout(ctx, restRequestTimeout)
	rawParameters, err := e.c.GetDeviceAllParameters(paramsCtx, dev.SN)
	cancel()
	e.telemetry.RestRequest("device_quota", err)

	device := EcoflowDevice{
		SN:     dev.SN,
		Name:   getDeviceName(e.mapping, dev.SN),
		Online: dev.Online,
	}
	if err != nil {
		if dev.Online != 0 {
			slog.Error("Cannot get device quota", "SN", dev.SN, "error", err)
			return nil
		}
		slog.Debug("Cannot get quota of an offline device, reporting its status only", "SN", dev.SN, "error", err)
		return NewDeviceUpdate(device, map[string]any{})
	}
	e.telemetry.DeviceSeen(device)
	return NewDeviceUpdate(device, rawParameters)
}

// Health reports an error when the device list could not be fetched for three intervals in a row
func (e *RestMetricsExporter) Health() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	last := e.lastSuccess
	if last.IsZero() {
		last = e.started
	}
	if limit := max(3*e.interval, 2*time.Minute); time.Since(last) > limit {
		return fmt.Errorf("no successful EcoFlow REST API call for %s", time.Since(last).Round(time.Second))
	}
	return nil
}
