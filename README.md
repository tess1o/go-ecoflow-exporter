# EcoFlow Prometheus exporter

Exports the parameters of your EcoFlow devices as Prometheus metrics.
It is a single small binary (about 10 MB of RAM for a few devices), built to run unattended for months.

Data is read in one of two ways:

- **MQTT** (recommended): the same real-time channel the EcoFlow app uses. Needs your EcoFlow app email and
  password, and the serial numbers of your devices.
- **REST API**: EcoFlow's official developer API, polled every 30 seconds. Needs an access key and a secret
  key (see [below](#getting-rest-api-keys)) and exports all devices linked to the account.

![Dashboard example](docs/images/dashboard_example.png)

## Quick start

1. Download [`docker-compose.yml`](docker-compose.yml) and fill in your credentials and device serial numbers.
2. Start the exporter: `docker compose up -d`
3. Check http://localhost:2112/metrics, then add the exporter to Prometheus:

   ```yaml
   scrape_configs:
     - job_name: ecoflow
       static_configs:
         - targets: ["<exporter host>:2112"]
   ```

4. Import [`grafana_dashboards/ecoflow-prometheus.json`](grafana_dashboards/ecoflow-prometheus.json) into Grafana
   (Dashboards → New → Import) and pick your Prometheus data source.

Without Docker: `go build -o ecoflow-exporter .` and run it with the environment variables below.

## Configuration

All settings are environment variables. Only the credentials (and the devices for MQTT) are required.

| Variable | Default | Description |
|---|---|---|
| `ECOFLOW_EMAIL`, `ECOFLOW_PASSWORD` | | MQTT mode: EcoFlow app account |
| `ECOFLOW_DEVICES` | | MQTT mode (required): device serial numbers, comma separated |
| `ECOFLOW_ACCESS_KEY`, `ECOFLOW_SECRET_KEY` | | REST API mode: developer keys |
| `ECOFLOW_DEVICES_PRETTY_NAMES` | | Names for the `device` label, JSON: `{"R33XXXXXXXXX":"My Delta 2"}`. Devices listed here don't need to be in `ECOFLOW_DEVICES` |
| `EXPORTER_TYPE` | detected | `mqtt` or `rest`. When not set it follows the credentials; REST wins when both are set |
| `METRIC_PREFIX` | `ecoflow` | Prefix of all metric names |
| `PROMETHEUS_PORT` | `2112` | Port serving `/metrics` and `/healthz` |
| `MQTT_DEVICE_OFFLINE_THRESHOLD_SECONDS` | `300` | A device that sends nothing for this long is reported offline |
| `MQTT_PING_INTERVAL_SECONDS` | `20` | How often devices are pinged to keep them sending data |
| `MQTT_STATE_REFRESH_INTERVAL_SECONDS` | `300` | How often devices are asked for their full state |
| `SCRAPING_INTERVAL` | `30` | REST API mode: seconds between polls |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` |
| `LOG_FORMAT` | `text` | `text` or `json` |
| `PROTOBUF_DEBUG` | `false` | Log raw protobuf messages, see [PROTOBUF_TROUBLESHOOTING.md](PROTOBUF_TROUBLESHOOTING.md) |

Secrets can also be read from files (Docker secrets): `ECOFLOW_PASSWORD_FILE`, `ECOFLOW_SECRET_KEY_FILE`.

The exporter refuses to start when a value is invalid (for example `PROMETHEUS_PORT=abc`) and reports all
problems at once, so a typo cannot silently fall back to a default.

## Metrics

- `<prefix>_<parameter>{device, serial_number}`: every numeric device parameter. EcoFlow names are converted to
  snake case, for example `inv.acInVol` → `ecoflow_inv_ac_in_vol`. Booleans are exported as 0/1.
- `<prefix>_online{device, serial_number}`: 1 when the device is online, 0 when it is offline.
- `<prefix>_exporter_*`: the health of the exporter itself, such as `mqtt_connected`,
  `last_message_timestamp_seconds`, `mqtt_forced_reconnects_total`, `mqtt_relogins_total`,
  `rest_requests_total` and `build_info`.

**Offline devices.** When a device goes offline, its parameter series are removed and only `<prefix>_online`
stays (with value 0). Charts then show a gap instead of a misleading drop to zero, and alerts don't see stale
values. For example, this rule fires only when all three devices are online and report no grid power:

```promql
count(ecoflow_inv_ac_in_vol < 180000) >= 3
```

## Reliability

- Logging in is retried with backoff, so an EcoFlow outage at startup doesn't stop the exporter.
- When no device sends data, the exporter reconnects to the broker (with growing pauses, because the devices may
  simply be switched off). If three reconnects bring nothing back, it logs in again to get fresh broker
  credentials.
- `/healthz` reports whether the exporter can collect data (Docker uses it for the container health status).
  If the exporter stays unhealthy for 30 minutes it exits, and the `restart: unless-stopped` policy starts it
  fresh.

## Getting REST API keys

1. Go to https://developer-eu.ecoflow.com/ and click "Become a Developer".
2. Log in with your EcoFlow account and wait for the approval email ("Approval notice from EcoFlow Developer
   Platform").
3. Create an access key and a secret key at https://developer-eu.ecoflow.com/us/security.

## Upgrading from v1

v2 exports to Prometheus only and needs fewer settings:

- TimescaleDB and Redis support is removed, together with all `TIMESCALE_*` and `REDIS_*` variables.
- `PROMETHEUS_ENABLED` is removed (Prometheus is always on), and so are `PROMETHEUS_METRIC_TTL_SECONDS`,
  `HANDLER_QUEUE_SIZE`, `HANDLER_TIMEOUT_SECONDS` and `HANDLER_UNHEALTHY_AFTER_SECONDS`.
- `DEBUG_ENABLED` is removed, use `LOG_LEVEL=debug`.
- Offline devices no longer report all parameters as 0. Their series are removed and only `<prefix>_online` stays.
  Alerts like `ecoflow_inv_ac_in_vol == 0` no longer fire for offline devices, so use `ecoflow_online == 0`
  to alert on those.
- The `<prefix>_exporter_handler_*` metrics are removed.
- `MQTT_DEVICE_OFFLINE_THRESHOLD_SECONDS` now defaults to 300 (was 60). Some devices stay silent for over a
  minute while their readings are stable.
- `EXPORTER_TYPE` is detected from the credentials when not set.
- Invalid values (for example `PROTOBUF_DEBUG=yes`) now stop the exporter at startup instead of being ignored.
- The `docker-compose/` folder (Prometheus, Grafana, Alertmanager, TimescaleDB, Redis setups) is replaced by a
  single [`docker-compose.yml`](docker-compose.yml) for the exporter.
- Docker images are published only for release tags: `2`, `2.x`, `2.x.y` and `latest`.

## Credits

Inspired by https://github.com/berezhinskiy/ecoflow_exporter. Metric names use the same `ecoflow` prefix and
conversion rules. Device communication is done with https://github.com/tess1o/go-ecoflow.
