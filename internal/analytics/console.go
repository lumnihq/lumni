package analytics

import (
	"context"
	"fmt"
	"time"
)

type ConsoleMetric struct {
	Bucket    time.Time `json:"bucket"`
	Actor     string    `json:"actor_id"`
	Operation string    `json:"operation"`
	App       string    `json:"app"`
	Kind      string    `json:"kind"`
	Reason    string    `json:"reason_code"`
	Outcome   string    `json:"outcome"`
	Would     string    `json:"would"`
	Total     int64     `json:"total"`
	Cost      int64     `json:"cost_micros_usd"`
	Kept      int64     `json:"kept_micros_usd"`
	Last      time.Time `json:"last_at"`
}

// ConsoleCellBounds identifies whole 15-minute cells. Callers separately read
// [from,min(to,start)) and [max(from,start,end),to), which never overlap.
func ConsoleCellBounds(from, to time.Time) (time.Time, time.Time) {
	start := from.UTC().Truncate(15 * time.Minute)
	if start.Before(from) {
		start = start.Add(15 * time.Minute)
	}
	return start, to.UTC().Truncate(15 * time.Minute)
}

type ConsoleStore interface {
	Console(context.Context, string, time.Time, time.Time) ([]ConsoleMetric, error)
}

func (p *Postgres) Console(ctx context.Context, tenant string, from, to time.Time) ([]ConsoleMetric, error) {
	// Whole cells come from aggregates; only the two partial cells read facts.
	start, end := ConsoleCellBounds(from, to)
	rows, err := p.pool.Query(ctx, `WITH cells AS (
 SELECT bucket,actor_id,operation,app,kind,reason_code,outcome,would,total,cost_micros_usd,kept_micros_usd,last_at
 FROM console_metrics WHERE tenant_id=$1 AND bucket>=$4 AND bucket<$5
 UNION ALL
 SELECT bucket,actor_id,operation,app,kind,reason_code,outcome,would,1,cost_micros_usd,kept_micros_usd,at
 FROM console_action_facts WHERE tenant_id=$1 AND at>=$2 AND at<LEAST($3::timestamptz,$4::timestamptz)
 UNION ALL
 SELECT bucket,actor_id,operation,app,kind,reason_code,outcome,would,1,cost_micros_usd,kept_micros_usd,at
 FROM console_action_facts WHERE tenant_id=$1 AND at>=GREATEST($2::timestamptz,$4::timestamptz,$5::timestamptz) AND at<$3
 ) SELECT bucket,actor_id,operation,app,kind,reason_code,outcome,would,sum(total),sum(cost_micros_usd),sum(kept_micros_usd),max(last_at)
 FROM cells GROUP BY bucket,actor_id,operation,app,kind,reason_code,outcome,would HAVING sum(total)>0 ORDER BY bucket`, tenant, from, to, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ConsoleMetric{}
	for rows.Next() {
		var m ConsoleMetric
		if err = rows.Scan(&m.Bucket, &m.Actor, &m.Operation, &m.App, &m.Kind, &m.Reason, &m.Outcome, &m.Would, &m.Total, &m.Cost, &m.Kept, &m.Last); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
func (c *ClickHouse) Console(ctx context.Context, tenant string, from, to time.Time) ([]ConsoleMetric, error) {
	// Exact sub-cell boundaries are authoritative PostgreSQL reads. Resilient
	// falls back for these; aligned historic windows use the analytics profile.
	if from.Unix()%900 != 0 || to.Unix()%900 != 0 || from.Nanosecond() != 0 || to.Nanosecond() != 0 {
		return nil, fmt.Errorf("partial console analytics cell")
	}
	var rows []struct {
		Bucket            string `json:"bucket"`
		Actor             string `json:"actor_id"`
		Operation         string `json:"operation"`
		App               string `json:"app"`
		Kind              string `json:"kind"`
		Reason            string `json:"reason_code"`
		Outcome           string `json:"outcome"`
		Would             string `json:"would"`
		Total, Cost, Kept int64
		Last              string `json:"last_at"`
	}
	q := fmt.Sprintf(`SELECT toString(bucket) bucket,actor_id,operation,app,kind,reason_code,outcome,would,total,cost_micros_usd Cost,kept_micros_usd Kept,toString(last_at) last_at FROM console_metrics FINAL WHERE tenant_id='%s' AND bucket>=toDateTime64('%s',6,'UTC') AND bucket<toDateTime64('%s',6,'UTC') AND total>0`, escape(tenant), from.UTC().Format("2006-01-02 15:04:05.000000"), to.UTC().Format("2006-01-02 15:04:05.000000"))
	if err := c.query(ctx, q, &rows); err != nil {
		return nil, err
	}
	out := []ConsoleMetric{}
	for _, r := range rows {
		b, e := parseClickHouseTime(r.Bucket)
		if e != nil {
			return nil, e
		}
		last, e := parseClickHouseTime(r.Last)
		if e != nil {
			return nil, e
		}
		out = append(out, ConsoleMetric{Bucket: b, Actor: r.Actor, Operation: r.Operation, App: r.App, Kind: r.Kind, Reason: r.Reason, Outcome: r.Outcome, Would: r.Would, Total: r.Total, Cost: r.Cost, Kept: r.Kept, Last: last})
	}
	return out, nil
}
func (r *Resilient) Console(ctx context.Context, tenant string, from, to time.Time) ([]ConsoleMetric, error) {
	if primary, ok := r.primary.(ConsoleStore); ok {
		if p, ok := r.fallback.(*Postgres); ok {
			if out, e := p.hybridConsole(ctx, primary, tenant, from, to); e == nil {
				return out, nil
			}
		}
	}
	if fallback, ok := r.fallback.(ConsoleStore); ok {
		return fallback.Console(ctx, tenant, from, to)
	}
	return nil, fmt.Errorf("console analytics store is unavailable")
}
func metricKey(m ConsoleMetric) string {
	return fmt.Sprintf("%d|%s|%s|%s|%s|%s|%s|%s", m.Bucket.Unix(), m.Actor, m.Operation, m.App, m.Kind, m.Reason, m.Outcome, m.Would)
}
func (p *Postgres) hybridConsole(ctx context.Context, primary ConsoleStore, tenant string, from, to time.Time) ([]ConsoleMetric, error) {
	start := time.Unix((from.Unix()+899)/900*900, 0).UTC()
	if from.Nanosecond() > 0 && from.Unix()%900 == 0 {
		start = start.Add(15 * time.Minute)
	}
	end := time.Unix(to.Unix()/900*900, 0).UTC()
	if !start.Before(end) {
		return p.Console(ctx, tenant, from, to)
	}
	base, err := primary.Console(ctx, tenant, start, end)
	if err != nil {
		return nil, err
	}
	cells := map[string]ConsoleMetric{}
	for _, m := range base {
		if m.Total > 0 {
			cells[metricKey(m)] = m
		}
	}
	// Unexported replacements win over ClickHouse, including zeroed cells.
	rows, err := p.pool.Query(ctx, `SELECT bucket,actor_id,operation,app,kind,reason_code,outcome,would,total,cost_micros_usd,kept_micros_usd,last_at FROM console_metrics WHERE tenant_id=$1 AND bucket>=$2 AND bucket<$3 AND exported_at IS NULL`, tenant, start, end)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var m ConsoleMetric
		if err = rows.Scan(&m.Bucket, &m.Actor, &m.Operation, &m.App, &m.Kind, &m.Reason, &m.Outcome, &m.Would, &m.Total, &m.Cost, &m.Kept, &m.Last); err != nil {
			break
		}
		key := metricKey(m)
		if m.Total <= 0 {
			delete(cells, key)
		} else {
			cells[key] = m
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, bounds := range [][2]time.Time{{from, start}, {end, to}} {
		if bounds[0].Before(bounds[1]) {
			edge, e := p.Console(ctx, tenant, bounds[0], bounds[1])
			if e != nil {
				return nil, e
			}
			for _, m := range edge {
				cells[metricKey(m)] = m
			}
		}
	}
	out := make([]ConsoleMetric, 0, len(cells))
	for _, m := range cells {
		out = append(out, m)
	}
	return out, nil
}
