package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lumnihq/lumni/pkg/protocol"
)

type Principal struct {
	TenantID   string
	KeyID      string
	Scopes     []string
	SessionID  string
	ViaSession bool
	CSRFHash   []byte
}

type contextKey struct{}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, contextKey{}, p)
}

func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(contextKey{}).(Principal)
	return p, ok
}

type Service struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

func Hash(secret string) []byte {
	sum := sha256.Sum256([]byte(secret))
	return sum[:]
}

func Prefix(secret string) string {
	if len(secret) <= 12 {
		return secret
	}
	return secret[:12]
}

func Generate() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "lm_" + base64.RawURLEncoding.EncodeToString(raw), nil
}

func (s *Service) Bootstrap(ctx context.Context, name, slug, secret string, mode protocol.EnforcementMode) (string, error) {
	if secret == "" {
		return "", errors.New("bootstrap key is empty")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var tenantID string
	err = tx.QueryRow(ctx, `
		INSERT INTO tenants (name, slug, enforcement_mode)
		VALUES ($1, $2, $3)
		ON CONFLICT (slug) DO UPDATE SET name = EXCLUDED.name
		RETURNING id::text`, name, slug, mode).Scan(&tenantID)
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO api_keys (tenant_id, name, prefix, secret_hash, scopes)
		VALUES ($1, 'bootstrap-admin', $2, $3, ARRAY['admin','ingest','admission','query'])
		ON CONFLICT (secret_hash) DO NOTHING`, tenantID, Prefix(secret), Hash(secret))
	if err != nil {
		return "", err
	}
	return tenantID, tx.Commit(ctx)
}

func (s *Service) Authenticate(ctx context.Context, secret string) (Principal, error) {
	var p Principal
	var expiresAt, revokedAt *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT tenant_id::text, id::text, scopes, expires_at, revoked_at
		FROM api_keys WHERE secret_hash = $1`, Hash(secret)).Scan(&p.TenantID, &p.KeyID, &p.Scopes, &expiresAt, &revokedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Principal{}, errors.New("invalid API key")
		}
		return Principal{}, err
	}
	now := time.Now()
	if revokedAt != nil || (expiresAt != nil && !expiresAt.After(now)) {
		return Principal{}, errors.New("expired or revoked API key")
	}
	_, _ = s.pool.Exec(ctx, `UPDATE api_keys SET last_used_at = clock_timestamp() WHERE id = $1`, p.KeyID)
	return p, nil
}

type BrowserSession struct {
	Token     string
	CSRFToken string
	TenantID  string
	Scopes    []string
	ExpiresAt time.Time
}

func (s *Service) CreateBrowserSession(ctx context.Context, principal Principal, idleTTL, absoluteTTL time.Duration) (BrowserSession, error) {
	if principal.TenantID == "" || principal.KeyID == "" {
		return BrowserSession{}, errors.New("authenticated API key is required")
	}
	if idleTTL <= 0 {
		idleTTL = 8 * time.Hour
	}
	if absoluteTTL <= 0 {
		absoluteTTL = 24 * time.Hour
	}
	token, err := Generate()
	if err != nil {
		return BrowserSession{}, err
	}
	csrf, err := Generate()
	if err != nil {
		return BrowserSession{}, err
	}
	now := time.Now().UTC()
	idleExpires := now.Add(idleTTL)
	absoluteExpires := now.Add(absoluteTTL)
	_, err = s.pool.Exec(ctx, `INSERT INTO browser_sessions
		(tenant_id,api_key_id,token_hash,csrf_hash,scopes,idle_expires_at,absolute_expires_at)
		VALUES($1,$2,$3,$4,$5,$6,$7)`, principal.TenantID, principal.KeyID, Hash(token), Hash(csrf), principal.Scopes, idleExpires, absoluteExpires)
	if err != nil {
		return BrowserSession{}, err
	}
	return BrowserSession{Token: token, CSRFToken: csrf, TenantID: principal.TenantID, Scopes: principal.Scopes, ExpiresAt: absoluteExpires}, nil
}

