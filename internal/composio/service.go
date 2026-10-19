package composio

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"lumni/internal/coordination"
	"lumni/internal/core"
	"lumni/internal/durable"
	"lumni/internal/resource"
	"lumni/pkg/protocol"
	lumni "lumni/sdk/go"
)

const Operation = "github.pull_request.set_state"
const versionSource = "github_pr_updated_at"

var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

type Service struct {
	pool    *pgxpool.Pool
	client  *Client
	kernel  *coordination.Kernel
	durable *durable.Service
	core    *core.Service
	logger  *slog.Logger
}

func NewService(pool *pgxpool.Pool, client *Client, kernel *coordination.Kernel, runtime *durable.Service, logger *slog.Logger) *Service {
	return &Service{pool: pool, client: client, kernel: kernel, durable: runtime, core: core.New(pool), logger: logger}
}

type Connection struct {
	ID                     string    `json:"id"`
	Repository             string    `json:"repository"`
	Toolkit                string    `json:"toolkit"`
	Status                 string    `json:"status"`
	Disabled               bool      `json:"disabled"`
	CreatedAt              time.Time `json:"created_at"`
	user, account, session string
}

func (s *Service) connection(ctx context.Context, tenant, id string) (Connection, error) {
	var c Connection
	err := s.pool.QueryRow(ctx, `SELECT id,repository,status,disabled,created_at,user_id,COALESCE(connected_account_id,''),COALESCE(session_id,''),toolkit FROM composio_connections WHERE tenant_id=$1 AND id=$2`, tenant, id).Scan(&c.ID, &c.Repository, &c.Status, &c.Disabled, &c.CreatedAt, &c.user, &c.account, &c.session, &c.Toolkit)
	return c, err
}

