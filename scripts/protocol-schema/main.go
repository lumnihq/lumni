// Generates the additive durable API schemas from the public Go protocol types.
package main

import (
	"encoding/json"
	"fmt"
	"github.com/lumnihq/lumni/pkg/protocol"
	"os"
	"reflect"
	"strings"
	"time"
)

var definitions = map[string]any{}
var rawType = reflect.TypeOf(json.RawMessage{})
var timeType = reflect.TypeOf(time.Time{})

func schema(t reflect.Type) map[string]any {
	if t.Kind() == reflect.Pointer {
		return schema(t.Elem())
	}
	if t == rawType {
		return map[string]any{}
	}
	if t == timeType {
		return map[string]any{"type": "string", "format": "date-time"}
	}
	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int64, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": schema(t.Elem())}
	case reflect.Slice:
		return map[string]any{"type": "array", "items": schema(t.Elem())}
	case reflect.Struct:
		name := t.Name()
		if _, ok := definitions[name]; !ok {
			definitions[name] = map[string]any{}
			properties := map[string]any{}
			required := []string{}
			var fields func(reflect.Type)
			fields = func(st reflect.Type) {
				for i := 0; i < st.NumField(); i++ {
					f := st.Field(i)
					tag := f.Tag.Get("json")
					if tag == "-" {
						continue
					}
					if f.Anonymous && tag == "" {
						fields(f.Type)
						continue
					}
					parts := strings.Split(tag, ",")
					key := parts[0]
					if key == "" {
						key = f.Name
					}
					properties[key] = schema(f.Type)
					if !strings.Contains(tag, "omitempty") && f.Type.Kind() != reflect.Pointer {
						required = append(required, key)
					}
				}
			}
			fields(t)
			out := map[string]any{"type": "object", "properties": properties}
			if len(required) > 0 {
				out["required"] = required
			}
			definitions[name] = out
		}
		return map[string]any{"$ref": "#/$defs/" + name}
	}
	return map[string]any{}
}
func main() {
	for _, v := range []any{protocol.ToolMapping{}, protocol.ReconciliationHandler{}, protocol.ReconciliationInput{}, protocol.EffectIdentityResolution{}, protocol.RunInput{}, protocol.ClaimInput{}, protocol.RunClaim{}, protocol.RuntimeAuth{}, protocol.StepInput{}, protocol.Step{}, protocol.StepResult{}, protocol.WaitInput{}, protocol.SignalInput{}, protocol.Verifier{}, protocol.VerificationEvidence{}, protocol.DependencyInput{}, protocol.EnforcementRule{}, protocol.OperationVerifier{}, protocol.ActionCheckInput{}, protocol.ActionResultInput{}, protocol.ObservationInput{}, protocol.CostInput{}} {
		schema(reflect.TypeOf(v))
	}
	out := map[string]any{"$schema": "https://json-schema.org/draft/2020-12/schema", "title": "Lumni durable protocol additive v1", "$defs": definitions}
	b, e := json.MarshalIndent(out, "", "  ")
	if e != nil {
		panic(e)
	}
	if e = os.WriteFile("api/schemas/durable-1.1.json", append(b, '\n'), 0644); e != nil {
		panic(e)
	}
	fmt.Println("Updated durable protocol schema")
}
