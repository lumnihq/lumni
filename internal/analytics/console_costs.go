package analytics

import (
	"context"
	"time"
)

type ConsoleCost struct {
	Bucket                time.Time
	Actor, App, Operation string
	Cost                  int64
}

func (p *Postgres) ConsoleCosts(ctx context.Context, tenant string, from, to time.Time) ([]ConsoleCost, error) {
	start, end := ConsoleCellBounds(from, to)
	rows, err := p.pool.Query(ctx, `WITH raw AS(
 SELECT c.* FROM costs c WHERE tenant_id=$1 AND observed_at>=$2 AND observed_at<LEAST($3::timestamptz,$4::timestamptz)
 UNION ALL SELECT c.* FROM costs c WHERE tenant_id=$1 AND observed_at>=GREATEST($2::timestamptz,$4::timestamptz,$5::timestamptz) AND observed_at<$3),
 cells AS(
 SELECT bucket,actor_id,app,operation,cost_micros_usd FROM console_cost_metrics WHERE tenant_id=$1 AND bucket>=$4 AND bucket<$5
 UNION ALL SELECT date_bin(interval '15 minutes',c.observed_at,'2000-01-01'::timestamptz),COALESCE(c.actor_id,a.actor_id,w.actor_id,''),lower(trim(COALESCE(NULLIF(a.metadata->>'app',''),split_part(a.operation,'.',1),c.metadata->>'app',''))),COALESCE(a.operation,''),c.amount_micros_usd
 FROM raw c LEFT JOIN actions a ON a.tenant_id=c.tenant_id AND a.id=c.action_id LEFT JOIN work w ON w.tenant_id=c.tenant_id AND w.id=c.work_id)
 SELECT bucket,actor_id,app,operation,sum(cost_micros_usd) FROM cells GROUP BY bucket,actor_id,app,operation`, tenant, from, to, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ConsoleCost{}
	for rows.Next() {
		var c ConsoleCost
		if err = rows.Scan(&c.Bucket, &c.Actor, &c.App, &c.Operation, &c.Cost); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
