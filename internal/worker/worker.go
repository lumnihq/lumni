package worker

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lumnihq/lumni/internal/analytics"
	"github.com/lumnihq/lumni/internal/composio"
	"github.com/lumnihq/lumni/internal/coordination"
	"github.com/lumnihq/lumni/internal/durable"
	githubingest "github.com/lumnihq/lumni/internal/github"
	"github.com/lumnihq/lumni/internal/notifications"
	"github.com/lumnihq/lumni/internal/secrets"
	"github.com/lumnihq/lumni/internal/semantic"
)

type Worker struct {
	durable       *durable.Service
	pool          *pgxpool.Pool
	kernel        *coordination.Kernel
	client        *http.Client
	logger        *slog.Logger
	secrets       *secrets.Manager
	github        *githubingest.Backfiller
	analytics     *analytics.Exporter
	semantic      *semantic.Service
	notifications *notifications.Service
}

type Option func(*Worker)

func WithComposio(client *composio.Client) Option {
	return func(w *Worker) {
		if client != nil {
			w.durable.SetGitHubConnectorReader(composio.NewService(w.pool, client, w.kernel, w.durable, w.logger))
			w.notifications = notifications.New(w.pool, composio.NewService(w.pool, client, w.kernel, w.durable, w.logger)).WithCipher(w.secrets)
		}
	}
}

func WithAnalyticsExporter(exporter *analytics.Exporter) Option {
	return func(worker *Worker) { worker.analytics = exporter }
}

func WithSemanticService(service *semantic.Service) Option {
	return func(worker *Worker) { worker.semantic = service }
}

func New(pool *pgxpool.Pool, kernel *coordination.Kernel, timeout time.Duration, logger *slog.Logger, encryptionKey, githubToken, githubAPIURL string, options ...Option) *Worker {
	manager, err := secrets.New(encryptionKey)
	if err != nil {
		panic(err)
	}
	client := &http.Client{Timeout: timeout}
	worker := &Worker{pool: pool, kernel: kernel, client: client, logger: logger, secrets: manager, github: githubingest.NewBackfiller(pool, client, logger, githubToken, githubAPIURL)}
	worker.durable = durable.New(pool, kernel, manager)
	worker.notifications = notifications.New(pool, nil).WithCipher(manager)

	for _, option := range options {
		option(worker)
	}
	return worker
}

func (w *Worker) Run(ctx context.Context, interval time.Duration) error {
	if w.semantic != nil {
		go w.semantic.Run(ctx, interval)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := w.Once(ctx); err != nil {
				w.logger.Error("worker iteration failed", "error", err)
			}
		}
	}
}

func (w *Worker) Once(ctx context.Context) error {
	if _, err := w.pool.Exec(ctx, `DELETE FROM onboarding_exchange_rate WHERE bucket<clock_timestamp()-interval '10 minutes'`); err != nil {
		return err
	}
	if err := analytics.DetectBursts(ctx, w.pool); err != nil {
		w.logger.Error("incident detection failed", "error", err)
	}
	if err := w.notifications.Once(ctx); err != nil {
		w.logger.Error("notification delivery failed", "error", err)
	}
	if err := w.durable.Tick(ctx); err != nil {
		w.logger.Error("durable maintenance failed", "error", err)
	}
	if _, err := w.kernel.ExpireLeases(ctx); err != nil {
		return err
	}
	if _, err := w.pool.Exec(ctx, `UPDATE approvals SET status='EXPIRED',resolved_at=clock_timestamp() WHERE status='PENDING' AND expires_at<=clock_timestamp()`); err != nil {
		return err
	}
	if err := w.attributeWaste(ctx); err != nil {
		return err
	}
	if err := w.github.Once(ctx); err != nil {
		return err
	}
	if w.analytics != nil && w.analytics.Enabled() {
		if _, err := w.analytics.ExportConsole(ctx); err != nil {
			w.logger.Warn("console analytics export deferred", "error", err)
		}
		if _, err := w.analytics.Once(ctx); err != nil {
			w.logger.Warn("analytics export deferred", "error", err)
		}
	}
	if _, err := w.pool.Exec(ctx, `DELETE FROM browser_sessions WHERE revoked_at IS NOT NULL OR idle_expires_at<=clock_timestamp() OR absolute_expires_at<=clock_timestamp()`); err != nil {
		return err
	}
	if _, err := w.pool.Exec(ctx, `DELETE FROM graph_changes WHERE occurred_at<clock_timestamp()-interval '7 days'`); err != nil {
		return err
	}
	if _, err := w.pool.Exec(ctx, `DELETE FROM public_proof_rate WHERE bucket<now()-interval '15 minutes'`); err != nil {
		return err
	}
	return w.publish(ctx)
}

