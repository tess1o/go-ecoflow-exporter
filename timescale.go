package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tess1o/go-ecoflow"
	"log"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"
)

// check that TimescaleExporter implements MetricHandler
var _ MetricHandler = (*TimescaleExporter)(nil)
var _ Shutdownable = (*TimescaleExporter)(nil)

type TimescaleExporterConfig struct {
	Prefix             string
	TimescaleUrl       string
	MigrationSourceUrl string
}

type TimescaleExporter struct {
	config         *TimescaleExporterConfig
	ConnectionPool *pgxpool.Pool
}

func NewTimescaleExporter(config *TimescaleExporterConfig) *TimescaleExporter {
	slog.Info("Creating timescale exporter")
	runDBMigration(config)
	dbUrl, err := convertURLToConnString(config.TimescaleUrl)
	if err != nil {
		log.Fatalf("cannot get db url from string: "+config.TimescaleUrl+". error:%+v\n", err)
	}

	pool, err := pgxpool.New(context.Background(), dbUrl)
	if err != nil {
		slog.Error("Failed to create connection pool", "error", err)
		os.Exit(1)
	}

	return &TimescaleExporter{
		config:         config,
		ConnectionPool: pool,
	}
}

func convertURLToConnString(pgURL string) (string, error) {
	u, err := url.Parse(pgURL)
	if err != nil {
		return "", err
	}
	user := u.User.Username()
	password, _ := u.User.Password()
	host := u.Hostname()
	port := u.Port()
	dbname := strings.TrimPrefix(u.Path, "/")

	// Extract the query parameters
	queryParams := u.Query()
	sslmode := queryParams.Get("sslmode")

	connString := fmt.Sprintf("user=%s password=%s dbname=%s sslmode=%s port=%s host=%s",
		user, password, dbname, sslmode, port, host)

	return connString, nil
}

func runDBMigration(config *TimescaleExporterConfig) {
	// for now hardcoding these constants.
	const maxRetries = 5
	const retryInterval = 10 * time.Second

	var migration *migrate.Migrate
	var err error

	for i := 0; i < maxRetries; i++ {
		migration, err = migrate.New(config.MigrationSourceUrl, config.TimescaleUrl)
		if err == nil {
			break
		}
		slog.Error("cannot create new migrate instance", "error", err)
		time.Sleep(retryInterval)
	}
	if err != nil {
		slog.Error("failed to create migrate instance after retries", "error", err)
		os.Exit(1)
	}

	for i := 0; i < maxRetries; i++ {
		err = migration.Up()
		if err == nil || errors.Is(err, migrate.ErrNoChange) {
			break
		}
		slog.Error("failed to run migrate up, retrying...", "error", err, "attempt", i+1)
		time.Sleep(retryInterval)
	}
	if err != nil && !errors.Is(err, migrate.ErrNoChange) {
		slog.Error("failed to run migrate up after retries", "error", err)
	}

	slog.Info("DB migrated successfully")
}

func (t *TimescaleExporter) Handle(ctx context.Context, device ecoflow.DeviceInfo, rawParameters map[string]interface{}) {
	if device.Online == 0 {
		slog.Info("Device is offline. Setting all metrics to 0", "SN", device.SN)
		rawParameters = t.handleOfflineDevice(rawParameters, device)
	}
	rawParameters["online"] = float64(device.Online)
	t.handleTimeScaleMetrics(ctx, rawParameters, device)
}

func (t *TimescaleExporter) handleOfflineDevice(metrics map[string]interface{}, dev ecoflow.DeviceInfo) map[string]interface{} {
	for k := range metrics {
		if strings.Contains(k, dev.SN) {
			metrics[k] = 0
		}
	}
	return metrics
}

func (t *TimescaleExporter) handleTimeScaleMetrics(ctx context.Context, metrics map[string]interface{}, dev ecoflow.DeviceInfo) {
	slog.Info("Handling metrics for device", "dev", dev.SN)
	dbMetrics := make(map[string]interface{})
	for field, val := range metrics {
		metricName, _, err := generateMetricName(field, t.config.Prefix, dev.SN)
		if err != nil {
			slog.Error("Unable to generate metric name", "metric", field)
			continue
		}
		slog.Debug("Updating metric", "metric", metricName, "value", val, "device", dev.SN)
		_, ok := val.([]interface{})
		if ok {
			slog.Debug("The value is an array, skipping it", "metric", metricName)
			continue
		}
		floatVal, ok := val.(float64)
		if ok {
			dbMetrics[metricName] = floatVal
		} else {
			slog.Error("Unable to convert value to float, skipping metric", "value", val, "metric", metricName)
		}
	}

	conn, err := t.ConnectionPool.Acquire(ctx)
	if err != nil {
		slog.Error("Can't connect to TimescaleDB", "error", err)
		return
	}
	defer conn.Release()

	err = insertMetric(ctx, conn, dev.SN, dbMetrics)

	if err != nil {
		slog.Error("Unable to insert metric", "db_error", err)
	} else {
		slog.Debug("Inserted metrics", "device", dev.SN)
	}
}

const insertMetricQuery = `
INSERT INTO ecoflow_metrics (serial_number, metrics)
VALUES ($1, $2)
`

func insertMetric(ctx context.Context, conn *pgxpool.Conn, sn string, dbMetrics map[string]interface{}) error {
	timescaleMetrics, err := json.Marshal(dbMetrics)
	if err != nil {
		return err
	}
	_, err = conn.Exec(ctx, insertMetricQuery, sn, timescaleMetrics)
	return err
}

func (t *TimescaleExporter) Close(_ context.Context) {
	slog.Debug("Trying to close connection pool")
	t.ConnectionPool.Close()
	slog.Debug("Closed connection pool")
}
