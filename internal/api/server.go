package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"lumni/internal/analytics"
	"lumni/internal/auth"
	"lumni/internal/composio"
	"lumni/internal/coordination"
	"lumni/internal/core"
	"lumni/internal/durable"
	githubingest "lumni/internal/github"
	"lumni/internal/graph"
	"lumni/internal/secrets"
	"lumni/pkg/protocol"
)

type Server struct {
	composio           *composio.Service
	durable            *durable.Service
	pool               *pgxpool.Pool
	auth               *auth.Service
	core               *core.Service
	kernel             *coordination.Kernel
	github             *githubingest.Service
	githubSecret       string
	secrets            *secrets.Manager
	logger             *slog.Logger
	bodyLimit          int64
	router             chi.Router
	registry           *prometheus.Registry
	checks             *prometheus.CounterVec
	latency            *prometheus.HistogramVec
	graphSnapshots     *prometheus.CounterVec
	graphStreams       prometheus.Gauge
	analyticsFallbacks prometheus.Counter
	graph              *graph.Service
	analytics          analytics.Store
	consoleDir         string
	sessionIdleTTL     time.Duration
	sessionAbsoluteTTL time.Duration

	tenants tenantSignup
	quotas  quotas
}

func New(pool *pgxpool.Pool, authService *auth.Service, coreService *core.Service, kernel *coordination.Kernel, logger *slog.Logger, bodyLimit int64, githubSecret, encryptionKey string) *Server {
	secretManager, err := secrets.New(encryptionKey)
	if err != nil {
		panic(err)
	}
	s := &Server{pool: pool, auth: authService, core: coreService, kernel: kernel, github: githubingest.New(pool), githubSecret: githubSecret, secrets: secretManager, logger: logger, bodyLimit: bodyLimit, graph: graph.New(pool), analytics: analytics.NewPostgres(pool), sessionIdleTTL: 8 * time.Hour, sessionAbsoluteTTL: 24 * time.Hour}
	s.durable = durable.New(pool, kernel, secretManager)

	s.checks = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "lumni_decisions_total", Help: "Coordination decisions."}, []string{"decision", "reason", "enforced"})
	s.latency = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "lumni_decision_latency_seconds", Help: "Admission latency.", Buckets: prometheus.DefBuckets}, []string{"decision"})
	s.graphSnapshots = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "lumni_graph_snapshots_total", Help: "Operational graph snapshots."}, []string{"lens", "source"})
	s.graphStreams = prometheus.NewGauge(prometheus.GaugeOpts{Name: "lumni_graph_stream_connections", Help: "Active operational graph SSE connections."})
	s.analyticsFallbacks = prometheus.NewCounter(prometheus.CounterOpts{Name: "lumni_analytics_fallback_total", Help: "Analytics queries served by PostgreSQL after primary analytics failure."})
	s.registry = prometheus.NewRegistry()
	s.registry.MustRegister(s.checks, s.latency, s.graphSnapshots, s.graphStreams, s.analyticsFallbacks)
	s.router = s.routes()
	return s
}

func (s *Server) Handler() http.Handler { return s.router }

type RuntimeOptions struct {
	Composio           *composio.Client
	ConsoleDir         string
	Analytics          analytics.Store
	SessionIdleTTL     time.Duration
	SessionAbsoluteTTL time.Duration

	// PlatformKeys may create tenants (POST /v1/tenants); TenantsPerHour caps
	// that across them (0 means 60).
	PlatformKeys   []string
	TenantsPerHour int

	// Limits written into workspaces made by sign-up (0: none).
	TenantChecksPerDay    int
	TenantChecksPerSecond int
}

func (s *Server) Configure(options RuntimeOptions) {
	if options.Composio != nil {
		s.composio = composio.NewService(s.pool, options.Composio, s.kernel, s.durable, s.logger)
		s.durable.SetGitHubConnectorReader(s.composio)
	}
	if options.Analytics != nil {
		s.analytics = options.Analytics
	}
	if options.ConsoleDir != "" {
		s.consoleDir = options.ConsoleDir
	}
	if options.SessionIdleTTL > 0 {
		s.sessionIdleTTL = options.SessionIdleTTL
	}
	if options.SessionAbsoluteTTL > 0 {
		s.sessionAbsoluteTTL = options.SessionAbsoluteTTL
	}
	s.tenants.keys = options.PlatformKeys
	s.tenants.perHour = options.TenantsPerHour
	s.tenants.limits = tenantLimits{PerDay: options.TenantChecksPerDay, PerSecond: options.TenantChecksPerSecond}
}

