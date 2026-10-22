-- +migrate Up
CREATE TABLE register_readings (
    time     TIMESTAMPTZ      NOT NULL,
    device   TEXT             NOT NULL, -- device name from the config
    register TEXT             NOT NULL, -- register name from the config, e.g. rUHV
    value    DOUBLE PRECISION NOT NULL
) WITH (
    timescaledb.hypertable,
    timescaledb.partition_column = 'time',
    timescaledb.chunk_interval = '1 day',
    timescaledb.segmentby = 'device, register',
    timescaledb.orderby = 'time DESC'
);

CREATE INDEX register_readings_device_register_time_idx
    ON register_readings (device, register, time DESC);

-- +migrate Down
DROP TABLE register_readings;
