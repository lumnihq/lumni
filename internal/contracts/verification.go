package contracts

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/lumnihq/lumni/internal/automation"
	"github.com/lumnihq/lumni/pkg/protocol"
)

// VerifyWatchOnce records read-back observations without changing action outcomes.
func (s *Service) VerifyWatchOnce(ctx context.Context, verify func(context.Context, string, string, json.RawMessage) (protocol.VerificationEvidence, error)) error {
	var tenant, action, contract, rule, verifier, token string
	var expected []byte
	token = uuid.NewString()
	e := s.Pool.QueryRow(ctx, `WITH candidate AS(SELECT c.tenant_id,c.action_id,c.contract_id,c.rule_id FROM contract_assertions c JOIN actions a ON a.tenant_id=c.tenant_id AND a.id=c.action_id JOIN verifiers v ON v.tenant_id=c.tenant_id AND v.id=c.verifier_id WHERE c.status IN('PENDING','UNKNOWN') AND c.attempts<12 AND c.next_attempt_at<=now() AND(c.claim_until IS NULL OR c.claim_until<=now()) AND a.status IN('SUCCEEDED','UNKNOWN','FAILED') AND a.started_at IS NOT NULL AND v.enabled AND v.kind NOT IN('CUSTOM','EVENT') ORDER BY c.next_attempt_at FOR UPDATE OF c SKIP LOCKED LIMIT 1) UPDATE contract_assertions c SET claim_token=$1,claim_until=now()+interval '30 seconds',attempts=attempts+1,next_attempt_at=now()+interval '10 seconds' FROM candidate x WHERE c.tenant_id=x.tenant_id AND c.action_id=x.action_id AND c.contract_id=x.contract_id AND c.rule_id=x.rule_id RETURNING c.tenant_id::text,c.action_id,c.contract_id,c.rule_id,c.verifier_id,c.expected`, token).Scan(&tenant, &action, &contract, &rule, &verifier, &expected)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	readCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	evidence, e := verify(readCtx, tenant, verifier, expected)
	cancel()
	if e != nil {
		evidence = protocol.VerificationEvidence{Status: "UNKNOWN", Reason: "Verifier unavailable"}
	}
	evidence.ID = token
	return s.WatchEvidence(ctx, tenant, contract, action, rule, evidence, token)
}
func (s *Service) WatchEvidence(ctx context.Context, tenant, contract, action, rule string, in protocol.VerificationEvidence, claim string) error {
	if in.ID == "" || len(in.ID) > 200 || !Has([]string{"VERIFIED", "MISMATCH", "PARTIAL", "PENDING", "UNKNOWN", "FAILED"}, in.Status) {
		return automation.Invalid("evidence id and valid status required")
	}
	return automation.Transaction(ctx, s.Pool, func(tx pgx.Tx) error {
		var current string
		var expiry *time.Time
		var token *string
		var until *time.Time
		e := tx.QueryRow(ctx, `SELECT c.status,ar.recorded_at+make_interval(secs=>c.within_seconds),c.claim_token,c.claim_until FROM contract_assertions c LEFT JOIN action_results ar ON ar.tenant_id=c.tenant_id AND ar.action_id=c.action_id WHERE c.tenant_id=$1 AND c.contract_id=$2 AND c.action_id=$3 AND c.rule_id=$4 FOR UPDATE OF c`, tenant, contract, action, rule).Scan(&current, &expiry, &token, &until)
		if errors.Is(e, pgx.ErrNoRows) {
			return automation.Missing("watch assertion not found")
		}
		if e != nil {
			return e
		}
		if claim != "" && (token == nil || *token != claim || until == nil || !until.After(time.Now())) {
			return automation.Conflict("watch verification claim expired")
		}
		hash := automation.Hash(in)
		var old string
		e = tx.QueryRow(ctx, `SELECT input_hash FROM contract_assertion_receipts WHERE tenant_id=$1 AND action_id=$2 AND contract_id=$3 AND rule_id=$4 AND id=$5`, tenant, action, contract, rule, in.ID).Scan(&old)
		if e == nil {
			if old != hash {
				return automation.Conflict("evidence identity mismatch")
			}
			return nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if current == "VERIFIED" && in.Status != "VERIFIED" {
			return automation.Conflict("verified assertion cannot be downgraded")
		}
		status := in.Status
		reason := in.Reason
		if expiry != nil && expiry.Before(time.Now()) && current != "VERIFIED" {
			status = "FAILED"
			reason = "CONTRACT_VERIFICATION_DEADLINE"
		}
		observed := in.Observed
		if len(observed) == 0 {
			observed = []byte("null")
		}
		cipher, e := s.Cipher.Encrypt(observed)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO contract_assertion_receipts(tenant_id,action_id,contract_id,rule_id,id,input_hash)VALUES($1,$2,$3,$4,$5,$6)`, tenant, action, contract, rule, in.ID, hash); e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `UPDATE contract_assertions SET status=$5,reason=$6,observed_ciphertext=$7,claim_until=NULL,claim_token=NULL,updated_at=now() WHERE tenant_id=$1 AND action_id=$2 AND contract_id=$3 AND rule_id=$4`, tenant, action, contract, rule, status, reason, cipher)
		return e
	})
}
