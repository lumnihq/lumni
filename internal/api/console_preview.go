package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"lumni/internal/resource"
	"lumni/pkg/protocol"
)

func (s *Server) previewPolicyRule(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Rule struct {
			Operation    string   `json:"operation"`
			ResourceType string   `json:"resource_type"`
			Pointer      *string  `json:"argument_pointer"`
			Threshold    *float64 `json:"numeric_threshold"`
			MinimumRisk  *string  `json:"minimum_risk"`
			Outcome      string   `json:"outcome"`
			Reason       string   `json:"reason_code"`
		} `json:"rule"`
		From, To time.Time
		TZ       string `json:"tz"`
		Examples *int   `json:"examples"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if in.Rule.Operation == "" {
		in.Rule.Operation = "*"
	}
	if in.Rule.ResourceType == "" {
		in.Rule.ResourceType = "*"
	}
	if in.Rule.Reason == "" || !contains([]string{"REQUIRE_APPROVAL", "REJECT", "WARN"}, in.Rule.Outcome) || (in.Rule.Pointer != nil && !strings.HasPrefix(*in.Rule.Pointer, "/")) || ((in.Rule.Pointer == nil) != (in.Rule.Threshold == nil)) {
		s.fail(w, r, 400, "INVALID_RULE", "Rule outcome, reason, and paired JSON pointer/threshold are required", nil)
		return
	}
	if in.Rule.MinimumRisk != nil && !contains([]string{"LOW", "MEDIUM", "HIGH", "CRITICAL"}, *in.Rule.MinimumRisk) {
		s.fail(w, r, 400, "INVALID_RULE", "minimum_risk must be LOW, MEDIUM, HIGH, or CRITICAL", nil)
		return
	}
	if in.TZ == "" {
		in.TZ = "UTC"
	}
	z, e := time.LoadLocation(in.TZ)
	if e != nil || !in.From.Before(in.To) || in.To.Sub(in.From) > 30*24*time.Hour {
		s.fail(w, r, 400, "INVALID_TIME_RANGE", "Valid timezone and a positive range of at most 30 days are required", nil)
		return
	}
	exampleLimit := 3
	if in.Examples != nil {
		exampleLimit = *in.Examples
	}
	if exampleLimit < 0 || exampleLimit > 50 {
		s.fail(w, r, 400, "INVALID_QUERY", "examples must be 0–50", nil)
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT a.id,a.requested_at,a.risk,a.arguments,a.arguments_ciphertext,a.metadata,COALESCE(d.decision,'ALLOW'),COALESCE(d.evidence,'[]'),EXISTS(SELECT 1 FROM approvals p WHERE p.tenant_id=a.tenant_id AND p.action_id=a.id AND p.status='APPROVED') FROM actions a LEFT JOIN LATERAL(SELECT decision,evidence FROM decisions WHERE tenant_id=a.tenant_id AND action_id=a.id ORDER BY created_at DESC,id DESC LIMIT 1)d ON true WHERE a.tenant_id=$1 AND a.requested_at>=$2 AND a.requested_at<$3 AND ($4='*' OR a.operation=$4) AND ($5='*' OR EXISTS(SELECT 1 FROM action_resources ar JOIN resources res ON res.tenant_id=ar.tenant_id AND res.id=ar.resource_id WHERE ar.tenant_id=a.tenant_id AND ar.action_id=a.id AND res.type=$5)) ORDER BY a.requested_at,a.id`, tenant(r), in.From, in.To, in.Rule.Operation, in.Rule.ResourceType)
	if err != nil {
		s.respond(w, r, 0, nil, err)
		return
	}
	defer rows.Close()
	matched, handled, falsePos, unavailable := 0, 0, 0, 0
	days := map[string]int{}
	hours := make([]int, 24)
	examples := []map[string]any{}
	rank := func(v string) int {
		switch v {
		case "LOW":
			return 0
		case "MEDIUM":
			return 1
		case "HIGH":
			return 2
		case "CRITICAL":
			return 3
		}
		return -1
	}
	for rows.Next() {
		var id, risk, then string
		var at time.Time
		var args, cipher, meta, evidence []byte
		var approved bool
		if err = rows.Scan(&id, &at, &risk, &args, &cipher, &meta, &then, &evidence, &approved); err != nil {
			break
		}
		if in.Rule.MinimumRisk != nil && rank(risk) < rank(*in.Rule.MinimumRisk) {
			continue
		}
		if in.Rule.Pointer != nil {
			if len(cipher) > 0 {
				args, err = s.secrets.Decrypt(cipher)
				if err != nil {
					break
				}
			}
			if len(args) == 0 {
				unavailable++
				continue
			}
			var value any
			if json.Unmarshal(args, &value) != nil {
				unavailable++
				continue
			}
			v, ok := resource.LookupPointer(value, *in.Rule.Pointer)
			number, isNumber := v.(float64)
			if !ok || !isNumber || number <= *in.Rule.Threshold {
				continue
			}
		}
		matched++
		date := at.In(z).Format("2006-01-02")
		days[date]++
		hours[at.In(z).Hour()]++
		var ev []protocol.Evidence
		_ = json.Unmarshal(evidence, &ev)
		already := false
		for _, e := range ev {
			if e.Type == "POLICY" && then == in.Rule.Outcome {
				already = true
			}
		}
		if already {
			handled++
		}
		if approved && in.Rule.Outcome != "ALLOW" {
			falsePos++
		}
		if len(examples) < exampleLimit {
			var metadata map[string]any
			_ = json.Unmarshal(meta, &metadata)
			examples = append(examples, map[string]any{"action_id": id, "summary": metadata["summary"], "at": at, "decision_then": then, "decision_with_rule": in.Rule.Outcome})
		}
	}
	if err == nil {
		err = rows.Err()
	}
	byDay := []map[string]any{}
	local := in.From.In(z)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, z)
	for d := start; d.Before(in.To); d = d.AddDate(0, 0, 1) {
		date := d.Format("2006-01-02")
		byDay = append(byDay, map[string]any{"date": date, "matched": days[date]})
	}
	s.respond(w, r, 200, map[string]any{"matched": matched, "would_change": matched - handled, "already_handled": handled, "by_day": byDay, "by_hour": hours, "examples": examples, "false_positive_estimate": falsePos, "unavailable_arguments": unavailable, "truncated": unavailable > 0}, err)
}
