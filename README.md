# ⚡ EcoFlow to Prometheus exporter in Go via Rest API

# Caution

It's a beta version. It works fine, I need to provide more documentation

## About the project

This is an Ecoflow metrics exporter to Prometheus implemented in Go.\
It uses library https://github.com/tess1o/go-ecoflow to fetch the metrics from your Ecoflow devices via Ecoflow Rest
API. More details about the API are on their website: https://developer-eu.ecoflow.com/

Other known to me projects use MQTT protocol to scrap the metrics, this implementation uses Rest API.

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

# TODO

1. Instructions how to get AccessToken and SecretToken
2. Instructions how to run exporter, grafana and prometheus
3. Instructions how to import grafana dashboard
4. Github workflow to automatically push new 