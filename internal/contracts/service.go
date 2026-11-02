// Package contracts owns versioned outcome rules without modifying manual policies.
package contracts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lumnihq/lumni/internal/automation"
	"github.com/lumnihq/lumni/internal/secrets"
	"github.com/lumnihq/lumni/pkg/protocol"
)

// EffectiveModeSQL uses the c (contract) and t (tenant) aliases. MIXED is read-only.
const EffectiveModeSQL = `CASE WHEN c.mode<>'protect' THEN 'SHADOW' ELSE (SELECT CASE WHEN count(DISTINCT m.mode)=1 THEN min(m.mode) ELSE 'MIXED' END FROM (SELECT COALESCE((SELECT er.mode FROM enforcement_rules er WHERE er.tenant_id=c.tenant_id AND er.operation IN(r->'binding'->>'operation','*') AND er.rule='*' ORDER BY(er.operation=r->'binding'->>'operation')DESC LIMIT 1),t.enforcement_mode) mode FROM jsonb_array_elements(c.rules) r)m) END`

var Kinds = []string{"once-per-key", "approval-over", "end-state", "amount-cap", "order-before", "rate-limit", "human-wins"}

type Service struct {
	Pool     *pgxpool.Pool
	Cipher   *secrets.Manager
	Registry *automation.Registry
}

func New(p *pgxpool.Pool, c *secrets.Manager) *Service { return &Service{p, c, automation.New(p, c)} }
func Has(a []string, v string) bool {
	for _, s := range a {
		if s == v {
			return true
		}
	}
	return false
}
func Number(v any) (int64, bool) {
	n, ok := v.(float64)
	if !ok {
		switch x := v.(type) {
		case int:
			return int64(x), true
		case int64:
			return x, true
		default:
			return 0, false
		}
	}
	if math.IsNaN(n) || math.IsInf(n, 0) || n != math.Trunc(n) || math.Abs(n) > 9007199254740991 {
		return 0, false
	}
	return int64(n), true
}
func (s *Service) Get(ctx context.Context, tenant, id string) (protocol.Contract, error) {
	var c protocol.Contract
	var b []byte
	e := s.Pool.QueryRow(ctx, `SELECT c.id,c.title,c.text,c.rules,c.revision,c.mode,c.was,c.created_at,c.updated_at,`+EffectiveModeSQL+` FROM contracts c JOIN tenants t ON t.id=c.tenant_id WHERE c.tenant_id=$1 AND c.id=$2 AND c.deleted_at IS NULL`, tenant, id).Scan(&c.ID, &c.Title, &c.Text, &b, &c.Revision, &c.Mode, &c.Was, &c.CreatedAt, &c.UpdatedAt, &c.EffectiveMode)
	if errors.Is(e, pgx.ErrNoRows) {
		return c, automation.Missing("contract not found")
	}
	if e != nil {
		return c, e
	}
	e = json.Unmarshal(b, &c.Rules)
	return c, e
}
func ValidateRules(rules []protocol.ContractRule, active bool) error {
	if len(rules) < 1 || len(rules) > 50 {
		return automation.Invalid("1-50 rules required")
	}
	seen := map[string]bool{}
	for i := range rules {
		r := &rules[i]
		if r.ID == "" {
			r.ID = fmt.Sprintf("r%d", i+1)
		}
		if len(r.ID) > 200 || seen[r.ID] {
			return automation.Invalid("unique bounded rule ids required")
		}
		seen[r.ID] = true
		if !Has(Kinds, r.Type) {
			return automation.Invalid("unknown contract type")
		}
		if r.Slots == nil {
			r.Slots = map[string]any{}
		}
		if !active {
			continue
		}
		b := r.Binding
		if b.Operation == "" || b.Operation == "*" {
			return automation.Invalid("explicit operation required")
		}
		if b.MaxAgeSeconds < 0 || b.MaxAgeSeconds > 3600 || b.WithinSeconds < 0 || b.WithinSeconds > 86400 {
			return automation.Invalid("invalid freshness/deadline")
		}
		for _, p := range []string{b.KeyPointer, b.AmountPointer, b.CurrencyPointer, b.ValuePointer, b.LastEditorPointer, b.LastEditedAtPointer} {
			if p != "" && !strings.HasPrefix(p, "/") {
				return automation.Invalid("bindings must use JSON pointers")
			}
		}
		if Has([]string{"once-per-key", "amount-cap", "order-before", "rate-limit", "human-wins"}, r.Type) && b.KeyPointer == "" {
			return automation.Invalid("key_pointer required")
		}
		if Has([]string{"approval-over", "amount-cap"}, r.Type) && (b.AmountPointer == "" || b.Currency == "") {
			return automation.Invalid("amount_pointer and currency required")
		}
		switch r.Type {
		case "approval-over":
			n, ok := Number(r.Slots["amount_minor"])
			if !ok || n < 0 {
				return automation.Invalid("nonnegative amount_minor required")
			}
		case "amount-cap":
			if b.ReadBindingID == "" || b.ValuePointer == "" {
				return automation.Invalid("amount cap requires authoritative read and value pointer")
			}
		case "rate-limit":
			n, ok := Number(r.Slots["n"])
			if !ok || n < 1 || !Has([]string{"minute", "hour", "day", "week"}, fmt.Sprint(r.Slots["window"])) {
				return automation.Invalid("positive n and minute/hour/day/week required")
			}
		case "order-before":
			if b.PredecessorOperation == "" {
				return automation.Invalid("predecessor_operation required")
			}
		case "human-wins":
			n, ok := Number(r.Slots["minutes"])
			if !ok || n < 1 || b.ReadBindingID == "" || b.LastEditorPointer == "" || b.LastEditedAtPointer == "" {
				return automation.Invalid("human-wins needs minutes and authoritative editor/timestamp pointers")
			}
		case "end-state":
			if b.VerifierID == "" {
				return automation.Invalid("end-state requires a verifier")
			}
		}
		if b.VerifierID != "" {
			var expected map[string]any
			if json.Unmarshal(b.Expected, &expected) != nil || len(expected) == 0 {
				return automation.Invalid("nonempty expected verification template required")
			}
		}
	}
	return nil
}

type rowReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (s *Service) Ready(ctx context.Context, tenant string, rules []protocol.ContractRule) (map[string]any, error) {
	return s.ready(ctx, s.Pool, tenant, rules)
}
func (s *Service) ready(ctx context.Context, db rowReader, tenant string, rules []protocol.ContractRule) (map[string]any, error) {
	missing := []string{}
	apps := []any{}
	if e := ValidateRules(rules, true); e != nil {
		missing = append(missing, e.Error())
	}
	for _, r := range rules {
		b := r.Binding
		if b.ReadBindingID != "" {
			var raw []byte
			var enabled, granted bool
			e := db.QueryRow(ctx, `SELECT b.document,b.enabled,EXISTS(SELECT 1 FROM integration_read_grants g WHERE g.tenant_id=b.tenant_id AND g.binding_id=b.id AND g.binding_version=b.version AND g.revoked_at IS NULL AND g.objects @> (b.document->'objects')) FROM tool_bindings b WHERE b.tenant_id=$1 AND b.id=$2`, tenant, b.ReadBindingID).Scan(&raw, &enabled, &granted)
			if e != nil && !errors.Is(e, pgx.ErrNoRows) {
				return nil, e
			}
			var tool protocol.ToolBinding
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &tool); err != nil {
					return nil, err
				}
			}
			ok := e == nil && enabled && tool.ReadOnly && granted
			apps = append(apps, map[string]any{"app": tool.App, "binding_id": b.ReadBindingID, "objects": tool.Objects, "configured": e == nil, "granted": ok, "provenance": "operator_configured"})
			if !ok {
				missing = append(missing, "read binding/grant: "+b.ReadBindingID)
			}
		}
		if b.VerifierID != "" {
			var ok bool
			e := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM verifiers WHERE tenant_id=$1 AND id=$2 AND enabled)`, tenant, b.VerifierID).Scan(&ok)
			if e != nil {
				return nil, e
			}
			if !ok {
				missing = append(missing, "verifier: "+b.VerifierID)
			}
		}
	}
	return map[string]any{"ready": len(missing) == 0, "missing": missing, "apps": apps}, nil
}
func (s *Service) Save(ctx context.Context, tenant, by, id string, in protocol.ContractInput) (protocol.Contract, error) {
	for i := range in.Rules {
		b := &in.Rules[i].Binding
		b.Operation = strings.ToLower(strings.TrimSpace(b.Operation))
		b.PredecessorOperation = strings.ToLower(strings.TrimSpace(b.PredecessorOperation))
		b.Currency = strings.ToUpper(strings.TrimSpace(b.Currency))
	}
	create := id == ""
	if create {
		id = "ctr_" + uuid.NewString()
	}
	if strings.TrimSpace(in.Title) == "" || len(in.Title) > 300 || strings.TrimSpace(in.Text) == "" || len(in.Text) > 32000 {
		return protocol.Contract{}, automation.Invalid("bounded title and text required")
	}
	if e := ValidateRules(in.Rules, false); e != nil {
		return protocol.Contract{}, e
	}
	e := automation.Transaction(ctx, s.Pool, func(tx pgx.Tx) error {
		var mode string
		var revision int
		var old []byte
		if !create {
			e := tx.QueryRow(ctx, `SELECT mode,revision,to_jsonb(c) FROM contracts c WHERE tenant_id=$1 AND id=$2 AND deleted_at IS NULL FOR UPDATE`, tenant, id).Scan(&mode, &revision, &old)
			if errors.Is(e, pgx.ErrNoRows) {
				return automation.Missing("contract not found")
			}
			if e != nil {
				return e
			}
			if revision != in.Revision {
				return automation.Conflict("contract revision mismatch")
			}
		}
		if Has([]string{"watch", "protect"}, mode) {
			ready, e := s.ready(ctx, tx, tenant, in.Rules)
			if e != nil {
				return e
			}
			if ready["ready"] != true {
				return automation.Conflict("edited active contract is not ready")
			}
		}
		in.Revision = revision + 1
		if create {
			if _, e := tx.Exec(ctx, `INSERT INTO contracts(tenant_id,id,title,text,rules)VALUES($1,$2,$3,$4,$5)`, tenant, id, in.Title, in.Text, automation.JSON(in.Rules)); e != nil {
				return e
			}
		} else {
			if _, e := tx.Exec(ctx, `UPDATE contracts SET title=$3,text=$4,rules=$5,revision=$6,updated_at=now() WHERE tenant_id=$1 AND id=$2`, tenant, id, in.Title, in.Text, automation.JSON(in.Rules), in.Revision); e != nil {
				return e
			}
		}
		if _, e := tx.Exec(ctx, `INSERT INTO contract_revisions(tenant_id,contract_id,revision,document)VALUES($1,$2,$3,$4)`, tenant, id, in.Revision, automation.JSON(in)); e != nil {
			return e
		}
		return automation.Audit(ctx, tx, tenant, by, "contract.saved", id, json.RawMessage(old), in)
	})
	if e != nil {
		return protocol.Contract{}, e
	}
	return s.Get(ctx, tenant, id)
}
func (s *Service) Mode(ctx context.Context, tenant, by, id, mode string, revision int) (protocol.Contract, error) {
	if !Has([]string{"draft", "watch", "protect", "paused"}, mode) {
		return protocol.Contract{}, automation.Invalid("invalid mode")
	}
	e := automation.Transaction(ctx, s.Pool, func(tx pgx.Tx) error {
		var v int
		var old, was string
		var data []byte
		e := tx.QueryRow(ctx, `SELECT revision,mode,was,rules FROM contracts WHERE tenant_id=$1 AND id=$2 AND deleted_at IS NULL FOR UPDATE`, tenant, id).Scan(&v, &old, &was, &data)
		if errors.Is(e, pgx.ErrNoRows) {
			return automation.Missing("contract not found")
		}
		if e != nil {
			return e
		}
		if v != revision {
			return automation.Conflict("contract revision mismatch")
		}
		if mode == old || mode == "paused" && old == "draft" {
			return automation.Conflict("invalid mode transition")
		}
		if Has([]string{"watch", "protect"}, mode) {
			var rules []protocol.ContractRule
			if e = json.Unmarshal(data, &rules); e != nil {
				return e
			}
			ready, e := s.ready(ctx, tx, tenant, rules)
			if e != nil {
				return e
			}
			if ready["ready"] != true {
				return automation.Conflict("contract needs explicit bindings and granted reads")
			}
			was = mode
		} else if Has([]string{"watch", "protect"}, old) {
			was = old
		}
		if _, e = tx.Exec(ctx, `UPDATE contracts SET mode=$3,was=$4,revision=revision+1,updated_at=now() WHERE tenant_id=$1 AND id=$2`, tenant, id, mode, was); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO contract_revisions(tenant_id,contract_id,revision,document)SELECT tenant_id,contract_id,$3,jsonb_set(document,'{revision}',to_jsonb($3::int)) FROM contract_revisions WHERE tenant_id=$1 AND contract_id=$2 AND revision=$4`, tenant, id, v+1, v); e != nil {
			return e
		}
		return automation.Audit(ctx, tx, tenant, by, "contract.mode", id, map[string]string{"mode": old}, map[string]string{"mode": mode})
	})
	if e != nil {
		return protocol.Contract{}, e
	}
	return s.Get(ctx, tenant, id)
}
func (s *Service) Delete(ctx context.Context, tenant, by, id string) error {
	return automation.Transaction(ctx, s.Pool, func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, `UPDATE contracts SET deleted_at=now(),mode='draft',updated_at=now() WHERE tenant_id=$1 AND id=$2 AND deleted_at IS NULL`, tenant, id)
		if e != nil {
			return e
		}
		if tag.RowsAffected() == 0 {
			return automation.Missing("contract not found")
		}
		return automation.Audit(ctx, tx, tenant, by, "contract.deleted", id, nil, nil)
	})
}
