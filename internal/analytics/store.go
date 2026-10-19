// Package analytics separates eventually-consistent analytical queries from
// Lumni's authoritative PostgreSQL coordination path.
package analytics

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"lumni/pkg/protocol"
)

type Store interface {
	FinOps(ctx context.Context, tenantID string, from, to time.Time) (protocol.FinOpsGraph, error)
	Trends(ctx context.Context, tenantID string, from, to time.Time) ([]protocol.GraphTrendPoint, protocol.AnalyticsFreshness, error)
}

type Postgres struct{ pool *pgxpool.Pool }

func NewPostgres(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

func (p *Postgres) FinOps(ctx context.Context, tenantID string, from, to time.Time) (protocol.FinOpsGraph, error) {
	result := protocol.FinOpsGraph{SchemaVersion: protocol.SchemaVersion, PeriodStart: from, PeriodEnd: to}
	series, freshness, err := p.Trends(ctx, tenantID, from, to)
	if err != nil {
		return protocol.FinOpsGraph{}, err
	}
	result.Series, result.Analytics = series, freshness
	if err := p.pool.QueryRow(ctx, `SELECT COALESCE(sum(amount_micros_usd),0),COALESCE(sum(amount_micros_usd)FILTER(WHERE waste_reason IS NOT NULL),0)
		FROM costs WHERE tenant_id=$1 AND observed_at BETWEEN $2 AND $3`, tenantID, from, to).Scan(&result.TotalMicros, &result.WasteMicros); err != nil {
		return protocol.FinOpsGraph{}, err
	}
	result.ByProvider, err = p.breakdown(ctx, tenantID, from, to, "provider")
	if err != nil {
		return protocol.FinOpsGraph{}, err
	}
	result.ByModel, err = p.breakdown(ctx, tenantID, from, to, "COALESCE(model,'unknown')")
	if err != nil {
		return protocol.FinOpsGraph{}, err
	}
	result.ByWaste, err = p.breakdown(ctx, tenantID, from, to, "COALESCE(waste_reason,'useful')")
	if err != nil {
		return protocol.FinOpsGraph{}, err
	}
	result.Nodes, result.Edges = finOpsTopology(result)
	initializeFinOpsSlices(&result)
	return result, nil
}

func (p *Postgres) Trends(ctx context.Context, tenantID string, from, to time.Time) ([]protocol.GraphTrendPoint, protocol.AnalyticsFreshness, error) {
	bucket := "hour"
	if to.Sub(from) > 7*24*time.Hour {
		bucket = "day"
	}
	rows, err := p.pool.Query(ctx, `WITH buckets AS (
		SELECT date_trunc($4,occurred_at) AS bucket,0::bigint actions,0::bigint decisions,0::bigint failures,0::bigint cost,0::bigint waste
		FROM events WHERE tenant_id=$1 AND occurred_at BETWEEN $2 AND $3
		UNION ALL
		SELECT date_trunc($4,requested_at),count(*),0,count(*)FILTER(WHERE status IN('FAILED','UNKNOWN')),0,0
		FROM actions WHERE tenant_id=$1 AND requested_at BETWEEN $2 AND $3 GROUP BY 1
		UNION ALL
		SELECT date_trunc($4,created_at),0,count(*),count(*)FILTER(WHERE decision IN('REJECT','REPLAN')),0,0
		FROM decisions WHERE tenant_id=$1 AND created_at BETWEEN $2 AND $3 GROUP BY 1
		UNION ALL
		SELECT date_trunc($4,observed_at),0,0,0,sum(amount_micros_usd),sum(amount_micros_usd)FILTER(WHERE waste_reason IS NOT NULL)
		FROM costs WHERE tenant_id=$1 AND observed_at BETWEEN $2 AND $3 GROUP BY 1)
		SELECT bucket,sum(actions),sum(decisions),sum(failures),sum(cost),COALESCE(sum(waste),0)
		FROM buckets GROUP BY bucket ORDER BY bucket`, tenantID, from, to, bucket)
	if err != nil {
		return nil, protocol.AnalyticsFreshness{}, err
	}
	defer rows.Close()
	var out []protocol.GraphTrendPoint
	for rows.Next() {
		var point protocol.GraphTrendPoint
		if err := rows.Scan(&point.Bucket, &point.Actions, &point.Decisions, &point.Failures, &point.CostMicrosUSD, &point.WasteMicrosUSD); err != nil {
			return nil, protocol.AnalyticsFreshness{}, err
		}
		out = append(out, point)
	}
	if err := rows.Err(); err != nil {
		return nil, protocol.AnalyticsFreshness{}, err
	}
	now := time.Now().UTC()
	return out, protocol.AnalyticsFreshness{Source: "postgres", AsOf: now}, nil
}

func (p *Postgres) breakdown(ctx context.Context, tenantID string, from, to time.Time, expression string) ([]protocol.FinOpsBreakdown, error) {
	allowed := map[string]bool{"provider": true, "COALESCE(model,'unknown')": true, "COALESCE(waste_reason,'useful')": true}
	if !allowed[expression] {
		return nil, errors.New("unsupported analytics dimension")
	}
	query := fmt.Sprintf(`SELECT %s,sum(amount_micros_usd),COALESCE(sum(amount_micros_usd)FILTER(WHERE waste_reason IS NOT NULL),0),count(*)
		FROM costs WHERE tenant_id=$1 AND observed_at BETWEEN $2 AND $3 GROUP BY 1 ORDER BY 2 DESC LIMIT 50`, expression)
	rows, err := p.pool.Query(ctx, query, tenantID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []protocol.FinOpsBreakdown
	for rows.Next() {
		var item protocol.FinOpsBreakdown
		if err := rows.Scan(&item.Key, &item.AmountMicrosUSD, &item.WasteMicrosUSD, &item.Count); err != nil {
			return nil, err
		}
		item.Label = item.Key
		out = append(out, item)
	}
	return out, rows.Err()
}

type ClickHouse struct {
	endpoint string
	database string
	user     string
	password string
	client   *http.Client
}

func NewClickHouse(endpoint, database, user, password string, timeout time.Duration) *ClickHouse {
	return &ClickHouse{endpoint: strings.TrimRight(endpoint, "/"), database: database, user: user, password: password, client: &http.Client{Timeout: timeout}}
}

func (c *ClickHouse) Ping(ctx context.Context) error {
	var rows []struct {
		OK int `json:"ok"`
	}
	if err := c.query(ctx, "SELECT 1 ok", &rows); err != nil {
		return err
	}
	if len(rows) != 1 || rows[0].OK != 1 {
		return errors.New("clickhouse ping returned an unexpected result")
	}
	return nil
}

func (c *ClickHouse) FinOps(ctx context.Context, tenantID string, from, to time.Time) (protocol.FinOpsGraph, error) {
	result := protocol.FinOpsGraph{SchemaVersion: protocol.SchemaVersion, PeriodStart: from, PeriodEnd: to}
	series, freshness, err := c.Trends(ctx, tenantID, from, to)
	if err != nil {
		return protocol.FinOpsGraph{}, err
	}
	result.Series, result.Analytics = series, freshness
	where := clickHouseWhere("occurred_at", tenantID, from, to)
	var totals []struct {
		Total int64 `json:"total"`
		Waste int64 `json:"waste"`
	}
	if err := c.query(ctx, `SELECT sum(amount_micros_usd) total,sumIf(amount_micros_usd,waste_reason!='') waste FROM analytics_facts FINAL WHERE kind='cost' AND `+where, &totals); err != nil {
		return protocol.FinOpsGraph{}, err
	}
	if len(totals) > 0 {
		result.TotalMicros, result.WasteMicros = totals[0].Total, totals[0].Waste
	}
	result.ByProvider, err = c.breakdown(ctx, where, "provider")
	if err != nil {
		return protocol.FinOpsGraph{}, err
	}
	result.ByModel, err = c.breakdown(ctx, where, "if(model='', 'unknown', model)")
	if err != nil {
		return protocol.FinOpsGraph{}, err
	}
	result.ByWaste, err = c.breakdown(ctx, where, "if(waste_reason='', 'useful', waste_reason)")
	if err != nil {
		return protocol.FinOpsGraph{}, err
	}
	result.Nodes, result.Edges = finOpsTopology(result)
	initializeFinOpsSlices(&result)
	return result, nil
}

func (c *ClickHouse) Trends(ctx context.Context, tenantID string, from, to time.Time) ([]protocol.GraphTrendPoint, protocol.AnalyticsFreshness, error) {
	table := "analytics_rollup_hourly"
	if to.Sub(from) > 7*24*time.Hour {
		table = "analytics_rollup_daily"
	}
	where := clickHouseWhere("r.bucket", tenantID, from, to)
	var rows []struct {
		Bucket        string `json:"bucket"`
		Actions       int64  `json:"actions"`
		Decisions     int64  `json:"decisions"`
		Failures      int64  `json:"failures"`
		Cost          int64  `json:"cost"`
		Waste         int64  `json:"waste"`
		RollupVersion string `json:"rollup_version"`
	}
	query := fmt.Sprintf(`SELECT toString(r.bucket) bucket,sum(r.actions) actions,sum(r.decisions) decisions,sum(r.failures) failures,sum(r.cost_micros_usd) cost,sum(r.waste_micros_usd) waste,toString(max(r.rollup_version)) rollup_version FROM %s AS r FINAL WHERE %s GROUP BY r.bucket ORDER BY r.bucket`, table, where)
	if err := c.query(ctx, query, &rows); err != nil {
		return nil, protocol.AnalyticsFreshness{}, err
	}
	var out []protocol.GraphTrendPoint
	var asOf time.Time
	for _, row := range rows {
		bucket, err := parseClickHouseTime(row.Bucket)
		if err != nil {
			return nil, protocol.AnalyticsFreshness{}, err
		}
		out = append(out, protocol.GraphTrendPoint{Bucket: bucket, Actions: row.Actions, Decisions: row.Decisions, Failures: row.Failures, CostMicrosUSD: row.Cost, WasteMicrosUSD: row.Waste})
		if parsed, err := parseClickHouseTime(row.RollupVersion); err == nil && parsed.After(asOf) {
			asOf = parsed
		}
	}
	if asOf.IsZero() {
		asOf = time.Now().UTC()
	}
	lag := int64(time.Since(asOf).Seconds())
	if lag < 0 {
		lag = 0
	}
	return out, protocol.AnalyticsFreshness{Source: "clickhouse", AsOf: asOf, LagSeconds: lag}, nil
}

func (c *ClickHouse) breakdown(ctx context.Context, where, expression string) ([]protocol.FinOpsBreakdown, error) {
	var rows []struct {
		Key    string `json:"key"`
		Amount int64  `json:"amount"`
		Waste  int64  `json:"waste"`
		Count  int64  `json:"count"`
	}
	query := fmt.Sprintf(`SELECT %s key,sum(amount_micros_usd) amount,sumIf(amount_micros_usd,waste_reason!='') waste,count() count FROM analytics_facts FINAL WHERE kind='cost' AND %s GROUP BY key ORDER BY amount DESC LIMIT 50`, expression, where)
	if err := c.query(ctx, query, &rows); err != nil {
		return nil, err
	}
	out := make([]protocol.FinOpsBreakdown, 0, len(rows))
	for _, row := range rows {
		out = append(out, protocol.FinOpsBreakdown{Key: row.Key, Label: row.Key, AmountMicrosUSD: row.Amount, WasteMicrosUSD: row.Waste, Count: row.Count})
	}
	return out, nil
}

func (c *ClickHouse) query(ctx context.Context, statement string, target any) error {
	endpoint, err := url.Parse(c.endpoint)
	if err != nil {
		return err
	}
	params := endpoint.Query()
	params.Set("database", c.database)
	params.Set("default_format", "JSONEachRow")
	params.Set("output_format_json_quote_64bit_integers", "0")
	endpoint.RawQuery = params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), strings.NewReader(statement+" FORMAT JSONEachRow"))
	if err != nil {
		return err
	}
	if c.user != "" {
		req.SetBasicAuth(c.user, c.password)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("clickhouse query failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return decodeRows(resp.Body, target)
}

type Resilient struct {
	primary  Store
	fallback Store
	logger   *slog.Logger
}

func NewResilient(primary, fallback Store, logger *slog.Logger) *Resilient {
	return &Resilient{primary: primary, fallback: fallback, logger: logger}
}

func (r *Resilient) FinOps(ctx context.Context, tenantID string, from, to time.Time) (protocol.FinOpsGraph, error) {
	result, err := r.primary.FinOps(ctx, tenantID, from, to)
	if err == nil {
		return result, nil
	}
	r.logger.Warn("analytics query fell back to postgres", "error", err)
	result, fallbackErr := r.fallback.FinOps(ctx, tenantID, from, to)
	if fallbackErr != nil {
		return protocol.FinOpsGraph{}, errors.Join(err, fallbackErr)
	}
	result.Analytics.Fallback = true
	result.Analytics.Message = "ClickHouse unavailable; served from PostgreSQL"
	return result, nil
}

func (r *Resilient) Trends(ctx context.Context, tenantID string, from, to time.Time) ([]protocol.GraphTrendPoint, protocol.AnalyticsFreshness, error) {
	points, freshness, err := r.primary.Trends(ctx, tenantID, from, to)
	if err == nil {
		return points, freshness, nil
	}
	r.logger.Warn("analytics trend query fell back to postgres", "error", err)
	points, freshness, fallbackErr := r.fallback.Trends(ctx, tenantID, from, to)
	if fallbackErr != nil {
		return nil, protocol.AnalyticsFreshness{}, errors.Join(err, fallbackErr)
	}
	freshness.Fallback = true
	freshness.Message = "ClickHouse unavailable; served from PostgreSQL"
	return points, freshness, nil
}

func finOpsTopology(result protocol.FinOpsGraph) ([]protocol.GraphNode, []protocol.GraphLink) {
	var nodes []protocol.GraphNode
	var edges []protocol.GraphLink
	for _, provider := range result.ByProvider {
		nodes = append(nodes, protocol.GraphNode{ID: "provider:" + provider.Key, EntityID: provider.Key, Type: "provider", Label: provider.Label, CostMicros: provider.AmountMicrosUSD})
	}
	for _, model := range result.ByModel {
		nodes = append(nodes, protocol.GraphNode{ID: "model:" + model.Key, EntityID: model.Key, Type: "model", Label: model.Label, CostMicros: model.AmountMicrosUSD})
	}
	for _, waste := range result.ByWaste {
		if waste.Key == "useful" {
			continue
		}
		nodes = append(nodes, protocol.GraphNode{ID: "waste:" + waste.Key, EntityID: waste.Key, Type: "waste", Label: waste.Label, Status: "WASTE", CostMicros: waste.AmountMicrosUSD})
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].CostMicros > nodes[j].CostMicros })
	return nodes, edges
}

