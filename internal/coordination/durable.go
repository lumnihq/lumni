package coordination

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/lumnihq/lumni/internal/secrets"
	"github.com/lumnihq/lumni/pkg/protocol"
)

func (k *Kernel) SetPayloadCipher(cipher *secrets.Manager) { k.cipher = cipher }

// StartOnce is a one-shot invocation boundary for external runtimes. A lost
// response deliberately consumes the permission; recovery must use evidence.
func (k *Kernel) StartOnce(ctx context.Context, tenant, action, token string) (bool, error) {
	started := false
	err := k.withLease(ctx, tenant, action, token, func(tx pgx.Tx, hasLease bool) error {
		var enforced, write, already bool
		if err := tx.QueryRow(ctx, `SELECT d.enforced,EXISTS(SELECT 1 FROM action_resources ar WHERE ar.tenant_id=a.tenant_id AND ar.action_id=a.id AND ar.mode IN('WRITE','CREATE','DELETE')),a.started_at IS NOT NULL FROM actions a JOIN LATERAL(SELECT enforced FROM decisions WHERE tenant_id=a.tenant_id AND action_id=a.id ORDER BY created_at DESC LIMIT 1)d ON true WHERE a.tenant_id=$1 AND a.id=$2 FOR UPDATE OF a`, tenant, action).Scan(&enforced, &write, &already); err != nil {
			return err
		}
		if already {
			return nil
		}
		if enforced && write && !hasLease {
			return ErrInvalidLease
		}
		tag, err := tx.Exec(ctx, `UPDATE actions SET status='ACTIVE',started_at=now() WHERE tenant_id=$1 AND id=$2 AND started_at IS NULL AND status IN('PROPOSED','RESERVED')`, tenant, action)
		started = tag.RowsAffected() == 1
		if err == nil && started {
			_, err = tx.Exec(ctx, `INSERT INTO work_journal(tenant_id,work_id,step_id,type,identity,payload)SELECT tenant_id,work_id,step_id,'CALL_STARTED','call-start:'||id,jsonb_build_object('action_id',id) FROM actions WHERE tenant_id=$1 AND id=$2 ON CONFLICT DO NOTHING`, tenant, action)
			if err == nil {
				_, err = tx.Exec(ctx, `INSERT INTO outbox(tenant_id,topic,key,payload)VALUES($1,'CALL_STARTED',$2,jsonb_build_object('action_id',$2::text))`, tenant, action)
			}
		}
		return err
	})
	return started, err
}

