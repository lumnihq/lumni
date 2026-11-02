package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/lumnihq/lumni/internal/automation"
	"github.com/lumnihq/lumni/internal/compiled"
	"github.com/lumnihq/lumni/pkg/protocol"
)

const pathStateSQL = `CASE WHEN p.paused THEN 'paused' WHEN EXISTS(SELECT 1 FROM compiled_versions v WHERE v.tenant_id=p.tenant_id AND v.path_id=p.id AND status='live') THEN CASE WHEN EXISTS(SELECT 1 FROM compiled_drift d WHERE d.tenant_id=p.tenant_id AND d.path_id=p.id AND d.open AND d.fixed_by IS NULL) THEN 'drifted' ELSE 'live' END WHEN EXISTS(SELECT 1 FROM compiled_versions v WHERE v.tenant_id=p.tenant_id AND v.path_id=p.id AND status='shadow') THEN 'shadow' WHEN EXISTS(SELECT 1 FROM compiled_versions v WHERE v.tenant_id=p.tenant_id AND v.path_id=p.id AND status='candidate') THEN 'candidate' ELSE 'watching' END`

func (s *Server) pathList(w http.ResponseWriter, r *http.Request) {
	stats := compiled.StatisticsProjection("lp.tenant_id", "lp.id", "date_trunc('day',now())", "now()")
	s.pagedList(w, r, listSpec{From: `(SELECT p.*,` + pathStateSQL + ` AS status FROM compiled_paths p)lp`, Tenant: "lp.tenant_id", ID: "lp.id", Key: "lp.id", Time: "lp.created_at", Actor: "lp.actor_id", Operation: "lp.operation", Status: "lp.status", Search: "lp.name||' '||lp.operation", Condition: `EXISTS(SELECT 1 FROM compiled_versions v WHERE v.tenant_id=lp.tenant_id AND v.path_id=lp.id)`, JSON: `to_jsonb(lp)-'tenant_id'||jsonb_build_object('stats',(` + stats + `),'week',(SELECT COALESCE(jsonb_agg(jsonb_build_object('date',d.day::date,'served',d.n)ORDER BY d.day),'[]') FROM(SELECT gs.day,count(cr.id)FILTER(WHERE cr.served_by='code' AND cr.status='COMPLETED' AND NOT cr.reported)n FROM generate_series(date_trunc('day',now())-interval '6 days',date_trunc('day',now()),interval '1 day')gs(day)LEFT JOIN compiled_runs cr ON cr.tenant_id=lp.tenant_id AND cr.path_id=lp.id AND cr.created_at>=gs.day AND cr.created_at<gs.day+interval '1 day' GROUP BY gs.day)d))`}, "", false)
}
func (s *Server) pathSuggestions(w http.ResponseWriter, r *http.Request) {
	s.pagedList(w, r, listSpec{From: "compiled_paths p", Tenant: "p.tenant_id", ID: "p.id", Key: "p.id", Time: "p.created_at", Search: "p.name", Condition: `NOT EXISTS(SELECT 1 FROM compiled_versions v WHERE v.tenant_id=p.tenant_id AND v.path_id=p.id AND status IN('candidate','shadow','live'))`, JSON: `to_jsonb(p)-'tenant_id'||jsonb_build_object('status','watching','projected',true,'estimated_savings_micros_usd',NULL,'compile_agreement',NULL,'traces',(SELECT count(*) FROM path_traces t WHERE t.tenant_id=p.tenant_id AND t.metadata->>'logical_signature'=p.signature))`}, "", false)
}
func (s *Server) pathTotals(w http.ResponseWriter, r *http.Request) {
	from, to, _, e := automationRange(r, 7)
	if e != nil {
		s.automationRespond(w, r, 0, nil, e)
		return
	}
	v, e := s.pathsService().Stats(r.Context(), tenant(r), "", from, to)
	s.automationRespond(w, r, 200, v, e)
}
func (s *Server) pathGet(w http.ResponseWriter, r *http.Request) {
	v, e := s.pathsService().Get(r.Context(), tenant(r), pathID(r, "id"))
	s.automationRespond(w, r, 200, v, e)
}
func (s *Server) pathDiscover(w http.ResponseWriter, r *http.Request) {
	var in struct{}
	if !s.strict(w, r, &in) {
		return
	}
	v, e := s.pathsService().Enqueue(r.Context(), tenant(r), s.principalName(r), "", "discover", 0)
	s.automationRespond(w, r, 202, v, e)
}
func (s *Server) pathTransition(verb string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Revision int    `json:"revision"`
			Why      string `json:"why"`
		}
		if !s.strict(w, r, &in) {
			return
		}
		if len(in.Why) > 4000 {
			s.automationRespond(w, r, 0, nil, automation.Invalid("why exceeds 4000 bytes"))
			return
		}
		if verb == "compile" || verb == "recompile" {
			v, e := s.pathsService().Enqueue(r.Context(), tenant(r), s.principalName(r), pathID(r, "id"), verb, in.Revision)
			s.automationRespond(w, r, 202, v, e)
		} else {
			v, e := s.pathsService().Transition(r.Context(), tenant(r), s.principalName(r), pathID(r, "id"), verb, in.Why, in.Revision)
			s.automationRespond(w, r, 200, v, e)
		}
	}
}
func (s *Server) pathRoute(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Actor     string `json:"actor_id"`
		Operation string `json:"operation"`
	}
	if !s.strict(w, r, &in) {
		return
	}
	if in.Actor == "" || in.Operation == "" {
		s.automationRespond(w, r, 0, nil, automation.Invalid("actor_id and operation required"))
		return
	}
	v, e := s.pathsService().Route(r.Context(), tenant(r), in.Actor, in.Operation)
	s.automationRespond(w, r, 200, v, e)
}
func (s *Server) pathExecute(w http.ResponseWriter, r *http.Request) {
	var in protocol.CompiledInvocation
	if !s.strict(w, r, &in) {
		return
	}
	v, e := s.pathsService().Invoke(r.Context(), tenant(r), pathID(r, "id"), in)
	s.automationRespond(w, r, 202, v, e)
}
func (s *Server) pathLabel(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Right string `json:"right"`
	}
	if !s.strict(w, r, &in) {
		return
	}
	e := s.pathsService().Label(r.Context(), tenant(r), s.principalName(r), pathID(r, "id"), pathID(r, "case_id"), in.Right)
	s.automationRespond(w, r, 200, map[string]any{"reviewed": e == nil}, e)
}
func (s *Server) pathReplay(w http.ResponseWriter, r *http.Request) {
	v, e := strconv.Atoi(pathID(r, "v"))
	if e != nil || v < 1 {
		s.automationRespond(w, r, 0, nil, automation.Invalid("positive version required"))
		return
	}
	var data []byte
	e = s.pool.QueryRow(r.Context(), `SELECT jsonb_build_object('version',v.v,'complete',v.replay_complete,'n',count(c.id),'agree',count(c.id) FILTER(WHERE c.result='agree'),'disagree',count(c.id) FILTER(WHERE c.result='disagree'),'out',count(c.id) FILTER(WHERE c.result='out'),'rate',count(c.id) FILTER(WHERE c.result='agree')::numeric/NULLIF(count(c.id)FILTER(WHERE c.result<>'out'),0),'samples',COALESCE(jsonb_agg(jsonb_build_object('id',c.id,'trace_id',c.trace_id,'kind',c.kind,'result',c.result,'right',c.label,'reviewed_by',c.reviewed_by)ORDER BY c.created_at,c.id)FILTER(WHERE c.id IS NOT NULL),'[]')) FROM compiled_versions v LEFT JOIN compiled_replay_cases c ON c.tenant_id=v.tenant_id AND c.path_id=v.path_id AND c.version=v.v WHERE v.tenant_id=$1 AND v.path_id=$2 AND v.v=$3 GROUP BY v.v,v.replay_complete`, tenant(r), pathID(r, "id"), v).Scan(&data)
	s.automationRespond(w, r, 200, json.RawMessage(data), e)
}
func (s *Server) pathFallbacks(w http.ResponseWriter, r *http.Request) {
	if _, e := s.pathsService().Get(r.Context(), tenant(r), pathID(r, "id")); e != nil {
		s.automationRespond(w, r, 0, nil, e)
		return
	}
	from, to, z, e := automationRange(r, 7)
	if e != nil {
		s.automationRespond(w, r, 0, nil, e)
		return
	}
	if day := r.URL.Query().Get("day"); day != "" {
		d, err := time.ParseInLocation("2006-01-02", day, z)
		if err != nil {
			s.automationRespond(w, r, 0, nil, automation.Invalid("day must be YYYY-MM-DD"))
			return
		}
		from = d
		to = d.AddDate(0, 0, 1)
	}
	var data []byte
	e = s.pool.QueryRow(r.Context(), `SELECT COALESCE(jsonb_agg(jsonb_build_object('reason',reason,'count',n)ORDER BY n DESC,reason),'[]')FROM(SELECT COALESCE(reason,'UNSPECIFIED')reason,count(*)n FROM compiled_runs WHERE tenant_id=$1 AND path_id=$2 AND served_by='fallback' AND created_at>=$3 AND created_at<$4 GROUP BY reason)t`, tenant(r), pathID(r, "id"), from, to).Scan(&data)
	s.automationRespond(w, r, 200, map[string]any{"items": json.RawMessage(data), "from": from, "to": to}, e)
}
func (s *Server) pathRace(w http.ResponseWriter, r *http.Request) {
	if _, e := s.pathsService().Get(r.Context(), tenant(r), pathID(r, "id")); e != nil {
		s.automationRespond(w, r, 0, nil, e)
		return
	}
	from, to, _, e := automationRange(r, 1)
	if e != nil {
		s.automationRespond(w, r, 0, nil, e)
		return
	}
	var data []byte
	e = s.pool.QueryRow(r.Context(), `SELECT jsonb_build_object('trace_id',t.id,'case_id',c.id,'version',v.v,'llm_duration_ms',t.metadata->'duration_ms','code_duration_ms',r.duration_ms,'output_payload_available',t.payload_ciphertext IS NOT NULL) FROM compiled_paths p JOIN compiled_versions v ON v.tenant_id=p.tenant_id AND v.path_id=p.id JOIN compiled_replay_cases c ON c.tenant_id=v.tenant_id AND c.path_id=v.path_id AND c.version=v.v JOIN path_traces t ON t.tenant_id=c.tenant_id AND t.id=c.trace_id JOIN compiled_runs r ON r.tenant_id=p.tenant_id AND r.path_id=p.id AND r.version=v.v AND r.request_hash=t.input_hash WHERE p.tenant_id=$1 AND p.id=$2 AND r.status='COMPLETED' AND NOT r.reported AND r.created_at>=$3 AND r.created_at<$4 ORDER BY r.created_at DESC LIMIT 1`, tenant(r), pathID(r, "id"), from, to).Scan(&data)
	if e == pgx.ErrNoRows {
		writeJSON(w, 200, map[string]any{"available": false, "reason": "NO_COMPARABLE_RECORDED_RUN"})
		return
	}
	s.automationRespond(w, r, 200, json.RawMessage(data), e)
}
func (s *Server) pathRun(w http.ResponseWriter, r *http.Request) {
	var data []byte
	e := s.pool.QueryRow(r.Context(), `SELECT to_jsonb(r)-'tenant_id'-'input_ciphertext'-'output_ciphertext'-'input_hash'-'request_hash'||jsonb_build_object('output_available',r.output_ciphertext IS NOT NULL) FROM compiled_runs r WHERE tenant_id=$1 AND id=$2`, tenant(r), pathID(r, "id")).Scan(&data)
	if r.URL.Query().Get("include") == "output" {
		s.withScope("payload", func(w http.ResponseWriter, r *http.Request) {
			if e != nil {
				s.automationRespond(w, r, 0, nil, e)
				return
			}
			var cipher []byte
			e = s.pool.QueryRow(r.Context(), `SELECT output_ciphertext FROM compiled_runs WHERE tenant_id=$1 AND id=$2`, tenant(r), pathID(r, "id")).Scan(&cipher)
			var out map[string]any
			_ = json.Unmarshal(data, &out)
			if e == nil && len(cipher) > 0 {
				plain, err := s.secrets.Decrypt(cipher)
				e = err
				out["output"] = json.RawMessage(plain)
			}
			s.automationRespond(w, r, 200, out, e)
		})(w, r)
		return
	}
	s.automationRespond(w, r, 200, json.RawMessage(data), e)
}
func (s *Server) pathReport(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID       string          `json:"id"`
		Version  *int            `json:"version"`
		Actor    string          `json:"actor_id"`
		ServedBy string          `json:"served_by"`
		Reason   string          `json:"reason"`
		Duration int64           `json:"duration_ms"`
		Cost     *int64          `json:"cost_micros_usd"`
		Output   json.RawMessage `json:"output,omitempty"`
	}
	if !s.strict(w, r, &in) {
		return
	}
	if in.ID == "" || len(in.ID) > 200 || in.Actor == "" || in.Duration < 0 || in.Cost != nil && *in.Cost < 0 || (in.ServedBy != "llm" && in.ServedBy != "fallback") {
		s.automationRespond(w, r, 0, nil, automation.Invalid("id, actor and nonnegative llm/fallback telemetry required"))
		return
	}
	var actor string
	e := s.pool.QueryRow(r.Context(), `SELECT actor_id FROM compiled_paths WHERE tenant_id=$1 AND id=$2`, tenant(r), pathID(r, "id")).Scan(&actor)
	if e != nil {
		s.automationRespond(w, r, 0, nil, e)
		return
	}
	if actor != in.Actor {
		s.automationRespond(w, r, 0, nil, automation.Invalid("actor mismatch"))
		return
	}
	id := "reported:" + in.ID
	hash := automation.Hash(in)
	var cipher []byte
	if len(in.Output) > 0 {
		cipher, e = s.secrets.Encrypt(in.Output)
		if e != nil {
			s.automationRespond(w, r, 0, nil, e)
			return
		}
	}
	tag, e := s.pool.Exec(r.Context(), `INSERT INTO compiled_runs(tenant_id,id,path_id,version,actor_id,input_hash,status,served_by,reason,duration_ms,cost_micros_usd,reported,output_ciphertext,completed_at)VALUES($1,$2,$3,$4,$5,$6,'COMPLETED',$7,$8,$9,$10,true,$11,now())ON CONFLICT(tenant_id,id)DO NOTHING`, tenant(r), id, pathID(r, "id"), in.Version, in.Actor, hash, in.ServedBy, in.Reason, in.Duration, in.Cost, cipher)
	if e == nil && tag.RowsAffected() == 0 {
		var old string
		e = s.pool.QueryRow(r.Context(), `SELECT input_hash FROM compiled_runs WHERE tenant_id=$1 AND id=$2`, tenant(r), id).Scan(&old)
		if e == nil && old != hash {
			e = automation.Conflict("report identity mismatch")
		}
	}
	s.automationRespond(w, r, 201, map[string]any{"id": id, "reported": true}, e)
}
func (s *Server) tracePut(w http.ResponseWriter, r *http.Request) {
	var in protocol.Trace
	if !s.strict(w, r, &in) {
		return
	}
	e := s.pathsService().Trace(r.Context(), tenant(r), in)
	s.automationRespond(w, r, 201, map[string]any{"id": in.ID, "recorded": e == nil}, e)
}
func (s *Server) traceList(w http.ResponseWriter, r *http.Request) {
	s.pagedList(w, r, listSpec{From: "path_traces t", Tenant: "t.tenant_id", Key: "t.id", ID: "t.id", Time: "t.completed_at", Work: "t.work_id", Actor: "t.actor_id", Operation: "t.operation", JSON: "to_jsonb(t)-'tenant_id'-'payload_ciphertext'-'payload_hash'"}, "", false)
}
func (s *Server) tracePayload(w http.ResponseWriter, r *http.Request) {
	v, e := s.pathsService().ReadTrace(r.Context(), tenant(r), pathID(r, "id"))
	s.automationRespond(w, r, 200, v, e)
}
func (s *Server) automationJob(w http.ResponseWriter, r *http.Request) {
	var data []byte
	e := s.pool.QueryRow(r.Context(), `SELECT to_jsonb(j)-'tenant_id'-'claim_token'-'input' FROM automation_jobs j WHERE tenant_id=$1 AND id=$2`, tenant(r), pathID(r, "id")).Scan(&data)
	s.automationRespond(w, r, 200, json.RawMessage(data), e)
}
