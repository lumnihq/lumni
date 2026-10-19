package analytics

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Exporter struct {
	pool               *pgxpool.Pool
	endpoint           string
	database           string
	user               string
	password           string
	client             *http.Client
	logger             *slog.Logger
	consoleSchemaMu    sync.Mutex
	consoleSchemaReady bool
}

func NewExporter(pool *pgxpool.Pool, endpoint, database, user, password string, timeout time.Duration, logger *slog.Logger) *Exporter {
	return &Exporter{pool: pool, endpoint: strings.TrimRight(endpoint, "/"), database: database, user: user, password: password, client: &http.Client{Timeout: timeout}, logger: logger}
}

func (e *Exporter) Enabled() bool { return e != nil && e.endpoint != "" }

type fact struct {
	TenantID       string  `json:"tenant_id"`
	EventID        string  `json:"event_id"`
	SourceSequence uint64  `json:"source_sequence"`
	Kind           string  `json:"kind"`
	EntityType     string  `json:"entity_type"`
	EntityID       string  `json:"entity_id"`
	Relationship   string  `json:"relationship"`
	SourceID       string  `json:"source_id"`
	TargetID       string  `json:"target_id"`
	Status         string  `json:"status"`
	Decision       string  `json:"decision"`
	ReasonCode     string  `json:"reason_code"`
	Provider       string  `json:"provider"`
	Model          string  `json:"model"`
	WasteReason    string  `json:"waste_reason"`
	AmountMicros   int64   `json:"amount_micros_usd"`
	DurationMS     float64 `json:"duration_ms"`
	OccurredAt     string  `json:"occurred_at"`
	IngestVersion  uint64  `json:"ingest_version"`
}

func (e *Exporter) Once(ctx context.Context) (int, error) {
	if !e.Enabled() {
		return 0, nil
	}
	tx, err := e.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT id,tenant_id::text,event_id,kind,entity_type,entity_id,relationship,source_id,target_id,
		status,decision,reason_code,provider,model,waste_reason,amount_micros_usd,duration_ms,occurred_at
		FROM analytics_outbox WHERE exported_at IS NULL AND next_attempt_at<=clock_timestamp()
		ORDER BY id LIMIT 1000 FOR UPDATE SKIP LOCKED`)
	if err != nil {
		return 0, err
	}
	var ids []int64
	var facts []fact
	for rows.Next() {
		var id int64
		var occurred time.Time
		var item fact
		if err := rows.Scan(&id, &item.TenantID, &item.EventID, &item.Kind, &item.EntityType, &item.EntityID,
			&item.Relationship, &item.SourceID, &item.TargetID, &item.Status, &item.Decision, &item.ReasonCode,
			&item.Provider, &item.Model, &item.WasteReason, &item.AmountMicros, &item.DurationMS, &occurred); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
		item.SourceSequence = uint64(id)
		item.IngestVersion = uint64(time.Now().UTC().UnixMicro())
		item.OccurredAt = occurred.UTC().Format("2006-01-02 15:04:05.000000")
		facts = append(facts, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	if len(facts) == 0 {
		return 0, tx.Commit(ctx)
	}
	if err := e.insert(ctx, facts); err != nil {
		_, _ = tx.Exec(ctx, `UPDATE analytics_outbox SET attempts=attempts+1,last_error=$2,
			next_attempt_at=clock_timestamp()+make_interval(secs=>LEAST(300,power(2,LEAST(attempts+1,8))::int))
			WHERE id=ANY($1)`, ids, truncateError(err))
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return 0, commitErr
		}
		return 0, err
	}
	if _, err := tx.Exec(ctx, `UPDATE analytics_outbox SET exported_at=clock_timestamp(),attempts=attempts+1,last_error=NULL WHERE id=ANY($1)`, ids); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	if err := e.refreshRollups(ctx); err != nil {
		e.logger.Warn("clickhouse rollup refresh deferred", "error", err)
	}
	return len(facts), nil
}

func (e *Exporter) insert(ctx context.Context, facts []fact) error {
	var body bytes.Buffer
	body.WriteString("INSERT INTO analytics_facts FORMAT JSONEachRow\n")
	encoder := json.NewEncoder(&body)
	encoder.SetEscapeHTML(false)
	for _, item := range facts {
		if err := encoder.Encode(item); err != nil {
			return err
		}
	}
	return e.execute(ctx, body.Bytes())
}

func (e *Exporter) refreshRollups(ctx context.Context) error {
	queries := []string{
		`INSERT INTO analytics_rollup_hourly
		 SELECT tenant_id,toStartOfHour(occurred_at) bucket,
		 countIf(kind='event' AND startsWith(status,'ACTION_')) actions,
		 countIf(kind='decision') decisions,
		 countIf((kind='event' AND (status='ACTION_FAILED' OR status='ACTION_UNKNOWN')) OR (kind='decision' AND decision IN('REJECT','REPLAN'))) failures,
		 sumIf(amount_micros_usd,kind='cost') cost_micros_usd,
		 sumIf(amount_micros_usd,kind='cost' AND waste_reason!='') waste_micros_usd,
		 now64(6) rollup_version
		 FROM analytics_facts FINAL WHERE occurred_at>=now()-INTERVAL 48 HOUR
		 GROUP BY tenant_id,bucket`,
		`INSERT INTO analytics_rollup_daily
		 SELECT tenant_id,toStartOfDay(occurred_at) bucket,
		 countIf(kind='event' AND startsWith(status,'ACTION_')) actions,
		 countIf(kind='decision') decisions,
		 countIf((kind='event' AND (status='ACTION_FAILED' OR status='ACTION_UNKNOWN')) OR (kind='decision' AND decision IN('REJECT','REPLAN'))) failures,
		 sumIf(amount_micros_usd,kind='cost') cost_micros_usd,
		 sumIf(amount_micros_usd,kind='cost' AND waste_reason!='') waste_micros_usd,
		 now64(6) rollup_version
		 FROM analytics_facts FINAL WHERE occurred_at>=now()-INTERVAL 120 DAY
		 GROUP BY tenant_id,bucket`,
	}
	for _, query := range queries {
		if err := e.execute(ctx, []byte(query)); err != nil {
			return err
		}
	}
	return nil
}

func (e *Exporter) execute(ctx context.Context, body []byte) error {
	endpoint, err := url.Parse(e.endpoint)
	if err != nil {
		return err
	}
	query := endpoint.Query()
	query.Set("database", e.database)
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	if e.user != "" {
		req.SetBasicAuth(e.user, e.password)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("clickhouse insert failed: %s: %s", resp.Status, strings.TrimSpace(string(payload)))
	}
	return nil
}

func truncateError(err error) string {
	value := err.Error()
	if len(value) > 1000 {
		return value[:1000]
	}
	return value
}
