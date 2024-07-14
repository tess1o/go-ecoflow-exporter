package main

import (
	"context"
	"github.com/tess1o/go-ecoflow"
	"log/slog"
	"time"
)

type MetricHandler interface {
	Handle(ctx context.Context, device ecoflow.DeviceInfo, rawParameters map[string]interface{})
}

type MetricsExporter struct {
	c        *ecoflow.Client
	interval time.Duration
	handlers []MetricHandler
}

func NewMetricsExporter(c *ecoflow.Client, interval time.Duration, handlers ...MetricHandler) *MetricsExporter {
	return &MetricsExporter{
		c:        c,
		interval: interval,
		handlers: handlers,
	}
}

func (e *MetricsExporter) ExportMetrics() {
	ticker := time.NewTicker(e.interval)
	now := time.Now()
	for _ = time.Now(); ; now = <-ticker.C {
		ctx := context.Background()
		slog.Info("Getting ecoflow devices", "time", now)
		devices, err := e.c.GetDeviceList(ctx)
		if err != nil {
			slog.Error("Cannot get devices list", "error", err)
			continue
		}

		for _, dev := range devices.Devices {
			slog.Info("Getting ecoflow parameters", "sn", dev.SN)
			rawParameters, pErr := e.c.GetDeviceAllParameters(ctx, dev.SN)
			if pErr != nil {
				slog.Error("Cannot get device quota", "SN", dev.SN, "error", pErr)
				continue
			}

			for _, handler := range e.handlers {
				hh := handler
				go hh.Handle(ctx, dev, rawParameters)
			}
		}
	}
}