func (w *Worker) attributeWaste(ctx context.Context) error {
	if _, err := w.pool.Exec(ctx, `
		WITH latest_decision AS (
			SELECT DISTINCT ON (tenant_id, action_id)
				tenant_id, action_id, reason_code
			FROM decisions
			WHERE action_id IS NOT NULL
			ORDER BY tenant_id, action_id, created_at DESC
		)
		UPDATE costs AS c
		SET waste_reason = CASE d.reason_code
			WHEN 'EXACT_EFFECT_ALREADY_COMMITTED' THEN 'DUPLICATE'
			WHEN 'EXACT_DUPLICATE_ACTIVE' THEN 'DUPLICATE'
			WHEN 'STALE_OBSERVATION' THEN 'STALE'
			WHEN 'WRITE_WRITE_CONFLICT' THEN 'CONFLICT'
			WHEN 'CONFLICTING_ACTIVE_MUTATION' THEN 'CONFLICT'
			ELSE c.waste_reason
		END
		FROM latest_decision AS d
		WHERE c.tenant_id = d.tenant_id
			AND c.action_id = d.action_id
			AND c.waste_reason IS NULL`); err != nil {
		return err
	}
	_, err := w.pool.Exec(ctx, `UPDATE costs AS c SET waste_reason='SUPERSEDED'
		FROM work AS w WHERE c.tenant_id=w.tenant_id AND c.work_id=w.id
		AND w.status='SUPERSEDED' AND c.waste_reason IS NULL`)
	return err
}

type outboxItem struct {
	ID                   int64
	TenantID, Topic, Key string
	Payload              []byte
}

func (w *Worker) publish(ctx context.Context) error {
	tx, err := w.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT id,tenant_id::text,topic,key,payload FROM outbox WHERE published_at IS NULL AND next_attempt_at<=clock_timestamp() ORDER BY id LIMIT 50 FOR UPDATE SKIP LOCKED`)
	if err != nil {
		return err
	}
	var items []outboxItem
	for rows.Next() {
		var item outboxItem
		if err := rows.Scan(&item.ID, &item.TenantID, &item.Topic, &item.Key, &item.Payload); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	rows.Close()
	for _, item := range items {
		if err := w.deliver(ctx, tx, item); err != nil {
			_, _ = tx.Exec(ctx, `UPDATE outbox SET attempts=attempts+1,next_attempt_at=clock_timestamp()+make_interval(secs=>LEAST(300,power(2,LEAST(attempts+1,8))::int)) WHERE id=$1`, item.ID)
			w.logger.Warn("outbox delivery deferred", "outbox_id", item.ID, "error", err)
			continue
		}
		_, err = tx.Exec(ctx, `UPDATE outbox SET published_at=clock_timestamp(),attempts=attempts+1 WHERE id=$1`, item.ID)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (w *Worker) deliver(ctx context.Context, tx pgx.Tx, item outboxItem) error {
	rows, err := tx.Query(ctx, `SELECT id::text,url,secret_ciphertext FROM webhook_endpoints WHERE tenant_id=$1 AND enabled AND ($2=ANY(event_types) OR '*'=ANY(event_types))`, item.TenantID, item.Topic)
	if err != nil {
		return err
	}
	type endpoint struct {
		id, url    string
		ciphertext []byte
	}
	var endpoints []endpoint
	for rows.Next() {
		var value endpoint
		if err := rows.Scan(&value.id, &value.url, &value.ciphertext); err != nil {
			rows.Close()
			return err
		}
		endpoints = append(endpoints, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, endpoint := range endpoints {
		secret, err := w.secrets.Decrypt(endpoint.ciphertext)
		if err != nil {
			return err
		}
		deliveryID := uuid.NewString()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.url, bytes.NewReader(item.Payload))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Lumni-Delivery", deliveryID)
		req.Header.Set("X-Lumni-Event", item.Topic)
		req.Header.Set("X-Lumni-Signature-256", signature(secret, item.Payload))
		resp, err := w.client.Do(req)
		code := 0
		if resp != nil {
			code = resp.StatusCode
			_ = resp.Body.Close()
		}
		status := "FAILED"
		if err == nil && code >= 200 && code < 300 {
			status = "DELIVERED"
		}
		_, dbErr := tx.Exec(ctx, `INSERT INTO webhook_deliveries(id,tenant_id,endpoint_id,outbox_id,status,response_code,attempts,delivered_at)VALUES($1,$2,$3,$4,$5,$6,1,CASE WHEN $5='DELIVERED'THEN clock_timestamp()ELSE NULL END)ON CONFLICT(endpoint_id,outbox_id)DO UPDATE SET status=EXCLUDED.status,response_code=EXCLUDED.response_code,attempts=webhook_deliveries.attempts+1,delivered_at=EXCLUDED.delivered_at`, deliveryID, item.TenantID, endpoint.id, item.ID, status, code)
		if dbErr != nil {
			return dbErr
		}
		if err != nil {
			return err
		}
		if status != "DELIVERED" {
			return fmt.Errorf("webhook returned %d", code)
		}
	}
	return nil
}

func signature(secret, payload []byte) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func (s *Worker) SetPayloadRetention(retention time.Duration) { s.durable.SetRetention(retention) }
