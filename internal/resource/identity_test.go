package resource

import (
	"encoding/json"
	"testing"

	"lumni/pkg/protocol"
)

func TestCanonicalID(t *testing.T) {
	ref := Canonicalize(protocol.ResourceRef{Type: "Subscription", ExternalID: "sub/91"})
	if ref.ID != "default:subscription:sub%2F91" {
		t.Fatalf("unexpected id %q", ref.ID)
	}
}
func TestEffectFingerprintOrderIndependent(t *testing.T) {
	a := []protocol.ResourceIntent{{ResourceRef: protocol.ResourceRef{ID: "b"}, Mode: protocol.AccessWrite}, {ResourceRef: protocol.ResourceRef{ID: "a"}, Mode: protocol.AccessWrite}}
	b := []protocol.ResourceIntent{a[1], a[0]}
	if EffectFingerprint("refund", a, "") != EffectFingerprint("refund", b, "") {
		t.Fatal("effect fingerprint changed with resource order")
	}
}

func TestEffectFingerprintIncludesDomainArguments(t *testing.T) {
	resources := []protocol.ResourceIntent{{ResourceRef: protocol.ResourceRef{ID: "payment"}, Mode: protocol.AccessWrite}}
	a, err := EffectFingerprintV2("refund", resources, json.RawMessage(`{"amount":100,"currency":"USD","request":"one"}`), []string{"/amount", "/currency"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := EffectFingerprintV2("refund", resources, json.RawMessage(`{"request":"two","currency":"USD","amount":100}`), []string{"/currency", "/amount"})
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("equivalent domain effect changed identity")
	}
	c, _ := EffectFingerprintV2("refund", resources, json.RawMessage(`{"amount":200,"currency":"USD"}`), []string{"/amount", "/currency"})
	if a == c {
		t.Fatal("different refund amounts deduplicated")
	}
	if _, err := EffectFingerprintV2("refund", resources, json.RawMessage(`{}`), []string{"/amount"}); err == nil {
		t.Fatal("missing domain identity accepted")
	}
	resources[0].Mode = protocol.AccessRead
	key, err := EffectFingerprintV2("read", resources, json.RawMessage(`{}`), nil)
	if err != nil || key != "" {
		t.Fatal("read incorrectly has effect identity")
	}
}
func TestLookupPointer(t *testing.T) {
	var value any
	_ = json.Unmarshal([]byte(`{"subscription":{"id":"sub_1"},"items":[7]}`), &value)
	if got, ok := LookupPointer(value, "/subscription/id"); !ok || got != "sub_1" {
		t.Fatalf("got %v %v", got, ok)
	}
	if got, ok := LookupPointer(value, "/items/0"); !ok || got != float64(7) {
		t.Fatalf("got %v %v", got, ok)
	}
}

func FuzzCanonicalID(f *testing.F) {
	f.Add("default", "subscription", "sub/91")
	f.Add("github:repo", "file", "internal/kernel.go")
	f.Fuzz(func(t *testing.T, namespace, typ, externalID string) {
		ref := protocol.ResourceRef{Namespace: namespace, Type: typ, ExternalID: externalID}
		first := CanonicalID(ref)
		second := CanonicalID(ref)
		if first != second {
			t.Fatalf("canonical ID is nondeterministic: %q != %q", first, second)
		}
	})
}

func FuzzLookupPointer(f *testing.F) {
	f.Add("/subscription/id")
	f.Add("/items/0")
	f.Add("/~0/~1")
	f.Fuzz(func(t *testing.T, pointer string) {
		value := map[string]any{"subscription": map[string]any{"id": "sub_1"}, "items": []any{float64(7)}}
		_, _ = LookupPointer(value, pointer)
	})
}
