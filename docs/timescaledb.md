## Database structure

All metrics are stored in a single table `ecoflow_metrics`. The structure of the table:

```sql
CREATE TABLE if not exists ecoflow_metrics
(
    timestamp     TIMESTAMPTZ default NOW(),
    serial_number VARCHAR NOT NULL,
    metrics       JSONB   NOT NULL,
    PRIMARY KEY (timestamp, serial_number)
);

SELECT create_hypertable('ecoflow_metrics', 'timestamp', if_not_exists => TRUE);
```

The metric values are stored in one JSONB column `metrics`. It's a key/value map: "metric1" : "value1", "metric2" : "
value2".
One row in the table stores all metrics for given device for given timestamp.
You can easily get the metric name using jsonb operator: `cast(metrics ->> 'METRIC_NAME' AS float)`.
You can find usage examples below in this document.

There is no cleanup procedure implemented at the moment, so you might want to cleanup all records by yourself.

## How to run the TimescaleDB, Exporter and Grafana using docker-compose

1. Go to docker-compose folder: `cd docker-compose`
2. Update `.env` file with two mandatory parameters:
    - `ECOFLOW_ACCESS_KEY` - the access key from the Ecoflow development website
    - `ECOFLOW_SECRET_KEY` - the secret key from the Ecoflow development website
    - `TIMESCALE_ENABLED` - enable integration with TimescaleDB
3. (OPTIONALLY) Update other variables if you need to:
    - `TIMESCALE_USERNAME` - TimescaleDB username. Default value: `postgres`
    - `TIMESCALE_PASSWORD` - TimescaleDB password. Default value: `postgres`
    - `TIMESCALE_URL` - TimescaleDB connection string. The username and password should match the values
      from `TIMESCALE_USERNAME` and `TIMESCALE_PASSWORD`. Default
      value: `postgresql://postgres:postgres@timescaledb:5432/postgres?sslmode=disable`
    - `METRIC_PREFIX`: the prefix that will be added to all metrics. Default value is `ecoflow`. For instance
      metric `bms_bmsStatus.minCellTemp` will be exported to prometheus as `ecoflow.bms_bmsStatus.minCellTemp`.
    - `SCRAPING_INTERVAL` - scrapping interval in seconds. How often should the exporter execute requests to Ecoflow
      Rest API in order to get the data. Default value is 30 seconds.
    - `DEBUG_ENABLED` - enable debug log messages. Default value is "false". To enable use values `true` or `1`
    - `GRAFANA_USERNAME` - admin username in Grafana. Default value: `grafana`. Can be changed later in Grafana UI
    - `GRAFANA_PASSWORD` - admin password in Grafana. Default value: `grafana`. Can be changed later in Grafana UI

4. Save `.env` file with your changes.
5. Start timescaledb container: `docker-compose -f docker-compose/timescale-compose.yml up -d`.\
   :exclamation: *NOTE*: The exporter does not wait until the TimescaleDB is UP, because TimescaleDB is an optional dependency for the
   exporter. Thus, it's important to start the DB first and then the exporter. There is a retry mechanism to wait for the DB
   to be operational, however it's better to do not rely on it and start the DB before the exporter.
6. Start the exporter and
   grafana: `docker-compose -f docker-compose/grafana-compose.yml -f docker-compose/exporter-remote-compose.yml up -d`
7. The services are available here:
    - http://localhost:3000 - Grafana
    - TimescaleDB is available at the value of `TIMESCALE_URL` variable
8. Configure a new TimescaleDB datasource in Grafana according to example below:
   ![timescaledb-datasource.png](images/timescaledb-datasource.png)
9. Create your dashboard.

## Dashboard example

![img.png](images/dashboard_example.png)

## Tips how to fetch data from TimescaleDB

1. To fetch one metric you can use query like this:

```sql
SELECT EXTRACT(EPOCH FROM timestamp)                        as time,
       cast(metrics ->> 'ecoflow_inv_input_watts' as float) AS "Input Watts"
FROM ecoflow_metrics
where $__timeFilter(timestamp)
and serial_number = '$device'
```

Prometheus equivalent is: `ecoflow_inv_input_watts{device="$device"}`

2. To fetch one value ("current" metric value) use the query:

```sql
SELECT EXTRACT(EPOCH FROM timestamp)                        AS time,
       cast(metrics ->> 'ecoflow_pd_watts_in_sum' as float) as value
FROM ecoflow_metrics
where serial_number = '$device'
ORDER BY 1 DESC
LIMIT 1;
```

Prometheus equivalent is: `ecoflow_pd_watts_in_sum{device="$device"}`

3. You can check how long you had a power outage and how long you got grid power on using the query:

```sql
WITH PowerEvents AS (SELECT timestamp,
                            CASE
                                WHEN cast(metrics ->> 'ecoflow_inv_ac_in_vol' AS float) = 0 THEN 'Power Off'
                                ELSE 'Power On'
                                END AS power_status,
                            serial_number
                     FROM ecoflow_metrics
                     WHERE serial_number = '$device'
                       and $__timeFilter(timestamp)),
     FilteredEvents AS (SELECT timestamp,
                               power_status,
                               LAG(power_status) OVER (ORDER BY timestamp) AS prev_power_status
                        FROM PowerEvents),
     ChangedEvents AS (SELECT timestamp,
                              power_status
                       FROM FilteredEvents
                       WHERE power_status IS DISTINCT FROM prev_power_status),
     NextEvents AS (SELECT timestamp                                 AS event_time,
                           power_status,
                           LEAD(timestamp) OVER (ORDER BY timestamp) AS next_event_time
                    FROM ChangedEvents)
SELECT event_time   as "Event Time",
       power_status as "Power Status",
       round(COALESCE(
                     EXTRACT(EPOCH FROM (next_event_time - event_time)) / 60,
                     EXTRACT(EPOCH FROM (NOW() - event_time)) / 60
             ), 0)  AS "Duration Minutes"
FROM NextEvents
ORDER BY event_time DESC;
```

Example:
![img.png](images/timescale_power_outage_table.png)
