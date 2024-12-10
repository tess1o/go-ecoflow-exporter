## How to add Alertmanager to get alerts

You can add AlertManager and configure Prometheus to send alert notifications depending on metric values. For instance when some
device goes offline or battery level is less than some value or temperature is too high, etc

### Pre-requisites:

Step #1 is mandatory, all other steps have good enough default settings. You can adjust them if required.

1. Update `bot_token` and `chat_id` in [alertmanager.yml](../docker-compose/alertmanager/alertmanager.yml)
2. Make sure that `alertmanagers` is added to
   `prometheus.yml`: [prometheus.yml](../docker-compose/prometheus/prometheus.yml)
3. Configure alert rules in Prometheus. See example here: [ecoflow.yml](../docker-compose/prometheus/alerts/ecoflow.yml)
4. Adjust notification template if needed: [telegram.tmpl](../docker-compose/alertmanager/templates/telegram.tmpl)

### Installation

* Run `docker compose -f prometheus-compose.yml stop` to stop Prometheus (can be skipped if Prometheus is not running)
* Run `docker compose -f alertmanager-compose.yml up -d` to start Alertmanager
* Run `docker compose -f prometheus-compose.yml up -d` to start Prometheus

As a result you should get notifications in Telegram:

```
‼️ [FIRING:1] Inverter temperature is too high

Description: Inverter temperature Delta 2 Max is too high
```

```
✅ [RESOLVED] Inverter temperature is too high

Description: Inverter temperature Delta 2 Max is too high
```

To find `chat_id` you can use this approach: https://stackoverflow.com/questions/32423837/telegram-bot-how-to-get-a-group-chat-id

Almost all configs were taken from this repository: https://github.com/berezhinskiy/ecoflow_exporter