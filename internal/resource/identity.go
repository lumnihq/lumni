package resource

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"lumni/pkg/protocol"
)

func CanonicalID(ref protocol.ResourceRef) string {
	if ref.ID != "" {
		return ref.ID
	}
	namespace := ref.Namespace
	if namespace == "" {
		namespace = "default"
	}
	return fmt.Sprintf("%s:%s:%s", url.PathEscape(namespace), url.PathEscape(strings.ToLower(ref.Type)), url.PathEscape(ref.ExternalID))
}

// EffectFingerprintV2 includes the domain arguments. Read calls have no effect
// identity and must never deduplicate solely because their write set is empty.
func EffectFingerprintV2(operation string, resources []protocol.ResourceIntent, arguments json.RawMessage, fields []string) (string, error) {
	write := false
	for _, r := range resources {
		if protocol.IsWrite(r.Mode) {
			write = true
		}
	}
	if !write {
		return "", nil
	}
	canonical, err := canonicalJSON(arguments)
	if err != nil {
		return "", err
	}
	if len(fields) > 0 {
		var value any
		dec := json.NewDecoder(strings.NewReader(string(canonical)))
		dec.UseNumber()
		if err := dec.Decode(&value); err != nil {
			return "", err
		}
		selected := map[string]any{}
		for _, field := range fields {
			v, ok := LookupPointer(value, field)
			if !ok {
				return "", fmt.Errorf("effect field %q is missing", field)
			}
			selected[field] = v
		}
		canonical, err = json.Marshal(selected)
		if err != nil {
			return "", err
		}
	}
	sum := sha256.Sum256(append([]byte(EffectFingerprint(operation, resources, "")+"|"), canonical...))
	return "effect:v2:" + hex.EncodeToString(sum[:]), nil
}

func Canonicalize(ref protocol.ResourceRef) protocol.ResourceRef {
	if ref.Namespace == "" {
		ref.Namespace = "default"
	}
	ref.Type = strings.ToLower(strings.TrimSpace(ref.Type))
	ref.ExternalID = strings.TrimSpace(ref.ExternalID)
	ref.ID = CanonicalID(ref)
	return ref
}

func EffectFingerprint(operation string, resources []protocol.ResourceIntent, explicit string) string {
	if explicit != "" {
		return explicit
	}
	parts := make([]string, 0, len(resources))
	for _, r := range resources {
		if protocol.IsWrite(r.Mode) {
			parts = append(parts, string(r.Mode)+":"+CanonicalID(r.ResourceRef))
		}
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(operation)) + "|" + strings.Join(parts, "|")))
	return "effect:" + hex.EncodeToString(sum[:])
}
