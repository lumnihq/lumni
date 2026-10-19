package analytics

import (
	"context"
	"lumni/pkg/protocol"
	"sort"
	"strings"
	"time"
)

func StructuredWaste(in []protocol.FinOpsBreakdown) []protocol.FinOpsBreakdown {
	by := map[string]protocol.FinOpsBreakdown{}
	for _, r := range in {
		if r.Key == "useful" {
			continue
		}
		k := strings.ToLower(r.Key)
		switch k {
		case "duplicate", "repeat", "repeat_stopped":
			k = "repeat_stopped"
		case "stale", "stale_redo":
			k = "stale_redo"
		case "loop", "retry_loop":
			k = "retry_loop"
		case "superseded":
		case "failed", "mismatch", "partial", "never_happened":
			k = "never_happened"
		default:
			k = "other"
		}
		v := by[k]
		v.Key = k
		v.Label = map[string]string{"repeat_stopped": "Repeats stopped", "stale_redo": "Stale reads redone", "retry_loop": "Retry loops", "superseded": "Superseded work", "never_happened": "Actions that never happened", "other": "Other waste"}[k]
		v.Avoided = k == "repeat_stopped" || k == "retry_loop"
		v.Count += r.Count
		v.AmountMicrosUSD += r.AmountMicrosUSD
		v.WasteMicrosUSD += r.WasteMicrosUSD
		by[k] = v
	}
	out := []protocol.FinOpsBreakdown{}
	for _, r := range by {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}
func (p *Postgres) ActorCosts(ctx context.Context, tenant string, from, to time.Time) ([]protocol.ActorCost, error) {
	rows, err := p.pool.Query(ctx, `SELECT COALESCE(c.actor_id,a.actor_id,w.actor_id,'unknown'),sum(c.amount_micros_usd),count(DISTINCT c.action_id) FROM costs c LEFT JOIN actions a ON a.tenant_id=c.tenant_id AND a.id=c.action_id LEFT JOIN work w ON w.tenant_id=c.tenant_id AND w.id=c.work_id WHERE c.tenant_id=$1 AND c.observed_at>=$2 AND c.observed_at<$3 GROUP BY 1 ORDER BY 2 DESC`, tenant, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.ActorCost{}
	for rows.Next() {
		var a protocol.ActorCost
		if err = rows.Scan(&a.ActorID, &a.CostMicrosUSD, &a.Actions); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
