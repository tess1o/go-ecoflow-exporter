## How to run the Prometheus, Exporter and Grafana using docker-compose

1. Go to docker-compose folder: `cd docker-compose`
2. Update `.env` file with two mandatory parameters:
    - `ECOFLOW_ACCESS_KEY` - the access key from the Ecoflow development website
    - `ECOFLOW_SECRET_KEY` - the secret key from the Ecoflow development website
    - `PROMETHEUS_ENABLED` - true (or 1) if you want to enable integration with Prometheus. Default value is false
3. (OPTIONALLY) Update other variables if you need to:
    - `METRIC_PREFIX`: the prefix that will be added to all metrics. Default value is `ecoflow`. For instance
      metric `bms_bmsStatus.minCellTemp` will be exported to prometheus as `ecoflow.bms_bmsStatus.minCellTemp`. With
      default value `ecoflow` you can use Grafana Dashboard with ID `17812` without any changes.
    - `SCRAPING_INTERVAL` - scrapping interval in seconds. How often should the exporter execute requests to Ecoflow
      Rest API in order to get the data. Default value is 30 seconds. Align this value
      with `docker-compose/prometheus/prometheus.yml`
    - `DEBUG_ENABLED` - enable debug log messages. Default value is "false". To enable use values `true` or `1`
    - `GRAFANA_USERNAME` - admin username in Grafana. Default value: `grafana`. Can be changed later in Grafana UI
    - `GRAFANA_PASSWORD` - admin password in Grafana. Default value: `grafana`. Can be changed later in Grafana UI
4. Save `.env` file with your changes.
5. Start all containers: `docker-compose -f docker-compose/grafana-compose.yml -f docker-compose/exporter-remote-compose.yml up -f docker-compose/prometheus-compose.yml up -d`
     ```
   CONTAINER ID   IMAGE                                COMMAND                  CREATED          STATUS         PORTS                                         NAMES
   93c9cf317861   docker-compose-go_ecoflow_exporter   "/app/ecoflow-export…"   6 seconds ago    Up 5 seconds   0.0.0.0:2112->2112/tcp, :::2112->2112/tcp     go_ecoflow_exporter
   fea150b4ef5d   grafana/grafana                      "/run.sh"                16 minutes ago   Up 5 seconds   0.0.0.0:3000->3000/tcp, :::3000->3000/tcp     grafana
   823c6adfad90   prom/prometheus                      "/bin/prometheus --c…"   16 minutes ago   Up 5 seconds   0.0.0.0:9090->9090/tcp, :::9090->9090/tcp     prometheus
   ```
6. The services are available here:
    - http://localhost:2112/metrics - the exporter
    - http://localhost:9090 - Prometheus
    - http://localhost:3000 - Grafana

7. Navigate to http://localhost:3000 in your web browser and use GRAFANA_USERNAME / GRAFANA_PASSWORD credentials from
   .env file to access Grafana. It is already configured with prometheus as the default datasource.
   Navigate to Dashboards → Import dashboard → import ID `17812`, select the only existing Prometheus datasource.
   (The Grafana dashboard was implemented
   in https://github.com/berezhinskiy/ecoflow_exporter/tree/master/docker-compose)