func (s *Service) AuthenticateBrowserSession(ctx context.Context, token string, idleTTL time.Duration) (Principal, error) {
	if token == "" {
		return Principal{}, errors.New("browser session is missing")
	}
	if idleTTL <= 0 {
		idleTTL = 8 * time.Hour
	}
	var principal Principal
	var csrfHash []byte
	err := s.pool.QueryRow(ctx, `SELECT bs.tenant_id::text,bs.api_key_id::text,bs.id::text,bs.scopes,bs.csrf_hash
		FROM browser_sessions bs JOIN api_keys k ON k.id=bs.api_key_id AND k.tenant_id=bs.tenant_id
		WHERE bs.token_hash=$1 AND bs.revoked_at IS NULL AND bs.idle_expires_at>clock_timestamp()
		AND bs.absolute_expires_at>clock_timestamp() AND k.revoked_at IS NULL
		AND (k.expires_at IS NULL OR k.expires_at>clock_timestamp())`, Hash(token)).Scan(&principal.TenantID, &principal.KeyID, &principal.SessionID, &principal.Scopes, &csrfHash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Principal{}, errors.New("expired or invalid browser session")
		}
		return Principal{}, err
	}
	principal.ViaSession = true
	principal.CSRFHash = csrfHash
	_, _ = s.pool.Exec(ctx, `UPDATE browser_sessions SET last_seen_at=clock_timestamp(),
		idle_expires_at=LEAST(absolute_expires_at,clock_timestamp()+$2::interval)
		WHERE id=$1`, principal.SessionID, durationInterval(idleTTL))
	return principal, nil
}

func (s *Service) ValidateCSRF(principal Principal, token string) bool {
	if !principal.ViaSession || token == "" || len(principal.CSRFHash) == 0 {
		return false
	}
	hash := Hash(token)
	return subtle.ConstantTimeCompare(hash, principal.CSRFHash) == 1
}

func (s *Service) RevokeBrowserSession(ctx context.Context, principal Principal) error {
	if !principal.ViaSession || principal.SessionID == "" {
		return errors.New("browser session required")
	}
	_, err := s.pool.Exec(ctx, `UPDATE browser_sessions SET revoked_at=clock_timestamp() WHERE tenant_id=$1 AND id=$2`, principal.TenantID, principal.SessionID)
	return err
}

func (s *Service) ExpireBrowserSessions(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM browser_sessions WHERE revoked_at IS NOT NULL OR idle_expires_at<=clock_timestamp() OR absolute_expires_at<=clock_timestamp()`)
	return tag.RowsAffected(), err
}

func durationInterval(value time.Duration) string {
	return fmt.Sprintf("%f seconds", value.Seconds())
}

func (s *Service) Create(ctx context.Context, tenantID string, in protocol.APIKeyCreateInput) (protocol.APIKey, error) {
	secret, err := Generate()
	if err != nil {
		return protocol.APIKey{}, err
	}
	id := uuid.NewString()
	var out protocol.APIKey
	err = s.pool.QueryRow(ctx, `
		INSERT INTO api_keys (id, tenant_id, name, prefix, secret_hash, scopes, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id::text, name, prefix, scopes, created_at, expires_at`,
		id, tenantID, in.Name, Prefix(secret), Hash(secret), in.Scopes, in.ExpiresAt,
	).Scan(&out.ID, &out.Name, &out.Prefix, &out.Scopes, &out.CreatedAt, &out.ExpiresAt)
	out.Secret = secret
	return out, err
}

func (s *Service) List(ctx context.Context, tenantID string) ([]protocol.APIKey, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, name, prefix, scopes, created_at, expires_at, revoked_at
		FROM api_keys WHERE tenant_id = $1 ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []protocol.APIKey
	for rows.Next() {
		var key protocol.APIKey
		if err := rows.Scan(&key.ID, &key.Name, &key.Prefix, &key.Scopes, &key.CreatedAt, &key.ExpiresAt, &key.RevokedAt); err != nil {
			return nil, err
		}
		out = append(out, key)
	}
	return out, rows.Err()
}

func (s *Service) Revoke(ctx context.Context, tenantID, id string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE api_keys SET revoked_at = clock_timestamp() WHERE tenant_id = $1 AND id = $2 AND revoked_at IS NULL`, tenantID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		secret, ok := strings.CutPrefix(header, "Bearer ")
		if !ok || strings.TrimSpace(secret) == "" {
			http.Error(w, "missing bearer token", http.StatusUnauthorized)
			return
		}
		p, err := s.Authenticate(r.Context(), secret)
		if err != nil {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		if requested := r.Header.Get("X-Lumni-Tenant"); requested != "" && requested != p.TenantID {
			http.Error(w, "tenant mismatch", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
	})
}

func RequireScope(scope string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PrincipalFrom(r.Context())
		if !ok || (!slices.Contains(p.Scopes, scope) && !slices.Contains(p.Scopes, "admin")) {
			http.Error(w, fmt.Sprintf("scope %s required", scope), http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
