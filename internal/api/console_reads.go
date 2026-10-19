package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/lumnihq/lumni/internal/analytics"
)

const journeySQL = `SELECT jsonb_build_object(
 'action',to_jsonb(a)-'arguments'-'arguments_ciphertext'-'lease_token_hash'-'tenant_id'||jsonb_build_object('resources',COALESCE((SELECT jsonb_agg(to_jsonb(ar)-'tenant_id') FROM action_resources ar WHERE ar.tenant_id=a.tenant_id AND ar.action_id=a.id),'[]')),
 'decision',(SELECT to_jsonb(d)-'snapshot'-'lease_token'-'tenant_id'-'id'||jsonb_build_object('receipt_id',d.id) FROM decisions d WHERE d.tenant_id=a.tenant_id AND d.action_id=a.id ORDER BY created_at DESC,id DESC LIMIT 1),
 'earlier',COALESCE((SELECT jsonb_agg(x.data ORDER BY x.created_at,x.id) FROM (SELECT d.id,d.created_at,to_jsonb(d)-'snapshot'-'lease_token'-'tenant_id'-'id'||jsonb_build_object('receipt_id',d.id) data FROM decisions d WHERE d.tenant_id=a.tenant_id AND d.action_id=a.id AND d.decision NOT IN('ALLOW','WOULD_ALLOW') AND d.id<>(SELECT id FROM decisions WHERE tenant_id=a.tenant_id AND action_id=a.id ORDER BY created_at DESC,id DESC LIMIT 1))x),'[]'),
 'approval',(SELECT to_jsonb(p)-'tenant_id'||jsonb_build_object('status',CASE WHEN p.status='PENDING' AND p.expires_at<=now() THEN 'EXPIRED' ELSE p.status END) FROM approvals p WHERE p.tenant_id=a.tenant_id AND p.action_id=a.id ORDER BY requested_at DESC,id DESC LIMIT 1),
 'outcome',(SELECT to_jsonb(o)-'tenant_id'-'claim_token'-'claim_until' FROM outcomes o WHERE o.tenant_id=a.tenant_id AND o.action_id=a.id),
 'attempts',COALESCE((SELECT jsonb_agg(to_jsonb(v)-'tenant_id' ORDER BY v.created_at,v.id) FROM verification_attempts v JOIN outcomes o ON o.tenant_id=v.tenant_id AND o.id=v.outcome_id WHERE o.tenant_id=a.tenant_id AND o.action_id=a.id),'[]'),
 'duplicate_of',(SELECT d.snapshot->>'existing_action_id' FROM decisions d WHERE d.tenant_id=a.tenant_id AND d.action_id=a.id AND d.decision IN('DEDUPLICATE','WOULD_DEDUPLICATE') ORDER BY d.created_at DESC,d.id DESC LIMIT 1)
 ) FROM actions a WHERE a.tenant_id=$1 AND a.id=$2`