func (s *Server) routes() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer, rememberPeer, middleware.RealIP, s.requestID, s.accessLog)
	r.Get("/v1/health/live", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "live"})
	})
	r.Get("/v1/health/ready", s.ready)
	r.Post("/v1/webhooks/github", s.githubWebhook)
	r.Post("/v1/webhooks/composio", s.composioWebhook)
	r.Post("/v1/auth/session", s.createBrowserSession)
	r.Post("/v1/onboarding/exchange", s.exchangeOnboardingCode)
	r.Post("/v1/tenants", s.createTenant)
	r.Put("/v1/tenants/{id}/plan", s.setTenantPlan)
	r.Get("/v1/tenants/{id}/usage", s.tenantUsage)
	r.Get("/v1/public/proofs/{token}", s.publicProof)
	r.Get("/p/{token}", s.publicProofPage)
	r.Get("/console", s.serveConsole)
	r.Get("/console/*", s.serveConsole)
	r.Handle("/metrics", promhttp.HandlerFor(s.registry, promhttp.HandlerOpts{}))
	r.Group(func(r chi.Router) {
		r.Use(s.authenticate)
		r.Use(s.requireCSRF)
		r.Route("/v1", func(r chi.Router) {
			s.durableRoutes(r)
			s.composioRoutes(r)
			s.consoleRoutes(r)
			r.Post("/onboarding/codes", s.withScope("admin", s.createOnboardingCode))
			r.Get("/onboarding/codes/{code}/status", s.withScope("admin", s.onboardingStatus))
			r.Get("/auth/session", s.getBrowserSession)
			r.Delete("/auth/session", s.deleteBrowserSession)
			r.Post("/actors", s.withScope("ingest", s.putActor))
			r.Get("/actors", s.withScope("query", s.listActors))
			r.Get("/actors/{id}", s.withScope("query", s.getActor))
			r.Post("/agents", s.withScope("ingest", s.putAgent))
			r.Get("/agents", s.withScope("query", s.listAgents))
			r.Post("/sessions", s.withScope("ingest", s.startSession))
			r.Post("/sessions/{id}/complete", s.withScope("ingest", s.completeSession))
			r.Post("/work", s.withScope("ingest", s.startWork))
			r.Get("/work", s.withScope("query", s.listWork))
			r.Get("/work/{id}", s.withScope("query", s.getWork))
			r.Post("/work/{id}/complete", s.withScope("ingest", s.workStatus(protocol.WorkCompleted)))
			r.Post("/work/{id}/fail", s.withScope("ingest", s.workStatus(protocol.WorkFailed)))
			r.Post("/work/{id}/cancel", s.withScope("ingest", s.workStatus(protocol.WorkCancelled)))
			r.Post("/work/{id}/supersede", s.withScope("ingest", s.workStatus(protocol.WorkSuperseded)))
			r.Get("/work/{id}/graph", s.withScope("query", s.workGraph))
			r.Post("/observations", s.withScope("ingest", s.observe))
			r.Post("/observations/batch", s.withScope("ingest", s.observeBatch))
			r.Post("/resources/resolve", s.withScope("ingest", s.putResource))
			r.Get("/resources", s.withScope("query", s.listResources))
			r.Get("/resources/{id}", s.withScope("query", s.getResource))
			r.Post("/resources/{id}/version", s.withScope("ingest", s.updateVersion))
			r.Post("/actions/check", s.withScope("admission", s.checkAction(false)))
			r.Post("/actions/observe", s.withScope("admission", s.checkAction(true)))
			r.Get("/actions/{id}", s.withScope("query", s.getAction))
			r.Post("/actions/{id}/start", s.withScope("admission", s.startAction))
			r.Post("/actions/{id}/heartbeat", s.withScope("admission", s.heartbeatAction))
			r.Post("/actions/{id}/result", s.withScope("admission", s.actionResult))
			r.Post("/actions/{id}/cancel-reservation", s.withScope("admission", s.cancelReservation))
			r.Get("/approvals", s.withScope("query", s.listApprovals))
			r.Get("/approvals/{id}", s.withScope("query", s.getApproval))
			r.Post("/approvals/{id}/approve", s.withScope("admission", s.resolveApproval("APPROVED")))
			r.Post("/approvals/{id}/deny", s.withScope("admission", s.resolveApproval("DENIED")))
			r.Post("/events", s.withScope("ingest", s.ingestEvent))
			r.Post("/events/batch", s.withScope("ingest", s.ingestEvents))
			r.Post("/costs", s.withScope("ingest", s.recordCost))
			r.Get("/finops/summary", s.withScope("query", s.finopsSummary))
			r.Get("/finops/graph", s.withScope("query", s.finopsGraph))
			r.Get("/graph/work/{id}", s.withScope("query", s.workGraph))
			r.Get("/graph/resource/{id}", s.withScope("query", s.resourceGraph))
			r.Get("/graph/live", s.withScope("query", s.liveGraph))
			r.Get("/graph/snapshot", s.withScope("query", s.graphSnapshot))
			r.Get("/graph/entities/{type}/{id}", s.withScope("query", s.graphEntity))
			r.Get("/graph/stream", s.withScope("query", s.graphStream))
			r.Post("/api-keys", s.withScope("admin", s.createAPIKey))
			r.Get("/api-keys", s.withScope("admin", s.listAPIKeys))
			r.Delete("/api-keys/{id}", s.withScope("admin", s.revokeAPIKey))
			r.Get("/config", s.withScope("query", s.getConfig))
			r.Put("/config/mode", s.withScope("admin", s.setMode))
			r.Put("/config/semantic", s.withScope("admin", s.setSemantic))
			r.Get("/config/resource-rules", s.withScope("query", s.listRules("resource_rules")))
			r.Post("/config/resource-rules", s.withScope("admin", s.createResourceRule))
			r.Get("/config/conflict-rules", s.withScope("query", s.listRules("conflict_rules")))
			r.Post("/config/conflict-rules", s.withScope("admin", s.createConflictRule))
			r.Get("/config/policy-rules", s.withScope("query", s.listRules("policy_rules")))
			r.Post("/config/policy-rules", s.withScope("admin", s.createPolicyRule))
			r.Get("/replay/{actionID}", s.withScope("query", s.replay))
			r.Post("/repositories", s.withScope("ingest", s.putRepository))
			r.Get("/repositories", s.withScope("query", s.listRepositories))
			r.Post("/webhooks", s.withScope("admin", s.createWebhook))
			r.Get("/webhooks", s.withScope("admin", s.listWebhooks))
			r.Delete("/webhooks/{id}", s.withScope("admin", s.deleteWebhook))
		})
	})
	return r
}

func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = "req_" + uuid.NewString()
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

type requestIDKey struct{}
type peerKey struct{}

func rememberPeer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), peerKey{}, r.RemoteAddr)))
	})
}

func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		path := r.URL.Path
		if strings.HasPrefix(path, "/v1/onboarding/codes/") {
			path = "/v1/onboarding/codes/{code}/status"
		}
		s.logger.Info("http request", "request_id", requestID(r.Context()), "method", r.Method, "path", path, "duration_ms", time.Since(start).Milliseconds())
	})
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secret, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		var p auth.Principal
		var err error
		if ok && secret != "" {
			p, err = s.auth.Authenticate(r.Context(), secret)
		} else if cookie, cookieErr := r.Cookie("lumni_session"); cookieErr == nil {
			p, err = s.auth.AuthenticateBrowserSession(r.Context(), cookie.Value, s.sessionIdleTTL)
		} else {
			s.fail(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "Bearer token or browser session required", nil)
			return
		}
		if err != nil {
			s.fail(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "Invalid or expired authentication", nil)
			return
		}
		if header := r.Header.Get("X-Lumni-Tenant"); header != "" && header != p.TenantID {
			s.fail(w, r, http.StatusForbidden, "TENANT_MISMATCH", "Key is not valid for requested tenant", nil)
			return
		}
		next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
	})
}

func (s *Server) requireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := auth.PrincipalFrom(r.Context())
		if ok && principal.ViaSession && r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if !s.auth.ValidateCSRF(principal, r.Header.Get("X-CSRF-Token")) {
				s.fail(w, r, http.StatusForbidden, "CSRF_TOKEN_REQUIRED", "A valid CSRF token is required", nil)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) createBrowserSession(w http.ResponseWriter, r *http.Request) {
	secret, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || secret == "" {
		s.fail(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "Bearer token required for session exchange", nil)
		return
	}
	principal, err := s.auth.Authenticate(r.Context(), secret)
	if err != nil {
		s.fail(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "Invalid bearer token", nil)
		return
	}
	if header := r.Header.Get("X-Lumni-Tenant"); header != "" && header != principal.TenantID {
		s.fail(w, r, http.StatusForbidden, "TENANT_MISMATCH", "Key is not valid for requested tenant", nil)
		return
	}
	session, err := s.auth.CreateBrowserSession(r.Context(), principal, s.sessionIdleTTL, s.sessionAbsoluteTTL)
	if err != nil {
		s.respond(w, r, 0, nil, err)
		return
	}
	https := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	http.SetCookie(w, &http.Cookie{Name: "lumni_session", Value: session.Token, Path: "/", HttpOnly: true, Secure: https, SameSite: http.SameSiteStrictMode, Expires: session.ExpiresAt, MaxAge: int(time.Until(session.ExpiresAt).Seconds())})
	writeJSON(w, http.StatusCreated, map[string]any{"tenant_id": session.TenantID, "scopes": session.Scopes, "csrf_token": session.CSRFToken, "expires_at": session.ExpiresAt})
}

func (s *Server) getBrowserSession(w http.ResponseWriter, r *http.Request) {
	principal, _ := auth.PrincipalFrom(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"tenant_id": principal.TenantID, "scopes": principal.Scopes, "session": principal.ViaSession})
}

