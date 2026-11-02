package contracts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/lumnihq/lumni/internal/automation"
	"github.com/lumnihq/lumni/pkg/protocol"
)

type Snapshot struct {
	Arguments           any       `json:"arguments"`
	Fact                any       `json:"fact,omitempty"`
	FactAt              time.Time `json:"fact_at,omitempty"`
	ReadVersion         int       `json:"read_version,omitempty"`
	Unavailable         bool      `json:"unavailable"`
	PriorCount          int       `json:"prior_count"`
	ReservedAmount      int64     `json:"reserved_amount"`
	PredecessorVerified bool      `json:"predecessor_verified"`
	EvaluatedAt         time.Time `json:"evaluated_at"`
}
type Finding struct {
	ContractID string                `json:"contract_id"`
	Revision   int                   `json:"revision"`
	RuleID     string                `json:"rule_id"`
	Mode       string                `json:"mode"`
	Decision   protocol.DecisionType `json:"decision"`
	Reason     protocol.ReasonCode   `json:"reason"`
}
type Prepared struct {
	Contracts []protocol.Contract
	Facts     map[string]Snapshot
}

func factKey(contract, rule string) string { return contract + ":" + rule }
func (s *Service) Prepare(ctx context.Context, tenant string, in protocol.ActionCheckInput) (Prepared, error) {
	out := Prepared{Facts: map[string]Snapshot{}}
	readCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	readCache := map[string]Snapshot{}
	rows, e := s.Pool.Query(ctx, `SELECT id,rules,revision,mode FROM contracts WHERE tenant_id=$1 AND deleted_at IS NULL AND mode IN('watch','protect') ORDER BY id`, tenant)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var c protocol.Contract
		var b []byte
		if e = rows.Scan(&c.ID, &b, &c.Revision, &c.Mode); e != nil {
			break
		}
		if e = json.Unmarshal(b, &c.Rules); e != nil {
			break
		}
		out.Contracts = append(out.Contracts, c)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return out, e
	}
	// Rechecks keep the originally admitted revision, even after an edit or deletion.
	var pinned []protocol.Contract
	rows, e = s.Pool.Query(ctx, `SELECT contract_id,revision,mode,jsonb_agg(rule ORDER BY rule_id) FROM action_contract_rules WHERE tenant_id=$1 AND action_id=$2 GROUP BY contract_id,revision,mode ORDER BY contract_id`, tenant, in.ActionID)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var c protocol.Contract
		var b []byte
		if e = rows.Scan(&c.ID, &c.Revision, &c.Mode, &b); e != nil {
			break
		}
		if e = json.Unmarshal(b, &c.Rules); e != nil {
			break
		}
		pinned = append(pinned, c)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return out, e
	}
	if len(pinned) > 0 {
		out.Contracts = pinned
	}
	var args any
	if json.Unmarshal(in.Arguments, &args) != nil {
		args = map[string]any{}
	}
	for _, c := range out.Contracts {
		for _, r := range c.Rules {
			if r.Binding.Operation != in.Operation || (r.Binding.ActorID != "" && r.Binding.ActorID != in.ActorID) {
				continue
			}
			f := Snapshot{Arguments: args, EvaluatedAt: time.Now().UTC()}
			if r.Binding.ReadBindingID != "" {
				b, e := s.Registry.Get(ctx, tenant, r.Binding.ReadBindingID, 0)
				params := map[string]any{}
				if e == nil {
					for k, p := range r.Binding.ReadArguments {
						v, err := automation.Pointer(args, p)
						if err != nil {
							e = err
							break
						}
						params[k] = v
					}
				}
				if e == nil && s.Cipher != nil {
					var data []byte
					cacheKey := automation.Hash([]any{b.ID, b.Version, params})
					if cached, ok := readCache[cacheKey]; ok {
						f.Fact = cached.Fact
						f.FactAt = cached.FactAt
						f.ReadVersion = cached.ReadVersion
						out.Facts[factKey(c.ID, r.ID)] = f
						continue
					}
					data, e = s.Registry.Call(readCtx, tenant, b, automation.JSON(params), "")
					if e == nil {
						e = json.Unmarshal(data, &f.Fact)
						f.FactAt = time.Now().UTC()
						f.ReadVersion = b.Version
						readCache[cacheKey] = f
					}
				} else {
					f.Unavailable = true
				}
				if e != nil {
					f.Unavailable = true
				}
			}
			out.Facts[factKey(c.ID, r.ID)] = f
		}
	}
	return out, nil
}

