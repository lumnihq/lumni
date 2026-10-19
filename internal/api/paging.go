package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

type pageCursor struct {
	At     time.Time `json:"at"`
	Until  time.Time `json:"until,omitempty"`
	Key    string    `json:"key"`
	Filter string    `json:"filter"`
}

func encodeCursor(c pageCursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

type pageOptions struct {
	Limit    int
	From, To *time.Time
	Cursor   *pageCursor
}

func pageFingerprint(r *http.Request) string {
	q := r.URL.Query()
	q.Del("cursor")
	q.Del("limit")
	h := sha256.Sum256([]byte(tenant(r) + "|" + r.URL.Path + "|" + q.Encode()))
	return hex.EncodeToString(h[:16])
}
func parsePage(r *http.Request) (pageOptions, error) {
	p := pageOptions{Limit: 200}
	if v := r.URL.Query().Get("limit"); v != "" {
		n, e := strconv.Atoi(v)
		if e != nil || n < 1 || n > 1000 {
			return p, errors.New("limit must be between 1 and 1000")
		}
		p.Limit = n
	}
	for _, field := range []string{"from", "to"} {
		if v := r.URL.Query().Get(field); v != "" {
			t, e := time.Parse(time.RFC3339Nano, v)
			if e != nil {
				return p, errors.New(field + " must be RFC 3339")
			}
			t = t.UTC()
			if field == "from" {
				p.From = &t
			} else {
				p.To = &t
			}
		}
	}
	if p.From != nil && p.To != nil && !p.From.Before(*p.To) {
		return p, errors.New("from must precede to")
	}
	if v := r.URL.Query().Get("cursor"); v != "" {
		b, e := base64.RawURLEncoding.DecodeString(v)
		if e != nil || len(b) > 2048 {
			return p, errors.New("invalid cursor")
		}
		var c pageCursor
		if json.Unmarshal(b, &c) != nil || c.Key == "" || c.At.IsZero() || c.Filter != pageFingerprint(r) {
			return p, errors.New("cursor does not belong to this query")
		}
		p.Cursor = &c
	}
	return p, nil
}
func csv(v string) []string {
	out := []string{}
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

type listSpec struct{ From, JSON, Tenant, Key, Time, ID, Work, Status, Actor, Operation, Action, Kind, Scope, Type string }

func durableSpec(kind string) listSpec {
	s := listSpec{Tenant: "t.tenant_id", JSON: "to_jsonb(t)", Key: "t.id::text", ID: "t.id::text", Time: "t.created_at"}
	switch kind {
	case "actions":
		s.From = "actions t"
		s.JSON = "to_jsonb(t)-'arguments'-'arguments_ciphertext'-'lease_token_hash'"
		s.Time = "t.requested_at"
		s.Work = "t.work_id"
		s.Actor = "t.actor_id"
		s.Operation = "t.operation"
		s.Status = "t.status"
	case "outcomes":
		s.From = "outcomes t"
		s.JSON = "to_jsonb(t)-'claim_token'-'claim_until'"
		s.Work = "t.work_id"
		s.Action = "t.action_id"
		s.Status = "t.status"
	case "incidents", "findings":
		s.From = "incidents t"
		s.JSON = "to_jsonb(t)||CASE WHEN t.kind='BURST' THEN t.evidence ELSE '{}'::jsonb END"
		s.Work = "t.work_id"
		s.Action = "t.action_id"
		s.Status = "t.status"
		s.Kind = "t.kind"
	case "verifiers":
		s.From = "verifiers t"
		s.JSON = "to_jsonb(t)-'secret_ciphertext'"
	case "model-calls":
		s.From = "model_calls t"
		s.JSON = "to_jsonb(t)-'input_ciphertext'-'output_ciphertext'"
		s.Work = "t.work_id"
	case "semantic-evaluations":
		s.From = "semantic_evaluations t"
		s.Work = "t.work_id"
	case "effects":
		s.From = "effect_occurrences t JOIN actions a ON a.tenant_id=t.tenant_id AND a.id=t.action_id"
		s.Work = "a.work_id"
	case "steps":
		s.From = "steps t"
		s.JSON = "to_jsonb(t)-'input_ciphertext'-'output_ciphertext'-'admission_ciphertext'"
		s.Work = "t.work_id"
	case "waits":
		s.From = "durable_waits t"
		s.Work = "t.work_id"
		s.ID = "t.step_id"
		s.Key = "t.step_id"
	case "reconciliation":
		s.From = "reconciliation_jobs t"
		s.Work = "t.work_id"
	case "activity":
		s.From = "work t"
		s.JSON = "to_jsonb(t)-'input_ciphertext'-'output_ciphertext'-'claim_hash'"
		s.Work = "t.id"
		s.Time = "t.started_at"
	case "verification-attempts":
		s.From = "verification_attempts t JOIN outcomes o ON o.tenant_id=t.tenant_id AND o.id=t.outcome_id"
		s.Key = "t.outcome_id||':'||t.id"
		s.ID = "t.outcome_id"
		s.Work = "o.work_id"
	case "step-attempts":
		s.From = "step_attempts t JOIN steps st ON st.tenant_id=t.tenant_id AND st.id=t.step_id"
		s.Key = "t.step_id||':'||t.attempt::text"
		s.ID = "t.step_id"
		s.Time = "t.started_at"
		s.Work = "st.work_id"
	case "journal":
		s.From = "work_journal t"
		s.Key = "t.sequence::text"
		s.ID = "t.work_id"
		s.Work = "t.work_id"
	case "operation-verifiers":
		s.From = "operation_verifiers t"
		s.Key = "t.operation"
		s.ID = s.Key
		s.Time = "t.updated_at"
	case "reconciliation-handlers":
		s.From = "reconciliation_handlers t"
		s.Key = "t.name||':'||t.version"
		s.ID = "t.name"
	case "discovery":
		s.From = "discovered_tools t"
		s.Time = "t.last_seen_at"
		s.Key = "t.source||':'||t.name"
		s.ID = "t.name"
	case "dependencies":
		s.From = "dependencies t"
	case "enforcement":
		s.From = "enforcement_rules t"
		s.Key = "t.operation||':'||t.rule"
		s.ID = "t.operation"
	}
	return s
}
func (s *Server) pagedList(w http.ResponseWriter, r *http.Request, spec listSpec, id string, single bool) {
	p, e := parsePage(r)
	if e != nil {
		s.fail(w, r, 400, "INVALID_QUERY", e.Error(), nil)
		return
	}
	args := []any{tenant(r)}
	where := []string{spec.Tenant + "=$1"}
	bind := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	if id != "" {
		where = append(where, spec.ID+"="+bind(id))
	}
	if p.From != nil {
		where = append(where, spec.Time+">="+bind(*p.From))
	}
	if p.To != nil {
		where = append(where, spec.Time+"<"+bind(*p.To))
	}
	if resource := r.URL.Query().Get("resource"); resource != "" && spec.Scope != "" {
		where = append(where, "COALESCE("+spec.Scope+"->>'resource','')<>'' AND console_scope_matches(jsonb_build_object('resource',"+spec.Scope+"->'resource'),'','',ARRAY["+bind(resource)+"])")
	}
	for _, f := range []struct {
		Param, Column string
		CSV           bool
	}{{"work_id", spec.Work, false}, {"actor_id", spec.Actor, false}, {"operation", spec.Operation, false}, {"status", spec.Status, true}, {"action_id", spec.Action, true}, {"kind", spec.Kind, true}, {"type", spec.Type, false}} {
		if v := r.URL.Query().Get(f.Param); v != "" && f.Column != "" {
			if f.CSV {
				vs := csv(v)
				if len(vs) > 100 {
					s.fail(w, r, 400, "INVALID_QUERY", "At most 100 filter values", nil)
					return
				}
				where = append(where, f.Column+"=ANY("+bind(vs)+"::text[])")
			} else {
				where = append(where, f.Column+"="+bind(v))
			}
		}
	}
	if p.Cursor != nil {
		where = append(where, "("+spec.Time+","+spec.Key+") < ("+bind(p.Cursor.At)+","+bind(p.Cursor.Key)+")")
	}
	query := "SELECT " + spec.JSON + "," + spec.Time + "," + spec.Key + " FROM " + spec.From + " WHERE " + strings.Join(where, " AND ") + " ORDER BY " + spec.Time + " DESC," + spec.Key + " DESC LIMIT " + bind(p.Limit+1)
	rows, err := s.pool.Query(r.Context(), query, args...)
	if err != nil {
		s.respond(w, r, 0, nil, err)
		return
	}
	defer rows.Close()
	items := []json.RawMessage{}
	var last pageCursor
	var next *string
	for rows.Next() {
		var b []byte
		var c pageCursor
		if err = rows.Scan(&b, &c.At, &c.Key); err != nil {
			break
		}
		if len(items) == p.Limit {
			last.Filter = pageFingerprint(r)
			data, _ := json.Marshal(last)
			v := base64.RawURLEncoding.EncodeToString(data)
			next = &v
			break
		}
		items = append(items, json.RawMessage(b))
		last = c
	}
	if err == nil {
		err = rows.Err()
	}
	if err != nil {
		s.respond(w, r, 0, nil, err)
		return
	}
	if single {
		if len(items) == 0 {
			s.fail(w, r, 404, "NOT_FOUND", "Entity was not found", nil)
		} else {
			writeJSON(w, 200, items[0])
		}
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "next_cursor": next})
}
func (s *Server) pagedDurable(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.pagedList(w, r, durableSpec(kind), chi.URLParam(r, "id"), false)
	}
}

func pathID(r *http.Request, name string) string {
	v := chi.URLParam(r, name)
	if r.URL.RawPath != "" {
		if decoded, e := url.PathUnescape(v); e == nil {
			return decoded
		}
	}
	return v
}
