CREATE DATABASE IF NOT EXISTS lumni;

CREATE TABLE IF NOT EXISTS lumni.analytics_facts
(
    tenant_id String,
    event_id String,
    source_sequence UInt64,
    kind LowCardinality(String),
    entity_type LowCardinality(String),
    entity_id String,
    relationship LowCardinality(String),
    source_id String,
    target_id String,
    status LowCardinality(String),
    decision LowCardinality(String),
    reason_code LowCardinality(String),
    provider LowCardinality(String),
    model LowCardinality(String),
    waste_reason LowCardinality(String),
    amount_micros_usd Int64,
    duration_ms Float64,
    occurred_at DateTime64(6, 'UTC'),
    ingest_version UInt64,
    ingested_at DateTime64(6, 'UTC') DEFAULT now64(6)
)
ENGINE = ReplacingMergeTree(ingest_version)
PARTITION BY toYYYYMM(occurred_at)
ORDER BY (tenant_id, event_id)
TTL occurred_at + INTERVAL 180 DAY DELETE;

CREATE TABLE IF NOT EXISTS lumni.analytics_rollup_hourly
(
    tenant_id String,
    bucket DateTime64(6, 'UTC'),
    actions Int64,
    decisions Int64,
    failures Int64,
    cost_micros_usd Int64,
    waste_micros_usd Int64,
    rollup_version DateTime64(6, 'UTC')
)
ENGINE = ReplacingMergeTree(rollup_version)
PARTITION BY toYYYYMM(bucket)
ORDER BY (tenant_id, bucket)
TTL bucket + INTERVAL 180 DAY DELETE;

CREATE TABLE IF NOT EXISTS lumni.analytics_rollup_daily
(
    tenant_id String,
    bucket DateTime64(6, 'UTC'),
    actions Int64,
    decisions Int64,
    failures Int64,
    cost_micros_usd Int64,
    waste_micros_usd Int64,
    rollup_version DateTime64(6, 'UTC')
)
ENGINE = ReplacingMergeTree(rollup_version)
PARTITION BY toYYYYMM(bucket)
ORDER BY (tenant_id, bucket)
TTL bucket + INTERVAL 730 DAY DELETE;

CREATE VIEW IF NOT EXISTS lumni.analytics_costs_current AS
SELECT tenant_id,event_id,entity_type,entity_id,provider,model,waste_reason,
       amount_micros_usd,occurred_at
FROM lumni.analytics_facts FINAL
WHERE kind = 'cost';