func (s *Server) actionJourney(w http.ResponseWriter, r *http.Request) {
	s.jsonRow(w, r, journeySQL, tenant(r), pathID(r, "id"))
}
func (s *Server) decideApprovals(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IDs      []string `json:"ids"`
		Decision string   `json:"decision"`
		Note     string   `json:"note"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if len(in.IDs) < 1 || len(in.IDs) > 200 || (in.Decision != "approve" && in.Decision != "deny") || len(in.Note) > 4000 {
		s.fail(w, r, 400, "INVALID_QUERY", "1–200 ids and approve or deny are required", nil)
		return
	}
	state := "APPROVED"
	if in.Decision == "deny" {
		state = "DENIED"
	}
	who := s.principalName(r)
	out := []map[string]any{}
	for _, id := range in.IDs {
		err := s.kernel.ResolveApprovalWithNote(r.Context(), tenant(r), id, who, state, in.Note)
		item := map[string]any{"id": id}
		if err == nil {
			item["status"] = state
		} else {
			code := "NOT_FOUND"
			if !strings.Contains(err.Error(), "not found") {
				code = "REQUEST_FAILED"
			}
			item["error"] = map[string]any{"code": code}
		}
		out = append(out, item)
	}
	writeJSON(w, 200, map[string]any{"results": out})
}

func (s *Server) resourceVersions(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("limit") == "" {
		q := r.URL.Query()
		q.Set("limit", "20")
		r.URL.RawQuery = q.Encode()
	}
	id := pathID(r, "id")
	var exists bool
	err := s.pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM resources WHERE tenant_id=$1 AND id=$2)`, tenant(r), id).Scan(&exists)
	if err != nil {
		s.respond(w, r, 0, nil, err)
		return
	}
	if !exists {
		s.respond(w, r, 0, nil, pgx.ErrNoRows)
		return
	}
	spec := listSpec{From: "resource_versions t", Tenant: "t.tenant_id", Key: "t.id::text", ID: "t.resource_id", Time: "t.observed_at", JSON: `jsonb_strip_nulls(jsonb_build_object('version',t.version,'at',t.observed_at,'actor_id',t.actor_id,'action_id',t.action_id,'summary',t.summary,'value_hash',t.value_hash))`}
	s.pagedList(w, r, spec, id, false)
}

const leaseJSON = `jsonb_build_object('resource_id',r.id,
 'holder',(SELECT jsonb_build_object('action_id',l.action_id,'actor_id',a.actor_id,'since',l.created_at,'expires_at',l.expires_at,'fencing_token',l.fencing_token) FROM leases l JOIN actions a ON a.tenant_id=l.tenant_id AND a.id=l.action_id WHERE l.tenant_id=r.tenant_id AND l.resource_id=r.id AND l.expires_at>now() ORDER BY l.created_at,l.id LIMIT 1),
 'queue',COALESCE((SELECT jsonb_agg(q.data ORDER BY q.since,q.id) FROM (SELECT a.id,a.requested_at since,jsonb_build_object('action_id',a.id,'actor_id',a.actor_id,'waiting_since',a.requested_at,'position',row_number() OVER(ORDER BY a.requested_at,a.id)) data FROM actions a WHERE a.tenant_id=r.tenant_id AND a.status='PROPOSED' AND EXISTS(SELECT 1 FROM action_resources ar WHERE ar.tenant_id=a.tenant_id AND ar.action_id=a.id AND ar.resource_id=r.id) AND (SELECT decision FROM decisions WHERE tenant_id=a.tenant_id AND action_id=a.id ORDER BY created_at DESC,id DESC LIMIT 1)='WAIT' ORDER BY a.requested_at,a.id LIMIT 1000)q),'[]'),
 'limit',jsonb_build_object('max_in_flight',(SELECT min(max_in_flight) FROM admission_limits WHERE tenant_id=r.tenant_id AND COALESCE(scope->>'resource','')<>'' AND console_scope_matches(jsonb_build_object('resource',scope->'resource'),'','',ARRAY[r.id])),'in_flight',(SELECT count(*) FROM leases WHERE tenant_id=r.tenant_id AND resource_id=r.id AND expires_at>now())),
 'handoffs_last_hour',(SELECT count(*) FROM resource_lease_events WHERE tenant_id=r.tenant_id AND resource_id=r.id AND at>now()-interval '1 hour'),
 'refused_late_writes_today',(SELECT count(*) FROM events e WHERE e.tenant_id=r.tenant_id AND e.type='FENCING_TOKEN_INVALID' AND e.occurred_at>=date_trunc('day',now()) AND e.payload->>'resource_id'=r.id)
 )`