func (s *Server) deleteBrowserSession(w http.ResponseWriter, r *http.Request) {
	principal, _ := auth.PrincipalFrom(r.Context())
	if err := s.auth.RevokeBrowserSession(r.Context(), principal); err != nil {
		s.respond(w, r, 0, nil, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "lumni_session", Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1, Expires: time.Unix(0, 0)})
	writeJSON(w, http.StatusOK, map[string]any{"revoked": true})
}

func (s *Server) withScope(scope string, fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := auth.PrincipalFrom(r.Context())
		if !ok || (!contains(p.Scopes, scope) && !contains(p.Scopes, "admin")) {
			s.fail(w, r, http.StatusForbidden, "SCOPE_REQUIRED", "Required scope: "+scope, nil)
			return
		}
		fn(w, r)
	}
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	if err := s.pool.Ping(ctx); err != nil {
		s.fail(w, r, http.StatusServiceUnavailable, "DATABASE_UNAVAILABLE", "Database is unavailable", nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready"})
}

func (s *Server) serveConsole(w http.ResponseWriter, r *http.Request) {
	if s.consoleDir == "" {
		http.NotFound(w, r)
		return
	}
	if r.URL.Path == "/console" {
		http.Redirect(w, r, "/console/", http.StatusTemporaryRedirect)
		return
	}
	relative := strings.TrimPrefix(r.URL.Path, "/console/")
	clean := filepath.Clean(relative)
	if clean == "." || strings.HasPrefix(clean, "..") {
		clean = "index.html"
	}
	path := filepath.Join(s.consoleDir, clean)
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		path = filepath.Join(s.consoleDir, "index.html")
	}
	if _, err := os.Stat(path); err != nil {
		s.fail(w, r, http.StatusServiceUnavailable, "CONSOLE_UNAVAILABLE", "The console has not been built", nil)
		return
	}
	if strings.Contains(filepath.Base(path), ".") && filepath.Base(path) != "index.html" {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeFile(w, r, path)
}

func (s *Server) putActor(w http.ResponseWriter, r *http.Request) {
	var in protocol.Actor
	if !s.decode(w, r, &in) {
		return
	}
	out, err := s.core.PutActor(r.Context(), tenant(r), in)
	if err == nil {
		p, _ := auth.PrincipalFrom(r.Context())
		_, err = s.pool.Exec(r.Context(), `UPDATE onboarding_codes SET actor_id=$3 WHERE tenant_id=$1 AND issued_key_id=$2 AND actor_id IS DISTINCT FROM $3`, p.TenantID, p.KeyID, out.ID)
	}
	s.respond(w, r, http.StatusCreated, out, err)
}
func (s *Server) listActors(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if since := q.Get("since"); since != "" {
		if q.Get("from") != "" && q.Get("from") != since {
			s.fail(w, r, 400, "INVALID_QUERY", "since and from must agree when both are supplied", nil)
			return
		}
		q.Set("from", since)
		r.URL.RawQuery = q.Encode()
	}
	if typ := q.Get("type"); typ != "" && !contains([]string{"AGENT", "HUMAN", "SERVICE", "CRON", "WEBHOOK", "CI", "WORKFLOW", "SYSTEM"}, typ) {
		s.fail(w, r, 400, "INVALID_QUERY", "Unknown actor type", nil)
		return
	}
	s.pagedList(w, r, listSpec{From: "actors t", Tenant: "t.tenant_id", Time: "t.created_at", Key: "t.id", ID: "t.id", Type: "t.type", JSON: `to_jsonb(t)-'tenant_id'||jsonb_build_object('paused',t.paused AND (t.until IS NULL OR t.until>now()))`}, "", false)
}
func (s *Server) getActor(w http.ResponseWriter, r *http.Request) {
	s.jsonRow(w, r, `SELECT to_jsonb(t)-'tenant_id'||jsonb_build_object('paused',t.paused AND (t.until IS NULL OR t.until>now())) FROM actors t WHERE tenant_id=$1 AND id=$2`, tenant(r), pathID(r, "id"))
}

func (s *Server) putAgent(w http.ResponseWriter, r *http.Request) {
	var in protocol.Agent
	if !s.decode(w, r, &in) {
		return
	}
	out, err := s.core.PutAgent(r.Context(), tenant(r), in)
	s.respond(w, r, http.StatusCreated, out, err)
}
func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	out, err := s.core.ListAgents(r.Context(), tenant(r))
	s.respond(w, r, http.StatusOK, protocol.List[protocol.Agent]{Items: out}, err)
}
func (s *Server) startSession(w http.ResponseWriter, r *http.Request) {
	var in protocol.Session
	if !s.decode(w, r, &in) {
		return
	}
	out, err := s.core.StartSession(r.Context(), tenant(r), in)
	s.respond(w, r, http.StatusCreated, out, err)
}
func (s *Server) completeSession(w http.ResponseWriter, r *http.Request) {
	err := s.core.CompleteSession(r.Context(), tenant(r), pathID(r, "id"))
	s.respond(w, r, http.StatusOK, map[string]any{"status": "COMPLETED"}, err)
}
func (s *Server) startWork(w http.ResponseWriter, r *http.Request) {
	var in protocol.Work
	if !s.decode(w, r, &in) {
		return
	}
	out, err := s.core.StartWork(r.Context(), tenant(r), in)
	s.respond(w, r, http.StatusCreated, out, err)
}
func (s *Server) listWork(w http.ResponseWriter, r *http.Request) {
	out, err := s.core.ListWork(r.Context(), tenant(r))
	s.respond(w, r, http.StatusOK, protocol.List[protocol.Work]{Items: out}, err)
}
func (s *Server) getWork(w http.ResponseWriter, r *http.Request) {
	out, err := s.core.GetWork(r.Context(), tenant(r), pathID(r, "id"))
	s.respond(w, r, http.StatusOK, out, err)
}
func (s *Server) workStatus(status protocol.WorkStatus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := s.core.SetWorkStatus(r.Context(), tenant(r), pathID(r, "id"), status)
		s.respond(w, r, http.StatusOK, map[string]any{"status": status}, err)
	}
}
func (s *Server) workGraph(w http.ResponseWriter, r *http.Request) {
	out, err := s.core.GraphForWork(r.Context(), tenant(r), pathID(r, "id"))
	s.respond(w, r, http.StatusOK, out, err)
}

