package api

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"lumni/internal/auth"
	"lumni/pkg/protocol"
)

// POST /v1/tenants: a new workspace (tenant) and its first admin key, for
// hosted sign-up (Lumni Cloud). Only a platform key may call it;
// with none configured the route answers 404, so self-hosted servers don't
// expose it.

// tenantSignup is the route's configuration and its rate limit.
type tenantSignup struct {
	keys    []string
	perHour int
	mu      sync.Mutex
	recent  []time.Time
	limits  tenantLimits
}

// allow records one creation, unless the last hour already has perHour.
func (t *tenantSignup) allow(now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	limit := t.perHour
	if limit <= 0 {
		limit = 60
	}
	kept := t.recent[:0]
	for _, at := range t.recent {
		if now.Sub(at) < time.Hour {
			kept = append(kept, at)
		}
	}
	t.recent = kept
	if len(t.recent) >= limit {
		return false
	}
	t.recent = append(t.recent, now)
	return true
}

// authorized compares the bearer token with every platform key in constant time.
func (t *tenantSignup) authorized(r *http.Request) bool {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		return false
	}
	given := sha256.Sum256([]byte(token))
	match := 0
	for _, key := range t.keys {
		want := sha256.Sum256([]byte(key))
		match |= subtle.ConstantTimeCompare(given[:], want[:])
	}
	return match == 1
}

var tenantSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)

var errSlugTaken = errors.New("slug taken")

// newTenantSettings starts a workspace made by sign-up with its limits.
func (s *Server) newTenantSettings() []byte {
	if !s.tenants.limits.limited() {
		return []byte(`{}`)
	}
	return jsonBytes(map[string]any{"limits": s.tenants.limits})
}

// slugFor derives a slug from a name, with a random suffix so workspaces with
// the same name don't collide: "Acme Inc." becomes "acme-inc-3f9a1c".
func slugFor(name string) (string, error) {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if b.Len() >= 40 {
			break
		}
		switch {
		case r < utf8.RuneSelf && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteRune(r)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	base := strings.Trim(b.String(), "-")
	if base == "" {
		base = "workspace"
	}
	suffix := make([]byte, 3)
	if _, err := rand.Read(suffix); err != nil {
		return "", err
	}
	return base + "-" + hex.EncodeToString(suffix), nil
}

// platform admits a request made with a platform key; without any configured,
// these routes don't exist.
func (s *Server) platform(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	if len(s.tenants.keys) == 0 {
		s.fail(w, r, http.StatusNotFound, "NOT_FOUND", "Not found", nil)
		return false
	}
	if !s.tenants.authorized(r) {
		s.fail(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "A platform key is required", nil)
		return false
	}
	return true
}

func (s *Server) createTenant(w http.ResponseWriter, r *http.Request) {
	if !s.platform(w, r) {
		return
	}
	var in struct {
		Name string `json:"name"`
		Slug string `json:"slug"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || utf8.RuneCountInString(name) > 80 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		s.fail(w, r, http.StatusBadRequest, "INVALID_TENANT_NAME", "Name must be 1 to 80 characters", nil)
		return
	}
	slug := strings.ToLower(strings.TrimSpace(in.Slug))
	if slug != "" && !tenantSlug.MatchString(slug) {
		s.fail(w, r, http.StatusBadRequest, "INVALID_TENANT_SLUG", "Slug must be 2 to 63 lowercase letters, digits or dashes", nil)
		return
	}
	if !s.tenants.allow(time.Now()) {
		w.Header().Set("Retry-After", "60")
		s.fail(w, r, http.StatusTooManyRequests, "RATE_LIMITED", "Too many workspaces created in the last hour", nil)
		return
	}
	var err error
	if slug == "" {
		if slug, err = slugFor(name); err != nil {
			s.respond(w, r, 0, nil, err)
			return
		}
	}
	secret, err := auth.Generate()
	if err != nil {
		s.respond(w, r, 0, nil, err)
		return
	}
	id, err := s.insertTenant(r, name, slug, secret)
	if errors.Is(err, errSlugTaken) {
		s.fail(w, r, http.StatusConflict, "SLUG_TAKEN", "That slug is taken", nil)
		return
	}
	s.respond(w, r, http.StatusCreated, map[string]any{"tenant_id": id, "admin_key": secret, "name": name, "slug": slug}, err)
}

// insertTenant creates the tenant the way bootstrap does (SHADOW, default
// settings), its admin key (stored hashed) and a tenant.created audit event,
// in one transaction.
func (s *Server) insertTenant(r *http.Request, name, slug, secret string) (string, error) {
	ctx := r.Context()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO tenants(name,slug,enforcement_mode,settings)VALUES($1,$2,$3,$4)ON CONFLICT(slug)DO NOTHING RETURNING id::text`, name, slug, protocol.ModeShadow, s.newTenantSettings()).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errSlugTaken
	}
	if err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO api_keys(tenant_id,name,prefix,secret_hash,scopes)VALUES($1,'workspace-admin',$2,$3,ARRAY['admin','ingest','admission','query'])`, id, auth.Prefix(secret), auth.Hash(secret)); err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(tenant_id,by,type,target,after)VALUES($1,'platform','tenant.created',$2,$3)`, id, jsonBytes(map[string]any{"tenant_id": id}), jsonBytes(map[string]any{"name": name, "slug": slug})); err != nil {
		return "", err
	}
	return id, tx.Commit(ctx)
}

