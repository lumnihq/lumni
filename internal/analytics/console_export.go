package analytics

import (
	"bytes"
	"context"
	"encoding/json"
	"time"
)

// ExportConsole ships replacement aggregates, including zeroed cells, so later
// outcomes and rechecks correct previously exported counts in ClickHouse.
func (e *Exporter) ExportConsole(ctx context.Context) (int, error) {
	if !e.Enabled() {
		return 0, nil
	}
	if err := e.ensureConsoleSchema(ctx); err != nil {
		return 0, err
	}
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT tenant_id::text,bucket,actor_id,operation,app,kind,reason_code,outcome,would,total,cost_micros_usd,kept_micros_usd,last_at,updated_at FROM console_metrics WHERE exported_at IS NULL ORDER BY updated_at LIMIT 1000 FOR UPDATE SKIP LOCKED`)
	if err != nil {
		return 0, err
	}
	type cell struct {
		Tenant string `json:"tenant_id"`
		ConsoleMetric
		Updated time.Time `json:"updated_at"`
	}
	cells := []cell{}
	for rows.Next() {
		var c cell
		if err = rows.Scan(&c.Tenant, &c.Bucket, &c.Actor, &c.Operation, &c.App, &c.Kind, &c.Reason, &c.Outcome, &c.Would, &c.Total, &c.Cost, &c.Kept, &c.Last, &c.Updated); err != nil {
			break
		}
		cells = append(cells, c)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return 0, err
	}
	if len(cells) == 0 {
		return 0, nil
	}
	var body bytes.Buffer
	body.WriteString("INSERT INTO console_metrics FORMAT JSONEachRow\n")
	encoder := json.NewEncoder(&body)
	for _, c := range cells {
		var row map[string]any
		_ = json.Unmarshal(jsonBytesConsole(c), &row)
		row["bucket"] = c.Bucket.UTC().Format("2006-01-02 15:04:05.000000")
		row["last_at"] = c.Last.UTC().Format("2006-01-02 15:04:05.000000")
		row["updated_at"] = c.Updated.UTC().Format("2006-01-02 15:04:05.000000")
		row["ingest_version"] = uint64(c.Updated.UnixMicro())
		if err = encoder.Encode(row); err != nil {
			return 0, err
		}
	}
	if err = e.execute(ctx, body.Bytes()); err != nil {
		return 0, err
	}
	for _, c := range cells {
		if _, err = tx.Exec(ctx, `UPDATE console_metrics SET exported_at=now() WHERE tenant_id=$1 AND bucket=$2 AND actor_id=$3 AND operation=$4 AND app=$5 AND kind=$6 AND reason_code=$7 AND outcome=$8 AND would=$9`, c.Tenant, c.Bucket, c.Actor, c.Operation, c.App, c.Kind, c.Reason, c.Outcome, c.Would); err != nil {
			return 0, err
		}
	}
	return len(cells), tx.Commit(ctx)
}
func (e *Exporter) ensureConsoleSchema(ctx context.Context) error {
	e.consoleSchemaMu.Lock()
	defer e.consoleSchemaMu.Unlock()
	if e.consoleSchemaReady {
		return nil
	}
	err := e.execute(ctx, []byte(`CREATE TABLE IF NOT EXISTS console_metrics (
 tenant_id String,bucket DateTime64(6,'UTC'),actor_id String,operation LowCardinality(String),app LowCardinality(String),kind LowCardinality(String),reason_code LowCardinality(String),outcome LowCardinality(String),would LowCardinality(String),total Int64,cost_micros_usd Int64,kept_micros_usd Int64,last_at DateTime64(6,'UTC'),updated_at DateTime64(6,'UTC'),ingest_version UInt64)
 ENGINE=ReplacingMergeTree(ingest_version) PARTITION BY toYYYYMM(bucket) ORDER BY(tenant_id,bucket,actor_id,operation,app,kind,reason_code,outcome,would) TTL bucket+INTERVAL 730 DAY DELETE`))
	if err == nil {
		e.consoleSchemaReady = true
	}
	return err
}
func jsonBytesConsole(v any) []byte { b, _ := json.Marshal(v); return b }
