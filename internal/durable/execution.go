package durable

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"lumni/pkg/protocol"
)

func (s *Service) StartStep(ctx context.Context, tenant, work, key string, in protocol.StepResult) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = authorize(ctx, tx, tenant, work, in.RuntimeAuth); err != nil {
		return err
	}
	st, _, admission, err := loadStep(ctx, tx, tenant, work, key)
	if err != nil {
		return err
	}
	if in.Attempt != st.Attempt {
		return errors.New("STEP_ATTEMPT_MISMATCH")
	}
	if st.Status != "READY" && st.Status != "RUNNING" {
		return errors.New("STEP_NOT_READY")
	}
	if st.Status == "READY" {
		if _, err = tx.Exec(ctx, `UPDATE steps SET status='RUNNING',updated_at=now() WHERE tenant_id=$1 AND id=$2`, tenant, st.ID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO step_attempts(tenant_id,step_id,attempt,claim_fence,status)VALUES($1,$2,$3,$4,'RUNNING')ON CONFLICT DO NOTHING`, tenant, st.ID, st.Attempt, in.Fence); err != nil {
			return err
		}
		if err = journal(ctx, tx, tenant, work, st.ID, "STEP_STARTED", fmt.Sprintf("start:%s:%d", st.ID, st.Attempt), map[string]any{"step_id": st.ID, "attempt": st.Attempt}); err != nil {
			return err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	if st.ActionID != nil {
		var decision protocol.Decision
		b, e := s.cipher.Decrypt(admission)
		if e != nil {
			return e
		}
		if e = json.Unmarshal(b, &decision); e != nil {
			return e
		}
		token := ""
		if decision.LeaseToken != nil {
			token = *decision.LeaseToken
		}
		return s.kernel.Start(ctx, tenant, *st.ActionID, token)
	}
	return nil
}

func (s *Service) CompleteStep(ctx context.Context, tenant, work, key string, in protocol.StepResult) (protocol.Step, error) {
	if in.Status != "COMPLETED" && in.Status != "FAILED" && in.Status != "UNKNOWN" {
		return protocol.Step{}, errors.New("invalid step result status")
	}
	output, err := canonical(in.Output)
	if err != nil {
		return protocol.Step{}, err
	}
	cipher, err := s.cipher.Encrypt(output)
	if err != nil {
		return protocol.Step{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return protocol.Step{}, err
	}
	defer tx.Rollback(ctx)
	receiptID := fmt.Sprintf("result:%s:%d", key, in.Attempt)
	var cached protocol.Step
	if found, e := s.receipt(ctx, tx, tenant, work, receiptID, in.Token, in, &cached); e != nil {
		return cached, e
	} else if found {
		return cached, tx.Commit(ctx)
	}
	if err = authorize(ctx, tx, tenant, work, in.RuntimeAuth); err != nil {
		return protocol.Step{}, err
	}
	st, _, admission, err := loadStep(ctx, tx, tenant, work, key)
	if err != nil {
		return st, err
	}
	if st.Attempt != in.Attempt {
		return st, errors.New("STEP_ATTEMPT_MISMATCH")
	}
	identityResult := in
	identityResult.RuntimeAuth = protocol.RuntimeAuth{}
	identityResult.Output = output
	resultIdentity, _ := json.Marshal(identityResult)
	var oldHash *string
	if err = tx.QueryRow(ctx, `SELECT result_hash FROM step_attempts WHERE tenant_id=$1 AND step_id=$2 AND attempt=$3`, tenant, st.ID, st.Attempt).Scan(&oldHash); err != nil {
		return st, err
	}
	if oldHash != nil {
		if *oldHash != digest(resultIdentity) {
			return st, errors.New("STEP_RESULT_MISMATCH")
		}
		st.Output = output
		return st, tx.Commit(ctx)
	}
	if st.Status != "RUNNING" {
		return st, errors.New("STEP_NOT_RUNNING")
	}
	status := in.Status
	if st.Type == "TOOL" && in.Status == "FAILED" && !in.EffectAbsent {
		status = "UNKNOWN"
	}
	if st.ActionID != nil {
		var d protocol.Decision
		b, e := s.cipher.Decrypt(admission)
		if e != nil {
			return st, e
		}
		if e = json.Unmarshal(b, &d); e != nil {
			return st, e
		}
		resultStatus := status
		if status == "COMPLETED" {
			resultStatus = "SUCCEEDED"
		}
		if err = s.kernel.RecordResultTx(ctx, tx, tenant, *st.ActionID, protocol.ActionResultInput{Status: resultStatus, LeaseToken: d.LeaseToken, ExternalResultID: in.ExternalResultID, Output: output, Retryable: &in.Retryable}); err != nil {
			return st, err
		}
		var configured bool
		if err = tx.QueryRow(ctx, `SELECT verifier_id IS NOT NULL FROM outcomes WHERE tenant_id=$1 AND action_id=$2`, tenant, *st.ActionID).Scan(&configured); err != nil {
			return st, err
		}
		if configured && status == "COMPLETED" {
			status = "WAITING"
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE step_attempts SET status=$4,result_hash=$5,error=$6,finished_at=now() WHERE tenant_id=$1 AND step_id=$2 AND attempt=$3 AND result_hash IS NULL`, tenant, st.ID, st.Attempt, in.Status, digest(resultIdentity), in.Error); err != nil {
		return st, err
	}
	if _, err = tx.Exec(ctx, `UPDATE steps SET status=$3,output_hash=$4,output_ciphertext=$5,error=$6,updated_at=now(),completed_at=CASE WHEN $3='COMPLETED' THEN now() END WHERE tenant_id=$1 AND id=$2`, tenant, st.ID, status, digest(output), cipher, in.Error); err != nil {
		return st, err
	}
	if status == "FAILED" && in.Retryable && (st.Type != "TOOL" || in.EffectAbsent) {
		var policy protocol.RetryPolicy
		var b []byte
		if err = tx.QueryRow(ctx, `SELECT retry_policy FROM steps WHERE tenant_id=$1 AND id=$2`, tenant, st.ID).Scan(&b); err != nil {
			return st, err
		}
		if err = json.Unmarshal(b, &policy); err != nil {
			return st, err
		}
		if policy.MaxAttempts == 0 {
			policy.MaxAttempts = 3
		}
		if policy.InitialDelayMS <= 0 {
			policy.InitialDelayMS = 1000
		}
		if policy.Multiplier < 1 {
			policy.Multiplier = 2
		}
		if policy.MaxDelayMS <= 0 {
			policy.MaxDelayMS = 30000
		}
		if st.Attempt < policy.MaxAttempts {
			delay := math.Min(float64(policy.MaxDelayMS), float64(policy.InitialDelayMS)*math.Pow(policy.Multiplier, float64(st.Attempt-1)))
			wake := time.Now().Add(time.Duration(delay) * time.Millisecond)
			if _, err = tx.Exec(ctx, `UPDATE steps SET action_id=NULL WHERE tenant_id=$1 AND id=$2`, tenant, st.ID); err != nil {
				return st, err
			}
			if err = s.persistWait(ctx, tx, tenant, work, st.ID, protocol.WaitInput{Type: "RETRY", WakeAt: &wake}); err != nil {
				return st, err
			}
			status = "WAITING"
		}
	} else if status == "WAITING" || status == "UNKNOWN" {
		if err = s.persistWait(ctx, tx, tenant, work, st.ID, protocol.WaitInput{Type: "OUTCOME"}); err != nil {
			return st, err
		}
		status = "WAITING"
	}
	if err = journal(ctx, tx, tenant, work, st.ID, "STEP_"+status, fmt.Sprintf("result:%s:%d", st.ID, st.Attempt), map[string]any{"step_id": st.ID, "status": status, "output_hash": digest(output)}); err != nil {
		return st, err
	}
	st.Status = status
	st.Output = output
	st.OutputHash = digest(output)
	st.Error = in.Error
	if err = s.saveReceipt(ctx, tx, tenant, work, receiptID, in.Token, in, st); err != nil {
		return st, err
	}
	return st, tx.Commit(ctx)
}

