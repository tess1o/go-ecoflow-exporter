package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"
)

// check that TimescaleExporter implements MetricHandler
var _ MetricHandler = (*TimescaleExporter)(nil)
var _ Shutdownable = (*TimescaleExporter)(nil)

//go:embed migrations/timescale/*.sql
var timescaleMigrations embed.FS

const (
	migrationRetries       = 5
	migrationRetryInterval = 10 * time.Second
)

type TimescaleExporterConfig struct {
	Prefix       string
	TimescaleUrl string
	// MigrationSourceUrl is a golang-migrate source URL (for example "file://migrations/timescale").
	// When empty, the migrations embedded in the binary are used.
	MigrationSourceUrl string
	// CompressionAfter enables native compression of chunks older than this. Zero leaves compression as it is.
	CompressionAfter time.Duration
	// Retention drops chunks older than this. Zero leaves the retention policy as it is.
	Retention time.Duration
}

// TimescaleExporter stores every update as one row with all parameters in a JSONB column
type TimescaleExporter struct {
	config         *TimescaleExporterConfig
	ConnectionPool *pgxpool.Pool
}

func NewTimescaleExporter(ctx context.Context, config *TimescaleExporterConfig) (*TimescaleExporter, error) {
	slog.Info("Creating timescale exporter")
	if err := runDBMigration(ctx, config); err != nil {
		// the schema may already be in place, so keep going; inserts will report errors if it is not
		slog.Error("Database migration failed, metrics may not be stored", "error", err)
	}

	poolConfig, err := pgxpool.ParseConfig(pgxConnString(config.TimescaleUrl))
	if err != nil {
		return nil, fmt.Errorf("cannot parse TIMESCALE_URL: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool: %w", err)
	}
	if err := applyTimescalePolicies(ctx, pool, config); err != nil {
		slog.Error("Unable to apply TimescaleDB compression/retention policies", "error", err)
	}

	return &TimescaleExporter{
		config:         config,
		ConnectionPool: pool,
	}, nil
}