// PUT /v1/tenants/{id}/plan: what a workspace's plan allows. Lumni Cloud's
// console sets it from Stripe: trial, paid, or watch-only once a trial ends.
func (s *Server) setTenantPlan(w http.ResponseWriter, r *http.Request) {
	if !s.platform(w, r) {
		return
	}
	id, err := uuid.Parse(pathID(r, "id"))
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "NOT_FOUND", "No such tenant", nil)
		return
	}
	var in struct {
		Plan      string       `json:"plan"`
		Limits    tenantLimits `json:"limits"`
		WatchOnly bool         `json:"watch_only"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if in.Limits.PerDay < 0 || in.Limits.PerSecond < 0 || len(in.Plan) > 40 {
		s.fail(w, r, http.StatusBadRequest, "INVALID_PLAN", "Limits must be zero or more, and the plan name short", nil)
		return
	}
	set := jsonBytes(map[string]any{"plan": in.Plan, "limits": in.Limits, "watch_only": in.WatchOnly})
	tag, err := s.pool.Exec(r.Context(), `UPDATE tenants SET settings=settings||$2::jsonb,updated_at=clock_timestamp() WHERE id=$1`, id, set)
	if err != nil {
		s.respond(w, r, 0, nil, err)
		return
	}
	if tag.RowsAffected() == 0 {
		s.fail(w, r, http.StatusNotFound, "NOT_FOUND", "No such tenant", nil)
		return
	}
	_, _ = s.pool.Exec(r.Context(), `INSERT INTO audit_events(tenant_id,by,type,target,after)VALUES($1,'platform','plan.set','{}'::jsonb,$2)`, id, set)
	s.quotas.forget(id.String())
	writeJSON(w, http.StatusOK, map[string]any{"tenant_id": id.String(), "plan": in.Plan, "limits": in.Limits, "watch_only": in.WatchOnly})
}

// GET /v1/tenants/{id}/usage?from=&to=: checks in [from, to), for billing.
func (s *Server) tenantUsage(w http.ResponseWriter, r *http.Request) {
	if !s.platform(w, r) {
		return
	}
	id, err := uuid.Parse(pathID(r, "id"))
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "NOT_FOUND", "No such tenant", nil)
		return
	}
	from, errFrom := time.Parse(time.RFC3339, r.URL.Query().Get("from"))
	to, errTo := time.Parse(time.RFC3339, r.URL.Query().Get("to"))
	if errFrom != nil || errTo != nil || !to.After(from) {
		s.fail(w, r, http.StatusBadRequest, "INVALID_RANGE", "from and to must be RFC 3339 times, from before to", nil)
		return
	}
	var checks int
	err = s.pool.QueryRow(r.Context(), `SELECT count(*) FROM actions WHERE tenant_id=$1 AND requested_at>=$2 AND requested_at<$3`, id, from, to).Scan(&checks)
	s.respond(w, r, http.StatusOK, map[string]any{"tenant_id": id.String(), "from": from, "to": to, "checks": checks}, err)
}