func (s *Server) observe(w http.ResponseWriter, r *http.Request) {
	var in protocol.ObservationInput
	if !s.decode(w, r, &in) {
		return
	}
	id, err := s.core.Observe(r.Context(), tenant(r), in)
	s.respond(w, r, http.StatusCreated, map[string]any{"id": id}, err)
}
func (s *Server) observeBatch(w http.ResponseWriter, r *http.Request) {
	var inputs []protocol.ObservationInput
	if !s.decode(w, r, &inputs) {
		return
	}
	ids := make([]string, 0, len(inputs))
	for _, in := range inputs {
		id, err := s.core.Observe(r.Context(), tenant(r), in)
		if err != nil {
			s.respond(w, r, 0, nil, err)
			return
		}
		ids = append(ids, id)
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ids": ids})
}
func (s *Server) putResource(w http.ResponseWriter, r *http.Request) {
	var in protocol.Resource
	if !s.decode(w, r, &in) {
		return
	}
	out, err := s.core.PutResource(r.Context(), tenant(r), in)
	s.respond(w, r, http.StatusCreated, out, err)
}
func (s *Server) listResources(w http.ResponseWriter, r *http.Request) {
	out, err := s.core.ListResources(r.Context(), tenant(r))
	s.respond(w, r, http.StatusOK, protocol.List[protocol.Resource]{Items: out}, err)
}
func (s *Server) getResource(w http.ResponseWriter, r *http.Request) {
	out, err := s.core.GetResource(r.Context(), tenant(r), pathID(r, "id"))
	s.respond(w, r, http.StatusOK, out, err)
}
func (s *Server) updateVersion(w http.ResponseWriter, r *http.Request) {
	var in protocol.ResourceVersionInput
	if !s.decode(w, r, &in) {
		return
	}
	err := s.core.UpdateVersion(r.Context(), tenant(r), pathID(r, "id"), in)
	s.respond(w, r, http.StatusOK, map[string]any{"updated": true}, err)
}

func (s *Server) checkAction(observe bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		watchOnly, ok := s.withinQuota(w, r)
		if !ok {
			return
		}
		var in protocol.ActionCheckInput
		if !s.decode(w, r, &in) {
			return
		}
		in.ObserveOnly = observe || in.ObserveOnly || watchOnly
		start := time.Now()
		out, err := s.kernel.Check(r.Context(), tenant(r), in)
		if err == nil {
			s.checks.WithLabelValues(string(out.Decision), string(out.ReasonCode), strconv.FormatBool(out.Enforced)).Inc()
			s.latency.WithLabelValues(string(out.Decision)).Observe(time.Since(start).Seconds())
		}
		s.respond(w, r, http.StatusOK, out, err)
	}
}

type leaseInput struct {
	LeaseToken string `json:"lease_token"`
}

func (s *Server) startAction(w http.ResponseWriter, r *http.Request) {
	var in leaseInput
	if r.ContentLength > 0 && !s.decode(w, r, &in) {
		return
	}
	err := s.kernel.Start(r.Context(), tenant(r), pathID(r, "id"), in.LeaseToken)
	s.respond(w, r, http.StatusOK, map[string]any{"status": "ACTIVE"}, err)
}
func (s *Server) heartbeatAction(w http.ResponseWriter, r *http.Request) {
	var in leaseInput
	if !s.decode(w, r, &in) {
		return
	}
	expires, err := s.kernel.Heartbeat(r.Context(), tenant(r), pathID(r, "id"), in.LeaseToken)
	s.respond(w, r, http.StatusOK, map[string]any{"expires_at": expires}, err)
}
func (s *Server) actionResult(w http.ResponseWriter, r *http.Request) {
	var in protocol.ActionResultInput
	if !s.decode(w, r, &in) {
		return
	}
	err := s.kernel.RecordResult(r.Context(), tenant(r), pathID(r, "id"), in)
	if errors.Is(err, coordination.ErrInvalidLease) {
		_, _ = s.pool.Exec(r.Context(), `INSERT INTO events(tenant_id,id,type,entity_type,entity_id,source,occurred_at,payload)SELECT tenant_id,$3||':'||resource_id,'FENCING_TOKEN_INVALID','resource',resource_id,'api',now(),jsonb_build_object('resource_id',resource_id,'action_id',action_id) FROM action_resources WHERE tenant_id=$1 AND action_id=$2`, tenant(r), pathID(r, "id"), "evt_"+uuid.NewString())
	}
	s.respond(w, r, http.StatusOK, map[string]any{"recorded": true}, err)
}
func (s *Server) cancelReservation(w http.ResponseWriter, r *http.Request) {
	err := s.kernel.CancelReservation(r.Context(), tenant(r), pathID(r, "id"))
	s.respond(w, r, http.StatusOK, map[string]any{"status": protocol.ActionExpired}, err)
}
func (s *Server) getAction(w http.ResponseWriter, r *http.Request) {
	out, err := s.core.Action(r.Context(), tenant(r), pathID(r, "id"))
	s.respond(w, r, http.StatusOK, out, err)
}

func (s *Server) listApprovals(w http.ResponseWriter, r *http.Request) { s.approvals(w, r, "") }
func (s *Server) getApproval(w http.ResponseWriter, r *http.Request) {
	s.approvals(w, r, pathID(r, "id"))
}
func (s *Server) approvals(w http.ResponseWriter, r *http.Request, id string) {
	include := csv(r.URL.Query().Get("include"))
	for _, v := range include {
		if v != "action" && v != "rule" {
			s.fail(w, r, 400, "INVALID_QUERY", "include supports action,rule", nil)
			return
		}
	}
	expression := `to_jsonb(t)-'tenant_id'||jsonb_build_object('resolved_by',COALESCE((SELECT COALESCE(NULLIF(display_name,''),id) FROM actors WHERE tenant_id=t.tenant_id AND id=t.resolved_by),(SELECT name FROM api_keys WHERE tenant_id=t.tenant_id AND id::text=t.resolved_by),t.resolved_by),'status',CASE WHEN t.status='PENDING' AND t.expires_at<=now() THEN 'EXPIRED' ELSE t.status END)`
	if contains(include, "action") {
		expression += `||jsonb_build_object('action',(SELECT to_jsonb(a)-'arguments'-'arguments_ciphertext'-'lease_token_hash'-'tenant_id' FROM actions a WHERE a.tenant_id=t.tenant_id AND a.id=t.action_id))`
	}
	if contains(include, "rule") {
		expression += `||jsonb_build_object('rule',(SELECT to_jsonb(p)-'tenant_id' FROM policy_rules p WHERE p.tenant_id=t.tenant_id AND p.id=COALESCE(t.policy_id,(SELECT (e->'data'->>'policy_id')::uuid FROM decisions d CROSS JOIN LATERAL jsonb_array_elements(COALESCE(d.evidence,'[]')) e WHERE d.tenant_id=t.tenant_id AND d.action_id=t.action_id AND e->>'type'='POLICY' ORDER BY d.created_at DESC LIMIT 1))))`
	}
	s.pagedList(w, r, listSpec{From: "approvals t", Tenant: "t.tenant_id", JSON: expression, Key: "t.id::text", ID: "t.id::text", Time: "t.requested_at", Action: "t.action_id", Status: `CASE WHEN t.status='PENDING' AND t.expires_at<=now() THEN 'EXPIRED' ELSE t.status END`}, id, id != "")
}
func (s *Server) resolveApproval(resolution string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := s.kernel.ResolveApproval(r.Context(), tenant(r), pathID(r, "id"), s.principalName(r), resolution)
		s.respond(w, r, http.StatusOK, map[string]any{"status": resolution}, err)
	}
}

