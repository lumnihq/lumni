package resource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"lumni/pkg/protocol"
)

const ResolverVersion = "resolver-v2"

type ResourceRule struct {
	Mode                   protocol.AccessMode `json:"mode"`
	Type                   string              `json:"type"`
	Namespace              string              `json:"namespace,omitempty"`
	IDPointer              string              `json:"id_pointer"`
	ObservedVersionPointer string              `json:"observed_version_pointer,omitempty"`
	VersionSource          string              `json:"version_source,omitempty"`
}

type Service struct {
	pool *pgxpool.Pool
}

func NewResolver(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

func (s *Service) Resolve(ctx context.Context, tenantID string, in protocol.ActionCheckInput) (protocol.ActionCheckInput, string, error) {
	in.Resources = append([]protocol.ResourceIntent(nil), in.Resources...)
	in.Operation = strings.ToLower(strings.TrimSpace(in.Operation))
	canonicalArgs, err := canonicalJSON(in.Arguments)
	if err != nil {
		return in, "", fmt.Errorf("arguments: %w", err)
	}
	in.Arguments = canonicalArgs
	hash := sha256.Sum256(canonicalArgs)
	argumentsHash := hex.EncodeToString(hash[:])

	var effectFields []string
	if in.Source != "" && in.Tool != "" {
		var operation string
		var rawRules, rawFields []byte
		err := s.pool.QueryRow(ctx, `
			SELECT operation, resources, effect_key_fields FROM resource_rules
			WHERE tenant_id = $1 AND source = $2 AND tool = $3 AND enabled`, tenantID, in.Source, in.Tool,
		).Scan(&operation, &rawRules, &rawFields)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) && len(in.Resources) == 0 {
				return in, argumentsHash, fmt.Errorf("%s: no deterministic mapping for source=%q tool=%q", protocol.ReasonResourceUnresolved, in.Source, in.Tool)
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return in, argumentsHash, err
			}
		}
		if operation != "" {
			in.Operation = strings.ToLower(strings.TrimSpace(operation))
		}
		if len(rawFields) > 0 {
			if err := json.Unmarshal(rawFields, &effectFields); err != nil {
				return in, argumentsHash, err
			}
		}
		var rules []ResourceRule
		if len(rawRules) > 0 {
			if err := json.Unmarshal(rawRules, &rules); err != nil {
				return in, argumentsHash, err
			}
		}
		var args any
		if err := json.Unmarshal(canonicalArgs, &args); err != nil {
			return in, argumentsHash, err
		}
		if len(in.Resources) == 0 {
			for _, rule := range rules {
				value, ok := LookupPointer(args, rule.IDPointer)
				if !ok {
					return in, argumentsHash, fmt.Errorf("%s: JSON pointer %q not found", protocol.ReasonResourceUnresolved, rule.IDPointer)
				}
				ref := Canonicalize(protocol.ResourceRef{Type: rule.Type, ExternalID: fmt.Sprint(value), Namespace: rule.Namespace})
				intent := protocol.ResourceIntent{ResourceRef: ref, Mode: rule.Mode, Confidence: 1, EvidenceType: "EXPLICIT", VersionSource: rule.VersionSource}
				if rule.ObservedVersionPointer != "" {
					if v, ok := LookupPointer(args, rule.ObservedVersionPointer); ok {
						version := fmt.Sprint(v)
						intent.ObservedVersion = &version
					}
				}
				in.Resources = append(in.Resources, intent)
			}
		}
	}
	for i := range in.Resources {
		in.Resources[i].ResourceRef = Canonicalize(in.Resources[i].ResourceRef)
		if in.Resources[i].Confidence == 0 {
			in.Resources[i].Confidence = 1
		}
		if in.Resources[i].EvidenceType == "" {
			in.Resources[i].EvidenceType = "EXPLICIT"
		}
	}
	if in.Verification == nil {
		var verifier string
		var template []byte
		err := s.pool.QueryRow(ctx, `SELECT verifier_id,expected_template FROM operation_verifiers WHERE tenant_id=$1 AND operation=$2`, tenantID, in.Operation).Scan(&verifier, &template)
		if err == nil {
			var value, args any
			if err = json.Unmarshal(template, &value); err != nil {
				return in, argumentsHash, err
			}
			if err = json.Unmarshal(canonicalArgs, &args); err != nil {
				return in, argumentsHash, err
			}
			value, err = BindExpected(value, args)
			if err != nil {
				return in, argumentsHash, err
			}
			expected, _ := json.Marshal(value)
			in.Verification = &protocol.OutcomeSpec{VerifierID: verifier, Expected: expected}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return in, argumentsHash, err
		}
	}
	if in.EffectKey == nil {
		// Identity needs a payload, not just a resource. Registered fields exclude
		// incidental request metadata; explicit SDK actions use the entire input.
		key, err := EffectFingerprintV2(in.Operation, in.Resources, canonicalArgs, effectFields)
		if err != nil {
			return in, argumentsHash, err
		}
		if key != "" {
			in.EffectKey = &key
		}
	}
	return in, argumentsHash, nil
}

// BindExpected expands only explicit JSON-pointer bindings, never task text.
func BindExpected(template, args any) (any, error) {
	switch v := template.(type) {
	case string:
		if strings.HasPrefix(v, "$args:") {
			value, ok := LookupPointer(args, strings.TrimPrefix(v, "$args:"))
			if !ok {
				return nil, fmt.Errorf("expected outcome binding missing: %s", v)
			}
			return value, nil
		}
		return v, nil
	case map[string]any:
		out := map[string]any{}
		for k, x := range v {
			value, err := BindExpected(x, args)
			if err != nil {
				return nil, err
			}
			out[k] = value
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, x := range v {
			value, err := BindExpected(x, args)
			if err != nil {
				return nil, err
			}
			out[i] = value
		}
		return out, nil
	default:
		return v, nil
	}
}

func canonicalJSON(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 {
		return []byte("{}"), nil
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func LookupPointer(value any, path string) (any, bool) {
	if path == "" || path == "/" {
		return value, true
	}
	if !strings.HasPrefix(path, "/") {
		return nil, false
	}
	current := value
	for _, raw := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		part := strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~")
		switch node := current.(type) {
		case map[string]any:
			var ok bool
			current, ok = node[part]
			if !ok {
				return nil, false
			}
		case []any:
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(node) {
				return nil, false
			}
			current = node[index]
		default:
			return nil, false
		}
	}
	return current, true
}
