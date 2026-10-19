package coordination

import (
	"context"
	"encoding/json"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"lumni/internal/resource"
	"lumni/pkg/protocol"
)

// Admission limits lock each matching scope before counting admitted actions.
// The locks remain held until the reservation and decision commit, so parallel
// admissions cannot all observe the same free slot.
func checkPaused(ctx context.Context, tx pgx.Tx, tenant, actor string) (draft, bool, error) {
	var paused bool
	err := tx.QueryRow(ctx, `SELECT t.paused OR (a.paused AND (a.until IS NULL OR a.until>now())) FROM tenants t JOIN actors a ON a.tenant_id=t.id WHERE t.id=$1 AND a.id=$2`, tenant, actor).Scan(&paused)
	if err != nil {
		return draft{}, false, err
	}
	retry := int64(1000)
	if paused {
		return draft{decision: protocol.DecisionWait, reason: protocol.ReasonActorPaused, retryAfterMS: &retry}, true, nil
	}
	return draft{}, false, nil
}

func checkControls(ctx context.Context, tx pgx.Tx, tenant string, in protocol.ActionCheckInput) (draft, bool, error) {
	retry := int64(1000)
	rids := []string{}
	for _, r := range in.Resources {
		rids = append(rids, r.ID)
	}
	rows, err := tx.Query(ctx, `SELECT id,max_in_flight,per_minute,hold,mode,scope FROM admission_limits WHERE tenant_id=$1 AND console_scope_matches(scope,$2,$3,$4) ORDER BY id`, tenant, in.Operation, in.ActorID, rids)
	if err != nil {
		return draft{}, false, err
	}
	type limit struct {
		ID       string
		Max, Per *int
		Hold     bool
		Mode     protocol.EnforcementMode
		Scope    []byte
	}
	limits := []limit{}
	for rows.Next() {
		var l limit
		if err = rows.Scan(&l.ID, &l.Max, &l.Per, &l.Hold, &l.Mode, &l.Scope); err != nil {
			rows.Close()
			return draft{}, false, err
		}
		limits = append(limits, l)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return draft{}, false, err
	}
	var shadow *draft
	for _, l := range limits {
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, tenant+":limit:"+l.ID); err != nil {
			return draft{}, false, err
		}
		var active, rate int
		var oldest *time.Time
		err = tx.QueryRow(ctx, `WITH admitted AS (
 SELECT action_id,max(created_at) admitted_at FROM decisions WHERE tenant_id=$1 AND decision IN('ALLOW','WOULD_ALLOW') AND created_at>now()-interval '1 minute' GROUP BY action_id
), candidates AS (
 SELECT id FROM actions WHERE tenant_id=$1 AND status IN('RESERVED','ACTIVE','UNKNOWN')
 UNION SELECT action_id FROM admitted
)
 SELECT count(*) FILTER(WHERE a.status IN('RESERVED','ACTIVE','UNKNOWN') AND (a.status<>'RESERVED' OR EXISTS(SELECT 1 FROM leases WHERE tenant_id=a.tenant_id AND action_id=a.id AND expires_at>now()))),count(d.admitted_at),min(d.admitted_at)
 FROM candidates c JOIN actions a ON a.tenant_id=$1 AND a.id=c.id LEFT JOIN admitted d ON d.action_id=a.id
 WHERE a.id<>$2 AND console_scope_matches($3,a.operation,a.actor_id,ARRAY(SELECT resource_id FROM action_resources ar WHERE ar.tenant_id=a.tenant_id AND ar.action_id=a.id))`, tenant, in.ActionID, l.Scope).Scan(&active, &rate, &oldest)
		if err != nil {
			return draft{}, false, err
		}
		code := protocol.ReasonCode("")
		if l.Hold || (l.Max != nil && active >= *l.Max) {
			code = protocol.ReasonConcurrencyLimit
		} else if l.Per != nil && rate >= *l.Per {
			code = protocol.ReasonRateLimited
			if oldest != nil {
				retry = int64(math.Max(1, time.Until(oldest.Add(time.Minute)).Seconds()*1000))
			}
		}
		if code != "" {
			d := draft{decision: protocol.DecisionWait, reason: code, retryAfterMS: &retry, controlMode: &l.Mode, evidence: []protocol.Evidence{{Type: "LIMIT", Data: map[string]any{"limit_id": l.ID, "in_flight": active, "per_minute_now": rate, "hold": l.Hold}}}}
			if l.Mode == protocol.ModeEnforce {
				return d, true, nil
			}
			if shadow == nil {
				shadow = &d
			}
		}
	}
	budgets, err := tx.Query(ctx, `SELECT l.daily_micros_usd,l.when_reached,now(),w.start_at,w.end_at,console_spent(l.tenant_id,l.scope_key,w.start_at,now()) FROM spend_limits l CROSS JOIN LATERAL console_spend_window(l.tz,now()) w WHERE l.tenant_id=$1 AND l.scope_key IN('',$2) ORDER BY scope_key`, tenant, in.ActorID)
	if err != nil {
		return draft{}, false, err
	}
	var budgetWait *draft
	for budgets.Next() {
		var budget, spent int64
		var when string
		var now, start, end time.Time
		if err = budgets.Scan(&budget, &when, &now, &start, &end, &spent); err != nil {
			budgets.Close()
			return draft{}, false, err
		}
		elapsed, duration := now.Sub(start).Seconds(), end.Sub(start).Seconds()
		paced := when == "SLOW" && float64(spent)*duration > float64(budget)*math.Max(1, elapsed)
		if spent >= budget || paced {
			delay := int64(1000)
			d := draft{decision: protocol.DecisionWait, reason: protocol.ReasonBudgetPacing, retryAfterMS: &delay}
			if when == "HOLD" {
				d.decision = protocol.DecisionReject
				budgets.Close()
				return d, true, nil
			}
			if spent >= budget {
				delay = int64(math.Max(1, end.Sub(now).Seconds()*1000))
			} else {
				delay = int64(math.Max(1000, (float64(spent)/float64(budget)*duration-elapsed)*1000))
			}
			if budgetWait == nil || *budgetWait.retryAfterMS < delay {
				budgetWait = &d
			}
		}
	}
	err = budgets.Err()
	budgets.Close()
	if err != nil {
		return draft{}, false, err
	}
	if budgetWait != nil {
		return *budgetWait, true, nil
	}
	if shadow != nil {
		return *shadow, true, nil
	}
	return draft{}, false, nil
}

