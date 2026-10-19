package api

import (
	"github.com/jackc/pgx/v5"
	"lumni/internal/analytics"
	"net/http"
	"time"
)

// Count full aggregate cells after the most recent slip, reading individual
// facts only within that slip's cell. A two-million-action clean streak does
// not require scanning two million action rows.
func (s *Server) actorStreak(r *http.Request, actor string, from, to time.Time, metrics []analytics.ConsoleMetric) (int64, error) {
	var dirty time.Time
	var id string
	err := s.pool.QueryRow(r.Context(), `SELECT at,action_id FROM console_action_facts WHERE tenant_id=$1 AND actor_id=$2 AND at>=$3 AND at<$4 AND NOT clean ORDER BY at DESC,action_id DESC LIMIT 1`, tenant(r), actor, from, to).Scan(&dirty, &id)
	if err != nil && err != pgx.ErrNoRows {
		return 0, err
	}
	var n int64
	if err == pgx.ErrNoRows {
		for _, m := range metrics {
			if m.Actor == actor && m.Kind == "allow" && m.Outcome != "MISMATCH" && m.Outcome != "PARTIAL" && m.Outcome != "FAILED" {
				n += m.Total
			}
		}
		return n, nil
	}
	end := time.Unix(dirty.Unix()/900*900+900, 0).UTC()
	for _, m := range metrics {
		if m.Actor == actor && m.Kind == "allow" && m.Outcome != "MISMATCH" && m.Outcome != "PARTIAL" && m.Outcome != "FAILED" && !m.Bucket.Before(end) {
			n += m.Total
		}
	}
	if end.After(to) {
		end = to
	}
	var partial int64
	err = s.pool.QueryRow(r.Context(), `SELECT count(*) FROM console_action_facts WHERE tenant_id=$1 AND actor_id=$2 AND (at,action_id)>($3,$4) AND at<$5 AND clean`, tenant(r), actor, dirty, id, end).Scan(&partial)
	return n + partial, err
}
