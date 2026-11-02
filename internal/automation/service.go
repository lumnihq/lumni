// Package automation contains the explicit tool registry shared by contracts and paths.
package automation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lumnihq/lumni/internal/resource"
	"github.com/lumnihq/lumni/internal/secrets"
	"github.com/lumnihq/lumni/pkg/protocol"
)

type Fault struct {
	Status        int
	Code, Message string
}

func (e *Fault) Error() string { return e.Code + ": " + e.Message }
func Invalid(s string) error   { return &Fault{400, "INVALID_AUTOMATION", s} }
func Conflict(s string) error  { return &Fault{409, "AUTOMATION_CONFLICT", s} }
func Missing(s string) error   { return &Fault{404, "NOT_FOUND", s} }
func JSON(v any) []byte        { b, _ := json.Marshal(v); return b }
func Hash(v any) string {
	b := JSON(v)
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var canonical any
	if d.Decode(&canonical) == nil {
		b = JSON(canonical)
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func Decode(b []byte) (any, error) { var v any; err := json.Unmarshal(b, &v); return v, err }
func Canonical(b []byte) ([]byte, error) {
	v, e := Decode(b)
	if e != nil {
		return nil, e
	}
	return json.Marshal(v)
}
func Schema(b []byte) (*jsonschema.Resolved, error) {
	if len(b) == 0 || len(b) > 65536 {
		return nil, Invalid("nonempty schema (at most 64 KiB) required")
	}
	var s jsonschema.Schema
	if e := json.Unmarshal(b, &s); e != nil {
		return nil, Invalid("invalid JSON schema")
	}
	r, e := s.Resolve(nil)
	if e != nil {
		return nil, Invalid(e.Error())
	}
	return r, nil
}
func Validate(schema, data []byte) error {
	s, e := Schema(schema)
	if e != nil {
		return e
	}
	v, e := Decode(data)
	if e != nil {
		return Invalid("invalid JSON payload")
	}
	if e = s.Validate(v); e != nil {
		return Invalid(e.Error())
	}
	return nil
}
func Pointer(v any, p string) (any, error) {
	x, ok := resource.LookupPointer(v, p)
	if !ok {
		return nil, Invalid("missing JSON pointer " + p)
	}
	return x, nil
}

// Template only substitutes explicit {"$arg":"/pointer"} references, never prose.
func Template(v any, args any) (any, error) {
	switch x := v.(type) {
	case map[string]any:
		if p, ok := x["$arg"].(string); ok && len(x) == 1 {
			return Pointer(args, p)
		}
		o := map[string]any{}
		for k, a := range x {
			n, e := Template(a, args)
			if e != nil {
				return nil, e
			}
			o[k] = n
		}
		return o, nil
	case []any:
		o := make([]any, len(x))
		for i, a := range x {
			n, e := Template(a, args)
			if e != nil {
				return nil, e
			}
			o[i] = n
		}
		return o, nil
	default:
		return v, nil
	}
}
func Audit(ctx context.Context, tx pgx.Tx, tenant, by, kind, id string, before, after any) error {
	_, e := tx.Exec(ctx, `INSERT INTO audit_events(tenant_id,by,type,target,before,after)VALUES($1,$2,$3,$4,$5,$6)`, tenant, by, kind, JSON(map[string]string{"id": id}), JSON(before), JSON(after))
	return e
}
func Transaction(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	tx, e := pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = fn(tx); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

type Registry struct {
	Pool   *pgxpool.Pool
	Cipher *secrets.Manager
	Client *http.Client
}

func New(pool *pgxpool.Pool, cipher *secrets.Manager) *Registry {
	return &Registry{pool, cipher, &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("tool redirects are disabled") }}}
}
func (s *Registry) Get(ctx context.Context, tenant, id string, version int) (protocol.ToolBinding, error) {
	var b []byte
	var current int
	var enabled bool
	e := s.Pool.QueryRow(ctx, `SELECT document,version,enabled FROM tool_bindings WHERE tenant_id=$1 AND id=$2`, tenant, id).Scan(&b, &current, &enabled)
	if errors.Is(e, pgx.ErrNoRows) {
		return protocol.ToolBinding{}, Missing("tool binding not found")
	}
	var out protocol.ToolBinding
	if e != nil {
		return out, e
	}
	if e = json.Unmarshal(b, &out); e != nil {
		return out, e
	}
	if !enabled || (version != 0 && version != current) {
		return out, Conflict("binding disabled or version changed")
	}
	return out, nil
}
func (s *Registry) Put(ctx context.Context, tenant, by, id string, in protocol.ToolBinding) (protocol.ToolBinding, error) {
	if in.ID != "" && in.ID != id {
		return in, Invalid("binding id mismatch")
	}
	in.ID = id
	in.Operation = strings.ToLower(strings.TrimSpace(in.Operation))
	u, e := url.Parse(in.URL)
	if e != nil || u.Host == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") || strings.Contains(in.URL, "{") {
		return in, Invalid("fixed http(s) URL required")
	}
	in.Method = strings.ToUpper(in.Method)
	if in.ReadOnly && len(in.Objects) == 0 {
		return in, Invalid("read bindings require explicit objects and fields")
	}
	if in.ReadOnly && in.Method != "GET" {
		return in, Invalid("read-only bindings require GET")
	}
	if !in.ReadOnly && !strings.Contains(" POST PUT PATCH DELETE ", " "+in.Method+" ") {
		return in, Invalid("mutation method required")
	}
	if in.App == "" || in.Operation == "" || id == "" {
		return in, Invalid("id, app and operation required")
	}
	if _, e = Schema(in.InputSchema); e != nil {
		return in, e
	}
	if _, e = Schema(in.OutputSchema); e != nil {
		return in, e
	}
	if !in.ReadOnly && (len(in.Resources) == 0 || len(in.EffectKeyFields) == 0 || in.VerifierID == "" || len(in.Expected) == 0) {
		return in, Invalid("mutations require resources, effect key fields and verification")
	}
	for _, p := range in.EffectKeyFields {
		if !strings.HasPrefix(p, "/") {
			return in, Invalid("effect fields must be JSON pointers")
		}
	}
	for _, r := range in.Resources {
		if r.Type == "" || !strings.HasPrefix(r.IDPointer, "/") || (!protocol.IsWrite(r.Mode) && r.Mode != protocol.AccessRead) {
			return in, Invalid("invalid resource mapping")
		}
	}
	if in.IdempotencyHeader != "" {
		for _, c := range in.IdempotencyHeader {
			if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return in, Invalid("invalid idempotency header")
			}
		}
		if strings.EqualFold(in.IdempotencyHeader, "Authorization") || strings.EqualFold(in.IdempotencyHeader, "Host") {
			return in, Invalid("reserved header")
		}
	}
	var secret []byte
	if in.Secret != "" {
		secret, e = s.Cipher.Encrypt([]byte(in.Secret))
		if e != nil {
			return in, e
		}
	}
	in.Secret = ""
	e = Transaction(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, tenant+":binding:"+id); e != nil {
			return e
		}
		var old []byte
		var v int
		e := tx.QueryRow(ctx, `SELECT version,document FROM tool_bindings WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenant, id).Scan(&v, &old)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if in.Version != v {
			return Conflict("binding version mismatch")
		}
		in.Version = v + 1
		if in.VerifierID != "" {
			var ok bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM verifiers WHERE tenant_id=$1 AND id=$2 AND enabled)`, tenant, in.VerifierID).Scan(&ok); e != nil {
				return e
			}
			if !ok {
				return Invalid("enabled verifier required")
			}
		}
		if _, e = tx.Exec(ctx, `INSERT INTO tool_bindings(tenant_id,id,version,document,secret_ciphertext,enabled)VALUES($1,$2,$3,$4,$5,$6)ON CONFLICT(tenant_id,id)DO UPDATE SET version=EXCLUDED.version,document=EXCLUDED.document,secret_ciphertext=COALESCE(EXCLUDED.secret_ciphertext,tool_bindings.secret_ciphertext),enabled=EXCLUDED.enabled,updated_at=now()`, tenant, id, in.Version, JSON(in), secret, in.Enabled); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO tool_binding_versions(tenant_id,binding_id,version,document)VALUES($1,$2,$3,$4)`, tenant, id, in.Version, JSON(in)); e != nil {
			return e
		}
		return Audit(ctx, tx, tenant, by, "binding.updated", id, json.RawMessage(old), in)
	})
	return in, e
}
func (s *Registry) HasGrant(ctx context.Context, tenant string, b protocol.ToolBinding) bool {
	var ok bool
	e := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM integration_read_grants WHERE tenant_id=$1 AND binding_id=$2 AND binding_version=$3 AND app=$4 AND revoked_at IS NULL AND objects @> $5::jsonb)`, tenant, b.ID, b.Version, b.App, JSON(b.Objects)).Scan(&ok)
	return e == nil && ok
}
func (s *Registry) Call(ctx context.Context, tenant string, b protocol.ToolBinding, input []byte, identity string) (json.RawMessage, error) {
	if len(input) > 1<<20 {
		return nil, Invalid("input exceeds 1 MiB")
	}
	if e := Validate(b.InputSchema, input); e != nil {
		return nil, e
	}
	current, e := s.Get(ctx, tenant, b.ID, b.Version)
	if e != nil {
		return nil, e
	}
	if current.ReadOnly && !s.HasGrant(ctx, tenant, current) {
		return nil, Conflict("read capability not granted")
	}
	var secret []byte
	if e = s.Pool.QueryRow(ctx, `SELECT secret_ciphertext FROM tool_bindings WHERE tenant_id=$1 AND id=$2 AND version=$3 AND enabled`, tenant, b.ID, b.Version).Scan(&secret); e != nil {
		return nil, e
	}
	target, _ := url.Parse(b.URL)
	var body io.Reader
	if b.ReadOnly {
		var fields map[string]any
		if json.Unmarshal(input, &fields) != nil {
			return nil, Invalid("GET input must be an object")
		}
		q := target.Query()
		for k, v := range fields {
			switch v.(type) {
			case string, float64, bool:
				q.Set(k, fmt.Sprint(v))
			default:
				return nil, Invalid("GET query values must be scalar")
			}
		}
		target.RawQuery = q.Encode()
	} else {
		body = bytes.NewReader(input)
	}
	req, e := http.NewRequestWithContext(ctx, b.Method, target.String(), body)
	if e != nil {
		return nil, e
	}
	req.Header.Set("Content-Type", "application/json")
	if len(secret) > 0 {
		plain, e := s.Cipher.Decrypt(secret)
		if e != nil {
			return nil, e
		}
		req.Header.Set("Authorization", "Bearer "+string(plain))
	}
	if b.IdempotencyHeader != "" {
		req.Header.Set(b.IdempotencyHeader, identity)
	}
	response, e := s.Client.Do(req)
	if e != nil {
		return nil, e
	}
	defer response.Body.Close()
	data, e := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if e != nil {
		return nil, e
	}
	if len(data) > 1<<20 {
		return nil, Invalid("tool output exceeds 1 MiB")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("tool HTTP status %d", response.StatusCode)
	}
	if b.ReadOnly {
		value, err := Decode(data)
		if err != nil {
			return nil, err
		}
		data = JSON(ProjectRead(value, b.Objects))
	}
	if e = Validate(b.OutputSchema, data); e != nil {
		return nil, e
	}
	return data, nil
}
func (s *Registry) Action(b protocol.ToolBinding, input []byte) (protocol.ActionCheckInput, error) {
	var args any
	if e := json.Unmarshal(input, &args); e != nil {
		return protocol.ActionCheckInput{}, e
	}
	in := protocol.ActionCheckInput{Operation: b.Operation, Source: "compiled", Tool: b.ID, Arguments: input, Risk: protocol.RiskHigh}
	for _, r := range b.Resources {
		x, e := Pointer(args, r.IDPointer)
		if e != nil {
			return in, e
		}
		id, ok := x.(string)
		if !ok || id == "" {
			return in, Invalid("resource id must be a string")
		}
		ref := protocol.ResourceIntent{ResourceRef: protocol.ResourceRef{Type: r.Type, Namespace: r.Namespace, ExternalID: id}, Mode: r.Mode, Confidence: 1, EvidenceType: "EXPLICIT", VersionSource: r.VersionSource}
		if r.ObservedVersionPointer != "" {
			v, e := Pointer(args, r.ObservedVersionPointer)
			if e != nil {
				return in, e
			}
			str, ok := v.(string)
			if !ok {
				return in, Invalid("observed version must be string")
			}
			ref.ObservedVersion = &str
		}
		in.Resources = append(in.Resources, ref)
	}
	fields := map[string]any{}
	for _, p := range b.EffectKeyFields {
		v, e := Pointer(args, p)
		if e != nil {
			return in, e
		}
		fields[p] = v
	}
	key := "compiled:" + b.Operation + ":" + Hash(fields)
	in.EffectKey = &key
	var expected any
	if e := json.Unmarshal(b.Expected, &expected); e != nil {
		return in, e
	}
	expected, e := Template(expected, args)
	if e != nil {
		return in, e
	}
	in.Verification = &protocol.OutcomeSpec{VerifierID: b.VerifierID, Expected: JSON(expected)}
	return in, nil
}