func (s *Service) persistWait(ctx context.Context, tx pgx.Tx, tenant, work, step string, in protocol.WaitInput) error {
	if in.Type == "TIMER" && in.WakeAt == nil && in.DelayMS > 0 {
		at := time.Now().Add(time.Duration(in.DelayMS) * time.Millisecond)
		in.WakeAt = &at
	}
	switch in.Type {
	case "TIMER", "SIGNAL", "RESOURCE", "WORK", "APPROVAL", "ACTION", "OUTCOME", "RETRY":
	default:
		return errors.New("invalid wait type")
	}
	if (in.Type == "TIMER" || in.Type == "RETRY") && in.WakeAt == nil {
		return errors.New("wake_at is required")
	}
	if in.Type == "SIGNAL" && in.Signal == "" {
		return errors.New("signal is required")
	}
	b, _ := json.Marshal(in)
	if _, err := tx.Exec(ctx, `INSERT INTO durable_waits(tenant_id,step_id,work_id,type,condition,wake_at)VALUES($1,$2,$3,$4,$5,$6)
	 ON CONFLICT(tenant_id,step_id)DO UPDATE SET type=EXCLUDED.type,condition=EXCLUDED.condition,wake_at=EXCLUDED.wake_at,status='WAITING',resolved_at=NULL`, tenant, step, work, in.Type, b, in.WakeAt); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE steps SET status='WAITING',updated_at=now() WHERE tenant_id=$1 AND id=$2`, tenant, step); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE work SET status='WAITING',claim_hash=NULL,claim_expires_at=NULL WHERE tenant_id=$1 AND id=$2`, tenant, work); err != nil {
		return err
	}
	return journal(ctx, tx, tenant, work, step, "WORK_WAITING", fmt.Sprintf("wait:%s:%s", step, digest(b)), map[string]any{"step_id": step, "wait_type": in.Type})
}

