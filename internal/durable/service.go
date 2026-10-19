// Package durable implements the optional agent runtime and authoritative
// verification plane. It never executes application functions in the server.
package durable

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"lumni/internal/auth"
	"lumni/internal/coordination"
	"lumni/internal/secrets"
	"lumni/pkg/protocol"
)

type Service struct {
	pool            *pgxpool.Pool
	kernel          *coordination.Kernel
	cipher          *secrets.Manager
	client          *http.Client
	retention       time.Duration
	githubConnector GitHubConnectorReader
}

// GitHubConnectorReader supplies fresh provider reads through an optional
// transport. Direct GitHub credentials remain the default verifier path.
type GitHubConnectorReader interface {
	ReadPullRequest(context.Context, string, string, string, int) (json.RawMessage, error)
}

func (s *Service) SetGitHubConnectorReader(reader GitHubConnectorReader) {
	s.githubConnector = reader
}

func New(pool *pgxpool.Pool, kernel *coordination.Kernel, cipher *secrets.Manager) *Service {
	kernel.SetPayloadCipher(cipher)
	return &Service{pool: pool, kernel: kernel, cipher: cipher, client: &http.Client{Timeout: 15 * time.Second}, retention: 30 * 24 * time.Hour}
}
func raw(v json.RawMessage) []byte {
	if len(v) == 0 {
		return []byte("null")
	}
	return v
}
func digest(v []byte) string { sum := sha256.Sum256(v); return hex.EncodeToString(sum[:]) }
func canonical(v json.RawMessage) ([]byte, error) {
	if len(v) == 0 {
		return []byte("null"), nil
	}
	var x any
	dec := json.NewDecoder(bytes.NewReader(v))
	dec.UseNumber()
	if err := dec.Decode(&x); err != nil {
		return nil, err
	}
	return json.Marshal(x)
}
func journal(ctx context.Context, tx pgx.Tx, tenant, work, step, kind, id string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO work_journal(tenant_id,work_id,step_id,type,identity,payload)VALUES($1,$2,NULLIF($3,''),$4,$5,$6)ON CONFLICT DO NOTHING`, tenant, work, step, kind, id, b)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox(tenant_id,topic,key,payload)VALUES($1,$2,$3,$4)`, tenant, kind, work, b)
	return err
}
func (s *Service) Submit(ctx context.Context, tenant string, in protocol.RunInput) (string, error) {
	if in.ID == "" || in.ActorID == "" || in.Handler == "" || in.HandlerVersion == "" {
		return "", errors.New("id, actor_id, handler and handler_version are required")
	}
	input, err := canonical(in.Input)
	if err != nil {
		return "", err
	}
	cipher, err := s.cipher.Encrypt(input)
	if err != nil {
		return "", err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, tenant+":submit:"+in.ID); err != nil {
		return "", err
	}
	if in.ParentWorkID != nil {
		var exists bool
		if *in.ParentWorkID == in.ID {
			return "", errors.New("DEPENDENCY_CYCLE")
		}
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM work WHERE tenant_id=$1 AND id=$2)`, tenant, *in.ParentWorkID).Scan(&exists); err != nil {
			return "", err
		}
		if !exists {
			return "", errors.New("parent work not found")
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO actors(tenant_id,id,type,runtime)VALUES($1,$2,'AGENT','lumni')ON CONFLICT DO NOTHING`, tenant, in.ActorID); err != nil {
		return "", err
	}
	expected, _ := json.Marshal(in.ExpectedEffects)
	if string(expected) == "null" {
		expected = []byte("{}")
	}
	var same bool
	var existingHash, handler, version string
	err = tx.QueryRow(ctx, `SELECT input_hash,handler,handler_version,(actor_id=$3 AND parent_work_id IS NOT DISTINCT FROM $4 AND expected_effects=$5::jsonb AND handle_child_failure=$6) FROM work WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenant, in.ID, in.ActorID, in.ParentWorkID, expected, in.HandleChildFailure).Scan(&existingHash, &handler, &version, &same)
	if err == nil {
		if !same || existingHash != digest(input) || handler != in.Handler || version != in.HandlerVersion {
			return "", errors.New("WORK_IDENTITY_MISMATCH")
		}
		return in.ID, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	_, err = tx.Exec(ctx, `INSERT INTO work(tenant_id,id,actor_id,goal,status,execution_mode,handler,handler_version,input_hash,input_ciphertext,parent_work_id,handle_child_failure)
	 VALUES($1,$2,$3,$4,'PENDING','NATIVE',$5,$6,$7,$8,$9,$10)`, tenant, in.ID, in.ActorID, in.Goal, in.Handler, in.HandlerVersion, digest(input), cipher, in.ParentWorkID, in.HandleChildFailure)
	if err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, `UPDATE work SET expected_effects=$3 WHERE tenant_id=$1 AND id=$2`, tenant, in.ID, expected); err != nil {
		return "", err
	}
	if err = journal(ctx, tx, tenant, in.ID, "", "WORK_CREATED", "created", map[string]any{"handler": in.Handler, "handler_version": in.HandlerVersion}); err != nil {
		return "", err
	}
	return in.ID, tx.Commit(ctx)
}

func (s *Service) Claim(ctx context.Context, tenant string, in protocol.ClaimInput) (*protocol.RunClaim, error) {
	if in.WorkerID == "" || len(in.Handlers) == 0 {
		return nil, errors.New("worker_id and versioned handlers are required")
	}
	handlers, _ := json.Marshal(in.Handlers)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var claim protocol.RunClaim
	var cipher []byte
	err = tx.QueryRow(ctx, `SELECT id,actor_id,handler,handler_version,input_ciphertext FROM work
	 WHERE tenant_id=$1 AND execution_mode='NATIVE' AND status IN ('PENDING','RUNNING') AND resume_at<=now()
	 AND (claim_expires_at IS NULL OR claim_expires_at<=now()) AND $2::jsonb->>handler=handler_version
	 ORDER BY priority DESC,resume_at,id FOR UPDATE SKIP LOCKED LIMIT 1`, tenant, handlers).Scan(&claim.WorkID, &claim.ActorID, &claim.Handler, &claim.HandlerVersion, &cipher)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(cipher) == 0 {
		return nil, errors.New("REPLAY_DATA_UNAVAILABLE")
	}
	claim.Input, err = s.cipher.Decrypt(cipher)
	if err != nil {
		return nil, err
	}
	claim.Token, err = auth.Generate()
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `WITH lost AS(UPDATE actions a SET status='UNKNOWN' FROM steps st WHERE a.tenant_id=$1 AND st.tenant_id=a.tenant_id AND st.action_id=a.id AND st.work_id=$2 AND st.status IN('RUNNING','READY') AND a.started_at IS NOT NULL AND a.status='ACTIVE' RETURNING a.tenant_id,a.id)INSERT INTO effect_holds(tenant_id,action_id,reason)SELECT tenant_id,id,'WORKER_LOST' FROM lost ON CONFLICT DO NOTHING`, tenant, claim.WorkID); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE actions a SET status='CANCELLED' FROM steps st WHERE a.tenant_id=$1 AND st.tenant_id=a.tenant_id AND st.action_id=a.id AND st.work_id=$2 AND st.status IN('RUNNING','READY') AND a.started_at IS NULL`, tenant, claim.WorkID); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM leases l USING actions a WHERE a.tenant_id=l.tenant_id AND a.id=l.action_id AND a.tenant_id=$1 AND a.work_id=$2 AND a.status='CANCELLED' AND a.started_at IS NULL`, tenant, claim.WorkID); err != nil {
		return nil, err
	}
	err = tx.QueryRow(ctx, `UPDATE work SET status='RUNNING',claim_hash=$3,claim_fence=claim_fence+1,claim_expires_at=now()+interval '30 seconds'
	 WHERE tenant_id=$1 AND id=$2 RETURNING claim_fence,claim_expires_at`, tenant, claim.WorkID, auth.Hash(claim.Token)).Scan(&claim.Fence, &claim.ExpiresAt)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE steps SET status=CASE WHEN type='TOOL' AND EXISTS(SELECT 1 FROM actions a WHERE a.tenant_id=steps.tenant_id AND a.id=steps.action_id AND a.started_at IS NOT NULL) THEN 'UNKNOWN' ELSE 'PENDING' END,
	 action_id=CASE WHEN EXISTS(SELECT 1 FROM actions a WHERE a.tenant_id=steps.tenant_id AND a.id=steps.action_id AND a.started_at IS NULL)THEN NULL ELSE action_id END,updated_at=now()
	 WHERE tenant_id=$1 AND work_id=$2 AND status IN ('RUNNING','READY')`, tenant, claim.WorkID); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE step_attempts sa SET status='UNKNOWN',finished_at=now() FROM steps st WHERE st.tenant_id=sa.tenant_id AND st.id=sa.step_id AND st.tenant_id=$1 AND st.work_id=$2 AND sa.finished_at IS NULL`, tenant, claim.WorkID); err != nil {
		return nil, err
	}
	if err = journal(ctx, tx, tenant, claim.WorkID, "", "WORK_RESUMED", fmt.Sprintf("claim:%d", claim.Fence), map[string]any{"worker_id": in.WorkerID, "fence": claim.Fence}); err != nil {
		return nil, err
	}
	return &claim, tx.Commit(ctx)
}

func authorize(ctx context.Context, tx pgx.Tx, tenant, work string, in protocol.RuntimeAuth) error {
	var valid bool
	err := tx.QueryRow(ctx, `SELECT status='RUNNING' AND claim_hash=$3 AND claim_fence=$4 AND claim_expires_at>now() FROM work WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenant, work, auth.Hash(in.Token), in.Fence).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return coordination.ErrInvalidLease
	}
	return nil
}
func (s *Service) Heartbeat(ctx context.Context, tenant, work string, in protocol.RuntimeAuth) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = authorize(ctx, tx, tenant, work, in); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE work SET claim_expires_at=now()+interval '30 seconds' WHERE tenant_id=$1 AND id=$2`, tenant, work); err != nil {
		return err
	}
	// Only the current fenced work owner can extend associated action leases.
	if _, err = tx.Exec(ctx, `UPDATE leases SET expires_at=now()+interval '30 seconds',heartbeat_at=now() WHERE tenant_id=$1 AND action_id IN(SELECT action_id FROM steps WHERE tenant_id=$1 AND work_id=$2 AND status IN('READY','RUNNING')) AND expires_at>now()`, tenant, work); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func loadStep(ctx context.Context, tx pgx.Tx, tenant, work, key string) (protocol.Step, []byte, []byte, error) {
	var st protocol.Step
	var output, admission []byte
	err := tx.QueryRow(ctx, `SELECT id,work_id,step_key,type,name,status,attempt,input_hash,output_hash,action_id,error,output_ciphertext,admission_ciphertext
	 FROM steps WHERE tenant_id=$1 AND work_id=$2 AND step_key=$3 FOR UPDATE`, tenant, work, key).Scan(&st.ID, &st.WorkID, &st.Key, &st.Type, &st.Name, &st.Status, &st.Attempt, &st.InputHash, &st.OutputHash, &st.ActionID, &st.Error, &output, &admission)
	return st, output, admission, err
}

func (s *Service) Begin(ctx context.Context, tenant, work string, in protocol.StepInput) (protocol.Step, error) {
	if in.RetryPolicy.MaxAttempts == 0 {
		in.RetryPolicy.MaxAttempts = 3
	}
	if in.RetryPolicy.MaxAttempts < 1 || in.RetryPolicy.MaxAttempts > 100 {
		return protocol.Step{}, errors.New("invalid retry bound")
	}
	if in.Key == "" {
		return protocol.Step{}, errors.New("stable step key is required")
	}
	if in.Type == "TOOL" && in.Action == nil {
		return protocol.Step{}, errors.New("tool action is required")
	}
	identity, _ := json.Marshal(struct {
		Type   string
		Retry  protocol.RetryPolicy
		Input  json.RawMessage
		Action *protocol.ActionCheckInput
		Wait   *protocol.WaitInput
	}{in.Type, in.RetryPolicy, in.Input, in.Action, in.Wait})
	input, err := canonical(identity)
	if err != nil {
		return protocol.Step{}, err
	}
	hash := digest(input)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return protocol.Step{}, err
	}
	defer tx.Rollback(ctx)
	waitReceipt := fmt.Sprintf("begin-wait:%s:%d", in.Key, in.Fence)
	var cached protocol.Step
	if found, e := s.receipt(ctx, tx, tenant, work, waitReceipt, in.Token, in, &cached); e != nil {
		return cached, e
	} else if found {
		return cached, tx.Commit(ctx)
	}
	if err = authorize(ctx, tx, tenant, work, in.RuntimeAuth); err != nil {
		return protocol.Step{}, err
	}
	st, output, admission, err := loadStep(ctx, tx, tenant, work, in.Key)
	if errors.Is(err, pgx.ErrNoRows) {
		cipher, err := s.cipher.Encrypt(input)
		if err != nil {
			return st, err
		}
		st = protocol.Step{ID: "step_" + uuid.NewString(), WorkID: work, Key: in.Key, Type: in.Type, Name: in.Name, Status: "PENDING", InputHash: hash}
		if st.Name == "" {
			st.Name = in.Key
		}
		if in.RetryPolicy.MaxAttempts == 0 {
			in.RetryPolicy.MaxAttempts = 3
		}
		policy, _ := json.Marshal(in.RetryPolicy)
		if _, err = tx.Exec(ctx, `INSERT INTO steps(tenant_id,id,work_id,step_key,type,name,input_hash,input_ciphertext,retry_policy)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, tenant, st.ID, work, in.Key, in.Type, st.Name, hash, cipher, policy); err != nil {
			return st, err
		}
	} else if err != nil {
		return st, err
	}
	if st.InputHash != hash || st.Type != in.Type {
		return st, errors.New("REPLAY_INCOMPATIBLE: step input or type changed")
	}
	if st.Status == "COMPLETED" {
		if len(output) == 0 {
			return st, errors.New("REPLAY_DATA_UNAVAILABLE")
		}
		st.Output, err = s.cipher.Decrypt(output)
		st.Replay = true
		if err != nil {
			return st, err
		}
		return st, tx.Commit(ctx)
	}
	if st.Status == "READY" || st.Status == "RUNNING" {
		if len(admission) > 0 {
			b, e := s.cipher.Decrypt(admission)
			if e != nil {
				return st, e
			}
			if e = json.Unmarshal(b, &st.Decision); e != nil {
				return st, e
			}
		}
		return st, tx.Commit(ctx)
	}
	if st.Status == "UNKNOWN" && st.ActionID != nil {
		if err = s.persistWait(ctx, tx, tenant, work, st.ID, protocol.WaitInput{Type: "OUTCOME"}); err != nil {
			return st, err
		}
		st.Status = "WAITING"
		if err = s.saveReceipt(ctx, tx, tenant, work, waitReceipt, in.Token, in, st); err != nil {
			return st, err
		}
		return st, tx.Commit(ctx)
	}
	if st.Status == "UNKNOWN" || st.Status == "FAILED" || st.Status == "CANCELLED" {
		return st, tx.Commit(ctx)
	}
	if in.Wait != nil {
		if err = s.persistWait(ctx, tx, tenant, work, st.ID, *in.Wait); err != nil {
			return st, err
		}
		st.Status = "WAITING"
		if err = s.saveReceipt(ctx, tx, tenant, work, waitReceipt, in.Token, in, st); err != nil {
			return st, err
		}
		return st, tx.Commit(ctx)
	}
	if st.ActionID == nil {
		st.Attempt++
	}
	var storedPolicy protocol.RetryPolicy
	var policyJSON []byte
	if err = tx.QueryRow(ctx, `SELECT retry_policy FROM steps WHERE tenant_id=$1 AND id=$2`, tenant, st.ID).Scan(&policyJSON); err != nil {
		return st, err
	}
	if err = json.Unmarshal(policyJSON, &storedPolicy); err != nil {
		return st, err
	}
	if st.Attempt > storedPolicy.MaxAttempts {
		return st, errors.New("RETRIES_EXHAUSTED")
	}
	recheck := st.ActionID != nil
	if in.Type == "TOOL" && !recheck {
		id := fmt.Sprintf("act_%s_%d", st.ID, st.Attempt)
		st.ActionID = &id
	}
	if _, err = tx.Exec(ctx, `UPDATE steps SET attempt=$3,status='PENDING',updated_at=now() WHERE tenant_id=$1 AND id=$2`, tenant, st.ID, st.Attempt); err != nil {
		return st, err
	}
	if err = tx.Commit(ctx); err != nil {
		return st, err
	}
	if in.Action != nil {
		action := *in.Action
		action.ActionID = *st.ActionID
		action.WorkID = work
		action.StepID = &st.ID
		var actor string
		if err = s.pool.QueryRow(ctx, `SELECT actor_id FROM work WHERE tenant_id=$1 AND id=$2`, tenant, work).Scan(&actor); err != nil {
			return st, err
		}
		action.ActorID = actor
		if action.Verification == nil {
			var spec []byte
			if e := s.pool.QueryRow(ctx, `SELECT expected_effects->$3 FROM work WHERE tenant_id=$1 AND id=$2`, tenant, work, in.Key).Scan(&spec); e != nil {
				return st, e
			}
			if len(spec) > 0 {
				if e := json.Unmarshal(spec, &action.Verification); e != nil {
					return st, e
				}
			}
		}
		var decision protocol.Decision
		var e error
		if recheck {
			decision, e = s.kernel.Recheck(ctx, tenant, *st.ActionID, fmt.Sprintf("resume:%d", in.Fence))
		} else {
			decision, e = s.kernel.Check(ctx, tenant, action)
		}
		if e != nil {
			return st, e
		}
		st.Decision = &decision
	}
	return s.ready(ctx, tenant, work, in.RuntimeAuth, st)
}