func (s *Server) leaseData(r *http.Request, id string) (json.RawMessage, error) {
	var data []byte
	err := s.pool.QueryRow(r.Context(), `SELECT `+leaseJSON+` FROM resources r WHERE r.tenant_id=$1 AND r.id=$2`, tenant(r), id).Scan(&data)
	return data, err
}
func (s *Server) resourceLease(w http.ResponseWriter, r *http.Request) {
	out, err := s.leaseData(r, pathID(r, "id"))
	s.respond(w, r, 200, out, err)
}
func (s *Server) contendedResources(w http.ResponseWriter, r *http.Request) {
	from, to, _, e := timeRange(r, 90*24*time.Hour)
	if e != nil {
		s.fail(w, r, 400, "INVALID_TIME_RANGE", e.Error(), nil)
		return
	}
	top := 10
	if v := r.URL.Query().Get("top"); v != "" {
		top, e = strconv.Atoi(v)
		if e != nil || top < 1 || top > 50 {
			s.fail(w, r, 400, "INVALID_QUERY", "top must be 1–50", nil)
			return
		}
	}
	rows, err := s.pool.Query(r.Context(), `SELECT `+leaseJSON+` FROM resources r JOIN (
 SELECT resource_id,sum(total) total FROM console_resource_metrics
 WHERE tenant_id=$1 AND bucket>=$2 AND bucket<$3 AND kind IN('stop','wait','recheck')
 GROUP BY resource_id HAVING sum(total)>0 ORDER BY sum(total) DESC,resource_id LIMIT $4
 ) c ON r.tenant_id=$1 AND r.id=c.resource_id ORDER BY c.total DESC,r.id`, tenant(r), from, to, top)
	if err != nil {
		s.respond(w, r, 0, nil, err)
		return
	}
	defer rows.Close()
	out := []json.RawMessage{}
	for rows.Next() {
		var item json.RawMessage
		if err = rows.Scan(&item); err != nil {
			break
		}
		out = append(out, item)
	}
	if err == nil {
		err = rows.Err()
	}
	s.respond(w, r, 200, map[string]any{"items": out}, err)
}