func (s *Server) ingestEvent(w http.ResponseWriter, r *http.Request) {
	var in protocol.EventEnvelope
	if !s.decode(w, r, &in) {
		return
	}
	created, err := s.core.IngestEvent(r.Context(), tenant(r), in)
	s.respond(w, r, http.StatusOK, map[string]any{"created": created}, err)
}
func (s *Server) ingestEvents(w http.ResponseWriter, r *http.Request) {
	var inputs []protocol.EventEnvelope
	if !s.decode(w, r, &inputs) {
		return
	}
	created := 0
	for _, in := range inputs {
		ok, err := s.core.IngestEvent(r.Context(), tenant(r), in)
		if err != nil {
			s.respond(w, r, 0, nil, err)
			return
		}
		if ok {
			created++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"created": created, "deduplicated": len(inputs) - created})
}
func (s *Server) recordCost(w http.ResponseWriter, r *http.Request) {
	var in protocol.CostInput
	if !s.decode(w, r, &in) {
		return
	}
	id, err := s.core.RecordCost(r.Context(), tenant(r), in)
	s.respond(w, r, http.StatusCreated, map[string]any{"id": id}, err)
}
func (s *Server) finopsSummary(w http.ResponseWriter, r *http.Request) {
	days := 7
	if v, _ := strconv.Atoi(r.URL.Query().Get("days")); v > 0 && v <= 366 {
		days = v
	}
	out, err := s.core.FinOpsSummary(r.Context(), tenant(r), time.Now().Add(-time.Duration(days)*24*time.Hour))
	s.respond(w, r, http.StatusOK, out, err)
}

func (s *Server) finopsGraph(w http.ResponseWriter, r *http.Request) {
	to := queryTime(r, "to", time.Now().UTC())
	from := queryTime(r, "from", to.Add(-24*time.Hour))
	if !from.Before(to) {
		s.fail(w, r, http.StatusBadRequest, "INVALID_TIME_RANGE", "from must be before to", nil)
		return
	}
	analyticsStore := s.analytics
	if to.Sub(from) <= 24*time.Hour {
		analyticsStore = analytics.NewPostgres(s.pool)
	}
	out, err := analyticsStore.FinOps(r.Context(), tenant(r), from, to)
	if err == nil {
		out.ByWaste = analytics.StructuredWaste(out.ByWaste)
		out.ByActor, err = analytics.NewPostgres(s.pool).ActorCosts(r.Context(), tenant(r), from, to)
	}
	if err == nil && out.Analytics.Fallback {
		s.analyticsFallbacks.Inc()
	}
	s.respond(w, r, http.StatusOK, out, err)
}

func (s *Server) graphSnapshot(w http.ResponseWriter, r *http.Request) {
	to := queryTime(r, "to", time.Now().UTC())
	from := queryTime(r, "from", to.Add(-24*time.Hour))
	lens := protocol.GraphLens(strings.ToLower(r.URL.Query().Get("lens")))
	switch lens {
	case "", protocol.GraphLensLive, protocol.GraphLensAgents, protocol.GraphLensWork, protocol.GraphLensResources, protocol.GraphLensTransaction, protocol.GraphLensFinOps:
	default:
		s.fail(w, r, http.StatusBadRequest, "INVALID_GRAPH_LENS", "Unsupported graph lens", nil)
		return
	}
	limit := queryInt(r, "limit", 500, 1, 500)
	depth := queryInt(r, "depth", 2, 1, 5)
	filter := graph.Filter{Lens: lens, From: from, To: to, FocusType: strings.ToLower(r.URL.Query().Get("focus_type")), FocusID: r.URL.Query().Get("focus_id"), Depth: depth, Status: r.URL.Query().Get("status"), Risk: protocol.RiskLevel(strings.ToUpper(r.URL.Query().Get("risk"))), Limit: limit}
	var out protocol.GraphSnapshot
	var err error
	if filter.FocusID == "" && (lens == protocol.GraphLensTransaction || r.URL.Query().Get("cursor") != "") {
		p, e := parsePage(r)
		if e != nil {
			s.fail(w, r, 400, "INVALID_QUERY", e.Error(), nil)
			return
		}
		after := ""
		if p.Cursor != nil {
			if p.Cursor.Until.IsZero() || !p.Cursor.At.Before(p.Cursor.Until) {
				s.fail(w, r, 400, "INVALID_QUERY", "Invalid graph cursor bounds", nil)
				return
			}
			filter.From = p.Cursor.At
			filter.To = p.Cursor.Until
			from, to = filter.From, filter.To
			after = p.Cursor.Key
		}
		var next string
		out, next, err = s.graph.PagedSnapshot(r.Context(), tenant(r), filter, after)
		if next != "" {
			v := encodeCursor(pageCursor{At: from, Until: to, Key: next, Filter: pageFingerprint(r)})
			out.NextCursor = &v
		}
	} else {
		out, err = s.graph.Snapshot(r.Context(), tenant(r), filter)
		if err == nil && out.Truncated && filter.FocusID == "" {
			v := encodeCursor(pageCursor{At: from, Until: to, Key: "!", Filter: pageFingerprint(r)})
			out.NextCursor = &v
		}
	}
	if err == nil {
		analyticsStore := s.analytics
		if to.Sub(from) <= 24*time.Hour {
			analyticsStore = analytics.NewPostgres(s.pool)
		}
		out.Trends, out.Analytics, err = analyticsStore.Trends(r.Context(), tenant(r), from, to)
	}
	if err == nil {
		s.graphSnapshots.WithLabelValues(string(out.Lens), out.Analytics.Source).Inc()
		if out.Analytics.Fallback {
			s.analyticsFallbacks.Inc()
		}
	}
	s.respond(w, r, http.StatusOK, out, err)
}

func (s *Server) graphEntity(w http.ResponseWriter, r *http.Request) {
	typ := strings.ToLower(chi.URLParam(r, "type"))
	switch typ {
	case "agent", "session", "work", "action", "resource", "decision", "approval", "lease":
	default:
		s.fail(w, r, http.StatusBadRequest, "INVALID_ENTITY_TYPE", "Unsupported graph entity type", nil)
		return
	}
	out, err := s.graph.Entity(r.Context(), tenant(r), typ, pathID(r, "id"))
	s.respond(w, r, http.StatusOK, out, err)
}

