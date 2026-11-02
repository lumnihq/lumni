package compiled

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/lumnihq/lumni/internal/automation"
	"github.com/lumnihq/lumni/internal/contracts"
	"github.com/lumnihq/lumni/pkg/protocol"
)

type job struct {
	Tenant, ID, Kind, Subject, By, Token string
	Revision, Attempts                   int
}

func (s *Service) Run(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if e := s.RuntimeOnce(ctx); e != nil {
					slog.Error("compiled runtime iteration failed", "code", errorCode(e))
				}
			}
		}
	}()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if e := s.Once(ctx); e != nil {
				slog.Error("compiler job failed", "code", errorCode(e))
			}
			if e := s.ShadowOnce(ctx); e != nil {
				slog.Error("shadow comparison failed", "code", errorCode(e))
			}
			if e := s.Maintain(ctx); e != nil {
				slog.Error("compiled maintenance failed", "code", errorCode(e))
			}
		}
	}
}
func (s *Service) Once(ctx context.Context) error {
	var j job
	var input []byte
	j.Token = uuid.NewString()
	e := s.Pool.QueryRow(ctx, `WITH candidate AS(SELECT tenant_id,id FROM automation_jobs WHERE attempts<3 AND (status='PENDING' OR(status='RUNNING' AND claim_until<now())) ORDER BY created_at,id FOR UPDATE SKIP LOCKED LIMIT 1)UPDATE automation_jobs j SET status='RUNNING',attempts=attempts+1,claim_token=$1,claim_until=now()+interval '5 minutes',updated_at=now() FROM candidate c WHERE j.tenant_id=c.tenant_id AND j.id=c.id RETURNING j.tenant_id::text,j.id,j.kind,j.subject_id,j.by,j.input,j.attempts`, j.Token).Scan(&j.Tenant, &j.ID, &j.Kind, &j.Subject, &j.By, &input, &j.Attempts)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	var in struct {
		Revision int `json:"revision"`
	}
	if e = json.Unmarshal(input, &in); e != nil {
		return e
	}
	j.Revision = in.Revision
	var result any
	if j.Kind == "discover" {
		result, e = s.discover(ctx, j)
	} else {
		result, e = s.compile(ctx, j)
	}
	status := "COMPLETED"
	code := ""
	if e != nil {
		status = "FAILED"
		code = errorCode(e)
		var fault *automation.Fault
		if j.Attempts < 3 && !errors.As(e, &fault) {
			status = "PENDING"
		}
	}
	finishErr := automation.Transaction(ctx, s.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE automation_jobs SET status=$4,result=$5,error=NULLIF($6,''),claim_until=NULL,updated_at=now() WHERE tenant_id=$1 AND id=$2 AND claim_token=$3 AND status='RUNNING'`, j.Tenant, j.ID, j.Token, status, automation.JSON(result), code)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return automation.Conflict("job claim lost")
		}
		if status == "FAILED" {
			if _, err = tx.Exec(ctx, `UPDATE compiled_versions SET status='rolled-back' WHERE tenant_id=$1 AND compilation_job=$2 AND NOT replay_complete`, j.Tenant, j.ID); err != nil {
				return err
			}
		}
		return automation.Audit(ctx, tx, j.Tenant, j.By, "job."+status, j.ID, nil, map[string]string{"code": code})
	})
	if finishErr != nil {
		return finishErr
	}
	return e
}
func guardJob(ctx context.Context, tx pgx.Tx, j job) error {
	var ok bool
	e := tx.QueryRow(ctx, `SELECT status='RUNNING' AND claim_token=$3 AND claim_until>now() FROM automation_jobs WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, j.Tenant, j.ID, j.Token).Scan(&ok)
	if e != nil {
		return e
	}
	if !ok {
		return automation.Conflict("job claim expired")
	}
	return nil
}
func (s *Service) discover(ctx context.Context, j job) (any, error) {
	count := 0
	e := automation.Transaction(ctx, s.Pool, func(tx pgx.Tx) error {
		if e := guardJob(ctx, tx, j); e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `SELECT metadata->>'logical_signature',actor_id,operation FROM path_traces WHERE tenant_id=$1 AND payload_ciphertext IS NOT NULL AND completed_at>=now()-interval '30 days' GROUP BY metadata->>'logical_signature',signature,actor_id,operation HAVING count(*)>=2 ORDER BY metadata->>'logical_signature' LIMIT 1000`, j.Tenant)
		if e != nil {
			return e
		}
		type candidate struct{ sig, actor, op string }
		all := []candidate{}
		for rows.Next() {
			var c candidate
			if e = rows.Scan(&c.sig, &c.actor, &c.op); e != nil {
				break
			}
			all = append(all, c)
		}
		if e == nil {
			e = rows.Err()
		}
		rows.Close()
		if e != nil {
			return e
		}
		for _, c := range all {
			id := "path_" + c.sig[:32]
			tag, e := tx.Exec(ctx, `INSERT INTO compiled_paths(tenant_id,id,name,actor_id,operation,signature)VALUES($1,$2,$3,$4,$5,$6)ON CONFLICT(tenant_id,signature)DO NOTHING`, j.Tenant, id, c.op, c.actor, c.op, c.sig)
			if e != nil {
				return e
			}
			count += int(tag.RowsAffected())
		}
		return automation.Audit(ctx, tx, j.Tenant, j.By, "paths.discovered", j.ID, nil, map[string]int{"created": count})
	})
	return map[string]int{"created": count}, e
}
func (s *Service) compile(ctx context.Context, j job) (any, error) {
	if !s.Enabled(ctx, j.Tenant) {
		return nil, automation.Conflict("compiler configuration/consent unavailable")
	}
	if result, found, e := s.resumeCompilation(ctx, j); found || e != nil {
		return result, e
	}
	var signature string
	var revision int
	if e := s.Pool.QueryRow(ctx, `SELECT signature,revision FROM compiled_paths WHERE tenant_id=$1 AND id=$2`, j.Tenant, j.Subject).Scan(&signature, &revision); e != nil {
		return nil, e
	}
	if revision != j.Revision {
		return nil, automation.Conflict("compilation superseded")
	}
	rows, e := s.Pool.Query(ctx, `SELECT id FROM path_traces WHERE tenant_id=$1 AND metadata->>'logical_signature'=$2 AND payload_ciphertext IS NOT NULL AND completed_at>=now()-interval '30 days' ORDER BY completed_at DESC,id DESC LIMIT 500`, j.Tenant, signature)
	if e != nil {
		return nil, e
	}
	traceIDs := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			break
		}
		traceIDs = append(traceIDs, id)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return nil, e
	}
	traces := []protocol.Trace{}
	selectedBytes := 0
	for _, id := range traceIDs {
		t, e := s.ReadTrace(ctx, j.Tenant, id)
		if e != nil {
			return nil, e
		}
		compatible := true
		for _, st := range t.Steps {
			if st.BindingID != "" {
				if _, err := s.Registry.Get(ctx, j.Tenant, st.BindingID, st.BindingVersion); err != nil {
					compatible = false
					break
				}
			}
		}
		if compatible {
			size := len(automation.JSON(t))
			if selectedBytes+size > 1<<20 {
				continue
			}
			selectedBytes += size
			traces = append(traces, t)
		}
	}
	sortTraces(traces)
	if len(traces) < 2 {
		return nil, automation.Conflict("at least two complete retained traces required")
	}
	held := (len(traces) + 4) / 5
	train, test := traces[:len(traces)-held], traces[len(traces)-held:]
	request := Synthesis{Traces: train, Bindings: []protocol.ToolBinding{}, Regressions: []json.RawMessage{}}
	allowed := map[string]bool{}
	for _, t := range train {
		for _, st := range t.Steps {
			if st.BindingID != "" && !allowed[st.BindingID] {
				b, e := s.Registry.Get(ctx, j.Tenant, st.BindingID, st.BindingVersion)
				if e != nil {
					return nil, e
				}
				allowed[b.ID] = true
				request.Bindings = append(request.Bindings, b)
			}
		}
	}
	rows, e = s.Pool.Query(ctx, `SELECT c.trace_id,c.label,c.output_ciphertext FROM compiled_replay_cases c WHERE c.tenant_id=$1 AND c.path_id=$2 AND c.label IS NOT NULL ORDER BY c.created_at,c.id LIMIT 500`, j.Tenant, j.Subject)
	if e != nil {
		return nil, e
	}
	type review struct {
		id, right string
		cipher    []byte
	}
	reviews := []review{}
	for rows.Next() {
		var r review
		if e = rows.Scan(&r.id, &r.right, &r.cipher); e != nil {
			break
		}
		reviews = append(reviews, r)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return nil, e
	}
	heldIDs := map[string]bool{}
	for _, t := range test {
		heldIDs[t.ID] = true
	}
	for _, r := range reviews {
		if heldIDs[r.id] {
			continue
		}
		t, e := s.ReadTrace(ctx, j.Tenant, r.id)
		if e != nil {
			continue
		}
		var expected any
		_ = json.Unmarshal(t.Output, &expected)
		if r.right == "code" {
			b, e := s.Cipher.Decrypt(r.cipher)
			if e != nil {
				continue
			}
			_ = json.Unmarshal(b, &expected)
		}
		request.Regressions = append(request.Regressions, automation.JSON(map[string]any{"trace": t, "expected": expected, "right": r.right}))
	}
	if j.Kind == "recompile" {
		var raw []byte
		if e = s.Pool.QueryRow(ctx, `SELECT program FROM compiled_versions WHERE tenant_id=$1 AND path_id=$2 AND status='live'`, j.Tenant, j.Subject).Scan(&raw); e == nil {
			var p protocol.CompiledProgram
			if json.Unmarshal(raw, &p) == nil {
				request.Previous = &p
			}
		} else if !errors.Is(e, pgx.ErrNoRows) {
			return nil, e
		}
	}
	genCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	program, e := s.Generator.Generate(genCtx, request)
	cancel()
	if e != nil {
		return nil, e
	}
	if e = ValidateProgram(program); e != nil {
		return nil, e
	}
	for _, n := range program.Nodes {
		if n.BindingID != "" && !allowed[n.BindingID] {
			return nil, automation.Invalid("generated program references a tool absent from synthesis traces")
		}
	}
	pins, e := s.bindings(ctx, j.Tenant, program)
	if e != nil {
		return nil, e
	}
	version := 0
	e = automation.Transaction(ctx, s.Pool, func(tx pgx.Tx) error {
		if e := guardJob(ctx, tx, j); e != nil {
			return e
		}
		var rev int
		if e := tx.QueryRow(ctx, `SELECT revision FROM compiled_paths WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, j.Tenant, j.Subject).Scan(&rev); e != nil {
			return e
		}
		if rev != j.Revision {
			return automation.Conflict("compilation superseded")
		}
		if e := tx.QueryRow(ctx, `SELECT COALESCE(max(v),0)+1 FROM compiled_versions WHERE tenant_id=$1 AND path_id=$2`, j.Tenant, j.Subject).Scan(&version); e != nil {
			return e
		}
		status := "candidate"
		if j.Kind == "recompile" {
			status = "shadow"
		}
		_, e := tx.Exec(ctx, `INSERT INTO compiled_versions(tenant_id,path_id,v,status,program,program_hash,bindings,synthesis_ids,replay_ids,note,compilation_job)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, j.Tenant, j.Subject, version, status, automation.JSON(program), automation.Hash(program), automation.JSON(pins), ids(train), ids(test), "Generated from retained traces; requires replay and human review", j.ID)
		return e
	})
	if e != nil {
		return nil, e
	}
	for _, t := range test {
		if e = s.replay(ctx, j.Tenant, j.Subject, version, program, t, "replay", nil); e != nil {
			return nil, e
		}
	}
	// Reviewed cases become persistent regression tests, independent of the held-out set.
	for _, r := range reviews {
		t, e := s.ReadTrace(ctx, j.Tenant, r.id)
		if e != nil {
			continue
		}
		var expected []byte
		if r.right == "code" {
			expected, e = s.Cipher.Decrypt(r.cipher)
			if e != nil {
				continue
			}
		}
		if e = s.replay(ctx, j.Tenant, j.Subject, version, program, t, "regression", expected); e != nil {
			return nil, e
		}
	}
	e = automation.Transaction(ctx, s.Pool, func(tx pgx.Tx) error {
		if e := guardJob(ctx, tx, j); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `UPDATE compiled_versions SET replay_complete=true WHERE tenant_id=$1 AND path_id=$2 AND v=$3`, j.Tenant, j.Subject, version); e != nil {
			return e
		}
		return automation.Audit(ctx, tx, j.Tenant, j.By, "path.compiled", j.Subject, nil, map[string]any{"version": version, "program_hash": automation.Hash(program), "synthesis": ids(train), "held_out": ids(test)})
	})
	return map[string]any{"path_id": j.Subject, "version": version}, e
}
func (s *Service) replay(ctx context.Context, tenant, path string, v int, p protocol.CompiledProgram, t protocol.Trace, kind string, expectedOverride []byte) error {
	used := map[int]bool{}
	result, e := Execute(ctx, p, t.Input, t.CompletedAt, func(ctx context.Context, n protocol.ProgramNode, args json.RawMessage) (json.RawMessage, error) {
		for i, st := range t.Steps {
			if !used[i] && st.BindingID == n.BindingID && st.BindingVersion == n.BindingVersion && automation.Hash(json.RawMessage(st.Input)) == automation.Hash(json.RawMessage(args)) {
				used[i] = true
				return st.Output, nil
			}
		}
		return nil, &Fallback{"UNRECORDED_TOOL_INPUT"}
	})
	expected := t.Output
	if len(expectedOverride) > 0 {
		expected = expectedOverride
	}
	outcome := "agree"
	if e != nil {
		outcome = "out"
		result = []byte("null")
	} else {
		if automation.Hash(json.RawMessage(result)) != automation.Hash(json.RawMessage(expected)) {
			outcome = "disagree"
		}
		for i, st := range t.Steps {
			if st.Kind == "call" && !used[i] {
				outcome = "disagree"
			}
		}
	}
	cipher, err := s.Cipher.Encrypt(result)
	if err != nil {
		return err
	}
	id := "case_" + automation.Hash([]any{path, v, t.ID, kind})[:32]
	_, err = s.Pool.Exec(ctx, `INSERT INTO compiled_replay_cases(tenant_id,path_id,version,id,trace_id,kind,result,output_ciphertext)VALUES($1,$2,$3,$4,$5,$6,$7,$8)ON CONFLICT(tenant_id,path_id,version,trace_id,kind)DO NOTHING`, tenant, path, v, id, t.ID, kind, outcome, cipher)
	return err
}
func (s *Service) ShadowOnce(ctx context.Context) error {
	var tenant, path, trace string
	var v int
	var b []byte
	e := s.Pool.QueryRow(ctx, `SELECT v.tenant_id::text,v.path_id,v.v,v.program,t.id FROM compiled_versions v JOIN compiled_paths p ON p.tenant_id=v.tenant_id AND p.id=v.path_id JOIN path_traces t ON t.tenant_id=p.tenant_id AND t.metadata->>'logical_signature'=p.signature WHERE v.status='shadow' AND t.created_at>v.created_at AND t.payload_ciphertext IS NOT NULL AND NOT EXISTS(SELECT 1 FROM compiled_replay_cases c WHERE c.tenant_id=v.tenant_id AND c.path_id=v.path_id AND c.version=v.v AND c.trace_id=t.id AND c.kind='shadow') ORDER BY t.created_at LIMIT 1`).Scan(&tenant, &path, &v, &b, &trace)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	var p protocol.CompiledProgram
	if e = json.Unmarshal(b, &p); e != nil {
		return e
	}
	t, e := s.ReadTrace(ctx, tenant, trace)
	if e != nil {
		return e
	}
	return s.replay(ctx, tenant, path, v, p, t, "shadow", nil)
}
func (s *Service) Maintain(ctx context.Context) error {
	if e := contracts.New(s.Pool, s.Cipher).VerifyWatchOnce(ctx, s.Runtime.VerifyAssertion); e != nil {
		return e
	}
	if _, e := s.Pool.Exec(ctx, `UPDATE contract_assertions c SET status='FAILED',reason='CONTRACT_VERIFICATION_DEADLINE',claim_until=NULL,updated_at=now() FROM action_results ar WHERE ar.tenant_id=c.tenant_id AND ar.action_id=c.action_id AND c.status IN('PENDING','UNKNOWN') AND ar.recorded_at+make_interval(secs=>c.within_seconds)<now()`); e != nil {
		return e
	}

	// Failed/expired claims are visible instead of leaving jobs permanently running.
	if _, e := s.Pool.Exec(ctx, `WITH dead AS(UPDATE automation_jobs SET status='FAILED',error='JOB_ATTEMPTS_EXHAUSTED',updated_at=now() WHERE status='RUNNING' AND claim_until<now() AND attempts>=3 RETURNING tenant_id,id)UPDATE compiled_versions v SET status='rolled-back' FROM dead WHERE v.tenant_id=dead.tenant_id AND v.compilation_job=dead.id AND NOT v.replay_complete`); e != nil {
		return e
	}

	if _, e := s.Pool.Exec(ctx, `WITH changed AS(INSERT INTO compiled_drift(tenant_id,path_id,id,kind,binding_id,detail)
 SELECT DISTINCT v.tenant_id,v.path_id,'binding:'||(n->>'binding_id')||':'||b.version,
 CASE WHEN old.document->>'policy_fingerprint' IS DISTINCT FROM b.document->>'policy_fingerprint' THEN 'policy' ELSE 'schema' END,n->>'binding_id','Binding, policy or read capability changed'
 FROM compiled_versions v CROSS JOIN LATERAL jsonb_array_elements(v.program->'nodes') n
 JOIN tool_bindings b ON b.tenant_id=v.tenant_id AND b.id=n->>'binding_id'
 JOIN tool_binding_versions old ON old.tenant_id=b.tenant_id AND old.binding_id=b.id AND old.version=(n->>'binding_version')::int
 WHERE v.status='live' AND (NOT b.enabled OR b.version<>(n->>'binding_version')::int OR ((b.document->>'read_only')::boolean AND NOT EXISTS(SELECT 1 FROM integration_read_grants g WHERE g.tenant_id=b.tenant_id AND g.binding_id=b.id AND g.binding_version=b.version AND g.revoked_at IS NULL AND g.objects @> (b.document->'objects'))))
 ON CONFLICT(tenant_id,path_id,id)DO UPDATE SET open=true,fixed_by=NULL WHERE NOT compiled_drift.open RETURNING tenant_id,path_id,id)
 INSERT INTO audit_events(tenant_id,by,type,target)SELECT tenant_id,'system','path.drift',jsonb_build_object('path_id',path_id,'id',id)FROM changed`); e != nil {
		return e
	}
	if _, e := s.Pool.Exec(ctx, `INSERT INTO compiled_drift(tenant_id,path_id,id,kind,detail)SELECT DISTINCT c.tenant_id,c.path_id,'regression:'||c.id,'policy','A reviewed regression case failed' FROM compiled_replay_cases c JOIN compiled_versions v ON v.tenant_id=c.tenant_id AND v.path_id=c.path_id AND v.v=c.version WHERE v.status='live' AND c.kind='regression' AND c.result<>'agree' AND c.label IS DISTINCT FROM 'code' ON CONFLICT DO NOTHING`); e != nil {
		return e
	}

	for _, q := range []string{`UPDATE path_traces SET payload_ciphertext=NULL WHERE created_at<now()-$1::interval`, `UPDATE compiled_replay_cases SET output_ciphertext=NULL WHERE created_at<now()-$1::interval`,
		`UPDATE contract_assertions SET observed_ciphertext=NULL WHERE updated_at<now()-$1::interval`, `UPDATE compiled_runs SET input_ciphertext=NULL,output_ciphertext=NULL WHERE completed_at<now()-$1::interval`,
		`UPDATE action_contract_rules cr SET facts_ciphertext=NULL WHERE facts_ciphertext IS NOT NULL AND (evaluated_at<now()-$1::interval OR EXISTS(SELECT 1 FROM actions a JOIN work w ON w.tenant_id=a.tenant_id AND w.id=a.work_id WHERE a.tenant_id=cr.tenant_id AND a.id=cr.action_id AND w.payload_expires_at<now()))`} {
		if _, e := s.Pool.Exec(ctx, q, s.Retention.String()); e != nil {
			return e
		}
	}
	return nil
}

