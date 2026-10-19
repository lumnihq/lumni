package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Per-workspace limits on checks, for cloud workspaces made by sign-up
// (POST /v1/tenants writes them to tenants.settings.limits, and
// PUT /v1/tenants/{id}/plan changes them). Workspaces without limits, such as
// a self-hosted one or the bootstrap tenant, aren't counted at all. A
// watch-only workspace (settings.watch_only, after a trial ends) has every
// check recorded as shadow: Lumni says what it would do, and never enforces.

type tenantLimits struct {
	PerDay    int  `json:"checks_per_day,omitempty"`
	PerSecond int  `json:"checks_per_second,omitempty"`
	WatchOnly bool `json:"-"`
}

func (l tenantLimits) limited() bool { return l.PerDay > 0 || l.PerSecond > 0 }

type cachedLimits struct {
	l  tenantLimits
	at time.Time
}

// bucket allows bursts of burstSeconds × rate, refilled at rate a second.
type bucket struct {
	tokens float64
	at     time.Time
}

const burstSeconds = 6

type quotas struct {
	mu      sync.Mutex
	limits  map[string]cachedLimits
	buckets map[string]*bucket
}

// limitsOf reads a workspace's limits, cached for 30 seconds.
func (q *quotas) limitsOf(ctx context.Context, s *Server, id string) (tenantLimits, error) {
	q.mu.Lock()
	c, ok := q.limits[id]
	q.mu.Unlock()
	if ok && time.Since(c.at) < 30*time.Second {
		return c.l, nil
	}
	var raw []byte
	var watch bool
	if err := s.pool.QueryRow(ctx, `SELECT COALESCE(settings->'limits','{}'::jsonb),COALESCE((settings->>'watch_only')::boolean,false) FROM tenants WHERE id=$1`, id).Scan(&raw, &watch); err != nil {
		return tenantLimits{}, err
	}
	var l tenantLimits
	_ = json.Unmarshal(raw, &l)
	l.WatchOnly = watch
	q.mu.Lock()
	if q.limits == nil {
		q.limits = map[string]cachedLimits{}
	}
	q.limits[id] = cachedLimits{l: l, at: time.Now()}
	q.mu.Unlock()
	return l, nil
}

// forget drops a workspace's cached limits, after its plan changes.
func (q *quotas) forget(id string) {
	q.mu.Lock()
	delete(q.limits, id)
	q.mu.Unlock()
}

// take spends one token from the workspace's bucket.
func (q *quotas) take(id string, rate int, now time.Time) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.buckets == nil {
		q.buckets = map[string]*bucket{}
	}
	capacity := float64(rate * burstSeconds)
	b, ok := q.buckets[id]
	if !ok {
		b = &bucket{tokens: capacity, at: now}
		q.buckets[id] = b
	}
	b.tokens = min(capacity, b.tokens+now.Sub(b.at).Seconds()*float64(rate))
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// withinQuota answers 429 when a limited workspace is over its rate or its
// day. It also says whether the workspace only watches.
func (s *Server) withinQuota(w http.ResponseWriter, r *http.Request) (watchOnly, ok bool) {
	id := tenant(r)
	l, err := s.quotas.limitsOf(r.Context(), s, id)
	if err != nil {
		return false, true
	}
	if !l.limited() {
		return l.WatchOnly, true
	}
	now := time.Now()
	if l.PerSecond > 0 && !s.quotas.take(id, l.PerSecond, now) {
		w.Header().Set("Retry-After", "1")
		s.fail(w, r, http.StatusTooManyRequests, "RATE_LIMITED", fmt.Sprintf("This workspace can send %d checks a second. Slow down and try again.", l.PerSecond), map[string]any{"checks_per_second": l.PerSecond})
		return false, false
	}
	if l.PerDay > 0 {
		day := now.UTC().Truncate(24 * time.Hour)
		var used int
		if err := s.pool.QueryRow(r.Context(), `SELECT count(*) FROM actions WHERE tenant_id=$1 AND requested_at>=$2`, id, day).Scan(&used); err == nil && used >= l.PerDay {
			w.Header().Set("Retry-After", strconv.Itoa(int(day.Add(24*time.Hour).Sub(now).Seconds())+1))
			s.fail(w, r, http.StatusTooManyRequests, "QUOTA_EXCEEDED", fmt.Sprintf("This workspace has used today's %d checks. The count resets at midnight UTC; upgrade in the console's Settings to raise it.", l.PerDay), map[string]any{"checks_per_day": l.PerDay, "used": used})
			return false, false
		}
	}
	return l.WatchOnly, true
}