func (s *Service) List(ctx context.Context, tenant string) ([]Connection, error) {
	rows, err := s.pool.Query(ctx, `SELECT id,repository,status,disabled,created_at,toolkit FROM composio_connections WHERE tenant_id=$1 ORDER BY created_at DESC LIMIT 100`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Connection{}
	for rows.Next() {
		var c Connection
		if err = rows.Scan(&c.ID, &c.Repository, &c.Status, &c.Disabled, &c.CreatedAt, &c.Toolkit); err != nil {
			return nil, err
		}
		items = append(items, c)
	}
	return items, rows.Err()
}

type ConnectInput struct {
	Repository             string `json:"repository"`
	Toolkit                string `json:"toolkit,omitempty"`
	CloudProcessingConsent bool   `json:"cloud_processing_consent"`
}
type ConnectResult struct {
	Connection  Connection `json:"connection"`
	RedirectURL string     `json:"redirect_url"`
	ExpiresAt   string     `json:"expires_at"`
}

func (s *Service) Connect(ctx context.Context, tenant string, in ConnectInput) (ConnectResult, error) {
	in.Repository = strings.ToLower(strings.TrimSpace(in.Repository))
	if in.Toolkit == "" {
		in.Toolkit = "github"
	}
	if (in.Toolkit != "github" && in.Toolkit != "slack") || (in.Toolkit == "github" && !repositoryPattern.MatchString(in.Repository)) || !in.CloudProcessingConsent {
		return ConnectResult{}, errors.New("COMPOSIO_INVALID_CONNECTION: repository owner/name and cloud_processing_consent=true are required")
	}
	c := Connection{ID: "con_" + uuid.NewString(), Repository: in.Repository, Status: "INITIATED", Toolkit: in.Toolkit}
	c.user = "lumni:" + tenant + ":" + c.ID
	err := s.pool.QueryRow(ctx, `INSERT INTO composio_connections(tenant_id,id,user_id,repository,toolkit)VALUES($1,$2,$3,$4,$5)RETURNING created_at`, tenant, c.ID, c.user, c.Repository, c.Toolkit).Scan(&c.CreatedAt)
	if err != nil {
		return ConnectResult{}, err
	}
	cfg, _ := json.Marshal(map[string]string{"connector_id": c.ID})
	if c.Toolkit == "github" {
		err = s.durable.PutVerifier(ctx, tenant, protocol.Verifier{ID: verifierID(c.ID), Name: "Composio GitHub read-back", Kind: "GITHUB", Version: "1", Config: cfg, Enabled: true})
	}
	if err != nil {
		return ConnectResult{}, err
	}
	link, err := s.client.ConnectToolkit(ctx, c.user, c.Toolkit)
	if err != nil {
		_, _ = s.pool.Exec(context.WithoutCancel(ctx), `UPDATE composio_connections SET status='ERROR' WHERE tenant_id=$1 AND id=$2`, tenant, c.ID)
		return ConnectResult{}, err
	}
	_, err = s.pool.Exec(ctx, `UPDATE composio_connections SET connected_account_id=$3,updated_at=now() WHERE tenant_id=$1 AND id=$2`, tenant, c.ID, link.ConnectedAccountID)
	return ConnectResult{Connection: c, RedirectURL: link.RedirectURL, ExpiresAt: link.ExpiresAt}, err
}
func verifierID(id string) string { return "composio_github_" + id }

func (s *Service) Status(ctx context.Context, tenant, id string) (Connection, error) {
	c, err := s.connection(ctx, tenant, id)
	if err != nil || c.Disabled || c.account == "" {
		return c, err
	}
	account, err := s.client.StatusToolkit(ctx, c.account, c.user, c.Toolkit)
	if err != nil {
		return c, err
	}
	c.Status = account.Status
	if c.Status == "ACTIVE" && c.session == "" {
		session, err := s.client.SessionToolkit(ctx, c.user, c.account, c.Toolkit)
		if err != nil {
			return c, err
		}
		_, err = s.pool.Exec(ctx, `UPDATE composio_connections SET session_id=COALESCE(session_id,$3) WHERE tenant_id=$1 AND id=$2 AND NOT disabled`, tenant, id, session)
		if err != nil {
			return c, err
		}
	}
	_, err = s.pool.Exec(ctx, `UPDATE composio_connections SET status=$3,updated_at=now() WHERE tenant_id=$1 AND id=$2 AND NOT disabled`, tenant, id, c.Status)
	if err != nil {
		return c, err
	}
	return s.connection(ctx, tenant, id)
}

func (s *Service) Disconnect(ctx context.Context, tenant, id string) error {
	c, err := s.connection(ctx, tenant, id)
	if err != nil {
		return err
	}
	// Local access stops even if the cloud is unavailable. Retrying DELETE only
	// retries account deletion; it can never retry an agent's mutation.
	_, err = s.pool.Exec(ctx, `UPDATE composio_connections SET disabled=true,status='DISCONNECTED',updated_at=now() WHERE tenant_id=$1 AND id=$2`, tenant, id)
	if err != nil || c.account == "" {
		return err
	}
	return s.client.Disconnect(ctx, c.account)
}

func (s *Service) active(ctx context.Context, tenant, id string) (Connection, error) {
	c, err := s.connection(ctx, tenant, id)
	if err != nil {
		return c, err
	}
	if c.Disabled || c.Status != "ACTIVE" || c.session == "" {
		return c, errors.New("COMPOSIO_NOT_CONNECTED: refresh connection status or reconnect")
	}
	return c, nil
}

type PullRequest struct {
	ID              int64  `json:"id"`
	Number          int    `json:"number"`
	State           string `json:"state"`
	UpdatedAt       string `json:"updated_at"`
	HTMLURL         string `json:"html_url"`
	Merged          bool   `json:"merged"`
	ObservedVersion string `json:"observed_version"`
	Head            struct {
		SHA string `json:"sha"`
	} `json:"head"`
}

func pullRequest(data json.RawMessage, number int) (PullRequest, error) {
	var pr PullRequest
	if json.Unmarshal(data, &pr) != nil || pr.Number != number || pr.ID <= 0 || (pr.State != "open" && pr.State != "closed") || pr.UpdatedAt == "" {
		return pr, errors.New("COMPOSIO_UNAVAILABLE: invalid GitHub pull request response")
	}
	if _, err := time.Parse(time.RFC3339, pr.UpdatedAt); err != nil {
		return pr, errors.New("COMPOSIO_UNAVAILABLE: invalid GitHub update timestamp")
	}
	return pr, nil
}
func ref(repository string, number int) protocol.ResourceRef {
	return resource.Canonicalize(protocol.ResourceRef{Namespace: "github", Type: "pull_request", ExternalID: repository + "#" + strconv.Itoa(number)})
}
func arguments(repository string, number int) map[string]any {
	parts := strings.Split(repository, "/")
	return map[string]any{"owner": parts[0], "repo": parts[1], "pull_number": number}
}

func (s *Service) call(ctx context.Context, tenant string, c Connection, kind, action, tool string, args any) (json.RawMessage, error) {
	start := time.Now()
	output, err := s.client.Execute(ctx, c.session, c.account, tool, args)
	status := "SUCCEEDED"
	if err != nil {
		status = "UNKNOWN"
	}
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if _, e := s.pool.Exec(recordCtx, `INSERT INTO composio_calls(tenant_id,connection_id,action_id,kind,status,duration_ms)VALUES($1,$2,NULLIF($3,''),$4,$5,$6)`, tenant, c.ID, action, kind, status, time.Since(start).Milliseconds()); e != nil {
		s.logger.Warn("Composio call metrics unavailable", "connection_id", c.ID)
	}
	_, _ = s.pool.Exec(recordCtx, `INSERT INTO app_call_timings(tenant_id,app,duration_ms,error,timeout)VALUES($1,$2,$3,$4,$5)`, tenant, c.Toolkit, time.Since(start).Milliseconds(), err != nil, ctx.Err() == context.DeadlineExceeded || time.Since(start) >= s.client.options.Timeout)
	return output, err
}

// ReadPullRequest is also the durable verifier transport. Results are fresh
// provider reads, never cached execution responses or webhook assertions.
func (s *Service) ReadPullRequest(ctx context.Context, tenant, id, repository string, number int) (json.RawMessage, error) {
	return s.read(ctx, tenant, id, repository, number, "VERIFY")
}
func (s *Service) read(ctx context.Context, tenant, id, repository string, number int, kind string) (json.RawMessage, error) {
	c, err := s.active(ctx, tenant, id)
	if err != nil {
		return nil, err
	}
	if c.Toolkit != "github" || number <= 0 || repository != c.Repository {
		return nil, errors.New("COMPOSIO_RESOURCE_DENIED: repository is outside the connection allowlist")
	}
	observedAt := time.Now().UTC()
	data, err := s.call(ctx, tenant, c, kind, "", ReadTool, arguments(repository, number))
	if err != nil {
		return nil, err
	}
	pr, err := pullRequest(data, number)
	if err != nil {
		return nil, err
	}
	r, err := s.core.PutResource(ctx, tenant, protocol.Resource{ResourceRef: ref(repository, number)})
	if err != nil {
		return nil, err
	}
	// Start-time ordering prevents a slower earlier read from replacing a later
	// observation, including when GitHub's second-resolution timestamps tie.
	version := pr.UpdatedAt + ":" + pr.State + ":" + pr.Head.SHA
	if err = s.core.UpdateVersion(ctx, tenant, r.ID, protocol.ResourceVersionInput{Version: version, VersionSource: versionSource, ObservedAt: &observedAt, Confidence: 1}); err != nil {
		return nil, err
	}
	pr.ObservedVersion = version
	// Return only the fields this pilot needs; no credential or session metadata.
	return json.Marshal(pr)
}
func (s *Service) Read(ctx context.Context, tenant, id string, number int) (json.RawMessage, error) {
	c, err := s.connection(ctx, tenant, id)
	if err != nil {
		return nil, err
	}
	return s.read(ctx, tenant, id, c.Repository, number, "READ")
}

type ExecuteInput struct {
	ActionID        string `json:"action_id"`
	WorkID          string `json:"work_id"`
	ActorID         string `json:"actor_id"`
	Number          int    `json:"number"`
	State           string `json:"state"`
	ObservedVersion string `json:"observed_version"`
}
type Execution struct {
	Status    string            `json:"status"`
	ActionID  string            `json:"action_id"`
	OutcomeID string            `json:"outcome_id,omitempty"`
	Decision  protocol.Decision `json:"decision"`
}

func (s *Service) Execute(ctx context.Context, tenant, id string, in ExecuteInput) (Execution, error) {
	if in.ActionID == "" || in.WorkID == "" || in.ActorID == "" || in.Number <= 0 || (in.State != "open" && in.State != "closed") || in.ObservedVersion == "" {
		return Execution{}, errors.New("COMPOSIO_INVALID_ACTION: action_id, work_id, actor_id, number, open/closed state and observed_version are required")
	}
	c, err := s.active(ctx, tenant, id)
	if err != nil {
		return Execution{}, err
	}
	// Require a prior read. Admission compares its version against current state.
	if c.Toolkit != "github" {
		return Execution{}, errors.New("COMPOSIO_RESOURCE_DENIED: GitHub connection required")
	}
	if _, err = s.core.GetResource(ctx, tenant, ref(c.Repository, in.Number).ID); err != nil {
		return Execution{}, err
	}
	// Build semantics here. Clients cannot override resources, risk, operation,
	// effect identity, verifier, account, session, tool slug, or arbitrary args.
	args, _ := json.Marshal(map[string]any{"repository": c.Repository, "number": in.Number, "state": in.State, "observed_version": in.ObservedVersion})
	expected, _ := json.Marshal(map[string]any{"repository": c.Repository, "number": in.Number, "state": in.State})
	action := protocol.ActionCheckInput{ActionID: in.ActionID, WorkID: in.WorkID, ActorID: in.ActorID, Operation: Operation, Source: "composio", Tool: UpdateTool, Arguments: args, Risk: protocol.RiskHigh, Resources: []protocol.ResourceIntent{{ResourceRef: ref(c.Repository, in.Number), Mode: protocol.AccessWrite, ObservedVersion: &in.ObservedVersion, VersionSource: versionSource, Confidence: 1, EvidenceType: "EXPLICIT"}}, Verification: &protocol.OutcomeSpec{VerifierID: verifierID(id), Expected: expected}}
	d, err := s.kernel.Check(ctx, tenant, action)
	result := Execution{Status: "BLOCKED", ActionID: d.ActionID, Decision: d}
	if err != nil {
		return result, err
	}
	if d.Decision == protocol.DecisionDeduplicate && d.ExistingActionID != nil {
		return s.execution(ctx, tenant, *d.ExistingActionID, d)
	}
	if lumni.RequireExecution(d) != nil {
		return result, nil
	}
	token := ""
	if d.LeaseToken != nil {
		token = *d.LeaseToken
	}
	started, err := s.kernel.StartOnce(ctx, tenant, d.ActionID, token)
	if err != nil {
		return result, err
	}
	if !started {
		return s.execution(ctx, tenant, d.ActionID, d)
	}
	// Recheck local revocation after claiming execution. Any uncertainty after
	// StartOnce is recorded UNKNOWN and can only be resolved by verification.
	c, err = s.active(ctx, tenant, id)
	var output json.RawMessage
	if err == nil {
		a := arguments(c.Repository, in.Number)
		a["state"] = in.State
		output, err = s.call(ctx, tenant, c, "EXECUTE", d.ActionID, UpdateTool, a)
		if err == nil {
			_, err = pullRequest(output, in.Number)
		}
	}
	status := "SUCCEEDED"
	if err != nil {
		status = "UNKNOWN"
		output = nil
	}
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if e := s.kernel.RecordResult(recordCtx, tenant, d.ActionID, protocol.ActionResultInput{Status: status, Output: output, LeaseToken: d.LeaseToken}); e != nil {
		return result, e
	}
	return s.execution(recordCtx, tenant, d.ActionID, d)
}
func (s *Service) execution(ctx context.Context, tenant, action string, d protocol.Decision) (Execution, error) {
	out := Execution{Status: "PENDING", ActionID: action, OutcomeID: "out_" + action, Decision: d}
	var status string
	err := s.pool.QueryRow(ctx, `SELECT status FROM outcomes WHERE tenant_id=$1 AND action_id=$2`, tenant, action).Scan(&status)
	if err == nil && status != "PENDING" && status != "UNKNOWN" {
		out.Status = status
	}
	return out, err
}

func (s *Service) Metrics(ctx context.Context, tenant, id string) ([]map[string]any, error) {
	if _, err := s.connection(ctx, tenant, id); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT kind,status,count(*),avg(duration_ms)::float8,percentile_cont(0.95)WITHIN GROUP(ORDER BY duration_ms)::float8 FROM composio_calls WHERE tenant_id=$1 AND connection_id=$2 GROUP BY kind,status ORDER BY kind,status`, tenant, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var kind, status string
		var count int64
		var avg, p95 float64
		if err = rows.Scan(&kind, &status, &count, &avg, &p95); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"kind": kind, "status": status, "calls": count, "mean_ms": avg, "p95_ms": p95})
	}
	return out, rows.Err()
}

type Webhook struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Metadata  struct {
		AccountID   string `json:"connected_account_id"`
		UserID      string `json:"user_id"`
		TriggerSlug string `json:"trigger_slug"`
	} `json:"metadata"`
	Data json.RawMessage `json:"data"`
}

func (s *Service) Ingest(ctx context.Context, body []byte, delivery string) (bool, error) {
	var event Webhook
	if json.Unmarshal(body, &event) != nil || event.ID != delivery || event.Timestamp.IsZero() || event.Type != "composio.trigger.message" || !strings.HasPrefix(event.Metadata.TriggerSlug, "GITHUB_") || len(event.Data) == 0 {
		return false, errors.New("COMPOSIO_INVALID_EVENT: expected a V3 GitHub trigger message")
	}
	var tenant, id string
	err := s.pool.QueryRow(ctx, `SELECT tenant_id::text,id FROM composio_connections WHERE connected_account_id=$1 AND user_id=$2 AND NOT disabled`, event.Metadata.AccountID, event.Metadata.UserID).Scan(&tenant, &id)
	if err != nil {
		return false, err
	}
	// Store observational events, not authoritative outcome evidence. The worker
	// performs fresh read-back; event bodies cannot release effect holds.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `INSERT INTO events(tenant_id,id,type,source,occurred_at,payload)VALUES($1,$2,$3,'composio',$4,$5)ON CONFLICT(tenant_id,id)DO NOTHING`, tenant, "composio:"+delivery, event.Metadata.TriggerSlug, event.Timestamp, body)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() > 0 {
		_, err = tx.Exec(ctx, `INSERT INTO outbox(tenant_id,topic,key,payload)VALUES($1,'COMPOSIO_EVENT',$2,jsonb_build_object('event_id',$3::text,'connection_id',$2::text))`, tenant, id, "composio:"+delivery)
		if err != nil {
			return false, err
		}
	}
	return tag.RowsAffected() > 0, tx.Commit(ctx)
}

// IsNotFound intentionally conceals whether a foreign tenant owns an ID.
func IsNotFound(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
func (s *Service) VerifyWebhook(headers map[string][]string, body []byte) bool {
	return VerifyWebhook(s.client.options.WebhookSecret, headers, body, time.Now())
}