func (s *Server) analyticsFreshness(w http.ResponseWriter, r *http.Request) {
	from, to, _, e := timeRange(r, 90*24*time.Hour)
	if e != nil {
		s.fail(w, r, 400, "INVALID_TIME_RANGE", e.Error(), nil)
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT a.actor_id,count(*),percentile_cont(0.5) WITHIN GROUP(ORDER BY (v->>'age_ms')::bigint),percentile_cont(0.9) WITHIN GROUP(ORDER BY (v->>'age_ms')::bigint),count(*)FILTER(WHERE v->>'current_version' IS NOT NULL AND v->>'observed_version' IS DISTINCT FROM v->>'current_version') FROM decisions d JOIN actions a ON a.tenant_id=d.tenant_id AND a.id=d.action_id CROSS JOIN LATERAL jsonb_array_elements(d.evaluations) e CROSS JOIN LATERAL jsonb_array_elements(COALESCE(e->'details'->'resources','[]')) v WHERE d.tenant_id=$1 AND d.created_at>=$2 AND d.created_at<$3 AND e->>'stage'='resource_versions' AND v->>'observed_version' IS NOT NULL GROUP BY a.actor_id ORDER BY count(*) DESC`, tenant(r), from, to)
	if err != nil {
		s.respond(w, r, 0, nil, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var actor string
		var n, stale int64
		var median, p90 *float64
		if err = rows.Scan(&actor, &n, &median, &p90, &stale); err != nil {
			break
		}
		out = append(out, map[string]any{"actor_id": actor, "reads": n, "median_age_ms": median, "p90_age_ms": p90, "stale": stale})
	}
	if err == nil {
		err = rows.Err()
	}
	s.respond(w, r, 200, map[string]any{"by_actor": out}, err)
}
func (s *Server) appHealth(w http.ResponseWriter, r *http.Request) {
	from, to, _, e := timeRange(r, 7*24*time.Hour)
	if e != nil {
		s.fail(w, r, 400, "INVALID_TIME_RANGE", e.Error(), nil)
		return
	}
	step := r.URL.Query().Get("step")
	if step == "" {
		step = "1m"
	}
	if step != "1m" && step != "5m" {
		s.fail(w, r, 400, "INVALID_QUERY", "step must be 1m or 5m", nil)
		return
	}
	interval := "1 minute"
	if step == "5m" {
		interval = "5 minutes"
	}
	app := chi.URLParam(r, "app")
	rows, err := s.pool.Query(r.Context(), `SELECT date_bin($4::interval,at,'2000-01-01'::timestamptz),percentile_cont(0.5)WITHIN GROUP(ORDER BY duration_ms),percentile_cont(0.95)WITHIN GROUP(ORDER BY duration_ms),count(*)FILTER(WHERE error),count(*)FILTER(WHERE timeout) FROM app_call_timings WHERE tenant_id=$1 AND app=$2 AND at>=$3 AND at<$5 GROUP BY 1 ORDER BY 1`, tenant(r), app, from, interval, to)
	if err != nil {
		s.respond(w, r, 0, nil, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var at time.Time
		var p50, p95 float64
		var errs, timeouts int64
		if err = rows.Scan(&at, &p50, &p95, &errs, &timeouts); err != nil {
			break
		}
		out = append(out, map[string]any{"at": at, "p50_ms": p50, "p95_ms": p95, "errors": errs, "timeouts": timeouts})
	}
	if err == nil {
		err = rows.Err()
	}
	s.respond(w, r, 200, map[string]any{"app": app, "series": out}, err)
}
func (s *Server) billingUsage(w http.ResponseWriter, r *http.Request) {
	from, to, _, e := timeRange(r, 366*24*time.Hour)
	if e != nil {
		s.fail(w, r, 400, "INVALID_TIME_RANGE", e.Error(), nil)
		return
	}
	metrics, err := s.consoleMetrics(r, from, to)
	if err != nil {
		s.respond(w, r, 0, nil, err)
		return
	}
	var n int64
	for _, m := range metrics {
		n += m.Total
	}
	price := int64(20000)
	plan := "team"
	err = s.pool.QueryRow(r.Context(), `SELECT price_per_action_micros_usd,plan FROM billing_plans WHERE tenant_id=$1`, tenant(r)).Scan(&price, &plan)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.respond(w, r, 0, nil, err)
		return
	}
	writeJSON(w, 200, map[string]any{"checked_actions": n, "price_per_action_micros_usd": price, "cost_micros_usd": n * price, "plan": plan})
}

// Whole cells use aggregates; the boundary cells retain exact [from,to) times.
const anomalyMetricWindow = `WITH window_metrics AS (
 SELECT bucket,actor_id,operation,kind,total,cost_micros_usd,last_at FROM console_metrics
 WHERE tenant_id=$1 AND bucket>=$4 AND bucket<$5 AND total>0
 UNION ALL SELECT at,actor_id,operation,kind,1,cost_micros_usd,at FROM console_action_facts
 WHERE tenant_id=$1 AND at>=$2 AND at<LEAST($3::timestamptz,$4::timestamptz)
 UNION ALL SELECT at,actor_id,operation,kind,1,cost_micros_usd,at FROM console_action_facts
 WHERE tenant_id=$1 AND at>=GREATEST($2::timestamptz,$4::timestamptz,$5::timestamptz) AND at<$3
), `

func (s *Server) analyticsAnomalies(w http.ResponseWriter, r *http.Request) {
	from, to, _, e := timeRange(r, 7*24*time.Hour)
	if e != nil {
		s.fail(w, r, 400, "INVALID_TIME_RANGE", e.Error(), nil)
		return
	}
	kinds := csv(r.URL.Query().Get("kinds"))
	if len(kinds) == 0 {
		kinds = []string{"loop", "runaway", "drift"}
	}
	for _, k := range kinds {
		if !contains([]string{"loop", "runaway", "drift"}, k) {
			s.fail(w, r, 400, "INVALID_QUERY", "Unknown anomaly kind", nil)
			return
		}
	}
	out := []json.RawMessage{}
	queries := map[string]string{
		"loop": `WITH win AS(SELECT f.*,a.arguments_hash payload_hash,
 count(*)OVER(PARTITION BY f.actor_id,f.operation,f.resources,a.arguments_hash ORDER BY f.at RANGE BETWEEN interval '10 minutes' PRECEDING AND CURRENT ROW)n,
 min(f.at)OVER(PARTITION BY f.actor_id,f.operation,f.resources,a.arguments_hash ORDER BY f.at RANGE BETWEEN interval '10 minutes' PRECEDING AND CURRENT ROW)since,
 sum(f.cost_micros_usd)OVER(PARTITION BY f.actor_id,f.operation,f.resources,a.arguments_hash ORDER BY f.at RANGE BETWEEN interval '10 minutes' PRECEDING AND CURRENT ROW)cost
 FROM console_action_facts f JOIN actions a ON a.tenant_id=f.tenant_id AND a.id=f.action_id WHERE f.tenant_id=$1 AND f.at>=$2::timestamptz-interval '10 minutes' AND f.at<$3),
 latest AS(SELECT DISTINCT ON(actor_id,operation,resources,payload_hash)* FROM win WHERE n>=5 AND at>=$2 ORDER BY actor_id,operation,resources,payload_hash,at DESC)
 SELECT jsonb_build_object('kind','loop','actor_id',actor_id,'operation',operation,'resource_id',resources[1],'count',n,'interval_ms',floor(extract(epoch FROM at-since)*1000/GREATEST(1,n-1)),'since',since,'cost_micros_usd',cost,'open',at>now()-interval '10 minutes') FROM latest ORDER BY n DESC LIMIT 100`,
		"runaway": anomalyMetricWindow + `cells AS (
 SELECT actor_id,operation,date_trunc('hour',bucket)h,sum(total)n,sum(cost_micros_usd)cost FROM window_metrics GROUP BY 1,2,3
), history AS (
 SELECT actor_id,operation,date_trunc('hour',bucket)h,sum(total)n FROM console_metrics
 WHERE tenant_id=$1 AND bucket>=date_trunc('hour',$2::timestamptz)-interval '6 hours' AND bucket<date_trunc('hour',$3::timestamptz)
 GROUP BY 1,2,3 HAVING sum(total)>0
), compared AS (
 SELECT c.*,(SELECT avg(b.n) FROM history b WHERE b.actor_id=c.actor_id AND b.operation=c.operation AND b.h>=c.h-interval '6 hours' AND b.h<c.h)baseline FROM cells c
)
 SELECT jsonb_build_object('kind','runaway','actor_id',actor_id,'operation',operation,'count',n,'baseline',baseline,'factor',n/GREATEST(1,baseline),'since',h,'cost_micros_usd',cost,'open',h>now()-interval '1 hour') FROM compared WHERE n>=10 AND n>3*baseline ORDER BY n/GREATEST(1,baseline) DESC,actor_id,operation,h LIMIT 100`,
		"drift": anomalyMetricWindow + `drifting AS (
 SELECT actor_id,operation,sum(total)n,sum(total)FILTER(WHERE kind='recheck')rechecks,min(bucket)since,sum(cost_micros_usd)cost,max(last_at)last_at
 FROM window_metrics GROUP BY actor_id,operation
) SELECT jsonb_build_object('kind','drift','actor_id',actor_id,'operation',operation,'count',rechecks,'baseline',0.01,'factor',rechecks::float8/GREATEST(1,n)/0.01,'since',since,'cost_micros_usd',cost,'open',last_at>now()-interval '10 minutes') FROM drifting WHERE n>=20 AND rechecks>0.05*n ORDER BY rechecks DESC,actor_id,operation LIMIT 100`,
	}
	start, end := analytics.ConsoleCellBounds(from, to)
	for _, kind := range kinds {
		args := []any{tenant(r), from, to}
		if kind != "loop" {
			args = append(args, start, end)
		}
		rows, err := s.pool.Query(r.Context(), queries[kind], args...)
		if err != nil {
			s.respond(w, r, 0, nil, err)
			return
		}
		for rows.Next() {
			var b []byte
			if err = rows.Scan(&b); err != nil {
				break
			}
			out = append(out, b)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			s.respond(w, r, 0, nil, err)
			return
		}
	}
	writeJSON(w, 200, map[string]any{"items": out})
}