// pgxConnString removes golang-migrate specific query parameters (x-migrations-table, ...) from the URL,
// pgx would otherwise send them to the server as runtime parameters
func pgxConnString(pgURL string) string {
	u, err := url.Parse(pgURL)
	if err != nil || u.Scheme == "" {
		return pgURL
	}
	q := u.Query()
	for key := range q {
		if strings.HasPrefix(key, "x-") {
			q.Del(key)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func newMigration(config *TimescaleExporterConfig) (*migrate.Migrate, error) {
	if config.MigrationSourceUrl != "" {
		return migrate.New(config.MigrationSourceUrl, config.TimescaleUrl)
	}
	source, err := iofs.New(timescaleMigrations, "migrations/timescale")
	if err != nil {
		return nil, err
	}
	return migrate.NewWithSourceInstance("iofs", source, config.TimescaleUrl)
}

func runDBMigration(ctx context.Context, config *TimescaleExporterConfig) error {
	var migration *migrate.Migrate
	err := retry(ctx, "create migrate instance", func() error {
		var err error
		migration, err = newMigration(config)
		return err
	})
	if err != nil {
		return err
	}
	defer func() {
		if srcErr, dbErr := migration.Close(); srcErr != nil || dbErr != nil {
			slog.Warn("Unable to close migration", "source_error", srcErr, "db_error", dbErr)
		}
	}()

	err = retry(ctx, "run migrate up", func() error {
		if err := migration.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	slog.Info("DB migrated successfully")
	return nil
}

func retry(ctx context.Context, what string, fn func() error) error {
	return retryWithBackoff(ctx, what, migrationRetries, migrationRetryInterval, migrationRetryInterval, fn)
}

// applyTimescalePolicies enables compression and retention when they are configured. Policies that are not
// configured are left untouched, so ones created by hand keep working.
func applyTimescalePolicies(ctx context.Context, pool *pgxpool.Pool, config *TimescaleExporterConfig) error {
	if config.CompressionAfter > 0 {
		var enabled bool
		err := pool.QueryRow(ctx, `SELECT compression_enabled FROM timescaledb_information.hypertables
			WHERE hypertable_name = 'ecoflow_metrics'`).Scan(&enabled)
		if err != nil {
			return fmt.Errorf("cannot read compression settings: %w", err)
		}
		if !enabled {
			_, err = pool.Exec(ctx, `ALTER TABLE ecoflow_metrics SET (timescaledb.compress,
				timescaledb.compress_segmentby = 'serial_number', timescaledb.compress_orderby = 'timestamp DESC')`)
			if err != nil {
				return fmt.Errorf("cannot enable compression: %w", err)
			}
		}
		// re-created on every start, so a changed interval takes effect
		_, err = pool.Exec(ctx, `SELECT remove_compression_policy('ecoflow_metrics', if_exists => true)`)
		if err == nil {
			_, err = pool.Exec(ctx, `SELECT add_compression_policy('ecoflow_metrics', $1::interval)`, pgInterval(config.CompressionAfter))
		}
		if err != nil {
			return fmt.Errorf("cannot set compression policy: %w", err)
		}
		slog.Info("TimescaleDB compression policy set", "compress_after", config.CompressionAfter.String())
	}
	if config.Retention > 0 {
		_, err := pool.Exec(ctx, `SELECT remove_retention_policy('ecoflow_metrics', if_exists => true)`)
		if err == nil {
			_, err = pool.Exec(ctx, `SELECT add_retention_policy('ecoflow_metrics', $1::interval)`, pgInterval(config.Retention))
		}
		if err != nil {
			return fmt.Errorf("cannot set retention policy: %w", err)
		}
		slog.Info("TimescaleDB retention policy set", "retention", config.Retention.String())
	}
	return nil
}

// pgInterval formats d for PostgreSQL, in days when possible so policies read well in timescaledb_information.jobs
func pgInterval(d time.Duration) string {
	const day = 24 * time.Hour
	if d%day == 0 {
		return fmt.Sprintf("%d days", d/day)
	}
	return fmt.Sprintf("%d seconds", int64(d.Seconds()))
}

func (t *TimescaleExporter) Handle(ctx context.Context, u *DeviceUpdate) error {
	device := u.Device
	if device.Online == 0 {
		slog.Info("Device is offline", "SN", device.SN)
	}
	slog.Debug("Handling metrics for device", "dev", device.SN)

	if err := t.insertMetric(ctx, u, timescaleMetrics(t.config.Prefix, u)); err != nil {
		return fmt.Errorf("unable to insert metrics: %w", err)
	}
	slog.Debug("Inserted metrics", "device", device.SN, "device_name", device.Name)
	return nil
}

// timescaleMetrics converts parameter names to metric names and drops arrays. Other values are stored as is.
func timescaleMetrics(prefix string, u *DeviceUpdate) map[string]any {
	samples := u.Samples(prefix)
	dbMetrics := make(map[string]any, len(samples)+1)
	for _, s := range samples {
		if _, isArray := s.Raw.([]any); isArray {
			slog.Debug("The value is an array, skipping it", "metric", s.Param)
			continue
		}
		dbMetrics[s.Name] = s.Raw
	}
	onlineName, _, _ := generateMetricName("online", prefix, u.Device.SN)
	dbMetrics[onlineName] = float64(u.Device.Online)
	return dbMetrics
}

// two updates of one device can share a timestamp (the primary key), the later one wins
const insertMetricQuery = `
INSERT INTO ecoflow_metrics (timestamp, serial_number, device_name, metrics)
VALUES ($1, $2, $3, $4)
ON CONFLICT (timestamp, serial_number) DO UPDATE
SET device_name = EXCLUDED.device_name, metrics = EXCLUDED.metrics
`

func (t *TimescaleExporter) insertMetric(ctx context.Context, u *DeviceUpdate, dbMetrics map[string]any) error {
	metrics, err := json.Marshal(dbMetrics)
	if err != nil {
		return err
	}
	_, err = t.ConnectionPool.Exec(ctx, insertMetricQuery, u.Time, u.Device.SN, u.Device.Name, metrics)
	return err
}

func (t *TimescaleExporter) Close(_ context.Context) {
	slog.Debug("Trying to close connection pool")
	t.ConnectionPool.Close()
	slog.Debug("Closed connection pool")
}