func (s *Service) Signal(ctx context.Context, tenant, work string, in protocol.SignalInput) error {
	if in.ID == "" || in.Name == "" {
		return errors.New("signal id and name are required")
	}
	b, err := canonical(in.Payload)
	if err != nil {
		return err
	}
	cipher, err := s.cipher.Encrypt(b)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO runtime_signals(tenant_id,work_id,id,name,payload_ciphertext,payload_hash)VALUES($1,$2,$3,$4,$5,$6)ON CONFLICT DO NOTHING`, tenant, work, in.ID, in.Name, cipher, digest(b)); err != nil {
		return err
	}
	var valid bool
	if err = tx.QueryRow(ctx, `SELECT name=$4 AND payload_hash=$5 FROM runtime_signals WHERE tenant_id=$1 AND work_id=$2 AND id=$3`, tenant, work, in.ID, in.Name, digest(b)).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return errors.New("SIGNAL_IDENTITY_MISMATCH")
	}
	if err = journal(ctx, tx, tenant, work, "", "SIGNAL_RECEIVED", "signal:"+in.ID, map[string]any{"signal": in.Name, "id": in.ID}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) Finish(ctx context.Context, tenant, work string, in protocol.StepResult) error {
	if in.Status != "COMPLETED" && in.Status != "FAILED" {
		return errors.New("invalid work result status")
	}
	b, err := canonical(in.Output)
	if err != nil {
		return err
	}
	cipher, err := s.cipher.Encrypt(b)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var cached bool
	if found, e := s.receipt(ctx, tx, tenant, work, "finished", in.Token, in, &cached); e != nil {
		return e
	} else if found {
		return tx.Commit(ctx)
	}
	if err = authorize(ctx, tx, tenant, work, in.RuntimeAuth); err != nil {
		return err
	}
	if in.Status == "COMPLETED" {
		var failedChild bool
		if err = tx.QueryRow(ctx, `SELECT NOT w.handle_child_failure AND EXISTS(SELECT 1 FROM work child WHERE child.tenant_id=w.tenant_id AND child.parent_work_id=w.id AND child.status IN('FAILED','CANCELLED')) FROM work w WHERE w.tenant_id=$1 AND w.id=$2`, tenant, work).Scan(&failedChild); err != nil {
			return err
		}
		if failedChild {
			return errors.New("CHILD_WORK_FAILED")
		}
		var incomplete bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM steps WHERE tenant_id=$1 AND work_id=$2 AND status<>'COMPLETED') OR EXISTS(SELECT 1 FROM work WHERE tenant_id=$1 AND parent_work_id=$2 AND status NOT IN('COMPLETED','FAILED','CANCELLED')) OR EXISTS(SELECT 1 FROM work w,LATERAL jsonb_object_keys(w.expected_effects) k WHERE w.tenant_id=$1 AND w.id=$2 AND NOT EXISTS(SELECT 1 FROM steps st JOIN outcomes o ON o.tenant_id=st.tenant_id AND o.action_id=st.action_id WHERE st.tenant_id=w.tenant_id AND st.work_id=w.id AND st.step_key=k AND o.status='VERIFIED' AND o.verifier_id=w.expected_effects->k->>'verifier_id' AND o.expected=w.expected_effects->k->'expected'))`, tenant, work).Scan(&incomplete); err != nil {
			return err
		}
		if incomplete {
			return errors.New("WORK_INCOMPLETE")
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE work SET status=$3,output_ciphertext=$4,runtime_error=$5,completed_at=now(),claim_hash=NULL,claim_expires_at=NULL,payload_expires_at=now()+$6::interval WHERE tenant_id=$1 AND id=$2`, tenant, work, in.Status, cipher, in.Error, s.retention.String()); err != nil {
		return err
	}
	if err = journal(ctx, tx, tenant, work, "", "WORK_"+in.Status, "finished", map[string]any{"status": in.Status}); err != nil {
		return err
	}
	if err = s.saveReceipt(ctx, tx, tenant, work, "finished", in.Token, in, true); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) Cancel(ctx context.Context, tenant, work string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// The recursive CTE is tenant scoped at every edge; effects are untouched.
	rows, err := tx.Query(ctx, `WITH RECURSIVE children AS(SELECT id FROM work WHERE tenant_id=$1 AND id=$2 UNION SELECT w.id FROM work w JOIN children c ON w.parent_work_id=c.id WHERE w.tenant_id=$1)
	 UPDATE work SET status='CANCELLED',cancelled_at=now(),claim_hash=NULL,claim_expires_at=NULL WHERE tenant_id=$1 AND id IN(SELECT id FROM children) AND status NOT IN('COMPLETED','FAILED','CANCELLED') RETURNING id`, tenant, work)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err = tx.Exec(ctx, `UPDATE steps SET status='CANCELLED' WHERE tenant_id=$1 AND work_id=$2 AND status NOT IN('COMPLETED','FAILED')`, tenant, id); err != nil {
			return err
		}
		if err = journal(ctx, tx, tenant, id, "", "WORK_CANCELLED", "cancelled", map[string]any{"work_id": id}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