func (k *Kernel) captureIntent(ctx context.Context, tx pgx.Tx, tenant string, in protocol.ActionCheckInput) error {
	var cipher []byte
	var err error
	if k.cipher != nil {
		cipher, err = k.cipher.Encrypt(jsonOrDefault(in.Arguments, []byte("{}")))
		if err != nil {
			return err
		}
	}
	var spec any
	if in.Verification != nil {
		b, _ := json.Marshal(in.Verification)
		spec = b
	}
	if _, err = tx.Exec(ctx, `UPDATE actions SET step_id=$3,verification=$4,effect_identity_version='v2',arguments_ciphertext=$5,
	 arguments=CASE WHEN $5::bytea IS NULL THEN arguments ELSE NULL END WHERE tenant_id=$1 AND id=$2`, tenant, in.ActionID, in.StepID, spec, cipher); err != nil {
		return err
	}
	var verifier *string
	expected := []byte("{}")
	status, version := "UNKNOWN", ""
	if in.Verification != nil {
		verifier = &in.Verification.VerifierID
		expected = jsonOrDefault(in.Verification.Expected, []byte("{}"))
		status = "PENDING"
		if err := tx.QueryRow(ctx, `SELECT version FROM verifiers WHERE tenant_id=$1 AND id=$2 AND enabled`, tenant, *verifier).Scan(&version); err != nil {
			return fmt.Errorf("verifier unavailable: %w", err)
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO outcomes(tenant_id,id,action_id,work_id,verifier_id,verifier_version,expected,status)
	 VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT DO NOTHING`, tenant, "out_"+in.ActionID, in.ActionID, in.WorkID, verifier, version, expected, status)
	return err
}

func (k *Kernel) Recheck(ctx context.Context, tenant, actionID, identity string) (protocol.Decision, error) {
	if identity == "" {
		return protocol.Decision{}, errors.New("recheck id is required")
	}
	var in protocol.ActionCheckInput
	var arguments, cipher, spec []byte
	in.ActionID = actionID
	in.RecheckID = identity
	err := k.pool.QueryRow(ctx, `SELECT work_id,actor_id,session_id,operation,tool,source,arguments,arguments_ciphertext,
	 idempotency_key,effect_key,risk,step_id,verification FROM actions WHERE tenant_id=$1 AND id=$2`, tenant, actionID).
		Scan(&in.WorkID, &in.ActorID, &in.SessionID, &in.Operation, &in.Tool, &in.Source, &arguments, &cipher, &in.IdempotencyKey, &in.EffectKey, &in.Risk, &in.StepID, &spec)
	if err != nil {
		return protocol.Decision{}, err
	}
	if len(cipher) > 0 {
		if k.cipher == nil {
			return protocol.Decision{}, errors.New("payload cipher unavailable")
		}
		arguments, err = k.cipher.Decrypt(cipher)
		if err != nil {
			return protocol.Decision{}, err
		}
	}
	if len(arguments) == 0 {
		return protocol.Decision{}, errors.New("REPLAY_DATA_UNAVAILABLE")
	}
	in.Arguments = arguments
	if len(spec) > 0 && string(spec) != "null" {
		if err := json.Unmarshal(spec, &in.Verification); err != nil {
			return protocol.Decision{}, err
		}
	}
	rows, err := k.pool.Query(ctx, `SELECT ar.resource_id,r.type,r.external_id,r.namespace,ar.mode,ar.observed_version,ar.expected_version,ar.version_source,ar.confidence,ar.evidence_type,ar.observed_at
	 FROM action_resources ar JOIN resources r ON r.tenant_id=ar.tenant_id AND r.id=ar.resource_id WHERE ar.tenant_id=$1 AND ar.action_id=$2`, tenant, actionID)
	if err != nil {
		return protocol.Decision{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var r protocol.ResourceIntent
		if err := rows.Scan(&r.ID, &r.Type, &r.ExternalID, &r.Namespace, &r.Mode, &r.ObservedVersion, &r.ExpectedVersion, &r.VersionSource, &r.Confidence, &r.EvidenceType, &r.ObservedAt); err != nil {
			return protocol.Decision{}, err
		}
		in.Resources = append(in.Resources, r)
	}
	if err := rows.Err(); err != nil {
		return protocol.Decision{}, err
	}
	rows.Close()
	return k.Check(ctx, tenant, in)
}

func checkHoldsAndDependencies(ctx context.Context, tx pgx.Tx, tenant string, in protocol.ActionCheckInput) (draft, bool, error) {
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM work WHERE tenant_id=$1 AND id=$2`, tenant, in.WorkID).Scan(&status); err != nil {
		return draft{}, false, err
	}
	if status == "CANCELLED" || status == "SUPERSEDED" {
		return draft{decision: protocol.DecisionReplan, reason: protocol.ReasonCode("WORK_" + status)}, true, nil
	}
	var superseded bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM dependencies WHERE tenant_id=$1 AND entity_type='action' AND relationship='SUPERSEDES' AND target_id=$2)`, tenant, in.ActionID).Scan(&superseded); err != nil {
		return draft{}, false, err
	}
	if superseded {
		return draft{decision: protocol.DecisionReplan, reason: protocol.ReasonCode("ACTION_SUPERSEDED")}, true, nil
	}
	var ids []string
	for _, r := range in.Resources {
		if protocol.IsWrite(r.Mode) {
			ids = append(ids, r.ID)
		}
	}
	var held string
	var legacy string
	if err := tx.QueryRow(ctx, `SELECT e.action_id FROM effect_occurrences e JOIN actions a ON a.tenant_id=e.tenant_id AND a.id=e.action_id JOIN action_resources ar ON ar.tenant_id=a.tenant_id AND ar.action_id=a.id WHERE e.tenant_id=$1 AND ar.resource_id=ANY($2) AND e.status IN('COMMITTED','VERIFIED','UNKNOWN') AND a.effect_identity_version='legacy' AND e.effect_key IS DISTINCT FROM $3 AND NOT EXISTS(SELECT 1 FROM effect_aliases al WHERE al.tenant_id=e.tenant_id AND al.legacy_key=e.effect_key) LIMIT 1`, tenant, ids, in.EffectKey).Scan(&legacy); err == nil {
		delay := int64(1000)
		return draft{decision: protocol.DecisionWait, reason: protocol.ReasonCode("LEGACY_EFFECT_IDENTITY_UNRESOLVED"), existingActionID: &legacy, retryAfterMS: &delay}, true, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return draft{}, false, err
	}
	// Materialize uncertainty during admission as well as during maintenance.
	// Correctness cannot depend on the expiry worker running before the next call.
	if _, err := tx.Exec(ctx, `INSERT INTO effect_holds(tenant_id,action_id,reason)
	 SELECT DISTINCT a.tenant_id,a.id,'UNKNOWN_EFFECT' FROM actions a JOIN action_resources ar ON ar.tenant_id=a.tenant_id AND ar.action_id=a.id
	 WHERE a.tenant_id=$1 AND ar.resource_id=ANY($2) AND ar.mode IN('WRITE','CREATE','DELETE')
	 AND (a.status='UNKNOWN' OR (a.status='ACTIVE' AND a.started_at IS NOT NULL AND NOT EXISTS(SELECT 1 FROM leases l WHERE l.tenant_id=a.tenant_id AND l.action_id=a.id AND l.expires_at>now())))
	 AND NOT EXISTS(SELECT 1 FROM outcomes o WHERE o.tenant_id=a.tenant_id AND o.action_id=a.id AND (o.status='VERIFIED' OR(o.effect_absent AND o.prior_attempt_terminated))) ON CONFLICT DO NOTHING`, tenant, ids); err != nil {
		return draft{}, false, err
	}
	err := tx.QueryRow(ctx, `SELECT h.action_id FROM effect_holds h JOIN action_resources ar ON ar.tenant_id=h.tenant_id AND ar.action_id=h.action_id
	 WHERE h.tenant_id=$1 AND ar.resource_id=ANY($2) LIMIT 1`, tenant, ids).Scan(&held)
	if err == nil {
		delay := int64(1000)
		return draft{decision: protocol.DecisionWait, reason: protocol.ReasonActionResultUnknown, existingActionID: &held, retryAfterMS: &delay}, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return draft{}, false, err
	}
	var blocked bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM dependencies d LEFT JOIN work w ON d.entity_type='work' AND w.tenant_id=d.tenant_id AND w.id=d.target_id
	 LEFT JOIN actions a ON d.entity_type='action' AND a.tenant_id=d.tenant_id AND a.id=d.target_id
	 WHERE d.tenant_id=$1 AND d.relationship='DEPENDS_ON' AND ((d.entity_type='work' AND d.source_id=$2 AND w.status<>'COMPLETED')
	 OR (d.entity_type='action' AND d.source_id=$3 AND a.status<>'SUCCEEDED' AND NOT EXISTS(SELECT 1 FROM outcomes o WHERE o.tenant_id=a.tenant_id AND o.action_id=a.id AND o.status='VERIFIED'))))`, tenant, in.WorkID, in.ActionID).Scan(&blocked)
	if blocked {
		delay := int64(1000)
		return draft{decision: protocol.DecisionWait, reason: protocol.ReasonCode("DEPENDENCY_INCOMPLETE"), retryAfterMS: &delay}, true, err
	}
	return draft{}, false, err
}

