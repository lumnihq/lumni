package compiled

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/lumnihq/lumni/internal/automation"
	"github.com/lumnihq/lumni/pkg/protocol"
)

var errSuspended = errors.New("compiled run suspended")

func (s *Service) Route(ctx context.Context, tenant, actor, operation string) (map[string]any, error) {
	return s.routeFor(ctx, tenant, actor, operation, "")
}
func (s *Service) routeFor(ctx context.Context, tenant, actor, operation, path string) (map[string]any, error) {
	var id string
	var version int
	var status string
	var program, raw []byte
	var paused bool
	var mode protocol.EnforcementMode
	e := s.Pool.QueryRow(ctx, `SELECT p.id,v.v,v.status,v.program,v.bindings,p.paused OR t.paused OR (a.paused AND(a.until IS NULL OR a.until>now())),COALESCE((SELECT mode FROM enforcement_rules WHERE tenant_id=p.tenant_id AND operation IN(p.operation,'*') AND rule='*' ORDER BY(operation=p.operation)DESC LIMIT 1),t.enforcement_mode) FROM compiled_paths p JOIN tenants t ON t.id=p.tenant_id JOIN actors a ON a.tenant_id=p.tenant_id AND a.id=p.actor_id JOIN compiled_versions v ON v.tenant_id=p.tenant_id AND v.path_id=p.id AND v.status IN('live','shadow') WHERE p.tenant_id=$1 AND p.actor_id=$2 AND p.operation=$3 AND($4='' OR p.id=$4) ORDER BY(v.status='live')DESC,p.updated_at DESC,p.id LIMIT 1`, tenant, actor, operation, path).Scan(&id, &version, &status, &program, &raw, &paused, &mode)
	if errors.Is(e, pgx.ErrNoRows) {
		return map[string]any{"mode": "none", "reason": "NO_LIVE_PATH"}, nil
	}
	if e != nil {
		return nil, e
	}
	out := map[string]any{"path_id": id, "version": version, "mode": status, "effective_mode": mode}
	if paused {
		out["mode"] = "none"
		out["reason"] = "PAUSED"
		return out, nil
	}
	if mode != protocol.ModeEnforce {
		out["mode"] = "shadow"
		out["reason"] = "WORKSPACE_SHADOW"
	}
	var pins map[string]any
	if e = json.Unmarshal(raw, &pins); e != nil {
		return nil, e
	}
	if e = s.CheckBindings(ctx, tenant, pins); e != nil {
		out["mode"] = "none"
		out["reason"] = "BINDING_DRIFT"
	}
	var drift bool
	if e = s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM compiled_drift WHERE tenant_id=$1 AND path_id=$2 AND open AND fixed_by IS NULL)`, tenant, id).Scan(&drift); e != nil {
		return nil, e
	}
	if drift {
		out["mode"] = "none"
		out["reason"] = "DRIFT"
	}
	out["program_hash"] = automation.Hash(json.RawMessage(program))
	return out, nil
}
func (s *Service) Invoke(ctx context.Context, tenant, path string, in protocol.CompiledInvocation) (map[string]any, error) {
	if in.InvocationID == "" || len(in.InvocationID) > 200 || in.ActorID == "" || !json.Valid(in.Input) || len(in.Input) > 1<<20 {
		return nil, automation.Invalid("bounded invocation_id, actor_id and input required")
	}
	identity := automation.Hash([]any{path, in.ActorID, in.ParentWorkID, json.RawMessage(in.Input)})
	var old string
	e := s.Pool.QueryRow(ctx, `SELECT input_hash FROM compiled_runs WHERE tenant_id=$1 AND id=$2`, tenant, in.InvocationID).Scan(&old)
	if e == nil {
		if old != identity {
			return nil, automation.Conflict("invocation identity mismatch")
		}
		return map[string]any{"id": in.InvocationID, "created": false}, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return nil, e
	}
	var actor, operation string
	if e = s.Pool.QueryRow(ctx, `SELECT actor_id,operation FROM compiled_paths WHERE tenant_id=$1 AND id=$2`, tenant, path).Scan(&actor, &operation); e != nil {
		return nil, e
	}
	if actor != in.ActorID {
		return nil, automation.Invalid("path actor mismatch")
	}
	route, e := s.routeFor(ctx, tenant, actor, operation, path)
	if e != nil {
		return nil, e
	}
	cipher, e := s.Cipher.Encrypt(in.Input)
	if e != nil {
		return nil, e
	}
	var version *int
	if v, ok := route["version"].(int); ok {
		version = &v
	}
	if route["mode"] != "live" || route["path_id"] != path {
		_, e = s.Pool.Exec(ctx, `INSERT INTO compiled_runs(tenant_id,id,path_id,version,actor_id,input_hash,input_ciphertext,status,served_by,reason,request_hash,completed_at)VALUES($1,$2,$3,$4,$5,$6,$7,'FALLBACK','fallback',$8,$9,now()) ON CONFLICT(tenant_id,id)DO NOTHING`, tenant, in.InvocationID, path, version, actor, identity, cipher, fmt.Sprint(route["reason"]), automation.Hash(json.RawMessage(in.Input)))
		if e != nil {
			return nil, e
		}
		var recordedHash, status string
		var reason *string
		if e = s.Pool.QueryRow(ctx, `SELECT input_hash,status,reason FROM compiled_runs WHERE tenant_id=$1 AND id=$2`, tenant, in.InvocationID).Scan(&recordedHash, &status, &reason); e != nil {
			return nil, e
		}
		if recordedHash != identity {
			return nil, automation.Conflict("invocation identity mismatch")
		}
		return map[string]any{"id": in.InvocationID, "status": status, "reason": reason}, nil
	}
	var programHash string
	if e = s.Pool.QueryRow(ctx, `SELECT program_hash FROM compiled_versions WHERE tenant_id=$1 AND path_id=$2 AND v=$3 AND status='live'`, tenant, path, version).Scan(&programHash); e != nil {
		return nil, e
	}
	work := "compiled_" + automation.Hash([]string{tenant, in.InvocationID})[:40]
	_, e = s.Runtime.SubmitWith(ctx, tenant, protocol.RunInput{Work: protocol.Work{ID: work, ActorID: actor, ParentWorkID: in.ParentWorkID, Goal: "Compiled path " + path}, Handler: "compiled:" + path, HandlerVersion: programHash, Input: in.Input}, func(tx pgx.Tx) error {
		var previous string
		var previousWork *string
		err := tx.QueryRow(ctx, `SELECT input_hash,work_id FROM compiled_runs WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenant, in.InvocationID).Scan(&previous, &previousWork)
		if err == nil {
			if previous == identity && previousWork != nil && *previousWork == work {
				return nil
			}
			return automation.Conflict("invocation already recorded")
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var valid bool
		if e := tx.QueryRow(ctx, `SELECT NOT p.paused AND v.status='live' FROM compiled_paths p JOIN compiled_versions v ON v.tenant_id=p.tenant_id AND v.path_id=p.id WHERE p.tenant_id=$1 AND p.id=$2 AND v.v=$3 FOR SHARE OF p,v`, tenant, path, version).Scan(&valid); e != nil {
			return e
		}
		if !valid {
			return automation.Conflict("path changed; retry invocation")
		}
		_, e := tx.Exec(ctx, `INSERT INTO compiled_runs(tenant_id,id,path_id,version,actor_id,work_id,input_hash,input_ciphertext,request_hash)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, tenant, in.InvocationID, path, version, actor, work, identity, cipher, automation.Hash(json.RawMessage(in.Input)))
		return e
	})
	if e != nil {
		var existing string
		err := s.Pool.QueryRow(ctx, `SELECT input_hash FROM compiled_runs WHERE tenant_id=$1 AND id=$2`, tenant, in.InvocationID).Scan(&existing)
		if err == nil {
			if existing == identity {
				return map[string]any{"id": in.InvocationID, "created": false}, nil
			}
			return nil, automation.Conflict("invocation identity mismatch")
		}
	}
	return map[string]any{"id": in.InvocationID, "work_id": work, "status": "PENDING", "created": true}, e
}
func (s *Service) RuntimeOnce(ctx context.Context) error {
	var tenant, path, hash string
	e := s.Pool.QueryRow(ctx, `SELECT r.tenant_id::text,r.path_id,v.program_hash FROM compiled_runs r JOIN work w ON w.tenant_id=r.tenant_id AND w.id=r.work_id JOIN compiled_versions v ON v.tenant_id=r.tenant_id AND v.path_id=r.path_id AND v.v=r.version WHERE NOT r.reported AND r.status IN('PENDING','RUNNING','WAITING') AND w.status IN('PENDING','RUNNING') AND w.resume_at<=now() AND(w.claim_expires_at IS NULL OR w.claim_expires_at<=now()) ORDER BY r.created_at,r.id LIMIT 1`).Scan(&tenant, &path, &hash)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	claim, e := s.Runtime.Claim(ctx, tenant, protocol.ClaimInput{WorkerID: "compiled_" + uuid.NewString(), Handlers: map[string]string{"compiled:" + path: hash}})
	if e != nil || claim == nil {
		return e
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	auth := protocol.RuntimeAuth{Token: claim.Token, Fence: claim.Fence}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(8 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				if s.Runtime.Heartbeat(runCtx, tenant, claim.WorkID, auth) != nil {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); wg.Wait() }()
	var program, pinRaw []byte
	var id string
	var at time.Time
	if e = s.Pool.QueryRow(runCtx, `SELECT v.program,v.bindings,r.id,r.created_at FROM compiled_runs r JOIN compiled_versions v ON v.tenant_id=r.tenant_id AND v.path_id=r.path_id AND v.v=r.version WHERE r.tenant_id=$1 AND r.work_id=$2`, tenant, claim.WorkID).Scan(&program, &pinRaw, &id, &at); e != nil {
		return e
	}
	var p protocol.CompiledProgram
	var pins map[string]any
	if e = json.Unmarshal(program, &p); e != nil {
		return e
	}
	if e = json.Unmarshal(pinRaw, &pins); e != nil {
		return e
	}
	_, e = s.Pool.Exec(runCtx, `UPDATE compiled_runs SET status='RUNNING' WHERE tenant_id=$1 AND id=$2`, tenant, id)
	if e != nil {
		return e
	}
	mutated := false
	var prior bool
	if e = s.Pool.QueryRow(runCtx, `SELECT EXISTS(SELECT 1 FROM actions WHERE tenant_id=$1 AND work_id=$2 AND started_at IS NOT NULL)`, tenant, claim.WorkID).Scan(&prior); e != nil {
		return e
	}
	mutated = prior
	start := time.Now()
	var output json.RawMessage
	var runErr error
	var paused, drift bool
	if e = s.Pool.QueryRow(runCtx, `SELECT p.paused,EXISTS(SELECT 1 FROM compiled_drift d WHERE d.tenant_id=p.tenant_id AND d.path_id=p.id AND d.open AND d.fixed_by IS NULL) FROM compiled_paths p WHERE p.tenant_id=$1 AND p.id=$2`, tenant, path).Scan(&paused, &drift); e != nil {
		return e
	}
	if paused || drift {
		reason := "PAUSED"
		if drift {
			reason = "DRIFT"
		}
		if !mutated {
			runErr = &Fallback{reason}
		} else {
			runErr = errSuspended
		}
	} else if e = s.CheckBindings(runCtx, tenant, pins); e != nil {
		runErr = &Fallback{"BINDING_DRIFT"}
	} else {
		output, runErr = Execute(runCtx, p, claim.Input, at, func(ctx context.Context, n protocol.ProgramNode, args json.RawMessage) (json.RawMessage, error) {
			b, e := s.Registry.Get(ctx, tenant, n.BindingID, n.BindingVersion)
			if e != nil {
				if !mutated {
					return nil, &Fallback{"BINDING_UNAVAILABLE"}
				}
				return nil, e
			}
			input := protocol.StepInput{RuntimeAuth: auth, Key: n.ID, Type: "CUSTOM", Input: args}
			if n.Kind == "call" {
				action, e := s.Registry.Action(b, args)
				if e != nil {
					return nil, e
				}
				input.Type = "TOOL"
				input.Action = &action
			}
			st, e := s.Runtime.Begin(ctx, tenant, claim.WorkID, input)
			if e != nil {
				return nil, e
			}
			if st.Status == "COMPLETED" {
				return st.Output, nil
			}
			if st.Status == "WAITING" || st.Status == "UNKNOWN" {
				return nil, errSuspended
			}
			if st.Status != "READY" {
				return nil, automation.Conflict("tool step " + st.Status)
			}
			if n.Kind == "call" {
				if st.Decision == nil || !st.Decision.Enforced || st.Decision.Decision != protocol.DecisionAllow {
					if !mutated {
						return nil, &Fallback{"ADMISSION_NOT_ENFORCED"}
					}
					return nil, errSuspended
				}
			}
			once, e := s.Runtime.StartStepOnce(ctx, tenant, claim.WorkID, n.ID, protocol.StepResult{RuntimeAuth: auth, Attempt: st.Attempt})
			if e != nil {
				return nil, e
			}
			if !once {
				return nil, errSuspended
			}
			identity := ""
			if n.Kind == "call" {
				mutated = true
				identity = *input.Action.EffectKey
			}
			callCtx, stop := context.WithTimeout(ctx, 15*time.Second)
			data, callErr := s.Registry.Call(callCtx, tenant, b, args, identity)
			stop()
			var schemaFault *automation.Fault
			if callErr != nil && errors.As(callErr, &schemaFault) && schemaFault.Status == 400 {
				if err := s.recordRuntimeDrift(ctx, tenant, path, b.ID); err != nil {
					return nil, err
				}
			}

			result := protocol.StepResult{RuntimeAuth: auth, Attempt: st.Attempt, Status: "COMPLETED", Output: data}
			if callErr != nil {
				result.Status = "FAILED"
				result.Error = "TOOL_UNAVAILABLE"
				if n.Kind == "call" {
					result.Status = "UNKNOWN"
				}
				result.Output = json.RawMessage("null")
			}
			st, e = s.Runtime.CompleteStep(ctx, tenant, claim.WorkID, n.ID, result)
			if e != nil {
				return nil, e
			}
			if st.Status == "WAITING" || st.Status == "UNKNOWN" {
				return nil, errSuspended
			}
			if callErr != nil {
				if !mutated {
					return nil, &Fallback{"TOOL_UNAVAILABLE"}
				}
				return nil, callErr
			}
			if st.Status != "COMPLETED" {
				return nil, automation.Conflict("tool result " + st.Status)
			}
			return data, nil
		})
	}
	if errors.Is(runErr, errSuspended) {
		var nativeStatus string
		if e = s.Pool.QueryRow(ctx, `SELECT status FROM work WHERE tenant_id=$1 AND id=$2`, tenant, claim.WorkID).Scan(&nativeStatus); e != nil {
			return e
		}
		if nativeStatus == "RUNNING" {
			_, e = s.Runtime.Begin(ctx, tenant, claim.WorkID, protocol.StepInput{RuntimeAuth: auth, Key: fmt.Sprintf("control:%d", claim.Fence), Type: "WAIT", Wait: &protocol.WaitInput{Type: "TIMER", DelayMS: 2000}})
			if e != nil {
				return e
			}
		}

		_, e = s.Pool.Exec(ctx, `UPDATE compiled_runs SET status='WAITING' WHERE tenant_id=$1 AND id=$2`, tenant, id)
		return e
	}
	status := "COMPLETED"
	served := "code"
	reason := ""
	workStatus := "COMPLETED"
	var fallback *Fallback
	if runErr != nil {
		status = "FAILED"
		workStatus = "FAILED"
		reason = errorCode(runErr)
		if errors.As(runErr, &fallback) && !mutated {
			status = "FALLBACK"
			served = "fallback"
			reason = fallback.Reason
		} else if mutated {
			status = "UNKNOWN"
			reason = "RECOVERY_REQUIRED"
		}
	}
	if len(output) == 0 {
		output = []byte("null")
	}
	if runCtx.Err() != nil {
		return runCtx.Err()
	}
	cipher, e := s.Cipher.Encrypt(output)
	if e != nil {
		return e
	}
	return s.Runtime.FinishWith(runCtx, tenant, claim.WorkID, protocol.StepResult{RuntimeAuth: auth, Status: workStatus, Output: output, Error: reason}, func(tx pgx.Tx) error {
		_, e := tx.Exec(runCtx, `UPDATE compiled_runs SET status=$3,served_by=$4,reason=NULLIF($5,''),output_ciphertext=$6,duration_ms=$7,completed_at=now() WHERE tenant_id=$1 AND id=$2`, tenant, id, status, served, reason, cipher, time.Since(start).Milliseconds())
		return e
	})
}

func (s *Service) recordRuntimeDrift(ctx context.Context, tenant, path, binding string) error {
	return automation.Transaction(ctx, s.Pool, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `INSERT INTO compiled_drift(tenant_id,path_id,id,kind,binding_id,detail)VALUES($1,$2,$3,'schema',$4,'Program/tool data no longer matches the registered schema')ON CONFLICT(tenant_id,path_id,id)DO UPDATE SET open=true,fixed_by=NULL`, tenant, path, "runtime:"+binding, binding)
		if e != nil {
			return e
		}
		return automation.Audit(ctx, tx, tenant, "system", "path.drift", path, nil, map[string]string{"binding_id": binding, "kind": "schema"})
	})
}