func (s *Server) graphStream(w http.ResponseWriter, r *http.Request) {
	types := csv(r.URL.Query().Get("types"))
	if len(types) > 20 {
		s.fail(w, r, 400, "INVALID_QUERY", "At most 20 event types are supported", nil)
		return
	}
	for _, typ := range types {
		if len(typ) > 80 || !strings.Contains(typ, ".") || strings.IndexFunc(typ, func(c rune) bool {
			return !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_')
		}) >= 0 {
			s.fail(w, r, 400, "INVALID_QUERY", "Invalid event type", nil)
			return
		}
	}
	cursorText := r.Header.Get("Last-Event-ID")
	if value, ok := r.URL.Query()["cursor"]; ok {
		cursorText = value[0]
	}
	var cursor int64
	if cursorText != "" {
		var err error
		cursor, err = strconv.ParseInt(cursorText, 10, 64)
		if err != nil || cursor < 0 {
			s.fail(w, r, 400, "INVALID_QUERY", "cursor must be a nonnegative sequence", nil)
			return
		}
	} else if err := s.pool.QueryRow(r.Context(), `SELECT COALESCE(max(sequence),0) FROM graph_changes WHERE tenant_id=$1`, tenant(r)).Scan(&cursor); err != nil {
		s.respond(w, r, 0, nil, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.fail(w, r, http.StatusInternalServerError, "STREAM_UNAVAILABLE", "Streaming is unavailable", nil)
		return
	}
	s.graphStreams.Inc()
	defer s.graphStreams.Dec()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	fmt.Fprintf(w, "retry: 2000\n\nevent: ready\nid: %d\ndata: {\"cursor\":%d}\n\n", cursor, cursor)
	flusher.Flush()
	ticker := time.NewTicker(time.Second)
	keepalive := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	defer keepalive.Stop()
	controller := http.NewResponseController(w)
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			_ = controller.SetWriteDeadline(time.Now().Add(30 * time.Second))
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		case <-ticker.C:
			var minimum int64
			_ = s.pool.QueryRow(r.Context(), `SELECT COALESCE(min(sequence),0) FROM graph_changes WHERE tenant_id=$1`, tenant(r)).Scan(&minimum)
			if cursor > 0 && minimum > 0 && cursor < minimum-1 {
				// The read below is exclusive. Resume before the first retained
				// event so resetting an expired cursor does not discard it.
				cursor = minimum - 1
				_ = controller.SetWriteDeadline(time.Now().Add(30 * time.Second))
				fmt.Fprintf(w, "event: reset\nid: %d\ndata: {\"reason\":\"cursor_expired\",\"cursor\":%d}\n\n", cursor, cursor)
				flusher.Flush()
			}
			rows, err := s.pool.Query(r.Context(), `SELECT sequence,event_type,entity_type,entity_id,affected_lenses,occurred_at
				FROM graph_changes WHERE tenant_id=$1 AND sequence>$2 AND (cardinality($3::text[])=0 OR event_type=ANY($3)) ORDER BY sequence LIMIT 100`, tenant(r), cursor, types)
			if err != nil {
				s.logger.Warn("graph stream query failed", "tenant_id", tenant(r), "error", err)
				continue
			}
			for rows.Next() {
				var sequence int64
				var eventType, entityType, entityID string
				var lenses []string
				var occurred time.Time
				if err := rows.Scan(&sequence, &eventType, &entityType, &entityID, &lenses, &occurred); err != nil {
					break
				}
				payload, _ := json.Marshal(map[string]any{"sequence": sequence, "event_type": eventType, "entity_type": entityType, "entity_id": entityID, "affected_lenses": lenses, "occurred_at": occurred})
				_ = controller.SetWriteDeadline(time.Now().Add(30 * time.Second))
				fmt.Fprintf(w, "event: invalidate\nid: %d\ndata: %s\n\n", sequence, payload)
				cursor = sequence
			}
			rows.Close()
			flusher.Flush()
		}
	}
}

