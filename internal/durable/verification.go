package durable

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/lumnihq/lumni/internal/resource"
	"github.com/lumnihq/lumni/pkg/protocol"
)

func (s *Service) PutVerifier(ctx context.Context, tenant string, in protocol.Verifier) error {
	return s.PutVerifierAs(ctx, tenant, in, "system")
}
func (s *Service) PutVerifierAs(ctx context.Context, tenant string, in protocol.Verifier, by string) error {
	if in.ID == "" || in.Name == "" || in.Version == "" {
		return errors.New("verifier id, name and version are required")
	}
	switch in.Kind {
	case "HTTP", "POSTGRES", "STRIPE", "GITHUB", "SHOPIFY", "CUSTOM", "EVENT", "COMPOSITE":
	default:
		return errors.New("unsupported verifier kind")
	}
	var cfg map[string]any
	if err := json.Unmarshal(in.Config, &cfg); err != nil {
		return err
	}
	if in.Kind == "HTTP" || in.Kind == "STRIPE" || in.Kind == "GITHUB" || in.Kind == "SHOPIFY" {
		for _, field := range []string{"url", "base_url"} {
			if value, ok := cfg[field].(string); ok {
				u, e := url.Parse(value)
				if e != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
					return errors.New("invalid verifier endpoint")
				}
			}
		}
	}
	var cipher []byte
	var err error
	if in.Secret != "" {
		cipher, err = s.cipher.Encrypt([]byte(in.Secret))
		if err != nil {
			return err
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT set_config('lumni.audit_by',$1,true)`, by); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO verifiers(tenant_id,id,name,kind,version,config,secret_ciphertext,enabled)VALUES($1,$2,$3,$4,$5,$6,$7,$8)
	 ON CONFLICT(tenant_id,id)DO UPDATE SET name=EXCLUDED.name,kind=EXCLUDED.kind,version=EXCLUDED.version,config=EXCLUDED.config,secret_ciphertext=COALESCE(EXCLUDED.secret_ciphertext,verifiers.secret_ciphertext),enabled=EXCLUDED.enabled
	 WHERE (NOT EXISTS(SELECT 1 FROM outcomes o WHERE o.tenant_id=verifiers.tenant_id AND o.verifier_id=verifiers.id)
	 AND NOT EXISTS(SELECT 1 FROM verifiers composite WHERE composite.tenant_id=verifiers.tenant_id AND composite.kind='COMPOSITE' AND composite.config->'verifiers' @> jsonb_build_array(verifiers.id)))
	 OR (verifiers.kind=EXCLUDED.kind AND verifiers.version=EXCLUDED.version AND verifiers.config=EXCLUDED.config AND EXCLUDED.secret_ciphertext IS NULL)`, tenant, in.ID, in.Name, in.Kind, in.Version, in.Config, cipher, in.Enabled)
	if err == nil && tag.RowsAffected() == 0 {
		return errors.New("verifier configuration is referenced by immutable outcomes; register a new verifier id")
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) Evidence(ctx context.Context, tenant, outcome string, in protocol.VerificationEvidence) error {
	return s.evidence(ctx, tenant, outcome, in, "")
}
func (s *Service) evidence(ctx context.Context, tenant, outcome string, in protocol.VerificationEvidence, claim string) error {
	if in.ID == "" {
		return errors.New("evidence id is required")
	}
	switch in.Status {
	case "VERIFIED", "MISMATCH", "PARTIAL", "PENDING", "UNKNOWN", "FAILED":
	default:
		return errors.New("invalid outcome status")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var action, work, current string
	var verifier *string
	if err = tx.QueryRow(ctx, `SELECT action_id,work_id,status,verifier_id FROM outcomes WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenant, outcome).Scan(&action, &work, &current, &verifier); err != nil {
		return err
	}
	if verifier == nil {
		return errors.New("an authoritative verifier is required to record evidence")
	}
	if claim != "" {
		var valid bool
		if err = tx.QueryRow(ctx, `SELECT claim_token=$3 AND claim_until>now() FROM outcomes WHERE tenant_id=$1 AND id=$2`, tenant, outcome, claim).Scan(&valid); err != nil {
			return err
		}
		if !valid {
			return errors.New("VERIFIER_CLAIM_EXPIRED")
		}
	}
	b, _ := json.Marshal(in)
	var old []byte
	err = tx.QueryRow(ctx, `SELECT evidence FROM verification_attempts WHERE tenant_id=$1 AND outcome_id=$2 AND id=$3`, tenant, outcome, in.ID).Scan(&old)
	if err == nil {
		var prev protocol.VerificationEvidence
		if err = json.Unmarshal(old, &prev); err != nil {
			return err
		}
		prior, _ := json.Marshal(prev)
		if !bytes.Equal(prior, b) {
			return errors.New("EVIDENCE_IDENTITY_MISMATCH")
		}
		return tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if current == "VERIFIED" && in.Status != "VERIFIED" {
		return errors.New("verified history cannot be downgraded")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO verification_attempts(tenant_id,outcome_id,id,evidence)VALUES($1,$2,$3,$4)`, tenant, outcome, in.ID, b); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE outcomes SET status=$3,observed=$4,effect_absent=$5,prior_attempt_terminated=$6,reason=$7,updated_at=now(),claim_until=NULL,claim_token=NULL
	 WHERE tenant_id=$1 AND id=$2`, tenant, outcome, in.Status, raw(in.Observed), in.EffectAbsent, in.PriorAttemptTerminated, in.Reason); err != nil {
		return err
	}
	if in.Status == "VERIFIED" {
		if _, err = tx.Exec(ctx, `INSERT INTO effects(tenant_id,id,action_id,operation,effect_key,status,external_id,occurred_at) SELECT tenant_id,'eff_verified_'||id,id,operation,effect_key,'VERIFIED',$3,now() FROM actions WHERE tenant_id=$1 AND id=$2 AND effect_key IS NOT NULL ON CONFLICT(tenant_id,effect_key)DO NOTHING`, tenant, action, in.ExternalResultID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO effect_occurrences(tenant_id,id,action_id,effect_key,status,external_id,evidence_type) SELECT tenant_id,'occ_'||id,id,effect_key,'COMMITTED',$3,'VERIFIER' FROM actions WHERE tenant_id=$1 AND id=$2 AND effect_key IS NOT NULL ON CONFLICT(tenant_id,action_id)DO NOTHING`, tenant, action, in.ExternalResultID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE effects SET status='VERIFIED',external_id=COALESCE($3,external_id),updated_at=now() WHERE tenant_id=$1 AND action_id=$2`, tenant, action, in.ExternalResultID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE effect_occurrences SET status='COMMITTED',external_id=COALESCE($3,external_id),evidence_type='VERIFIER' WHERE tenant_id=$1 AND action_id=$2`, tenant, action, in.ExternalResultID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE idempotency_records SET status='COMPLETED',updated_at=now() WHERE tenant_id=$1 AND action_id=$2`, tenant, action); err != nil {
			return err
		}
	}
	if in.Status == "VERIFIED" || (in.EffectAbsent && in.PriorAttemptTerminated) {
		if _, err = tx.Exec(ctx, `DELETE FROM effect_holds WHERE tenant_id=$1 AND action_id=$2`, tenant, action); err != nil {
			return err
		}
	}
	if in.Status == "MISMATCH" || in.Status == "PARTIAL" || in.Status == "FAILED" {
		if _, err = tx.Exec(ctx, `INSERT INTO incidents(tenant_id,id,kind,work_id,action_id,title,evidence)VALUES($1,$2,$3,$4,$5,$3,$6)
	 ON CONFLICT(tenant_id,id)DO UPDATE SET evidence=EXCLUDED.evidence,updated_at=now()`, tenant, "outcome_"+outcome, "OUTCOME_"+in.Status, work, action, b); err != nil {
			return err
		}
	}
	if in.Status == "VERIFIED" {
		if _, err = tx.Exec(ctx, `UPDATE incidents SET status='RESOLVED',updated_at=now() WHERE tenant_id=$1 AND action_id=$2 AND kind IN('OUTCOME_MISMATCH','OUTCOME_PARTIAL','OUTCOME_FAILED','ACTION_RESULT_UNKNOWN')`, tenant, action); err != nil {
			return err
		}
	}
	if err = journal(ctx, tx, tenant, work, "", "OUTCOME_"+in.Status, "evidence:"+outcome+":"+in.ID, map[string]any{"outcome_id": outcome, "action_id": action, "status": in.Status}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type verifyJob struct {
	Tenant, ID, Action, Kind, Version, Token string
	Expected, Config                         json.RawMessage
	Secret                                   []byte
}

func (s *Service) VerifyOnce(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var j verifyJob
	var retryID *string
	err = tx.QueryRow(ctx, `SELECT o.tenant_id::text,o.id,o.action_id,o.expected,v.kind,v.version,v.config,v.secret_ciphertext,o.retry_attempt_id FROM outcomes o
	 JOIN verifiers v ON v.tenant_id=o.tenant_id AND v.id=o.verifier_id JOIN actions a ON a.tenant_id=o.tenant_id AND a.id=o.action_id
	 WHERE o.status='PENDING' AND v.enabled AND v.kind NOT IN('CUSTOM','EVENT') AND o.next_attempt_at<=now()
	 AND o.attempts<12 AND COALESCE(o.retry_started_at,o.created_at)>now()-interval '24 hours' AND (o.claim_until IS NULL OR o.claim_until<=now())
	 AND a.status IN('SUCCEEDED','UNKNOWN','FAILED') AND (a.status='SUCCEEDED' OR a.started_at IS NOT NULL OR EXISTS(SELECT 1 FROM action_results ar WHERE ar.tenant_id=a.tenant_id AND ar.action_id=a.id)) ORDER BY o.next_attempt_at FOR UPDATE OF o SKIP LOCKED LIMIT 1`).Scan(&j.Tenant, &j.ID, &j.Action, &j.Expected, &j.Kind, &j.Version, &j.Config, &j.Secret, &retryID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	j.Token = uuid.NewString()
	if retryID != nil {
		j.Token = *retryID
	}
	if _, err = tx.Exec(ctx, `UPDATE outcomes SET attempts=attempts+1,claim_token=$3,retry_attempt_id=NULL,claim_until=now()+interval '30 seconds',next_attempt_at=now()+make_interval(secs=>LEAST(300,power(2,attempts)::int)) WHERE tenant_id=$1 AND id=$2`, j.Tenant, j.ID, j.Token); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	if len(j.Secret) > 0 {
		j.Secret, err = s.cipher.Decrypt(j.Secret)
		if err != nil {
			return err
		}
	}
	verifyCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	started := time.Now()
	evidence, err := s.verify(verifyCtx, j, 0)
	timedOut := errors.Is(err, context.DeadlineExceeded) || verifyCtx.Err() != nil
	cancel()
	metricCtx, metricCancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	_, _ = s.pool.Exec(metricCtx, `INSERT INTO app_call_timings(tenant_id,app,duration_ms,error,timeout)SELECT tenant_id,COALESCE(metadata->>'app',split_part(operation,'.',1)),$3,$4,$5 FROM actions WHERE tenant_id=$1 AND id=$2`, j.Tenant, j.Action, time.Since(started).Milliseconds(), err != nil, timedOut)
	metricCancel()
	if err != nil {
		evidence = protocol.VerificationEvidence{Status: "PENDING", Reason: "Verifier unavailable or response invalid"}
	}
	evidence.ID = j.Token
	return s.evidence(ctx, j.Tenant, j.ID, evidence, j.Token)
}

func (s *Service) verify(ctx context.Context, j verifyJob, depth int) (protocol.VerificationEvidence, error) {
	if depth > 4 {
		return protocol.VerificationEvidence{}, errors.New("composite verifier depth exceeded")
	}
	var cfg, expected map[string]any
	if err := json.Unmarshal(j.Config, &cfg); err != nil {
		return protocol.VerificationEvidence{}, err
	}
	if err := json.Unmarshal(j.Expected, &expected); err != nil {
		return protocol.VerificationEvidence{}, err
	}
	var observed any
	var err error
	switch j.Kind {
	case "POSTGRES":
		conn, e := pgx.Connect(ctx, string(j.Secret))
		if e != nil {
			return protocol.VerificationEvidence{}, e
		}
		defer conn.Close(ctx)
		tx, e := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
		if e != nil {
			return protocol.VerificationEvidence{}, e
		}
		defer tx.Rollback(ctx)
		if _, e = tx.Exec(ctx, `SET LOCAL statement_timeout='10s'`); e != nil {
			return protocol.VerificationEvidence{}, e
		}
		params, _ := expected["params"].([]any)
		query, _ := cfg["query"].(string)
		if query == "" {
			return protocol.VerificationEvidence{}, errors.New("query required")
		}
		var b []byte
		if e = tx.QueryRow(ctx, query, params...).Scan(&b); e != nil {
			return protocol.VerificationEvidence{}, e
		}
		err = json.Unmarshal(b, &observed)
	case "COMPOSITE":
		children, _ := cfg["verifiers"].([]any)
		if len(children) == 0 || len(children) > 16 {
			return protocol.VerificationEvidence{}, errors.New("composite requires 1-16 verifiers")
		}
		all := map[string]any{}
		verified := 0
		pending := false
		for _, child := range children {
			id, ok := child.(string)
			if !ok {
				return protocol.VerificationEvidence{}, errors.New("invalid child verifier")
			}
			sub := j
			sub.Secret = nil
			if subset, ok := expected[id]; ok {
				sub.Expected, _ = json.Marshal(subset)
			}
			var secret []byte
			if e := s.pool.QueryRow(ctx, `SELECT kind,version,config,secret_ciphertext FROM verifiers WHERE tenant_id=$1 AND id=$2 AND enabled`, j.Tenant, id).Scan(&sub.Kind, &sub.Version, &sub.Config, &secret); e != nil {
				return protocol.VerificationEvidence{}, e
			}
			if len(secret) > 0 {
				sub.Secret, err = s.cipher.Decrypt(secret)
				if err != nil {
					return protocol.VerificationEvidence{}, err
				}
			}
			result, e := s.verify(ctx, sub, depth+1)
			if e != nil {
				pending = true
				all[id] = map[string]string{"status": "PENDING"}
				continue
			}
			all[id] = map[string]any{"verifier_id": id, "verifier_version": sub.Version, "evidence": result}
			if result.Status == "VERIFIED" {
				verified++
			}
			if result.Status == "PENDING" || result.Status == "UNKNOWN" {
				pending = true
			}
		}
		status := "MISMATCH"
		if pending {
			status = "PENDING"
		} else if verified == len(children) {
			status = "VERIFIED"
		} else if verified > 0 {
			status = "PARTIAL"
		}
		b, _ := json.Marshal(all)
		return protocol.VerificationEvidence{Status: status, Observed: b}, nil
	case "STRIPE":
		if expected["amount"] == nil || expected["currency"] == nil {
			return protocol.VerificationEvidence{Status: "UNKNOWN", Reason: "Refund amount and currency are required"}, nil
		}
		base := stringValue(cfg, "base_url", "https://api.stripe.com")
		if id := stringValue(expected, "refund_id", ""); id != "" {
			observed, err = s.readJSON(ctx, http.MethodGet, base+"/v1/refunds/"+url.PathEscape(id), j.Secret, nil, "Bearer ", "")
		} else {
			payment := stringValue(expected, "payment_intent", "")
			charge := stringValue(expected, "charge", "")
			correlation := stringValue(expected, "correlation_id", "")
			if (payment == "" && charge == "") || correlation == "" {
				return protocol.VerificationEvidence{Status: "UNKNOWN", Reason: "Refund identity/correlation is required"}, nil
			}
			query := url.Values{"limit": {"100"}}
			if payment != "" {
				query.Set("payment_intent", payment)
			} else {
				query.Set("charge", charge)
			}
			for page := 0; page < 100; page++ {
				data, e := s.readJSON(ctx, http.MethodGet, base+"/v1/refunds?"+query.Encode(), j.Secret, nil, "Bearer ", "")
				if e != nil {
					return protocol.VerificationEvidence{}, e
				}
				obj, ok := data.(map[string]any)
				if !ok {
					return protocol.VerificationEvidence{}, errors.New("invalid refunds response")
				}
				items, _ := obj["data"].([]any)
				for _, item := range items {
					m, _ := item.(map[string]any)
					meta, _ := m["metadata"].(map[string]any)
					if meta["lumni_effect_id"] == correlation {
						observed = item
						break
					}
				}
				if observed != nil {
					break
				}
				if obj["has_more"] != true || len(items) == 0 {
					break
				}
				last, _ := items[len(items)-1].(map[string]any)
				query.Set("starting_after", stringValue(last, "id", ""))
			}
			if observed == nil {
				return protocol.VerificationEvidence{Status: "PENDING", Reason: "Refund not yet found; absence is not proof of non-commit"}, nil
			}
		}
		if id, ok := expected["refund_id"]; ok {
			expected["id"] = id
		}
		delete(expected, "refund_id")
		delete(expected, "correlation_id")
		if _, ok := expected["status"]; !ok {
			expected["status"] = "succeeded"
		}
	case "GITHUB":
		base := stringValue(cfg, "base_url", "https://api.github.com")
		repo := stringValue(expected, "repository", "")
		parts := strings.Split(repo, "/")
		if len(parts) != 2 {
			return protocol.VerificationEvidence{}, errors.New("repository owner/name required")
		}
		path := base + "/repos/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1]) + "/pulls/" + url.PathEscape(fmt.Sprint(expected["number"]))
		if connection := stringValue(cfg, "connector_id", ""); connection != "" {
			if s.githubConnector == nil {
				return protocol.VerificationEvidence{}, errors.New("GitHub connector transport is unavailable")
			}
			number, e := strconv.Atoi(fmt.Sprint(expected["number"]))
			if e != nil || number <= 0 {
				return protocol.VerificationEvidence{}, errors.New("positive pull request number required")
			}
			var data json.RawMessage
			data, err = s.githubConnector.ReadPullRequest(ctx, j.Tenant, connection, repo, number)
			if err == nil {
				err = json.Unmarshal(data, &observed)
			}
		} else {
			observed, err = s.readJSON(ctx, http.MethodGet, path, j.Secret, nil, "Bearer ", "")
		}
		delete(expected, "repository")
	case "SHOPIFY":
		base := stringValue(cfg, "url", "")
		if base == "" {
			return protocol.VerificationEvidence{}, errors.New("versioned Shopify GraphQL URL required")
		}
		body, _ := json.Marshal(map[string]any{"query": `query($id:ID!){order(id:$id){id cancelledAt refunds{id totalRefundedSet{shopMoney{amount currencyCode}} transactions(first:100){nodes{status kind}}}}}`, "variables": map[string]any{"id": expected["order_id"]}})
		observed, err = s.readJSON(ctx, http.MethodPost, base, j.Secret, body, "", "X-Shopify-Access-Token")
		if err == nil {
			envelope, ok := observed.(map[string]any)
			if !ok {
				return protocol.VerificationEvidence{}, errors.New("invalid Shopify response")
			}
			if faults, ok := envelope["errors"].([]any); ok && len(faults) > 0 {
				return protocol.VerificationEvidence{}, errors.New("Shopify query failed")
			}
			order, exists := resource.LookupPointer(envelope, "/data/order")
			if !exists || order == nil {
				return protocol.VerificationEvidence{Status: "PENDING", Reason: "Order is not yet visible"}, nil
			}
			observed = order
			if m, ok := order.(map[string]any); ok {
				m["cancelled"] = m["cancelledAt"] != nil
				if id, ok := expected["order_id"]; ok {
					expected["id"] = id
				}
				if refundID, ok := expected["refund_id"].(string); ok {
					items, _ := m["refunds"].([]any)
					found := false
					for _, item := range items {
						r, _ := item.(map[string]any)
						if r["id"] == refundID {
							m["refund"] = r
							found = true
							break
						}
					}
					if !found {
						return protocol.VerificationEvidence{Status: "PENDING", Reason: "Refund not yet visible"}, nil
					}
					delete(expected, "refund_id")
				}
			}
		}
		delete(expected, "order_id")
	case "HTTP":
		endpoint := stringValue(cfg, "url", "")
		for key, value := range expected {
			endpoint = strings.ReplaceAll(endpoint, "{"+key+"}", url.PathEscape(fmt.Sprint(value)))
		}
		observed, err = s.readJSON(ctx, http.MethodGet, endpoint, j.Secret, nil, "Bearer ", "")
	default:
		return protocol.VerificationEvidence{}, errors.New("verifier requires authenticated callback")
	}
	if err != nil {
		return protocol.VerificationEvidence{}, err
	}
	assertions, ok := expected["assertions"].(map[string]any)
	matched := true
	if ok {
		if len(assertions) == 0 {
			return protocol.VerificationEvidence{}, errors.New("expected assertions must not be empty")
		}
		for pointer, want := range assertions {
			got, exists := resource.LookupPointer(observed, pointer)
			if !exists || !reflect.DeepEqual(got, want) {
				matched = false
				break
			}
		}
	} else {
		delete(expected, "params")
		if len(expected) == 0 {
			return protocol.VerificationEvidence{}, errors.New("expected outcome must contain an assertion")
		}
		matched = subset(expected, observed)
	}
	status := "MISMATCH"
	if j.Kind == "STRIPE" && !matched {
		if m, ok := observed.(map[string]any); ok && (m["status"] == "pending" || m["status"] == "requires_action") {
			delete(expected, "status")
			if subset(expected, m) {
				status = "PENDING"
			}
		}
	}
	if matched {
		status = "VERIFIED"
	}
	b, _ := json.Marshal(observed)
	var external *string
	if m, ok := observed.(map[string]any); ok {
		if id, ok := m["id"].(string); ok {
			external = &id
		}
	}
	return protocol.VerificationEvidence{Status: status, Observed: b, ExternalResultID: external}, nil
}
func subset(want, got any) bool {
	if m, ok := want.(map[string]any); ok {
		g, ok := got.(map[string]any)
		if !ok {
			return false
		}
		for k, v := range m {
			if !subset(v, g[k]) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(want, got)
}
func stringValue(m map[string]any, key, fallback string) string {
	v, ok := m[key].(string)
	if !ok || v == "" {
		return fallback
	}
	return v
}
func (s *Service) readJSON(ctx context.Context, method, endpoint string, secret, body []byte, prefix, header string) (any, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, errors.New("invalid verifier URL")
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if len(secret) > 0 {
		if header == "" {
			header = "Authorization"
		}
		req.Header.Set(header, prefix+string(secret))
	}
	client := *s.client
	client.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if len(via) > 3 || r.URL.Host != u.Host || r.URL.Scheme != u.Scheme {
			return errors.New("cross-origin verifier redirect rejected")
		}
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("verifier HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20+1))
	if err != nil {
		return nil, err
	}
	if len(b) > 2<<20 {
		return nil, errors.New("verifier response too large")
	}
	var out any
	if err = json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}
