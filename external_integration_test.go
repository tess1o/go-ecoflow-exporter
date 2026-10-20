package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests need real servers and are skipped unless pointed at them, for example:
//
//	docker run -d --rm -e POSTGRES_PASSWORD=postgres -p 55432:5432 timescale/timescaledb-ha:pg16
//	docker run -d --rm -p 56379:6379 redislabs/redistimeseries:latest
//	TIMESCALE_TEST_URL='postgres://postgres:postgres@localhost:55432/postgres?sslmode=disable' \
//	REDIS_TEST_ADDR=localhost:56379 go test -run External ./...

func TestExternal_Timescale(t *testing.T) {
	dbUrl := os.Getenv("TIMESCALE_TEST_URL")
	if dbUrl == "" {
		t.Skip("TIMESCALE_TEST_URL is not set")
	}
	ctx := context.Background()
	exporter, err := NewTimescaleExporter(ctx, &TimescaleExporterConfig{Prefix: "ecoflow", TimescaleUrl: dbUrl})
	require.NoError(t, err)
	defer exporter.Close(ctx)

	sn := "EXT" + time.Now().Format("150405.000")
	params := map[string]any{"pd.soc": 77.0, "list": []any{1.0}}
	handle(ctx, exporter, EcoflowDevice{SN: sn, Name: "External", Online: 1}, params)
	assert.Len(t, params, 2, "input must not be modified")

	var name string
	var raw []byte
	err = exporter.ConnectionPool.QueryRow(ctx,
		"SELECT device_name, metrics FROM ecoflow_metrics WHERE serial_number = $1", sn).Scan(&name, &raw)
	require.NoError(t, err)
	var metrics map[string]any
	require.NoError(t, json.Unmarshal(raw, &metrics))
	assert.Equal(t, "External", name)
	assert.Equal(t, map[string]any{"ecoflow_pd_soc": 77.0, "ecoflow_online": 1.0}, metrics)

	// an update with the same timestamp replaces the row, the documented query keeps working
	u := NewDeviceUpdate(EcoflowDevice{SN: sn, Name: "External", Online: 1}, map[string]any{"pd.soc": 78.0})
	require.NoError(t, exporter.Handle(ctx, u))
	u2 := NewDeviceUpdate(u.Device, map[string]any{"pd.soc": 79.0})
	u2.Time = u.Time
	require.NoError(t, exporter.Handle(ctx, u2))
	var soc float64
	var ts time.Time
	err = exporter.ConnectionPool.QueryRow(ctx, `SELECT timestamp, cast(metrics ->> 'ecoflow_pd_soc' AS float)
		FROM ecoflow_metrics WHERE serial_number = $1 ORDER BY timestamp DESC LIMIT 1`, sn).Scan(&ts, &soc)
	require.NoError(t, err)
	assert.Equal(t, 79.0, soc)
	assert.WithinDuration(t, u.Time, ts, time.Millisecond, "the update time is stored")
}

