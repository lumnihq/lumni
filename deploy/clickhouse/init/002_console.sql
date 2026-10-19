CREATE TABLE IF NOT EXISTS lumni.console_metrics
(
 tenant_id String, bucket DateTime64(6,'UTC'), actor_id String, operation LowCardinality(String),
 app LowCardinality(String), kind LowCardinality(String), reason_code LowCardinality(String),
 outcome LowCardinality(String), would LowCardinality(String), total Int64,
 cost_micros_usd Int64, kept_micros_usd Int64, last_at DateTime64(6,'UTC'),
 updated_at DateTime64(6,'UTC'), ingest_version UInt64
)
ENGINE=ReplacingMergeTree(ingest_version)
PARTITION BY toYYYYMM(bucket)
ORDER BY(tenant_id,bucket,actor_id,operation,app,kind,reason_code,outcome,would)
TTL bucket+INTERVAL 730 DAY DELETE;
