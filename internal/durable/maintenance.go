package durable

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"lumni/pkg/protocol"
)

func (s *Service) Tick(ctx context.Context) error {
	// A gateway process can disappear without leaving a renewable lease.
	if _, err := s.pool.Exec(ctx, `WITH lost AS(UPDATE actions a SET status='UNKNOWN' WHERE a.status='ACTIVE' AND a.started_at<now()-interval '30 seconds' AND NOT EXISTS(SELECT 1 FROM leases l WHERE l.tenant_id=a.tenant_id AND l.action_id=a.id AND l.expires_at>now()) RETURNING a.tenant_id,a.id)INSERT INTO effect_holds(tenant_id,action_id,reason)SELECT tenant_id,id,'GATEWAY_LOST' FROM lost ON CONFLICT DO NOTHING`); err != nil {
		return err
	}
	// A missing worker is not evidence that a started external call failed.
	if _, err := s.pool.Exec(ctx, `WITH expired AS(UPDATE actions a SET status='UNKNOWN' FROM work w,steps st WHERE w.tenant_id=a.tenant_id AND st.tenant_id=a.tenant_id
	 AND st.action_id=a.id AND st.work_id=w.id AND w.execution_mode='NATIVE' AND w.claim_expires_at<=now() AND a.status='ACTIVE' RETURNING a.tenant_id,a.id)
	 INSERT INTO effect_holds(tenant_id,action_id,reason) SELECT tenant_id,id,'UNKNOWN_EFFECT' FROM expired ON CONFLICT DO NOTHING`); err != nil {
		return err
	}
	rows, err := s.pool.Query(ctx, `SELECT tenant_id::text,step_id FROM durable_waits WHERE status='WAITING' ORDER BY last_checked_at NULLS FIRST,created_at LIMIT 100`)
	if err != nil {
		return err
	}
	type ref struct{ tenant, step string }
	var refs []ref
	for rows.Next() {
		var r ref
		if err = rows.Scan(&r.tenant, &r.step); err != nil {
			rows.Close()
			return err
		}
		refs = append(refs, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var failures []error
	for _, r := range refs {
		if err = s.wake(ctx, r.tenant, r.step); err != nil {
			failures = append(failures, err)
		}
	}
	if err = s.VerifyOnce(ctx); err != nil {
		failures = append(failures, err)
	}
	if _, err = s.pool.Exec(ctx, `INSERT INTO incidents(tenant_id,id,kind,work_id,title,evidence)
	 SELECT w.tenant_id,'partial_'||w.id,'PARTIAL_COMMIT',w.id,'Some effects committed while others failed',jsonb_build_object('work_id',w.id)
	 FROM work w WHERE EXISTS(SELECT 1 FROM effect_occurrences e JOIN actions a ON a.tenant_id=e.tenant_id AND a.id=e.action_id WHERE a.tenant_id=w.tenant_id AND a.work_id=w.id AND e.status='COMMITTED')
	 AND (EXISTS(SELECT 1 FROM outcomes o WHERE o.tenant_id=w.tenant_id AND o.work_id=w.id AND o.status IN('MISMATCH','PARTIAL','FAILED')) OR (w.status IN('FAILED','CANCELLED','SUPERSEDED') AND EXISTS(SELECT 1 FROM jsonb_object_keys(w.expected_effects) k WHERE NOT EXISTS(SELECT 1 FROM steps st JOIN outcomes o ON o.tenant_id=st.tenant_id AND o.action_id=st.action_id WHERE st.tenant_id=w.tenant_id AND st.work_id=w.id AND st.step_key=k AND o.status='VERIFIED')))) ON CONFLICT DO NOTHING`); err != nil {
		failures = append(failures, err)
	}
	if _, err = s.pool.Exec(ctx, `UPDATE reconciliation_jobs j SET status=w.status FROM work w WHERE j.tenant_id=w.tenant_id AND j.work_id=w.id AND j.status IS DISTINCT FROM w.status`); err != nil {
		failures = append(failures, err)
	}
	// Terminal payload retention applies to every copy, including result and retry receipts.
	if _, err = s.pool.Exec(ctx, `UPDATE work SET payload_expires_at=COALESCE(completed_at,now())+$1::interval WHERE payload_expires_at IS NULL AND status IN('COMPLETED','FAILED','CANCELLED','SUPERSEDED')`, s.retention.String()); err != nil {
		failures = append(failures, err)
	}
	expired := `SELECT tenant_id,id FROM work WHERE payload_expires_at<=now() AND status IN('COMPLETED','FAILED','CANCELLED','SUPERSEDED')`
	for _, statement := range []string{
		`UPDATE actions SET arguments_ciphertext=NULL,arguments=NULL WHERE (tenant_id,work_id) IN(` + expired + `)`,
		`UPDATE action_results SET output_ciphertext=NULL WHERE (tenant_id,action_id) IN(SELECT tenant_id,id FROM actions WHERE (tenant_id,work_id) IN(` + expired + `))`,
		`UPDATE runtime_receipts SET response_ciphertext=NULL WHERE (tenant_id,work_id) IN(` + expired + `)`,
		`UPDATE runtime_signals SET payload_ciphertext=NULL WHERE (tenant_id,work_id) IN(` + expired + `)`,
		`UPDATE model_calls SET input_ciphertext=NULL,output_ciphertext=NULL WHERE (tenant_id,work_id) IN(` + expired + `)`,
		`UPDATE admission_payloads SET response_ciphertext=NULL WHERE (tenant_id,receipt_id) IN(SELECT d.tenant_id,d.id FROM decisions d JOIN actions a ON a.tenant_id=d.tenant_id AND a.id=d.action_id WHERE (a.tenant_id,a.work_id) IN(` + expired + `))`,
	} {
		if _, err = s.pool.Exec(ctx, statement); err != nil {
			failures = append(failures, err)
		}
	}
	// Metadata and immutable evidence remain.
	if _, err = s.pool.Exec(ctx, `UPDATE steps SET input_ciphertext=NULL,output_ciphertext=NULL,admission_ciphertext=NULL WHERE (tenant_id,work_id) IN(SELECT tenant_id,id FROM work WHERE payload_expires_at<=now() AND status IN('COMPLETED','FAILED','CANCELLED'))`); err != nil {
		failures = append(failures, err)
	}
	if _, err = s.pool.Exec(ctx, `UPDATE work SET input_ciphertext=NULL,output_ciphertext=NULL WHERE payload_expires_at<=now() AND status IN('COMPLETED','FAILED','CANCELLED')`); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

func (s *Service) wake(ctx context.Context, tenant, step string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var work, kind, status string
	var condition []byte
	var at *time.Time
	var action *string
	err = tx.QueryRow(ctx, `SELECT d.work_id,d.type,d.condition,d.wake_at,st.action_id,w.status FROM durable_waits d JOIN steps st ON st.tenant_id=d.tenant_id AND st.id=d.step_id
	 JOIN work w ON w.tenant_id=d.tenant_id AND w.id=d.work_id WHERE d.tenant_id=$1 AND d.step_id=$2 AND d.status='WAITING' FOR UPDATE OF w,st,d SKIP LOCKED`, tenant, step).Scan(&work, &kind, &condition, &at, &action, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if status != "WAITING" {
		return nil
	}
	var in protocol.WaitInput
	if err = json.Unmarshal(condition, &in); err != nil {
		return err
	}
	ready := false
	next := "COMPLETED"
	var output []byte
	var value any
	switch kind {
	case "TIMER", "RETRY", "ACTION":
		ready = at != nil && !at.After(time.Now())
		if kind != "TIMER" {
			next = "PENDING"
		}
	case "SIGNAL":
		err = tx.QueryRow(ctx, `UPDATE runtime_signals SET consumed_by=$4 WHERE tenant_id=$1 AND work_id=$2 AND id=(SELECT id FROM runtime_signals WHERE tenant_id=$1 AND work_id=$2 AND name=$3 AND consumed_by IS NULL ORDER BY created_at,id LIMIT 1 FOR UPDATE SKIP LOCKED) RETURNING payload_ciphertext`, tenant, work, in.Signal, step).Scan(&output)
		ready = err == nil
	case "RESOURCE":
		var version *string
		err = tx.QueryRow(ctx, `SELECT current_version FROM resources WHERE tenant_id=$1 AND id=$2`, tenant, in.ResourceID).Scan(&version)
		ready = err == nil && version != nil && *version != in.Version
		value = version
	case "WORK":
		var childState string
		err = tx.QueryRow(ctx, `SELECT status,output_ciphertext FROM work WHERE tenant_id=$1 AND id=$2`, tenant, in.WorkID).Scan(&childState, &output)
		ready = err == nil && (childState == "COMPLETED" || childState == "FAILED" || childState == "CANCELLED")
		if ready && childState != "COMPLETED" {
			var handled bool
			if e := tx.QueryRow(ctx, `SELECT handle_child_failure FROM work WHERE tenant_id=$1 AND id=$2`, tenant, work).Scan(&handled); e != nil {
				return e
			}
			value = map[string]any{"status": childState}
			if !handled {
				next = "FAILED"
			}
		}
	case "APPROVAL":
		var approval string
		err = tx.QueryRow(ctx, `SELECT status FROM approvals WHERE tenant_id=$1 AND id=$2`, tenant, in.ApprovalID).Scan(&approval)
		ready = err == nil && approval != "PENDING"
		value = map[string]any{"status": approval}
	case "OUTCOME":
		if action == nil {
			return errors.New("outcome wait missing action")
		}
		var outcome, actionStatus string
		var verifier *string
		var absent, terminated bool
		err = tx.QueryRow(ctx, `SELECT o.status,o.verifier_id,o.effect_absent,o.prior_attempt_terminated,a.status,COALESCE(ar.output_ciphertext,(SELECT output_ciphertext FROM steps WHERE tenant_id=$1 AND id=$3)) FROM outcomes o JOIN actions a ON a.tenant_id=o.tenant_id AND a.id=o.action_id LEFT JOIN action_results ar ON ar.tenant_id=a.tenant_id AND ar.action_id=a.id WHERE o.tenant_id=$1 AND o.action_id=$2`, tenant, *action, step).Scan(&outcome, &verifier, &absent, &terminated, &actionStatus, &output)
		if err == nil {
			ready = outcome == "VERIFIED" || (verifier == nil && actionStatus == "SUCCEEDED")
			if absent && terminated {
				ready = true
				next = "PENDING"
				if _, e := tx.Exec(ctx, `UPDATE steps SET action_id=NULL WHERE tenant_id=$1 AND id=$2`, tenant, step); e != nil {
					return e
				}
			}
			if !ready && (outcome == "MISMATCH" || outcome == "FAILED" || outcome == "PARTIAL") {
				ready = true
				next = "FAILED"
			}
			if ready && next == "COMPLETED" && (len(output) == 0 || actionStatus == "UNKNOWN") {
				// A lost response is reconstructed from authoritative verifier evidence.
				var observed []byte
				if e := tx.QueryRow(ctx, `SELECT observed FROM outcomes WHERE tenant_id=$1 AND action_id=$2`, tenant, *action).Scan(&observed); e != nil {
					return e
				}
				output, err = s.cipher.Encrypt(observed)
			}
		}
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if !ready {
		_, err = tx.Exec(ctx, `UPDATE durable_waits SET last_checked_at=now() WHERE tenant_id=$1 AND step_id=$2`, tenant, step)
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if len(output) == 0 {
		b, _ := json.Marshal(value)
		output, err = s.cipher.Encrypt(b)
		if err != nil {
			return err
		}
	}
	plain, err := s.cipher.Decrypt(output)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE steps SET status=$3,output_ciphertext=CASE WHEN $3='COMPLETED' THEN $4 ELSE output_ciphertext END,output_hash=CASE WHEN $3='COMPLETED' THEN $5 ELSE output_hash END,updated_at=now() WHERE tenant_id=$1 AND id=$2`, tenant, step, next, output, digest(plain)); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE durable_waits SET status='RESOLVED',resolved_at=now() WHERE tenant_id=$1 AND step_id=$2`, tenant, step); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE work SET status='PENDING',resume_at=now() WHERE tenant_id=$1 AND id=$2 AND status='WAITING'`, tenant, work); err != nil {
		return err
	}
	if err = journal(ctx, tx, tenant, work, step, "STEP_RESUMED", fmt.Sprintf("wake:%s:%d", step, time.Now().UnixNano()), map[string]any{"step_id": step, "status": next}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) Dependency(ctx context.Context, tenant string, in protocol.DependencyInput) error {
	if in.ID == "" || in.SourceID == "" || in.TargetID == "" || in.SourceID == in.TargetID {
		return errors.New("dependency identity and distinct endpoints required")
	}
	if in.EntityType != "work" && in.EntityType != "action" {
		return errors.New("invalid dependency entity type")
	}
	if in.Relationship != "DEPENDS_ON" && in.Relationship != "SUPERSEDES" {
		return errors.New("invalid relationship")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, tenant+":dependencies"); err != nil {
		return err
	}
	table := "work"
	if in.EntityType == "action" {
		table = "actions"
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE tenant_id=$1 AND id=ANY($2)`, tenant, []string{in.SourceID, in.TargetID}).Scan(&count); err != nil {
		return err
	}
	if in.EntityType == "action" {
		var started bool
		if err = tx.QueryRow(ctx, `SELECT started_at IS NOT NULL FROM actions WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenant, in.SourceID).Scan(&started); err != nil {
			return err
		}
		if started {
			return errors.New("ACTION_ALREADY_STARTED")
		}
	}
	if count != 2 {
		return errors.New("dependency endpoints not found in tenant")
	}
	var old protocol.DependencyInput
	err = tx.QueryRow(ctx, `SELECT id,entity_type,source_id,target_id,relationship FROM dependencies WHERE tenant_id=$1 AND id=$2`, tenant, in.ID).Scan(&old.ID, &old.EntityType, &old.SourceID, &old.TargetID, &old.Relationship)
	if err == nil {
		if old != in {
			return errors.New("DEPENDENCY_IDENTITY_MISMATCH")
		}
		return tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var cycle bool
	err = tx.QueryRow(ctx, `WITH RECURSIVE chain(id)AS(SELECT target_id FROM dependencies WHERE tenant_id=$1 AND entity_type=$2 AND source_id=$3 AND relationship=$5 UNION SELECT d.target_id FROM dependencies d JOIN chain c ON c.id=d.source_id WHERE d.tenant_id=$1 AND d.entity_type=$2 AND d.relationship=$5)SELECT EXISTS(SELECT 1 FROM chain WHERE id=$4)`, tenant, in.EntityType, in.TargetID, in.SourceID, in.Relationship).Scan(&cycle)
	if err != nil {
		return err
	}
	if cycle {
		return errors.New("DEPENDENCY_CYCLE")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO dependencies(tenant_id,id,entity_type,source_id,target_id,relationship)VALUES($1,$2,$3,$4,$5,$6)ON CONFLICT DO NOTHING`, tenant, in.ID, in.EntityType, in.SourceID, in.TargetID, in.Relationship); err != nil {
		return err
	}
	if in.Relationship == "DEPENDS_ON" && in.EntityType == "action" {
		if _, err = tx.Exec(ctx, `DELETE FROM leases WHERE tenant_id=$1 AND action_id=$2`, tenant, in.SourceID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE actions SET status='WAITING' WHERE tenant_id=$1 AND id=$2 AND started_at IS NULL`, tenant, in.SourceID); err != nil {
			return err
		}
	}
	if in.Relationship == "SUPERSEDES" && in.EntityType == "work" {
		if _, err = tx.Exec(ctx, `UPDATE work SET status='SUPERSEDED',superseded_at=now(),claim_hash=NULL,claim_expires_at=NULL WHERE tenant_id=$1 AND id=$2 AND status NOT IN('COMPLETED','CANCELLED')`, tenant, in.TargetID); err != nil {
			return err
		}
	}
	if in.Relationship == "SUPERSEDES" && in.EntityType == "action" {
		if _, err = tx.Exec(ctx, `UPDATE actions SET status='CANCELLED' WHERE tenant_id=$1 AND id=$2 AND status IN('PROPOSED','RESERVED','WAITING','AWAITING_APPROVAL')`, tenant, in.TargetID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `DELETE FROM leases WHERE tenant_id=$1 AND action_id=$2 AND EXISTS(SELECT 1 FROM actions WHERE tenant_id=$1 AND id=$2 AND status='CANCELLED')`, tenant, in.TargetID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