// Resume an artifact committed before a worker crash; never generate a second version for one job.
func (s *Service) resumeCompilation(ctx context.Context, j job) (any, bool, error) {
	var version int
	var data []byte
	var traceIDs []string
	var complete bool
	e := s.Pool.QueryRow(ctx, `SELECT v,program,replay_ids,replay_complete FROM compiled_versions WHERE tenant_id=$1 AND compilation_job=$2`, j.Tenant, j.ID).Scan(&version, &data, &traceIDs, &complete)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if e != nil {
		return nil, false, e
	}
	result := map[string]any{"path_id": j.Subject, "version": version}
	if complete {
		return result, true, nil
	}
	var p protocol.CompiledProgram
	if e = json.Unmarshal(data, &p); e != nil {
		return nil, true, e
	}
	for _, id := range traceIDs {
		t, e := s.ReadTrace(ctx, j.Tenant, id)
		if e != nil {
			return nil, true, e
		}
		if e = s.replay(ctx, j.Tenant, j.Subject, version, p, t, "replay", nil); e != nil {
			return nil, true, e
		}
	}
	e = automation.Transaction(ctx, s.Pool, func(tx pgx.Tx) error {
		if e := guardJob(ctx, tx, j); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, `UPDATE compiled_versions SET replay_complete=true WHERE tenant_id=$1 AND path_id=$2 AND v=$3`, j.Tenant, j.Subject, version)
		return e
	})
	return result, true, e
}
