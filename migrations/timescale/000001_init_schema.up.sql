CREATE TABLE if not exists ecoflow_metrics
(
    timestamp     TIMESTAMPTZ default NOW(),
    serial_number VARCHAR NOT NULL,
    metrics       JSONB   NOT NULL,
    PRIMARY KEY (timestamp, serial_number)
);


SELECT create_hypertable('ecoflow_metrics', 'timestamp', if_not_exists => TRUE);