func (s *Server) resourceGraph(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	res, err := s.core.GetResource(r.Context(), tenant(r), id)
	if err != nil {
		s.respond(w, r, 0, nil, err)
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT action_id,mode,operation FROM action_resources WHERE tenant_id=$1 AND resource_id=$2 ORDER BY action_id DESC LIMIT 500`, tenant(r), id)
	if err != nil {
		s.respond(w, r, 0, nil, err)
		return
	}
	defer rows.Close()
	var activity []map[string]any
	for rows.Next() {
		var a, m, o string
		_ = rows.Scan(&a, &m, &o)
		activity = append(activity, map[string]any{"action_id": a, "mode": m, "operation": o})
	}
	writeJSON(w, http.StatusOK, map[string]any{"resource": res, "activity": activity})
}
func (s *Server) liveGraph(w http.ResponseWriter, r *http.Request) {
	rows, err := s.pool.Query(r.Context(), `SELECT a.id,a.operation,a.status,ar.resource_id,ar.mode FROM actions a JOIN action_resources ar ON ar.tenant_id=a.tenant_id AND ar.action_id=a.id WHERE a.tenant_id=$1 AND a.status IN('PROPOSED','AWAITING_APPROVAL','RESERVED','ACTIVE') ORDER BY a.requested_at DESC LIMIT 1000`, tenant(r))
	if err != nil {
		s.respond(w, r, 0, nil, err)
		return
	}
	defer rows.Close()
	var edges []map[string]any
	for rows.Next() {
		var a, o, st, res, m string
		_ = rows.Scan(&a, &o, &st, &res, &m)
		edges = append(edges, map[string]any{"action_id": a, "operation": o, "status": st, "resource_id": res, "mode": m})
	}
	writeJSON(w, http.StatusOK, map[string]any{"edges": edges})
}

func (s *Server) createAPIKey(w http.ResponseWriter, r *http.Request) {
	var in protocol.APIKeyCreateInput
	if !s.decode(w, r, &in) {
		return
	}
	out, err := s.auth.Create(r.Context(), tenant(r), in)
	s.respond(w, r, http.StatusCreated, out, err)
}
func (s *Server) listAPIKeys(w http.ResponseWriter, r *http.Request) {
	out, err := s.auth.List(r.Context(), tenant(r))
	s.respond(w, r, http.StatusOK, protocol.List[protocol.APIKey]{Items: out}, err)
}
func (s *Server) revokeAPIKey(w http.ResponseWriter, r *http.Request) {
	err := s.auth.Revoke(r.Context(), tenant(r), pathID(r, "id"))
	s.respond(w, r, http.StatusOK, map[string]any{"revoked": true}, err)
}

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	var mode string
	var semanticEnabled bool
	var paused bool
	err := s.pool.QueryRow(r.Context(), `SELECT enforcement_mode,settings @> '{"semantic_enabled":true}'::jsonb,paused FROM tenants WHERE id=$1`, tenant(r)).Scan(&mode, &semanticEnabled, &paused)
	s.respond(w, r, http.StatusOK, map[string]any{"enforcement_mode": mode, "semantic_enabled": semanticEnabled, "paused": paused}, err)
}
func (s *Server) setMode(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Mode protocol.EnforcementMode `json:"mode"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if in.Mode != protocol.ModeShadow && in.Mode != protocol.ModeWarn && in.Mode != protocol.ModeEnforce {
		s.fail(w, r, http.StatusBadRequest, "INVALID_MODE", "Mode must be SHADOW, WARN, or ENFORCE", nil)
		return
	}
	err := s.auditTx(r, func(tx pgx.Tx) error {
		var before string
		if e := tx.QueryRow(r.Context(), `SELECT enforcement_mode FROM tenants WHERE id=$1 FOR UPDATE`, tenant(r)).Scan(&before); e != nil {
			return e
		}
		if _, e := tx.Exec(r.Context(), `UPDATE tenants SET enforcement_mode=$2,updated_at=clock_timestamp() WHERE id=$1`, tenant(r), in.Mode); e != nil {
			return e
		}
		_, e := tx.Exec(r.Context(), `INSERT INTO audit_events(tenant_id,by,type,target,before,after)VALUES($1,current_setting('lumni.audit_by'),'enforcement.set','{"operation":"*","rule":"*","workspace_default":true}',$2,$3)`, tenant(r), jsonBytes(map[string]any{"mode": before}), jsonBytes(map[string]any{"mode": in.Mode}))
		return e
	})
	s.respond(w, r, http.StatusOK, map[string]any{"enforcement_mode": in.Mode}, err)
}

func (s *Server) setSemantic(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if in.Enabled == nil {
		s.fail(w, r, http.StatusBadRequest, "INVALID_SEMANTIC_CONFIG", "enabled is required", nil)
		return
	}
	enabled := *in.Enabled
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.respond(w, r, 0, nil, err)
		return
	}
	defer tx.Rollback(r.Context())
	_, err = tx.Exec(r.Context(), `UPDATE tenants SET settings=jsonb_set(settings,'{semantic_enabled}',to_jsonb($2::boolean),true),updated_at=clock_timestamp() WHERE id=$1`, tenant(r), enabled)
	if err == nil && !enabled {
		_, err = tx.Exec(r.Context(), `UPDATE semantic_jobs SET status='FAILED',last_error='tenant opted out',claim_until=NULL,completed_at=clock_timestamp(),updated_at=clock_timestamp() WHERE tenant_id=$1 AND status IN ('PENDING','RUNNING')`, tenant(r))
	}
	if err == nil && enabled {
		_, err = tx.Exec(r.Context(), `UPDATE semantic_jobs SET status='PENDING',last_error='',next_attempt_at=clock_timestamp(),claim_until=NULL,completed_at=NULL,updated_at=clock_timestamp() WHERE tenant_id=$1 AND status='FAILED' AND last_error='tenant opted out'`, tenant(r))
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	s.respond(w, r, http.StatusOK, map[string]any{"semantic_enabled": enabled}, err)
}
func (s *Server) listRules(table string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		allowed := map[string]bool{"resource_rules": true, "conflict_rules": true, "policy_rules": true}
		if !allowed[table] {
			s.fail(w, r, 500, "INTERNAL", "Invalid table", nil)
			return
		}
		rows, err := s.pool.Query(r.Context(), fmt.Sprintf(`SELECT to_jsonb(t) FROM %s t WHERE tenant_id=$1 ORDER BY created_at DESC`, table), tenant(r))
		if err != nil {
			s.respond(w, r, 0, nil, err)
			return
		}
		defer rows.Close()
		out := []any{}
		for rows.Next() {
			var b []byte
			_ = rows.Scan(&b)
			var v any
			_ = json.Unmarshal(b, &v)
			out = append(out, v)
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}
func (s *Server) createResourceRule(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Source, Tool, Operation string
		Resources               json.RawMessage `json:"resources"`
		EffectKeyFields         json.RawMessage `json:"effect_key_fields"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if in.Source == "" || in.Tool == "" || in.Operation == "" || len(in.Resources) == 0 {
		s.fail(w, r, http.StatusBadRequest, "INVALID_RESOURCE_RULE", "source, tool, operation, and resources are required", nil)
		return
	}
	var id string
	err := s.pool.QueryRow(r.Context(), `INSERT INTO resource_rules(tenant_id,source,tool,operation,resources,effect_key_fields)VALUES($1,$2,$3,$4,$5,$6)ON CONFLICT(tenant_id,source,tool)DO UPDATE SET operation=EXCLUDED.operation,resources=EXCLUDED.resources,effect_key_fields=EXCLUDED.effect_key_fields,version=resource_rules.version+1,updated_at=clock_timestamp()RETURNING id::text`, tenant(r), in.Source, in.Tool, in.Operation, rawJSON(in.Resources, "[]"), rawJSON(in.EffectKeyFields, "[]")).Scan(&id)
	s.respond(w, r, http.StatusCreated, map[string]any{"id": id}, err)
}
func (s *Server) createConflictRule(w http.ResponseWriter, r *http.Request) {
	var in struct {
		LeftOperation  string `json:"left_operation"`
		RightOperation string `json:"right_operation"`
		ResourceType   string `json:"resource_type"`
		Outcome        string `json:"outcome"`
		ReasonCode     string `json:"reason_code"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if in.ResourceType == "" {
		in.ResourceType = "*"
	}
	if in.LeftOperation == "" || in.RightOperation == "" || in.Outcome != "WAIT" || in.ReasonCode == "" {
		s.fail(w, r, http.StatusBadRequest, "INVALID_CONFLICT_RULE", "left_operation, right_operation, WAIT outcome, and reason_code are required", nil)
		return
	}
	var id string
	err := s.pool.QueryRow(r.Context(), `INSERT INTO conflict_rules(tenant_id,left_operation,right_operation,resource_type,outcome,reason_code)VALUES($1,$2,$3,$4,$5,$6)ON CONFLICT(tenant_id,left_operation,right_operation,resource_type)DO UPDATE SET outcome=EXCLUDED.outcome,reason_code=EXCLUDED.reason_code,version=conflict_rules.version+1 RETURNING id::text`, tenant(r), in.LeftOperation, in.RightOperation, in.ResourceType, in.Outcome, in.ReasonCode).Scan(&id)
	s.respond(w, r, http.StatusCreated, map[string]any{"id": id}, err)
}
func (s *Server) createPolicyRule(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name             string   `json:"name"`
		Operation        string   `json:"operation"`
		ResourceType     string   `json:"resource_type"`
		MinimumRisk      *string  `json:"minimum_risk"`
		ArgumentPointer  *string  `json:"argument_pointer"`
		NumericThreshold *float64 `json:"numeric_threshold"`
		Outcome          string   `json:"outcome"`
		ReasonCode       string   `json:"reason_code"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if in.Operation == "" {
		in.Operation = "*"
	}
	if in.ResourceType == "" {
		in.ResourceType = "*"
	}
	if in.Name == "" || in.ReasonCode == "" || (in.Outcome != "WARN" && in.Outcome != "REQUIRE_APPROVAL" && in.Outcome != "REJECT") {
		s.fail(w, r, http.StatusBadRequest, "INVALID_POLICY_RULE", "name, typed outcome, and reason_code are required", nil)
		return
	}
	var id string
	err := s.auditTx(r, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `INSERT INTO policy_rules(tenant_id,name,operation,resource_type,minimum_risk,argument_pointer,numeric_threshold,outcome,reason_code)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)ON CONFLICT(tenant_id,name)DO UPDATE SET operation=EXCLUDED.operation,resource_type=EXCLUDED.resource_type,minimum_risk=EXCLUDED.minimum_risk,argument_pointer=EXCLUDED.argument_pointer,numeric_threshold=EXCLUDED.numeric_threshold,outcome=EXCLUDED.outcome,reason_code=EXCLUDED.reason_code,version=policy_rules.version+1,enabled=true,deleted_at=NULL RETURNING id::text`, tenant(r), in.Name, in.Operation, in.ResourceType, in.MinimumRisk, in.ArgumentPointer, in.NumericThreshold, in.Outcome, in.ReasonCode).Scan(&id)
	})
	s.respond(w, r, http.StatusCreated, map[string]any{"id": id}, err)
}

func (s *Server) replay(w http.ResponseWriter, r *http.Request) {
	actionID := chi.URLParam(r, "actionID")
	var snapshot []byte
	var kernelVersion, decision, reason string
	err := s.pool.QueryRow(r.Context(), `SELECT snapshot,kernel_version,decision,reason_code FROM decisions WHERE tenant_id=$1 AND action_id=$2 ORDER BY created_at DESC LIMIT 1`, tenant(r), actionID).Scan(&snapshot, &kernelVersion, &decision, &reason)
	if err != nil {
		s.respond(w, r, 0, nil, err)
		return
	}
	replayedDecision, replayedReason, replayErr := coordination.ReplaySnapshot(kernelVersion, snapshot)
	if errors.Is(replayErr, coordination.ErrUnsupportedKernelVersion) {
		s.fail(w, r, http.StatusUnprocessableEntity, "UNSUPPORTED_KERNEL_VERSION", "Recorded kernel version is unavailable", map[string]any{"kernel_version": kernelVersion})
		return
	}
	if replayErr != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, "INVALID_DECISION_SNAPSHOT", "Recorded decision snapshot cannot be replayed", map[string]any{"cause": replayErr.Error()})
		return
	}
	var input any
	_ = json.Unmarshal(snapshot, &input)
	writeJSON(w, http.StatusOK, map[string]any{"action_id": actionID, "kernel_version": kernelVersion, "recorded_decision": decision, "recorded_reason": reason, "replayed_decision": replayedDecision, "replayed_reason": replayedReason, "deterministic_match": string(replayedDecision) == decision && string(replayedReason) == reason, "snapshot": input})
}

func (s *Server) putRepository(w http.ResponseWriter, r *http.Request) {
	var in githubingest.RepositoryInput
	if !s.decode(w, r, &in) {
		return
	}
	out, err := s.github.PutRepository(r.Context(), tenant(r), in)
	s.respond(w, r, http.StatusCreated, out, err)
}
func (s *Server) listRepositories(w http.ResponseWriter, r *http.Request) {
	out, err := s.github.ListRepositories(r.Context(), tenant(r))
	s.respond(w, r, http.StatusOK, map[string]any{"items": out}, err)
}
func (s *Server) githubWebhook(w http.ResponseWriter, r *http.Request) {
	if s.githubSecret == "" {
		s.fail(w, r, http.StatusServiceUnavailable, "GITHUB_NOT_CONFIGURED", "GitHub webhook ingestion is disabled", nil)
		return
	}
	slug := r.URL.Query().Get("tenant")
	if slug == "" {
		s.fail(w, r, http.StatusBadRequest, "TENANT_REQUIRED", "tenant query parameter is required", nil)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.bodyLimit))
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "INVALID_PAYLOAD", "Unable to read payload", nil)
		return
	}
	if !githubingest.Verify(s.githubSecret, body, r.Header.Get("X-Hub-Signature-256")) {
		s.fail(w, r, http.StatusForbidden, "INVALID_SIGNATURE", "GitHub signature is invalid", nil)
		return
	}
	tenantID, err := s.github.TenantBySlug(r.Context(), slug)
	if err != nil {
		s.respond(w, r, 0, nil, err)
		return
	}
	created, err := s.github.Ingest(r.Context(), tenantID, r.Header.Get("X-GitHub-Delivery"), r.Header.Get("X-GitHub-Event"), body)
	s.respond(w, r, http.StatusAccepted, map[string]any{"created": created}, err)
}

func (s *Server) createWebhook(w http.ResponseWriter, r *http.Request) {
	var in struct {
		URL, Secret string
		EventTypes  []string `json:"event_types"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if in.URL == "" || in.Secret == "" || len(in.EventTypes) == 0 {
		s.fail(w, r, http.StatusBadRequest, "INVALID_WEBHOOK", "url, secret, and event_types are required", nil)
		return
	}
	ciphertext, err := s.secrets.Encrypt([]byte(in.Secret))
	if err != nil {
		s.respond(w, r, 0, nil, err)
		return
	}
	var id string
	err = s.pool.QueryRow(r.Context(), `INSERT INTO webhook_endpoints(tenant_id,url,secret_ciphertext,event_types)VALUES($1,$2,$3,$4)RETURNING id::text`, tenant(r), in.URL, ciphertext, in.EventTypes).Scan(&id)
	s.respond(w, r, http.StatusCreated, map[string]any{"id": id, "url": in.URL, "event_types": in.EventTypes}, err)
}
func (s *Server) listWebhooks(w http.ResponseWriter, r *http.Request) {
	rows, err := s.pool.Query(r.Context(), `SELECT id::text,url,event_types,enabled,created_at FROM webhook_endpoints WHERE tenant_id=$1 ORDER BY created_at DESC`, tenant(r))
	if err != nil {
		s.respond(w, r, 0, nil, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, url string
		var types []string
		var enabled bool
		var created time.Time
		if err := rows.Scan(&id, &url, &types, &enabled, &created); err != nil {
			s.respond(w, r, 0, nil, err)
			return
		}
		out = append(out, map[string]any{"id": id, "url": url, "event_types": types, "enabled": enabled, "created_at": created})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}
func (s *Server) deleteWebhook(w http.ResponseWriter, r *http.Request) {
	tag, err := s.pool.Exec(r.Context(), `DELETE FROM webhook_endpoints WHERE tenant_id=$1 AND id=$2`, tenant(r), pathID(r, "id"))
	if err == nil && tag.RowsAffected() == 0 {
		err = pgx.ErrNoRows
	}
	s.respond(w, r, http.StatusOK, map[string]any{"deleted": true}, err)
}

func (s *Server) decode(w http.ResponseWriter, r *http.Request, out any) bool {
	reader := http.MaxBytesReader(w, r.Body, s.bodyLimit)
	defer reader.Close()
	if err := json.NewDecoder(reader).Decode(out); err != nil {
		code := "MALFORMED_JSON"
		if errors.Is(err, io.EOF) {
			code = "EMPTY_BODY"
		}
		s.fail(w, r, http.StatusBadRequest, code, "Request body is invalid", map[string]any{"cause": err.Error()})
		return false
	}
	return true
}
func (s *Server) respond(w http.ResponseWriter, r *http.Request, status int, out any, err error) {
	if err == nil {
		if status == 0 {
			status = http.StatusOK
		}
		writeJSON(w, status, out)
		return
	}
	switch {
	case core.IsNotFound(err), errors.Is(err, coordination.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "NOT_FOUND", "Entity was not found", nil)
	case errors.Is(err, coordination.ErrInvalidLease):
		s.fail(w, r, http.StatusConflict, string(protocol.ReasonFencingTokenInvalid), "Lease is missing, invalid, or expired", nil)
	default:
		s.logger.Error("request failed", "request_id", requestID(r.Context()), "error", err)
		code := "REQUEST_FAILED"
		prefix := strings.SplitN(err.Error(), ":", 2)[0]
		if len(prefix) < 80 && strings.IndexFunc(prefix, func(r rune) bool { return !(r >= 'A' && r <= 'Z' || r == '_') }) < 0 {
			code = prefix
		}
		s.fail(w, r, http.StatusUnprocessableEntity, code, err.Error(), nil)
	}
}
func (s *Server) fail(w http.ResponseWriter, r *http.Request, status int, code, message string, details map[string]any) {
	writeJSON(w, status, protocol.ErrorResponse{Error: protocol.APIError{Code: code, Message: message, RequestID: requestID(r.Context()), Details: details}})
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func tenant(r *http.Request) string        { p, _ := auth.PrincipalFrom(r.Context()); return p.TenantID }
func requestID(ctx context.Context) string { id, _ := ctx.Value(requestIDKey{}).(string); return id }
func contains(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}
func rawJSON(value json.RawMessage, fallback string) []byte {
	if len(value) == 0 {
		return []byte(fallback)
	}
	return value
}

func queryTime(r *http.Request, name string, fallback time.Time) time.Time {
	value := r.URL.Query().Get(name)
	if value == "" {
		return fallback.UTC()
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return fallback.UTC()
	}
	return parsed.UTC()
}

func queryInt(r *http.Request, name string, fallback, minimum, maximum int) int {
	value, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil {
		return fallback
	}
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

func (s *Server) SetPayloadRetention(retention time.Duration) { s.durable.SetRetention(retention) }