func TestExternal_TimescalePolicies(t *testing.T) {
	dbUrl := os.Getenv("TIMESCALE_TEST_URL")
	if dbUrl == "" {
		t.Skip("TIMESCALE_TEST_URL is not set")
	}
	ctx := context.Background()
	cfg := &TimescaleExporterConfig{Prefix: "ecoflow", TimescaleUrl: dbUrl, CompressionAfter: 7 * 24 * time.Hour, Retention: 365 * 24 * time.Hour}
	for range 2 { // applying the policies again (a restart) must work
		exporter, err := NewTimescaleExporter(ctx, cfg)
		require.NoError(t, err)
		require.NoError(t, applyTimescalePolicies(ctx, exporter.ConnectionPool, cfg))
		exporter.Close(ctx)
	}
	cfg.CompressionAfter = 14 * 24 * time.Hour
	exporter, err := NewTimescaleExporter(ctx, cfg)
	require.NoError(t, err)
	defer exporter.Close(ctx)

	rows, err := exporter.ConnectionPool.Query(ctx, `SELECT proc_name, config->>'compress_after', config->>'drop_after'
		FROM timescaledb_information.jobs WHERE hypertable_name = 'ecoflow_metrics' ORDER BY proc_name`)
	require.NoError(t, err)
	var jobs []string
	for rows.Next() {
		var proc string
		var compressAfter, dropAfter *string
		require.NoError(t, rows.Scan(&proc, &compressAfter, &dropAfter))
		jobs = append(jobs, fmt.Sprintf("%s %v %v", proc, deref(compressAfter), deref(dropAfter)))
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []string{"policy_compression 14 days <nil>", "policy_retention <nil> 365 days"}, jobs)

	// compressed chunks stay readable with the documented queries
	sn := "EXTC" + time.Now().Format("150405.000")
	u := NewDeviceUpdate(EcoflowDevice{SN: sn, Name: "Old", Online: 1}, map[string]any{"pd.soc": 42.0})
	u.Time = time.Now().Add(-30 * 24 * time.Hour)
	require.NoError(t, exporter.Handle(ctx, u))
	_, err = exporter.ConnectionPool.Exec(ctx, `SELECT compress_chunk(c, if_not_compressed => true)
		FROM show_chunks('ecoflow_metrics', older_than => INTERVAL '14 days') c`)
	require.NoError(t, err)
	var soc float64
	require.NoError(t, exporter.ConnectionPool.QueryRow(ctx,
		`SELECT cast(metrics ->> 'ecoflow_pd_soc' AS float) FROM ecoflow_metrics WHERE serial_number = $1`, sn).Scan(&soc))
	assert.Equal(t, 42.0, soc)
}

func deref(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func TestExternal_Redis(t *testing.T) {
	addr := os.Getenv("REDIS_TEST_ADDR")
	if addr == "" {
		t.Skip("REDIS_TEST_ADDR is not set")
	}
	ctx := context.Background()
	exporter := NewRedisExporter(&RedisExporterConfig{Prefix: "ecoflow", RedisConfig: &redis.Options{Addr: addr, DisableIdentity: true}})
	defer exporter.Close(ctx)

	sn := "EXT" + time.Now().Format("150405.000")
	handle(ctx, exporter, EcoflowDevice{SN: sn, Name: "External", Online: 1}, map[string]any{"pd.soc": 77.0, "flag": true})

	client := redis.NewClient(&redis.Options{Addr: addr})
	defer client.Close()
	for key, want := range map[string]string{"ts:" + sn + ":ecoflow_pd_soc": "77", "ts:" + sn + ":ecoflow_flag": "1", "ts:" + sn + ":ecoflow_online": "1"} {
		res, err := client.Do(ctx, "TS.GET", key).Slice()
		require.NoError(t, err, key)
		require.Len(t, res, 2, key)
		assert.Equal(t, want, res[1], key)
	}
	last, err := client.Get(ctx, "device:last_access:"+sn).Result()
	require.NoError(t, err)
	assert.NotEmpty(t, last)

	// several updates within one second must not fail
	for _, v := range []float64{80, 81} {
		require.NoError(t, exporter.Handle(ctx, NewDeviceUpdate(EcoflowDevice{SN: sn, Name: "External", Online: 1}, map[string]any{"pd.soc": v})))
	}
	res, err := client.Do(ctx, "TS.GET", "ts:"+sn+":ecoflow_pd_soc").Slice()
	require.NoError(t, err)
	assert.Equal(t, "81", res[1], "the last value wins")

	// new keys get labels and the retention
	exporter.config.Retention = 30 * 24 * time.Hour
	sn2 := sn + "R"
	require.NoError(t, exporter.Handle(ctx, NewDeviceUpdate(EcoflowDevice{SN: sn2, Name: "My Delta", Online: 1}, map[string]any{"pd.soc": 1.0})))
	keys, err := client.Do(ctx, "TS.QUERYINDEX", "device=My Delta", "serial_number="+sn2).StringSlice()
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"ts:" + sn2 + ":ecoflow_pd_soc", "ts:" + sn2 + ":ecoflow_online"}, keys)
	info, err := client.Do(ctx, "TS.INFO", "ts:"+sn2+":ecoflow_pd_soc").Slice()
	require.NoError(t, err)
	for i := 0; i+1 < len(info); i += 2 {
		if info[i] == "retentionTime" {
			assert.EqualValues(t, 30*24*time.Hour.Milliseconds(), info[i+1])
		}
	}
}
