package automation

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReadProjectionLimitsFieldsIncludingNestedCollections(t *testing.T) {
	value := map[string]any{"balance": 100, "private_card": "secret", "customer": map[string]any{"name": "Allowed", "email": "private"}, "data": []any{map[string]any{"id": "one", "private": "no"}}}
	out := ProjectRead(value, map[string][]string{"orders": {"balance", "customer.name", "data.id"}})
	data := string(JSON(out))
	if strings.Contains(data, "private") || strings.Contains(data, "secret") || strings.Contains(data, "email") {
		t.Fatalf("grant leaked fields: %s", data)
	}
	if !strings.Contains(data, "Allowed") || !strings.Contains(data, "one") {
		t.Fatalf("allowed values lost: %s", data)
	}
}
func TestCanonicalHashesDoNotAliasLargeNumbers(t *testing.T) {
	if Hash(json.RawMessage(`{"a":1,"b":2}`)) != Hash(json.RawMessage(`{ "b":2,"a":1 }`)) {
		t.Fatal("key order changed identity")
	}
	if Hash(json.RawMessage(`9007199254740992`)) == Hash(json.RawMessage(`9007199254740993`)) {
		t.Fatal("large integers aliased")
	}
	if _, e := Schema([]byte(`{"$ref":"https://example.invalid/schema"}`)); e == nil {
		t.Fatal("remote schema resolved without an explicit loader")
	}
}
