-- name: InsertMetric :one
INSERT INTO ecoflow_metrics (serial_number, metrics)
VALUES ($1, $2)
RETURNING *;

-- name: GetMetricsByDevice :many
SELECT timestamp, serial_number, metrics
FROM ecoflow_metrics
WHERE serial_number = $1
ORDER BY timestamp DESC;