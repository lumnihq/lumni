package api

import (
	"crypto/rand"
	"encoding/base32"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/lumnihq/lumni/internal/auth"
)

func normalizeOnboardingCode(code string) string { return strings.ToUpper(strings.TrimSpace(code)) }

func (s *Server) createOnboardingCode(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Scopes  []string `json:"scopes"`
		Minutes *int     `json:"expires_in_minutes"`
		Actor   string   `json:"actor_hint"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	minutes := 15
	if in.Minutes != nil {
		minutes = *in.Minutes
	}
	if len(in.Scopes) == 0 {
		in.Scopes = []string{"ingest", "query", "runtime", "admission"}
	}
	if minutes < 1 || minutes > 15 || len(in.Actor) > 200 || strings.ContainsAny(in.Actor, "\r\n\x00") || len(in.Scopes) > 7 {
		s.fail(w, r, 400, "INVALID_ONBOARDING_CODE", "Expiry must be 1–15 minutes and actor_hint must be at most 200 characters", nil)
		return
	}
	seen := map[string]bool{}
	for _, scope := range in.Scopes {
		if !contains([]string{"ingest", "query", "runtime", "admission", "verification", "payload", "admin"}, scope) || seen[scope] {
			s.fail(w, r, 400, "INVALID_SCOPE", "Unique supported API scopes are required", nil)
			return
		}
		seen[scope] = true
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		s.respond(w, r, 0, nil, err)
		return
	}
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
	code := "LUM-" + encoded[:13] + "-" + encoded[13:]
	expires := time.Now().UTC().Add(time.Duration(minutes) * time.Minute)
	p, _ := auth.PrincipalFrom(r.Context())
	_, err := s.pool.Exec(r.Context(), `INSERT INTO onboarding_codes(tenant_id,code_hash,scopes,actor_hint,created_by,expires_at)VALUES($1,$2,$3,$4,$5,$6)`, p.TenantID, auth.Hash(code), in.Scopes, in.Actor, p.KeyID, expires)
	w.Header().Set("Cache-Control", "no-store")
	s.respond(w, r, 201, map[string]any{"code": code, "expires_at": expires}, err)
}

func (s *Server) exchangeOnboardingCode(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	peer, _ := r.Context().Value(peerKey{}).(string)
	if peer == "" {
		peer = r.RemoteAddr
	}
	if host, _, err := net.SplitHostPort(peer); err == nil {
		peer = host
	}
	var requests int
	err := s.pool.QueryRow(r.Context(), `INSERT INTO onboarding_exchange_rate(peer_hash,bucket,requests)VALUES($1,date_trunc('minute',clock_timestamp()),1)ON CONFLICT(peer_hash,bucket)DO UPDATE SET requests=onboarding_exchange_rate.requests+1 RETURNING requests`, auth.Hash(peer)).Scan(&requests)
	if err != nil {
		s.respond(w, r, 0, nil, err)
		return
	}
	if requests > 10 {
		w.Header().Set("Retry-After", "60")
		s.fail(w, r, 429, "RATE_LIMITED", "Too many onboarding attempts", nil)
		return
	}
	var in struct {
		Code    string `json:"code"`
		Host    string `json:"host"`
		Project string `json:"project"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if len(in.Code) > 128 || len(in.Host) > 200 || len(in.Project) > 200 || strings.ContainsAny(in.Host+in.Project, "\r\n\x00") {
		s.fail(w, r, 400, "INVALID_ONBOARDING_EXCHANGE", "Invalid code, host, or project", nil)
		return
	}
	secret, err := auth.Generate()
	if err != nil {
		s.respond(w, r, 0, nil, err)
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.respond(w, r, 0, nil, err)
		return
	}
	defer tx.Rollback(r.Context())
	var workspace, tenantID, actor string
	var scopes []string
	hash := auth.Hash(normalizeOnboardingCode(in.Code))
	err = tx.QueryRow(r.Context(), `SELECT c.tenant_id::text,c.scopes,c.actor_hint,t.name FROM onboarding_codes c JOIN tenants t ON t.id=c.tenant_id JOIN api_keys k ON k.id=c.created_by AND k.tenant_id=c.tenant_id WHERE c.code_hash=$1 AND c.used_at IS NULL AND c.expires_at>clock_timestamp() AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at>clock_timestamp()) FOR UPDATE OF c`, hash).Scan(&tenantID, &scopes, &actor, &workspace)
	if errors.Is(err, pgx.ErrNoRows) {
		s.fail(w, r, 404, "NOT_FOUND", "Onboarding code was not found", nil)
		return
	}
	if err != nil {
		s.respond(w, r, 0, nil, err)
		return
	}
	id := uuid.NewString()
	name := "onboarding: " + strings.TrimSpace(in.Project+" @ "+in.Host)
	_, err = tx.Exec(r.Context(), `INSERT INTO api_keys(id,tenant_id,name,prefix,secret_hash,scopes)VALUES($1,$2,$3,$4,$5,$6)`, id, tenantID, name, auth.Prefix(secret), auth.Hash(secret), scopes)
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE onboarding_codes SET used_at=clock_timestamp(),issued_key_id=$2,actor_id=NULLIF(actor_hint,''),host=$3,project=$4 WHERE code_hash=$1`, hash, id, in.Host, in.Project)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	base := strings.TrimRight(os.Getenv("LUMNI_PUBLIC_BASE_URL"), "/")
	if base == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		base = scheme + "://" + r.Host
	}
	s.respond(w, r, 200, map[string]any{"api_key": secret, "key_id": id, "tenant_name": workspace, "api_url": base, "actor_hint": actor}, err)
}

func (s *Server) onboardingStatus(w http.ResponseWriter, r *http.Request) {
	var used *time.Time
	var actor, host, project, keyID *string
	err := s.pool.QueryRow(r.Context(), `SELECT used_at,actor_id,host,project,issued_key_id::text FROM onboarding_codes WHERE tenant_id=$1 AND code_hash=$2`, tenant(r), auth.Hash(normalizeOnboardingCode(chi.URLParam(r, "code")))).Scan(&used, &actor, &host, &project, &keyID)
	w.Header().Set("Cache-Control", "no-store")
	s.respond(w, r, 200, map[string]any{"used": used != nil, "used_at": used, "actor_id": actor, "host": host, "project": project, "key_id": keyID}, err)
}