func autopilotExempts(ctx context.Context, tx pgx.Tx, tenant string, in protocol.ActionCheckInput, ruleID string) (bool, error) {
	var below, revert []byte
	var since time.Time
	err := tx.QueryRow(ctx, `SELECT below,revert_after,since FROM autopilot_grants WHERE tenant_id=$1 AND actor_id=$2 AND operation=$3 AND $4=ANY(skip_rules) AND active FOR UPDATE`, tenant, in.ActorID, in.Operation, ruleID).Scan(&below, &revert, &since)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var b struct {
		Pointer string  `json:"pointer"`
		Value   float64 `json:"value"`
	}
	var r struct{ Slips, Per int }
	_ = json.Unmarshal(below, &b)
	_ = json.Unmarshal(revert, &r)
	var slips int
	err = tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT kind,outcome FROM console_action_facts WHERE tenant_id=$1 AND actor_id=$2 AND operation=$3 AND at>=$4 ORDER BY at DESC,action_id DESC LIMIT $5) f WHERE kind='recheck' OR outcome IN('MISMATCH','PARTIAL','FAILED')`, tenant, in.ActorID, in.Operation, since, r.Per).Scan(&slips)
	if err != nil {
		return false, err
	}
	if slips >= r.Slips {
		_, err = tx.Exec(ctx, `UPDATE autopilot_grants SET active=false,reverted_at=now() WHERE tenant_id=$1 AND actor_id=$2`, tenant, in.ActorID)
		return false, err
	}
	var args any
	_ = json.Unmarshal(in.Arguments, &args)
	v, ok := resource.LookupPointer(args, b.Pointer)
	if !ok {
		var meta any
		_ = json.Unmarshal(in.Metadata, &meta)
		v, ok = resource.LookupPointer(meta, b.Pointer)
	}
	// An unknown amount must retain approval; numeric's policy fallback is zero.
	amount, isNumber := v.(float64)
	return ok && isNumber && amount < b.Value, nil
}

func freshnessDetails(ctx context.Context, tx pgx.Tx, tenant string, in protocol.ActionCheckInput) (map[string]any, error) {
	ids := []string{}
	refs := map[string]protocol.ResourceIntent{}
	versions := map[string]*string{}
	for _, r := range in.Resources {
		ids = append(ids, r.ID)
		if previous, ok := refs[r.ID]; !ok || previous.ObservedVersion == nil {
			refs[r.ID] = r
			versions[r.ID] = r.ObservedVersion
		}
	}
	rows, err := tx.Query(ctx, `SELECT r.id,r.current_version,o.observed_at,(SELECT rv.actor_id FROM resource_versions rv WHERE rv.tenant_id=r.tenant_id AND rv.resource_id=r.id AND rv.version=r.current_version AND rv.authoritative AND rv.version_source=r.version_source ORDER BY rv.observed_at DESC LIMIT 1) FROM resources r LEFT JOIN LATERAL(SELECT observed_at FROM observations WHERE tenant_id=r.tenant_id AND resource_id=r.id AND work_id=$3 AND observed_version=($4::jsonb->>r.id) ORDER BY observed_at DESC LIMIT 1)o ON true WHERE r.tenant_id=$1 AND r.id=ANY($2)`, tenant, ids, in.WorkID, jsonOrDefault(jsonBytesVersion(versions), []byte("{}")))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id string
		var version, actor *string
		var at *time.Time
		if err = rows.Scan(&id, &version, &at, &actor); err != nil {
			return nil, err
		}
		r := refs[id]
		if r.ObservedAt != nil {
			at = r.ObservedAt
		}
		if at != nil {
			utc := at.UTC()
			at = &utc
		}
		var age *int64
		if at != nil {
			v := int64(math.Max(0, float64(time.Since(*at).Milliseconds())))
			age = &v
		}
		out = append(out, map[string]any{"resource_id": id, "observed_version": r.ObservedVersion, "current_version": version, "observed_at": at, "age_ms": age, "changed_by_actor_id": actor})
	}
	return map[string]any{"resources": out}, rows.Err()
}

func jsonBytesVersion(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
