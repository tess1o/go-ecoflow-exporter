package main

import (
	"context"
	"github.com/tess1o/go-ecoflow"
	"log/slog"
	"time"
)

type RestMetricsExporter struct {
	c        *ecoflow.Client
	interval time.Duration
	handlers []MetricHandler
	mapping  map[string]string
}

func NewRestMetricsExporter(c *ecoflow.Client, interval time.Duration, mapping map[string]string, handlers ...MetricHandler) *RestMetricsExporter {
	return &RestMetricsExporter{
		c:        c,
		interval: interval,
		handlers: handlers,
		mapping:  mapping,
	}
}

func (e *RestMetricsExporter) ExportMetrics() {
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
				ecoflowDevice := EcoflowDevice{
					SN:     dev.SN,
					Name:   getDeviceName(e.mapping, dev.SN),
					Online: dev.Online,
				}
				go hh.Handle(ctx, ecoflowDevice, rawParameters)
			}
		}
	}
}