// BindTx pins rules and immutable verifier copies without replacing operation-level configuration.
func (s *Service) BindTx(ctx context.Context, tx pgx.Tx, tenant string, in *protocol.ActionCheckInput, p Prepared) error {
	children := []string{}
	expected := map[string]any{}

	if in.Verification != nil {
		if _, e := tx.Exec(ctx, `UPDATE actions SET base_verification=$3 WHERE tenant_id=$1 AND id=$2`, tenant, in.ActionID, automation.JSON(in.Verification)); e != nil {
			return e
		}
		children = append(children, in.Verification.VerifierID)
		var v any
		if e := json.Unmarshal(in.Verification.Expected, &v); e != nil {
			return e
		}
		expected[in.Verification.VerifierID] = v
	}
	for _, c := range p.Contracts {
		var rev int
		var mode string
		e := tx.QueryRow(ctx, `SELECT revision,mode FROM contracts WHERE tenant_id=$1 AND id=$2 AND deleted_at IS NULL FOR SHARE`, tenant, c.ID).Scan(&rev, &mode)
		if errors.Is(e, pgx.ErrNoRows) {
			continue
		}
		if e != nil {
			return e
		}
		if rev != c.Revision || mode != c.Mode {
			return automation.Conflict("contract changed during preparation; retry admission")
		}
		for _, r := range c.Rules {
			if r.Binding.Operation != in.Operation || (r.Binding.ActorID != "" && r.Binding.ActorID != in.ActorID) {
				continue
			}
			if _, e = tx.Exec(ctx, `INSERT INTO action_contract_rules(tenant_id,action_id,contract_id,revision,rule_id,rule,mode)VALUES($1,$2,$3,$4,$5,$6,$7)ON CONFLICT DO NOTHING`, tenant, in.ActionID, c.ID, c.Revision, r.ID, automation.JSON(r), c.Mode); e != nil {
				return e
			}
			b := r.Binding
			if b.VerifierID == "" {
				continue
			}
			var descriptor []byte
			if e = tx.QueryRow(ctx, `SELECT jsonb_build_object('kind',kind,'version',version,'config',config,'secret_hash',encode(sha256(COALESCE(secret_ciphertext,''::bytea)),'hex')) FROM verifiers WHERE tenant_id=$1 AND id=$2 AND enabled FOR SHARE`, tenant, b.VerifierID).Scan(&descriptor); e != nil {
				return e
			}
			id := "cv_" + automation.Hash([]any{c.ID, c.Revision, r.ID, json.RawMessage(descriptor)})[:32]
			tag, e := tx.Exec(ctx, `INSERT INTO verifiers(tenant_id,id,name,kind,version,config,secret_ciphertext,enabled)SELECT tenant_id,$3,$3,kind,version,config,secret_ciphertext,true FROM verifiers WHERE tenant_id=$1 AND id=$2 AND enabled ON CONFLICT DO NOTHING`, tenant, b.VerifierID, id)
			if e != nil {
				return e
			}
			if tag.RowsAffected() == 0 {
				var ok bool
				if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM verifiers WHERE tenant_id=$1 AND id=$2 AND enabled)`, tenant, id).Scan(&ok); e != nil {
					return e
				}
				if !ok {
					return automation.Conflict("contract verifier unavailable")
				}
			}
			var template any
			if e = json.Unmarshal(b.Expected, &template); e != nil {
				return e
			}
			args := p.Facts[factKey(c.ID, r.ID)].Arguments
			value, e := automation.Template(template, args)
			if e != nil {
				return e
			}
			if c.Mode == "watch" {
				seconds := b.WithinSeconds
				if seconds == 0 {
					seconds = 300
				}
				if _, e = tx.Exec(ctx, `INSERT INTO contract_assertions(tenant_id,action_id,contract_id,rule_id,verifier_id,expected,within_seconds)VALUES($1,$2,$3,$4,$5,$6,$7)ON CONFLICT DO NOTHING`, tenant, in.ActionID, c.ID, r.ID, id, automation.JSON(value), seconds); e != nil {
					return e
				}
				continue
			}
			children = append(children, id)
			expected[id] = value
			seconds := b.WithinSeconds
			if seconds == 0 {
				seconds = 300
			}
			if _, e = tx.Exec(ctx, `INSERT INTO contract_verification_deadlines(tenant_id,action_id,verifier_id,within_seconds)VALUES($1,$2,$3,$4)ON CONFLICT DO NOTHING`, tenant, in.ActionID, id, seconds); e != nil {
				return e
			}
		}
	}
	if len(children) > 1 { // Build bounded composites in layers; each child keeps its own expected assertion.
		for len(children) > 16 {
			var next []string
			nextExpected := map[string]any{}
			for start := 0; start < len(children); start += 16 {
				end := start + 16
				if end > len(children) {
					end = len(children)
				}
				part := children[start:end]
				sub := map[string]any{}
				for _, id := range part {
					sub[id] = expected[id]
				}
				id := "cc_" + automation.Hash([]any{in.ActionID, part})[:32]
				if e := composite(ctx, tx, tenant, id, part); e != nil {
					return e
				}
				next = append(next, id)
				nextExpected[id] = sub
			}
			children = next
			expected = nextExpected
		}
		id := "ca_" + automation.Hash([]any{in.ActionID, children})[:32]
		if e := composite(ctx, tx, tenant, id, children); e != nil {
			return e
		}
		in.Verification = &protocol.OutcomeSpec{VerifierID: id, Expected: automation.JSON(expected)}
	} else if len(children) == 1 {
		in.Verification = &protocol.OutcomeSpec{VerifierID: children[0], Expected: automation.JSON(expected[children[0]])}
	}

	return nil
}
func composite(ctx context.Context, tx pgx.Tx, tenant, id string, children []string) error {
	_, e := tx.Exec(ctx, `INSERT INTO verifiers(tenant_id,id,name,kind,version,config,enabled)VALUES($1,$2,$2,'COMPOSITE','contracts-v1',$3,true)ON CONFLICT DO NOTHING`, tenant, id, automation.JSON(map[string]any{"verifiers": children}))
	return e
}

// Evaluate is pure and shared by admission, historical preview, and regression fixtures.
func Evaluate(r protocol.ContractRule, f Snapshot) (protocol.DecisionType, protocol.ReasonCode) {
	hold := func() (protocol.DecisionType, protocol.ReasonCode) {
		return protocol.DecisionWait, "CONTRACT_EVIDENCE_UNAVAILABLE"
	}
	if f.Unavailable {
		return hold()
	}
	b := r.Binding
	if b.ReadBindingID != "" {
		max := b.MaxAgeSeconds
		if max == 0 {
			max = 30
		}
		if f.FactAt.IsZero() || f.FactAt.After(f.EvaluatedAt) || f.EvaluatedAt.Sub(f.FactAt) > time.Duration(max)*time.Second {
			return hold()
		}
	}
	amount := int64(0)
	if b.AmountPointer != "" {
		v, e := automation.Pointer(f.Arguments, b.AmountPointer)
		var ok bool
		amount, ok = Number(v)
		if e != nil || !ok || amount < 0 {
			return hold()
		}
		if b.CurrencyPointer != "" {
			v, e = automation.Pointer(f.Arguments, b.CurrencyPointer)
			if e != nil || !strings.EqualFold(fmt.Sprint(v), b.Currency) {
				return hold()
			}
		}
	}
	switch r.Type {
	case "once-per-key":
		if f.PriorCount > 0 {
			return protocol.DecisionReject, "CONTRACT_DUPLICATE"
		}
	case "approval-over":
		n, ok := Number(r.Slots["amount_minor"])
		if !ok {
			return hold()
		}
		if amount > n {
			return protocol.DecisionRequireApproval, "CONTRACT_APPROVAL_REQUIRED"
		}
	case "amount-cap":
		v, e := automation.Pointer(f.Fact, b.ValuePointer)
		limit, ok := Number(v)
		if e != nil || !ok || limit < 0 {
			return hold()
		}
		if amount > limit-f.ReservedAmount {
			return protocol.DecisionReject, "CONTRACT_AMOUNT_CAP"
		}
	case "order-before":
		if !f.PredecessorVerified {
			return protocol.DecisionWait, "CONTRACT_PREDECESSOR_PENDING"
		}
	case "rate-limit":
		n, ok := Number(r.Slots["n"])
		if !ok {
			return hold()
		}
		if int64(f.PriorCount) >= n {
			return protocol.DecisionWait, "CONTRACT_RATE_LIMIT"
		}
	case "human-wins":
		editor, e := automation.Pointer(f.Fact, b.LastEditorPointer)
		if e != nil {
			return hold()
		}
		edited, e := automation.Pointer(f.Fact, b.LastEditedAtPointer)
		if e != nil {
			return hold()
		}
		at, e := time.Parse(time.RFC3339Nano, fmt.Sprint(edited))
		if e != nil || at.After(f.EvaluatedAt) {
			return hold()
		}
		minutes, ok := Number(r.Slots["minutes"])
		if !ok {
			return hold()
		}
		if !Has([]string{"human", "person", "agent", "ai", "system"}, strings.ToLower(fmt.Sprint(editor))) {
			return hold()
		}
		if Has([]string{"human", "person"}, strings.ToLower(fmt.Sprint(editor))) && f.EvaluatedAt.Sub(at) < time.Duration(minutes)*time.Minute {
			return protocol.DecisionReject, "CONTRACT_HUMAN_WINS"
		}
	case "end-state": // The immutable post-action verifier owns this assertion.
	default:
		return hold()
	}
	return protocol.DecisionAllow, "CONTRACT_PASS"
}
func (s *Service) EvaluateTx(ctx context.Context, tx pgx.Tx, tenant string, in protocol.ActionCheckInput, p Prepared) ([]Finding, error) {
	rows, e := tx.Query(ctx, `SELECT contract_id,revision,rule_id,rule,mode,approval_id FROM action_contract_rules WHERE tenant_id=$1 AND action_id=$2 ORDER BY contract_id,rule_id`, tenant, in.ActionID)
	if e != nil {
		return nil, e
	}
	type item struct {
		f        Finding
		r        protocol.ContractRule
		approval *string
	}
	items := []item{}
	for rows.Next() {
		var x item
		var b []byte
		if e = rows.Scan(&x.f.ContractID, &x.f.Revision, &x.f.RuleID, &b, &x.f.Mode, &x.approval); e != nil {
			break
		}
		if e = json.Unmarshal(b, &x.r); e != nil {
			break
		}
		items = append(items, x)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return nil, e
	}
	// Every key lock is acquired in canonical order to avoid cross-contract deadlocks.
	keys := map[string]string{}
	locks := []string{}
	for _, x := range items {
		f := p.Facts[factKey(x.f.ContractID, x.r.ID)]
		key := ""
		if x.r.Binding.KeyPointer != "" {
			v, err := automation.Pointer(f.Arguments, x.r.Binding.KeyPointer)
			if err == nil {
				switch v.(type) {
				case string, float64:
					key = automation.Hash([]any{x.r.Binding.Operation, v})
				}
			}
		}
		keys[factKey(x.f.ContractID, x.r.ID)] = key
		if key != "" {
			locks = append(locks, tenant+":contract:"+x.f.ContractID+":"+scopeKey(x.r)+":"+key)
		}
	}
	sort.Strings(locks)
	for _, key := range locks {
		if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, key); e != nil {
			return nil, e
		}
	}
	out := []Finding{}
	for _, x := range items {
		f := p.Facts[factKey(x.f.ContractID, x.r.ID)]
		f.EvaluatedAt = time.Now().UTC()
		key := keys[factKey(x.f.ContractID, x.r.ID)]
		if x.r.Binding.KeyPointer != "" && key == "" {
			f.Unavailable = true
		}
		if x.r.Binding.ReadBindingID != "" {
			var version int
			var enabled, granted bool
			e = tx.QueryRow(ctx, `SELECT b.version,b.enabled,EXISTS(SELECT 1 FROM integration_read_grants g WHERE g.tenant_id=b.tenant_id AND g.binding_id=b.id AND g.binding_version=b.version AND g.revoked_at IS NULL AND g.objects @> (b.document->'objects')) FROM tool_bindings b WHERE b.tenant_id=$1 AND b.id=$2 FOR SHARE`, tenant, x.r.Binding.ReadBindingID).Scan(&version, &enabled, &granted)
			if errors.Is(e, pgx.ErrNoRows) {
				f.Unavailable = true
			} else if e != nil {
				return nil, e
			} else if version != f.ReadVersion || !enabled || !granted {
				f.Unavailable = true
			}
		}
		seconds := int64(0)
		if x.r.Type == "rate-limit" {
			seconds = map[string]int64{"minute": 60, "hour": 3600, "day": 86400, "week": 604800}[fmt.Sprint(x.r.Slots["window"])]
		}
		if key != "" {
			e = tx.QueryRow(ctx, `WITH prior AS(SELECT DISTINCT ON(k.action_id)k.amount,k.action_id,a.status,a.tenant_id,k.admitted_at,a.started_at FROM contract_key_claims k JOIN actions a ON a.tenant_id=k.tenant_id AND a.id=k.action_id JOIN action_contract_rules old ON old.tenant_id=k.tenant_id AND old.action_id=k.action_id AND old.contract_id=k.contract_id AND old.rule_id=k.rule_id WHERE k.tenant_id=$1 AND k.contract_id=$2 AND old.rule->>'type'=$3 AND old.rule->'binding'->>'operation'=$8 AND old.rule->'binding'->>'key_pointer'=$9 AND COALESCE(old.rule->'binding'->>'amount_pointer','')=$10 AND COALESCE(old.rule->'binding'->>'currency','')=$11 AND k.key=$4 AND k.action_id<>$5 AND ($6::bigint=0 OR k.admitted_at>=now()-make_interval(secs=>$6))) SELECT count(*),COALESCE(sum(amount)FILTER(WHERE status IN('RESERVED','ACTIVE','UNKNOWN','FAILED') OR ($7 AND status='SUCCEEDED')),0) FROM prior a WHERE (status IN('ACTIVE','UNKNOWN','SUCCEEDED') OR(status='RESERVED' AND EXISTS(SELECT 1 FROM leases WHERE tenant_id=a.tenant_id AND action_id=a.action_id AND expires_at>now())) OR(status='PROPOSED' AND EXISTS(SELECT 1 FROM decisions WHERE tenant_id=a.tenant_id AND action_id=a.action_id AND NOT enforced)) OR(status='FAILED' AND started_at IS NOT NULL AND NOT EXISTS(SELECT 1 FROM outcomes WHERE tenant_id=a.tenant_id AND action_id=a.action_id AND effect_absent AND prior_attempt_terminated)))`, tenant, x.f.ContractID, x.r.Type, key, in.ActionID, seconds, Has([]string{"amount paid", "order total", "daily budget"}, fmt.Sprint(x.r.Slots["limit"])), x.r.Binding.Operation, x.r.Binding.KeyPointer, x.r.Binding.AmountPointer, x.r.Binding.Currency).Scan(&f.PriorCount, &f.ReservedAmount)
			if e != nil {
				return nil, e
			}
		}
		if x.r.Type == "order-before" {
			val, err := automation.Pointer(f.Arguments, x.r.Binding.KeyPointer)
			if err != nil {
				f.Unavailable = true
			} else {
				f.PredecessorVerified, err = s.predecessor(ctx, tx, tenant, x.r.Binding.PredecessorOperation, x.r.Binding.KeyPointer, val)
				if err != nil {
					return nil, err
				}
			}
		}
		x.f.Decision, x.f.Reason = Evaluate(x.r, f)
		if x.f.Decision == protocol.DecisionRequireApproval && x.approval != nil {
			var status string
			e = tx.QueryRow(ctx, `SELECT CASE WHEN expires_at<=now() THEN 'EXPIRED' ELSE status END FROM approvals WHERE tenant_id=$1 AND id=$2`, tenant, *x.approval).Scan(&status)
			if e != nil {
				return nil, e
			}
			if status == "APPROVED" {
				x.f.Decision = protocol.DecisionAllow
				x.f.Reason = "CONTRACT_APPROVED"
			} else if status == "DENIED" || status == "EXPIRED" {
				x.f.Decision = protocol.DecisionReject
				x.f.Reason = "CONTRACT_APPROVAL_" + protocol.ReasonCode(status)
			}
		}
		var cipher []byte
		if s.Cipher != nil {
			cipher, e = s.Cipher.Encrypt(automation.JSON(f))
			if e != nil {
				return nil, e
			}
		}
		if _, e = tx.Exec(ctx, `UPDATE action_contract_rules SET key=$5,decision=$6,reason=$7,facts_ciphertext=$8,fact_hash=$9,evaluated_at=now() WHERE tenant_id=$1 AND action_id=$2 AND contract_id=$3 AND rule_id=$4`, tenant, in.ActionID, x.f.ContractID, x.r.ID, key, x.f.Decision, x.f.Reason, cipher, automation.Hash(f)); e != nil {
			return nil, e
		}
		out = append(out, x.f)
	}
	return out, nil
}
func AdmitTx(ctx context.Context, tx pgx.Tx, tenant string, in protocol.ActionCheckInput) error {
	var args any
	_ = json.Unmarshal(in.Arguments, &args)
	rows, e := tx.Query(ctx, `SELECT contract_id,rule_id,key,rule FROM action_contract_rules WHERE tenant_id=$1 AND action_id=$2 AND key<>'' ORDER BY contract_id,rule_id`, tenant, in.ActionID)
	if e != nil {
		return e
	}
	type claim struct {
		c, r, key string
		rule      protocol.ContractRule
	}
	all := []claim{}
	for rows.Next() {
		var x claim
		var b []byte
		if e = rows.Scan(&x.c, &x.r, &x.key, &b); e != nil {
			break
		}
		if e = json.Unmarshal(b, &x.rule); e != nil {
			break
		}
		all = append(all, x)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return e
	}
	for _, x := range all {
		amount := int64(0)
		if v, err := automation.Pointer(args, x.rule.Binding.AmountPointer); err == nil {
			amount, _ = Number(v)
		}
		if _, e = tx.Exec(ctx, `INSERT INTO contract_key_claims(tenant_id,contract_id,rule_id,key,action_id,amount)VALUES($1,$2,$3,$4,$5,$6)ON CONFLICT(tenant_id,contract_id,rule_id,key,action_id)DO UPDATE SET admitted_at=now()`, tenant, x.c, x.r, x.key, in.ActionID, amount); e != nil {
			return e
		}
	}
	return nil
}

// Correlate verified historical predecessors even when they predate contract creation.
func (s *Service) predecessor(ctx context.Context, tx pgx.Tx, tenant, operation, pointer string, key any) (bool, error) {
	rows, e := tx.Query(ctx, `SELECT a.arguments,a.arguments_ciphertext FROM actions a JOIN outcomes o ON o.tenant_id=a.tenant_id AND o.action_id=a.id WHERE a.tenant_id=$1 AND a.operation=$2 AND o.status='VERIFIED' ORDER BY a.requested_at DESC,a.id DESC LIMIT 1000`, tenant, operation)
	if e != nil {
		return false, e
	}
	defer rows.Close()
	for rows.Next() {
		var plain, cipher []byte
		if e = rows.Scan(&plain, &cipher); e != nil {
			return false, e
		}
		if len(cipher) > 0 && s.Cipher != nil {
			plain, e = s.Cipher.Decrypt(cipher)
			if e != nil {
				return false, e
			}
		}
		var args any
		if len(plain) == 0 || json.Unmarshal(plain, &args) != nil {
			continue
		}
		v, err := automation.Pointer(args, pointer)
		if err == nil && automation.Hash(v) == automation.Hash(key) {
			return true, nil
		}
	}
	return false, rows.Err()
}

func scopeKey(r protocol.ContractRule) string {
	return automation.Hash([]string{r.Type, r.Binding.Operation, r.Binding.KeyPointer, r.Binding.AmountPointer, r.Binding.Currency})
}