func (s *Service) ready(ctx context.Context, tenant, work string, owner protocol.RuntimeAuth, st protocol.Step) (protocol.Step, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return st, err
	}
	defer tx.Rollback(ctx)
	if err = authorize(ctx, tx, tenant, work, owner); err != nil {
		return st, err
	}
	st.Status = "READY"
	var admission []byte
	if st.Decision != nil {
		b, _ := json.Marshal(st.Decision)
		admission, err = s.cipher.Encrypt(b)
		if err != nil {
			return st, err
		}
		if st.Decision.Enforced {
			switch st.Decision.Decision {
			case protocol.DecisionWait, protocol.DecisionRequireApproval:
				st.Status = "WAITING"
			case protocol.DecisionReplan, protocol.DecisionReject:
				st.Status = "FAILED"
				st.Error = string(st.Decision.ReasonCode)
			case protocol.DecisionDeduplicate:
				if st.Decision.ExistingActionID != nil {
					result, e := s.kernel.PreviousResult(ctx, tenant, *st.Decision.ExistingActionID)
					if e != nil {
						st.Status = "FAILED"
						st.Error = e.Error()
					} else {
						output, e := s.cipher.Encrypt(result)
						if e != nil {
							return st, e
						}
						st.Status = "COMPLETED"
						st.Output = result
						if _, e = tx.Exec(ctx, `UPDATE steps SET output_ciphertext=$3,output_hash=$4 WHERE tenant_id=$1 AND id=$2`, tenant, st.ID, output, digest(result)); e != nil {
							return st, e
						}
						var pending bool
						if e = tx.QueryRow(ctx, `SELECT verifier_id IS NOT NULL AND status<>'VERIFIED' FROM outcomes WHERE tenant_id=$1 AND action_id=$2`, tenant, *st.ActionID).Scan(&pending); e != nil {
							return st, e
						}
						if pending {
							st.Status = "WAITING"
						}
					}
				}
			}
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE steps SET status=$3,action_id=$4,admission_ciphertext=$5,error=$6,updated_at=now() WHERE tenant_id=$1 AND id=$2`, tenant, st.ID, st.Status, st.ActionID, admission, st.Error); err != nil {
		return st, err
	}
	if st.Status == "WAITING" {
		wait := protocol.WaitInput{Type: "ACTION"}
		if st.Decision.Decision == protocol.DecisionDeduplicate {
			wait.Type = "OUTCOME"
		}
		if st.Decision.ApprovalID != nil {
			wait.ApprovalID = *st.Decision.ApprovalID
		}
		wake := time.Now().Add(time.Second)
		wait.WakeAt = &wake
		if err = s.persistWait(ctx, tx, tenant, work, st.ID, wait); err != nil {
			return st, err
		}
	}
	if err = journal(ctx, tx, tenant, work, st.ID, "STEP_"+st.Status, fmt.Sprintf("step:%s:%d:ready", st.ID, st.Attempt), map[string]any{"step_id": st.ID, "status": st.Status}); err != nil {
		return st, err
	}
	return st, tx.Commit(ctx)
}

func (s *Service) SetRetention(retention time.Duration) {
	if retention > 0 {
		s.retention = retention
	}
}
