package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/lumnihq/lumni/internal/automation"
	"github.com/lumnihq/lumni/internal/contracts"
	"github.com/lumnihq/lumni/pkg/protocol"
)

func (s *Server) contractClassify(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text string `json:"text"`
	}
	if !s.strict(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Text) == "" || len(in.Text) > 32000 {
		s.automationRespond(w, r, 0, nil, automation.Invalid("text must contain 1-32000 bytes"))
		return
	}
	out := contracts.Classify(in.Text)
	if len(out) > 50 {
		s.automationRespond(w, r, 0, nil, automation.Invalid("at most 50 sentences"))
		return
	}
	s.semanticClassification(r, out)
	writeJSON(w, 200, out)
}
func (s *Server) contractHook(w http.ResponseWriter, r *http.Request) {
	c, e := s.contractsService().Get(r.Context(), tenant(r), pathID(r, "id"))
	if e != nil {
		s.automationRespond(w, r, 0, nil, e)
		return
	}
	since := c.CreatedAt
	if v := r.URL.Query().Get("since"); v != "" {
		since, e = time.Parse(time.RFC3339Nano, v)
		if e != nil {
			s.automationRespond(w, r, 0, nil, automation.Invalid("invalid since"))
			return
		}
	}
	items := []any{}
	for _, rule := range c.Rules {
		var data []byte
		e = s.pool.QueryRow(r.Context(), `SELECT jsonb_build_object('operation',$2::text,'actor_id',NULLIF($4,''),'checks_observed',count(*),'results_observed',count(ar.action_id),'last_check_at',max(a.requested_at),'last_result_at',max(ar.recorded_at),'status',CASE WHEN count(*)=0 THEN 'not_observed' ELSE 'observed' END) FROM actions a LEFT JOIN action_results ar ON ar.tenant_id=a.tenant_id AND ar.action_id=a.id WHERE a.tenant_id=$1 AND a.operation=$2 AND a.requested_at>=$3 AND ($4='' OR a.actor_id=$4)`, tenant(r), rule.Binding.Operation, since, rule.Binding.ActorID).Scan(&data)
		if e != nil {
			break
		}
		items = append(items, json.RawMessage(data))
	}
	s.automationRespond(w, r, 200, map[string]any{"since": since, "items": items}, e)
}
func (s *Server) contractRuns(w http.ResponseWriter, r *http.Request) {
	if _, e := s.contractsService().Get(r.Context(), tenant(r), pathID(r, "id")); e != nil {
		s.automationRespond(w, r, 0, nil, e)
		return
	}
	s.pagedList(w, r, listSpec{From: `(SELECT cr.tenant_id,cr.contract_id,cr.action_id,a.actor_id,a.operation,a.requested_at,jsonb_build_object('action_id',cr.action_id,'actor_id',a.actor_id,'agent',COALESCE(ac.display_name,a.actor_id),'operation',a.operation,'at',a.requested_at,'decision',d.decision,'outcome',o.status,'receipt_id',d.id,'evidence',d.evidence,'rules',jsonb_agg(jsonb_build_object('rule_id',cr.rule_id,'revision',cr.revision,'decision',cr.decision,'reason',cr.reason,'mode',cr.mode,'verification_status',ca.status,'verification_reason',ca.reason)ORDER BY cr.rule_id)) AS data FROM action_contract_rules cr JOIN actions a ON a.tenant_id=cr.tenant_id AND a.id=cr.action_id JOIN actors ac ON ac.tenant_id=a.tenant_id AND ac.id=a.actor_id LEFT JOIN contract_assertions ca ON ca.tenant_id=cr.tenant_id AND ca.action_id=cr.action_id AND ca.contract_id=cr.contract_id AND ca.rule_id=cr.rule_id LEFT JOIN LATERAL(SELECT id,decision,evidence FROM decisions WHERE tenant_id=a.tenant_id AND action_id=a.id ORDER BY created_at DESC,id DESC LIMIT 1)d ON true LEFT JOIN outcomes o ON o.tenant_id=a.tenant_id AND o.action_id=a.id GROUP BY cr.tenant_id,cr.contract_id,cr.action_id,a.actor_id,a.operation,a.requested_at,ac.display_name,d.decision,d.id,d.evidence,o.status) t`, Tenant: "t.tenant_id", Key: "t.action_id", ID: "t.contract_id", Time: "t.requested_at", Actor: "t.actor_id", Operation: "t.operation", JSON: "t.data"}, pathID(r, "id"), false)
}
func (s *Server) contractWeek(w http.ResponseWriter, r *http.Request) {
	if _, e := s.contractsService().Get(r.Context(), tenant(r), pathID(r, "id")); e != nil {
		s.automationRespond(w, r, 0, nil, e)
		return
	}
	from, to, z, e := automationRange(r, 7)
	if e != nil {
		s.automationRespond(w, r, 0, nil, e)
		return
	}
	rows, e := s.pool.Query(r.Context(), `SELECT a.id,a.requested_at,CASE WHEN EXISTS(SELECT 1 FROM contract_assertions ca WHERE ca.tenant_id=cr.tenant_id AND ca.action_id=a.id AND ca.contract_id=cr.contract_id) THEN CASE WHEN bool_and(COALESCE((SELECT ca.status='VERIFIED' FROM contract_assertions ca WHERE ca.tenant_id=cr.tenant_id AND ca.action_id=a.id AND ca.contract_id=cr.contract_id AND ca.rule_id=cr.rule_id),true)) THEN 'VERIFIED' ELSE 'UNKNOWN' END ELSE COALESCE(o.status,'UNKNOWN') END,bool_or(cr.decision<>'ALLOW' AND cr.mode='watch'),bool_or(cr.decision<>'ALLOW' AND cr.mode='protect' AND d.enforced) FROM action_contract_rules cr JOIN actions a ON a.tenant_id=cr.tenant_id AND a.id=cr.action_id LEFT JOIN outcomes o ON o.tenant_id=a.tenant_id AND o.action_id=a.id LEFT JOIN LATERAL(SELECT enforced FROM decisions WHERE tenant_id=a.tenant_id AND action_id=a.id ORDER BY created_at DESC,id DESC LIMIT 1)d ON true WHERE cr.tenant_id=$1 AND cr.contract_id=$2 AND a.requested_at>=$3 AND a.requested_at<$4 GROUP BY a.id,a.requested_at,o.status,cr.tenant_id,cr.contract_id ORDER BY a.requested_at,a.id`, tenant(r), pathID(r, "id"), from, to)
	if e != nil {
		s.automationRespond(w, r, 0, nil, e)
		return
	}
	defer rows.Close()
	seen, verified, gaps, would, stopped := 0, 0, 0, 0, 0
	byDay := map[string]int{}
	for rows.Next() {
		var id, status string
		var at time.Time
		var watch, protect bool
		if e = rows.Scan(&id, &at, &status, &watch, &protect); e != nil {
			break
		}
		seen++
		byDay[at.In(z).Format("2006-01-02")]++
		if status == "VERIFIED" {
			verified++
		} else {
			gaps++
		}
		if watch {
			would++
		}
		if protect {
			stopped++
		}
	}
	if e == nil {
		e = rows.Err()
	}
	days := []any{}
	local := from.In(z)
	for d := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, z); d.Before(to); d = d.AddDate(0, 0, 1) {
		date := d.Format("2006-01-02")
		days = append(days, map[string]any{"date": date, "seen": byDay[date]})
	}
	s.automationRespond(w, r, 200, map[string]any{"from": from, "to": to, "tz": z.String(), "seen": seen, "verified": verified, "gaps": gaps, "would_stop": would, "stopped": stopped, "saved_micros_usd": nil, "days": days}, e)
}