func (k *Kernel) recordDurableResult(ctx context.Context, tx pgx.Tx, tenant, actionID string, in protocol.ActionResultInput, effectStatus string) error {
	if len(in.Output) > 0 && k.cipher != nil {
		cipher, err := k.cipher.Encrypt(in.Output)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(in.Output)
		if _, err = tx.Exec(ctx, `UPDATE action_results SET output_ciphertext=$3,output_hash=$4 WHERE tenant_id=$1 AND action_id=$2`, tenant, actionID, cipher, hex.EncodeToString(sum[:])); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO effect_occurrences(tenant_id,id,action_id,effect_key,status,external_id)
	 SELECT tenant_id,'occ_'||id,id,COALESCE(effect_key,''),$3,$4 FROM actions WHERE tenant_id=$1 AND id=$2 AND effect_key IS NOT NULL
	 ON CONFLICT(tenant_id,action_id) DO NOTHING`, tenant, actionID, effectStatus, in.ExternalResultID); err != nil {
		return err
	}
	if effectStatus == "UNKNOWN" {
		if _, err := tx.Exec(ctx, `INSERT INTO effect_holds(tenant_id,action_id,reason)VALUES($1,$2,'UNKNOWN_EFFECT') ON CONFLICT DO NOTHING`, tenant, actionID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE outcomes SET next_attempt_at=now(),updated_at=now() WHERE tenant_id=$1 AND action_id=$2`, tenant, actionID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO work_journal(tenant_id,work_id,step_id,type,identity,payload)
	 SELECT tenant_id,work_id,step_id,'EFFECT_RECORDED','result:'||id,jsonb_build_object('action_id',id,'status',$3::text) FROM actions WHERE tenant_id=$1 AND id=$2 ON CONFLICT DO NOTHING`, tenant, actionID, effectStatus)
	return err
}

func (k *Kernel) PreviousResult(ctx context.Context, tenant, actionID string) (json.RawMessage, error) {
	var cipher []byte
	var status, outcome string
	var observed []byte
	if err := k.pool.QueryRow(ctx, `SELECT COALESCE(ar.status,'UNKNOWN'),ar.output_ciphertext,COALESCE(o.status,'UNKNOWN'),o.observed FROM actions a LEFT JOIN action_results ar ON ar.tenant_id=a.tenant_id AND ar.action_id=a.id LEFT JOIN outcomes o ON o.tenant_id=a.tenant_id AND o.action_id=a.id WHERE a.tenant_id=$1 AND a.id=$2`, tenant, actionID).Scan(&status, &cipher, &outcome, &observed); err != nil {
		return nil, err
	}
	if status != "SUCCEEDED" {
		if outcome == "VERIFIED" && len(observed) > 0 {
			return observed, nil
		}
		if status == "UNKNOWN" {
			return nil, errors.New("RESULT_PENDING: invocation requires authoritative verification")
		}
		return nil, errors.New("RESULT_UNAVAILABLE: invocation has no successful result")
	}
	if len(cipher) == 0 || k.cipher == nil {
		return nil, errors.New("RESULT_UNAVAILABLE: original result was not retained")
	}
	return k.cipher.Decrypt(cipher)
}
