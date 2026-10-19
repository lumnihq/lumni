package analytics

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DetectBursts names ten-minute clusters that exceed five times an app's
// preceding hourly baseline. Deterministic IDs let workers safely upsert the
// same burst while it is still growing.
func DetectBursts(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `WITH recent AS(
 SELECT f.*,a.metadata->>'customer' customer FROM console_action_facts f JOIN actions a ON a.tenant_id=f.tenant_id AND a.id=f.action_id WHERE f.at>=now()-interval '80 minutes' AND (f.kind<>'allow' OR f.outcome IN('MISMATCH','PARTIAL','FAILED'))),
 wins AS(SELECT tenant_id,app,date_bin(interval '10 minutes',at,'2000-01-01'::timestamptz)bucket,count(*)n FROM recent WHERE at>=now()-interval '20 minutes' GROUP BY 1,2,3 HAVING count(*)>=10),
 selected AS(SELECT w.* FROM wins w WHERE w.n>=5*GREATEST(1,(SELECT count(*)/6.0 FROM recent r WHERE r.tenant_id=w.tenant_id AND r.app=w.app AND r.at>=w.bucket-interval '1 hour' AND r.at<w.bucket))),
 records AS(SELECT w.tenant_id,'inc_burst:'||w.app||':'||extract(epoch FROM w.bucket)::bigint id,w.app,w.bucket,
 jsonb_build_object('app',w.app,'started_at',min(r.at),'ended_at',max(r.at),'pages',0,
 'totals',jsonb_build_object('stepins',count(*),'stop',count(*)FILTER(WHERE kind='stop'),'wait',count(*)FILTER(WHERE kind='wait'),'recheck',count(*)FILTER(WHERE kind='recheck'),'ask',count(*)FILTER(WHERE kind='ask'),'kept_micros_usd',sum(kept_micros_usd),'customers',count(DISTINCT customer),'actors',count(DISTINCT actor_id)),
 'per_minute',(SELECT jsonb_agg(jsonb_build_object('at',m.at,'stepins',m.n) ORDER BY m.at) FROM (SELECT date_trunc('minute',at)at,count(*)n FROM recent WHERE tenant_id=w.tenant_id AND app=w.app AND at>=w.bucket AND at<w.bucket+interval '10 minutes' GROUP BY 1)m),
 'events',(SELECT jsonb_agg(jsonb_build_object('at',e.at,'kind',e.kind,'action_id',e.action_id,'text',e.operation||' '||e.reason_code) ORDER BY e.at) FROM (SELECT * FROM recent WHERE tenant_id=w.tenant_id AND app=w.app AND at>=w.bucket AND at<w.bucket+interval '10 minutes' ORDER BY at LIMIT 12)e)) evidence
 FROM selected w JOIN recent r ON r.tenant_id=w.tenant_id AND r.app=w.app AND r.at>=w.bucket AND r.at<w.bucket+interval '10 minutes' GROUP BY w.tenant_id,w.app,w.bucket)
 INSERT INTO incidents(tenant_id,id,kind,title,evidence,created_at) SELECT tenant_id,id,'BURST',app||' activity burst',evidence,bucket FROM records ON CONFLICT(tenant_id,id)DO UPDATE SET evidence=EXCLUDED.evidence,updated_at=now()`)
	return err
}