type previewInput struct {
	Rules    []protocol.ContractRule `json:"rules"`
	From     time.Time               `json:"from"`
	To       time.Time               `json:"to"`
	TZ       string                  `json:"tz"`
	ActionID string                  `json:"action_id"`
	Fixture  *contracts.Snapshot     `json:"fixture"`
}

func (s *Server) contractPreview(w http.ResponseWriter, r *http.Request) {
	var in previewInput
	if !s.strict(w, r, &in) {
		return
	}
	if e := contracts.ValidateRules(in.Rules, true); e != nil {
		s.automationRespond(w, r, 0, nil, e)
		return
	}
	if in.Fixture != nil {
		if len(in.Rules) != 1 {
			s.automationRespond(w, r, 0, nil, automation.Invalid("a fixture evaluates exactly one rule"))
			return
		}
		decision, reason := contracts.Evaluate(in.Rules[0], *in.Fixture)
		writeJSON(w, 200, map[string]any{"seen": 1, "results": []any{map[string]any{"decision": decision, "reason": reason}}})
		return
	}
	if in.TZ == "" {
		in.TZ = "UTC"
	}
	z, e := time.LoadLocation(in.TZ)
	if e != nil {
		s.automationRespond(w, r, 0, nil, automation.Invalid("invalid timezone"))
		return
	}
	if in.To.IsZero() {
		in.To = time.Now().UTC()
	}
	if in.From.IsZero() {
		in.From = in.To.AddDate(0, 0, -7)
	}
	if !in.From.Before(in.To) || in.To.Sub(in.From) > 7*24*time.Hour {
		s.automationRespond(w, r, 0, nil, automation.Invalid("preview window must be at most seven days"))
		return
	}
	ops := []string{}
	for _, rule := range in.Rules {
		ops = append(ops, rule.Binding.Operation)
	}
	rows, e := s.pool.Query(r.Context(), `SELECT a.id,a.operation,a.requested_at,a.arguments,a.arguments_ciphertext,a.status,COALESCE(o.status,'UNKNOWN') FROM actions a LEFT JOIN outcomes o ON o.tenant_id=a.tenant_id AND o.action_id=a.id WHERE a.tenant_id=$1 AND a.operation=ANY($2) AND a.requested_at<$4 AND a.requested_at>=$3 AND($5='' OR a.id=$5) ORDER BY a.requested_at,a.id LIMIT 10001`, tenant(r), ops, in.From.Add(-7*24*time.Hour), in.To, in.ActionID)
	if e != nil {
		s.automationRespond(w, r, 0, nil, e)
		return
	}
	type historical struct {
		id, op, status, outcome string
		at                      time.Time
		args, cipher            []byte
	}
	all := []historical{}
	for rows.Next() {
		var h historical
		if e = rows.Scan(&h.id, &h.op, &h.at, &h.args, &h.cipher, &h.status, &h.outcome); e != nil {
			break
		}
		all = append(all, h)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		s.automationRespond(w, r, 0, nil, e)
		return
	}
	truncated := len(all) > 10000
	if truncated {
		all = all[:10000]
	}
	seen, would, verified, gaps, unavailable := 0, 0, 0, 0, 0
	examples := []any{}
	results := []any{}
	days := map[string]int{}
	prior := map[string][]time.Time{}
	for _, h := range all {
		args := h.args
		if len(h.cipher) > 0 {
			args, e = s.secrets.Decrypt(h.cipher)
			if e != nil {
				break
			}
		}
		var arguments any
		valid := len(args) > 0 && json.Unmarshal(args, &arguments) == nil
		matched := false
		blocked := false
		missing := false
		for _, rule := range in.Rules {
			if rule.Binding.Operation != h.op {
				continue
			}
			matched = true
			f := contracts.Snapshot{Arguments: arguments, EvaluatedAt: h.at, Unavailable: !valid}
			var raw []byte
			err := s.pool.QueryRow(r.Context(), `SELECT facts_ciphertext FROM action_contract_rules WHERE tenant_id=$1 AND action_id=$2 AND rule->>'type'=$3 AND rule->'binding'=$4::jsonb ORDER BY contract_id,rule_id LIMIT 1`, tenant(r), h.id, rule.Type, automation.JSON(rule.Binding)).Scan(&raw)
			if err == nil && len(raw) > 0 {
				plain, err := s.secrets.Decrypt(raw)
				if err == nil {
					_ = json.Unmarshal(plain, &f)
				}
			} else if rule.Binding.ReadBindingID != "" || rule.Type == "order-before" || rule.Type == "once-per-key" {
				f.Unavailable = true
			}
			key := ""
			if v, err := automation.Pointer(arguments, rule.Binding.KeyPointer); err == nil {
				key = rule.ID + ":" + automation.Hash(v)
			}
			if rule.Type == "rate-limit" && key != "" {
				f.PriorCount = 0
				seconds := map[string]int{"minute": 60, "hour": 3600, "day": 86400, "week": 604800}[fmt.Sprint(rule.Slots["window"])]
				for _, at := range prior[key] {
					if !at.Before(h.at.Add(-time.Duration(seconds) * time.Second)) {
						f.PriorCount++
					}
				}
			}
			decision, reason := contracts.Evaluate(rule, f)
			if reason == "CONTRACT_EVIDENCE_UNAVAILABLE" {
				missing = true
			} else if decision != protocol.DecisionAllow {
				blocked = true
			}
			if !h.at.Before(in.From) {
				results = append(results, map[string]any{"action_id": h.id, "rule_id": rule.ID, "decision": decision, "reason": reason})
			}
			if key != "" && contracts.Has([]string{"SUCCEEDED", "ACTIVE", "UNKNOWN"}, h.status) {
				prior[key] = append(prior[key], h.at)
			}
		}
		if !matched || h.at.Before(in.From) {
			continue
		}
		seen++
		if blocked {
			would++
			days[h.at.In(z).Format("2006-01-02")]++
			if len(examples) < 10 {
				examples = append(examples, map[string]any{"action_id": h.id, "at": h.at, "operation": h.op})
			}
		}
		if h.outcome == "VERIFIED" {
			verified++
		} else {
			gaps++
		}
		if missing {
			unavailable++
		}
	}
	s.automationRespond(w, r, 200, map[string]any{"from": in.From, "to": in.To, "tz": in.TZ, "seen": seen, "would_stop": would, "verified": verified, "gaps": gaps, "saved_micros_usd": nil, "days": days, "examples": examples, "results": results, "unavailable_evidence": unavailable, "truncated": truncated || unavailable > 0}, e)
}
func (s *Server) contractTests(w http.ResponseWriter, r *http.Request) {
	lang := r.URL.Query().Get("lang")
	if lang != "ts" && lang != "py" {
		s.automationRespond(w, r, 0, nil, automation.Invalid("lang must be ts or py"))
		return
	}
	id := pathID(r, "id")
	action := r.URL.Query().Get("action")
	var rule, cipher []byte
	var decision string
	e := s.pool.QueryRow(r.Context(), `SELECT rule,facts_ciphertext,decision FROM action_contract_rules WHERE tenant_id=$1 AND contract_id=$2 AND action_id=$3 ORDER BY rule_id LIMIT 1`, tenant(r), id, action).Scan(&rule, &cipher, &decision)
	if e != nil {
		s.automationRespond(w, r, 0, nil, e)
		return
	}
	if len(cipher) == 0 {
		s.automationRespond(w, r, 0, nil, automation.Conflict("fixture retention expired"))
		return
	}
	plain, e := s.secrets.Decrypt(cipher)
	if e != nil {
		s.automationRespond(w, r, 0, nil, e)
		return
	}
	var snapshot contracts.Snapshot
	var contractRule protocol.ContractRule
	if json.Unmarshal(plain, &snapshot) != nil || json.Unmarshal(rule, &contractRule) != nil {
		s.automationRespond(w, r, 0, nil, automation.Invalid("invalid retained fixture"))
		return
	}
	payload := automation.JSON(previewInput{Rules: []protocol.ContractRule{contractRule}, Fixture: &snapshot})
	expected, _ := contracts.Evaluate(contractRule, snapshot)
	body := string(automation.JSON(string(payload)))
	want := string(automation.JSON(string(expected)))
	code := ""
	if lang == "ts" {
		code = "import test from 'node:test';\nimport assert from 'node:assert/strict';\nconst fixture = JSON.parse(" + body + ");\ntest('contract regression', async () => {\n const response = await fetch(process.env.LUMNI_URL + '/v1/contracts/preview', {method:'POST', headers:{Authorization:'Bearer '+process.env.LUMNI_API_KEY,'Content-Type':'application/json'},body:JSON.stringify(fixture)});\n assert.equal(response.status,200);\n assert.equal((await response.json()).results[0].decision," + want + ");\n});\n"
	} else {
		code = "import json, os, unittest, urllib.request\nfixture = json.loads(" + body + ")\nclass ContractRegression(unittest.TestCase):\n def test_recorded_case(self):\n  req = urllib.request.Request(os.environ['LUMNI_URL']+'/v1/contracts/preview', data=json.dumps(fixture).encode(), headers={'Authorization':'Bearer '+os.environ['LUMNI_API_KEY'],'Content-Type':'application/json'})\n  with urllib.request.urlopen(req) as response:\n   self.assertEqual(json.load(response)['results'][0]['decision']," + want + ")\nif __name__ == '__main__': unittest.main()\n"
	}
	writeJSON(w, 200, map[string]any{"action_id": action, "language": lang, "filename": "contract-regression." + lang, "fixture": json.RawMessage(payload), "code": code, "expected_decision": expected})
}

func (s *Server) contractWatchEvidence(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RuleID   string                        `json:"rule_id"`
		Evidence protocol.VerificationEvidence `json:"evidence"`
	}
	if !s.strict(w, r, &in) {
		return
	}
	e := s.contractsService().WatchEvidence(r.Context(), tenant(r), pathID(r, "id"), pathID(r, "action_id"), in.RuleID, in.Evidence, "")
	s.automationRespond(w, r, 202, map[string]any{"recorded": e == nil}, e)
}
