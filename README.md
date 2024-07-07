# EcoFlow metric exporter in Go via Rest API

## About the project

This is an Ecoflow metrics exporter different target systems implemented in Go. Currently, the supported target systems
are:

1. Prometheus
2. TimescaleDB

Depending on your configuration you can export the metrics to one of those systems or to all at once.

This project uses library https://github.com/tess1o/go-ecoflow to fetch the metrics from your Ecoflow devices via
Ecoflow Rest API. More details about the API are on their website: https://developer-eu.ecoflow.com/

Other known to me projects use MQTT protocol to scrap the metrics, this implementation uses Rest API.

## How to get Access Token and Secret Token

1. Go to https://developer-eu.ecoflow.com/
2. Click on "Become a Developer"
3. Login with your Ecoflow username and Password
4. Wait until the access is approved by Ecoflow
5. Receive email with subject "Approval notice from EcoFlow Developer Platform". May take some time
6. Go to https://developer-eu.ecoflow.com/us/security and create new AccessKey and SecretKey

## How to run the Prometheus, Exporter and Grafana using docker-compose

See documentation here: [Prometheus](docs/prometheus.md)

## How to run the TimescaleDB, Exporter and Grafana using docker-compose
See documentation here: [TimescaleDB](docs/timescaledb.md)

TimescaleDB allows to build more complex logic if you want so. For instance, you can calculate how long you had power
outages and how long the grid power was on. Since all metrics are stored in a PostgreSQL database (TimescaleDB to be
precise), you have the power of SQL to build any kind of metrics or reports you want. Prometheus doesn't provide such
flexibility.

## Compare to other exporters

This implementation is inspired by https://github.com/berezhinskiy/ecoflow_exporter, and it's fully
compatible with their grafana dashboard. This exporter by default uses the same prefix for the metrics (`ecoflow`)
Both exporters support all parameters returned by the API. MQTT and Rest API actually return the same set of parameters.
This implementation was tested on Delta 2 and River 2.

Some difference between this project and https://github.com/berezhinskiy/ecoflow_exporter:

1. This project requires `ACCESS_KEY` and `SECRET_KEY` that can be obtained on https://developer-eu.ecoflow.com/. For
   me, it took less than 1 day until Ecoflow approved access to the API. The other project needs only ecoflow
   credentials (login and password)
2. This project doesn't hardcode devices `Serial Numbers` and this exporter can export metrics from all linked devices.
   Internally it uses Ecoflow API to fetch the list of linked devices and then for each device it exports the
   metrics. https://github.com/berezhinskiy/ecoflow_exporter can export metrics for a single device only, and you have
   to hardcode it's Serial Number in the env variables. If you have 5 devices, then you need to run 5 instances of the
   exporter
3. The image size (not compressed!) of this exporter is only 21MB, `ghcr.io/berezhinskiy/ecoflow_exporter` is 142 MB
4. This implementation is extremely lightweight and barely consumes any RAM & CPU (it needs less than 10MB of RAM to
   scrap metrics from 2 devices)