func initializeFinOpsSlices(result *protocol.FinOpsGraph) {
	if result.Nodes == nil {
		result.Nodes = []protocol.GraphNode{}
	}
	if result.Edges == nil {
		result.Edges = []protocol.GraphLink{}
	}
	if result.Series == nil {
		result.Series = []protocol.GraphTrendPoint{}
	}
	if result.ByProvider == nil {
		result.ByProvider = []protocol.FinOpsBreakdown{}
	}
	if result.ByModel == nil {
		result.ByModel = []protocol.FinOpsBreakdown{}
	}
	if result.ByWaste == nil {
		result.ByWaste = []protocol.FinOpsBreakdown{}
	}
}

func clickHouseWhere(column, tenantID string, from, to time.Time) string {
	allowed := map[string]bool{"bucket": true, "occurred_at": true, "r.bucket": true}
	if !allowed[column] {
		column = "occurred_at"
	}
	tenantColumn := "tenant_id"
	if strings.HasPrefix(column, "r.") {
		tenantColumn = "r.tenant_id"
	}
	return fmt.Sprintf("%s='%s' AND %s >= toDateTime64('%s',6,'UTC') AND %s <= toDateTime64('%s',6,'UTC')", tenantColumn, escape(tenantID), column, from.UTC().Format("2006-01-02 15:04:05.000000"), column, to.UTC().Format("2006-01-02 15:04:05.000000"))
}

func escape(value string) string { return strings.ReplaceAll(value, "'", "\\'") }

func decodeRows(reader io.Reader, target any) error {
	var value [][]byte
	scanner := bufio.NewScanner(reader)
	buffer := make([]byte, 64*1024)
	scanner.Buffer(buffer, 4<<20)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		value = append(value, append([]byte(nil), line...))
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	joined := append([]byte("["), bytes.Join(value, []byte(","))...)
	joined = append(joined, ']')
	return json.Unmarshal(joined, target)
}

func parseClickHouseTime(value string) (time.Time, error) {
	formats := []string{"2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05", time.RFC3339Nano}
	for _, format := range formats {
		if parsed, err := time.ParseInLocation(format, value, time.UTC); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid ClickHouse time %q", value)
}