// ProjectRead limits the program's view to explicitly registered field paths.
// Object names describe the capability; field paths are rooted in the response.
func ProjectRead(value any, objects map[string][]string) any {
	type trie map[string]any
	root := trie{}
	for _, fields := range objects {
		for _, field := range fields {
			var parts []string
			if strings.HasPrefix(field, "/") {
				parts = strings.Split(strings.TrimPrefix(field, "/"), "/")
				for i := range parts {
					parts[i] = strings.ReplaceAll(strings.ReplaceAll(parts[i], "~1", "/"), "~0", "~")
				}
			} else {
				parts = strings.Split(field, ".")
			}
			node := root
			for i, p := range parts {
				if i == len(parts)-1 {
					node[p] = true
					break
				}
				if node[p] == true {
					break
				}
				child, ok := node[p].(trie)
				if !ok {
					child = trie{}
					node[p] = child
				}
				node = child
			}
		}
	}
	var project func(any, trie) any
	project = func(v any, allowed trie) any {
		switch x := v.(type) {
		case map[string]any:
			out := map[string]any{}
			for k, rule := range allowed {
				a, ok := x[k]
				if !ok {
					continue
				}
				if rule == true {
					out[k] = a
				} else {
					out[k] = project(a, rule.(trie))
				}
			}
			return out
		case []any:
			out := make([]any, len(x))
			for i, a := range x {
				out[i] = project(a, allowed)
			}
			return out
		default:
			return nil
		}
	}
	return project(value, root)
}
