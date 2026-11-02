package compiled

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/lumnihq/lumni/internal/automation"
	"github.com/lumnihq/lumni/pkg/protocol"
)

const Interpreter = "path-v1"

type Fallback struct{ Reason string }

func (e *Fallback) Error() string { return "fallback: " + e.Reason }

type ToolCall func(context.Context, protocol.ProgramNode, json.RawMessage) (json.RawMessage, error)

func ValidateProgram(p protocol.CompiledProgram) error {
	if len(automation.JSON(p)) > 1<<20 {
		return automation.Invalid("program exceeds 1 MiB")
	}
	if p.Interpreter != Interpreter || len(p.Nodes) < 1 || len(p.Nodes) > 256 {
		return automation.Invalid("path-v1 program with 1-256 nodes required")
	}
	if _, e := automation.Schema(p.InputSchema); e != nil {
		return e
	}
	if _, e := automation.Schema(p.OutputSchema); e != nil {
		return e
	}
	indices := map[string]int{}
	for i, n := range p.Nodes {
		if n.ID == "" || len(n.ID) > 100 {
			return automation.Invalid("bounded node id required")
		}
		if _, ok := indices[n.ID]; ok {
			return automation.Invalid("duplicate node")
		}
		indices[n.ID] = i
		if e := validateExpression(n.Value, 0); e != nil {
			return e
		}
	}
	states := map[int]map[bool]bool{0: {false: true}}
	for i, n := range p.Nodes {
		if !contains([]string{"let", "read", "call", "guard", "return", "fallback"}, n.Kind) {
			return automation.Invalid("unknown program instruction")
		}
		if (n.Kind == "read" || n.Kind == "call") && (n.BindingID == "" || n.BindingVersion < 1) {
			return automation.Invalid("versioned binding required")
		}
		targets := []string{}
		if n.Kind != "return" && n.Kind != "fallback" {
			targets = append(targets, n.Next)
		}
		if n.Kind == "guard" {
			targets = append(targets, n.Otherwise)
		} else if n.Otherwise != "" {
			return automation.Invalid("otherwise is only valid for guard")
		}
		if (n.Kind == "return" || n.Kind == "fallback") && n.Next != "" {
			return automation.Invalid("terminal instructions cannot continue")
		}
		for mutated := range states[i] {
			if mutated && (n.Kind == "read" || n.Kind == "guard" || n.Kind == "fallback") {
				return automation.Invalid("read/guard/fallback after mutation is forbidden")
			}
			for _, target := range targets {
				j, ok := indices[target]
				if !ok || j <= i {
					return automation.Invalid("edges must point forward to an existing node")
				}
				if states[j] == nil {
					states[j] = map[bool]bool{}
				}
				states[j][mutated || n.Kind == "call"] = true
			}
		}
		// Validate unreachable instructions as well.
		for _, target := range targets {
			j, ok := indices[target]
			if !ok || j <= i {
				return automation.Invalid("invalid program edge")
			}
		}
	}
	return nil
}
func contains(a []string, b string) bool {
	for _, v := range a {
		if v == b {
			return true
		}
	}
	return false
}
func validateExpression(e protocol.Expression, depth int) error {
	if depth > 16 {
		return automation.Invalid("expression nesting exceeds 16")
	}
	n := 0
	if len(e.Literal) > 0 {
		n++
		if !json.Valid(e.Literal) {
			return automation.Invalid("invalid literal")
		}
	}
	if e.Ref != "" {
		n++
		if e.Ref != "/input" && !strings.HasPrefix(e.Ref, "/input/") && !strings.HasPrefix(e.Ref, "/vars/") {
			return automation.Invalid("references must address /input or /vars")
		}
	}
	if e.Op != "" {
		n++
		if !contains([]string{"eq", "ne", "lt", "lte", "gt", "gte", "and", "or", "not", "add", "sub", "mul", "div", "concat", "coalesce", "days_since", "length"}, e.Op) {
			return automation.Invalid("unsupported expression operation")
		}
		if len(e.Args) < 1 || len(e.Args) > 32 {
			return automation.Invalid("1-32 operation arguments required")
		}
	}
	if e.Object != nil {
		n++
		if len(e.Object) > 100 {
			return automation.Invalid("object expression exceeds 100 fields")
		}
	}
	if n != 1 {
		return automation.Invalid("expression requires exactly one of literal, ref, op, object")
	}
	for _, a := range e.Args {
		if err := validateExpression(a, depth+1); err != nil {
			return err
		}
	}
	for _, a := range e.Object {
		if err := validateExpression(a, depth+1); err != nil {
			return err
		}
	}
	return nil
}
func expression(e protocol.Expression, env map[string]any, now time.Time) (any, error) {
	if len(e.Literal) > 0 {
		return automation.Decode(e.Literal)
	}
	if e.Ref != "" {
		return automation.Pointer(env, e.Ref)
	}
	if e.Object != nil {
		o := map[string]any{}
		for k, x := range e.Object {
			v, err := expression(x, env, now)
			if err != nil {
				return nil, err
			}
			o[k] = v
		}
		return o, nil
	}
	a := []any{}
	for _, x := range e.Args {
		v, err := expression(x, env, now)
		if err != nil {
			return nil, err
		}
		a = append(a, v)
	}
	if len(a) == 0 {
		return nil, automation.Invalid("empty expression")
	}
	switch e.Op {
	case "eq", "ne":
		if len(a) != 2 {
			return nil, automation.Invalid("comparison arity")
		}
		equal := automation.Hash(a[0]) == automation.Hash(a[1])
		return equal == (e.Op == "eq"), nil
	case "and", "or":
		result := e.Op == "and"
		for _, v := range a {
			b, ok := v.(bool)
			if !ok {
				return nil, automation.Invalid("boolean expected")
			}
			if e.Op == "and" {
				result = result && b
			} else {
				result = result || b
			}
		}
		return result, nil
	case "not":
		b, ok := a[0].(bool)
		if !ok || len(a) != 1 {
			return nil, automation.Invalid("not expects boolean")
		}
		return !b, nil
	case "concat":
		var b strings.Builder
		for _, v := range a {
			x, ok := v.(string)
			if !ok {
				return nil, automation.Invalid("concat expects strings")
			}
			b.WriteString(x)
			if b.Len() > 1<<20 {
				return nil, automation.Invalid("string limit")
			}
		}
		return b.String(), nil
	case "coalesce":
		for _, v := range a {
			if v != nil {
				return v, nil
			}
		}
		return nil, nil
	case "length":
		if len(a) != 1 {
			return nil, automation.Invalid("length arity")
		}
		switch x := a[0].(type) {
		case string:
			return float64(len(x)), nil
		case []any:
			return float64(len(x)), nil
		default:
			return nil, automation.Invalid("length expects string or array")
		}
	case "days_since":
		x, ok := a[0].(string)
		if !ok || len(a) != 1 {
			return nil, automation.Invalid("timestamp expected")
		}
		t, err := time.Parse(time.RFC3339Nano, x)
		if err != nil {
			return nil, err
		}
		return math.Floor(now.Sub(t).Hours() / 24), nil
	}
	if len(a) != 2 {
		return nil, automation.Invalid("numeric operator arity")
	}
	x, ok := a[0].(float64)
	y, ok2 := a[1].(float64)
	if !ok || !ok2 {
		return nil, automation.Invalid("numbers required")
	}
	switch e.Op {
	case "lt":
		return x < y, nil
	case "lte":
		return x <= y, nil
	case "gt":
		return x > y, nil
	case "gte":
		return x >= y, nil
	case "add":
		return finite(x + y)
	case "sub":
		return finite(x - y)
	case "mul":
		return finite(x * y)
	case "div":
		if y == 0 {
			return nil, automation.Invalid("division by zero")
		}
		return finite(x / y)
	}
	return nil, automation.Invalid("unknown operator")
}
func finite(v float64) (any, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > 9007199254740991 {
		return nil, automation.Invalid("numeric overflow")
	}
	return v, nil
}

// Execute bounds only evaluation CPU time. The supplied durable tool boundary has its own timeout.
func Execute(ctx context.Context, p protocol.CompiledProgram, input []byte, at time.Time, call ToolCall) (json.RawMessage, error) {
	if e := ValidateProgram(p); e != nil {
		return nil, e
	}
	if len(input) > 1<<20 {
		return nil, automation.Invalid("input exceeds 1 MiB")
	}
	if e := automation.Validate(p.InputSchema, input); e != nil {
		return nil, &Fallback{"INPUT_SCHEMA"}
	}
	v, _ := automation.Decode(input)
	env := map[string]any{"input": v, "vars": map[string]any{}}
	vars := env["vars"].(map[string]any)
	indices := map[string]int{}
	for i, n := range p.Nodes {
		indices[n.ID] = i
	}
	spent := time.Duration(0)
	mutated := false
	memory := 0
	for i := 0; i < len(p.Nodes); {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		start := time.Now()
		n := p.Nodes[i]
		value, e := expression(n.Value, env, at)
		spent += time.Since(start)
		if spent > time.Second {
			return nil, automation.Invalid("evaluation time limit")
		}
		if e != nil {
			if !mutated {
				return nil, &Fallback{"EXPRESSION_UNAVAILABLE"}
			}
			return nil, e
		}
		data := automation.JSON(value)
		if len(data) > 1<<20 {
			return nil, automation.Invalid("intermediate value exceeds 1 MiB")
		}
		switch n.Kind {
		case "let":
			memory += len(data)
			if memory > 2<<20 {
				return nil, automation.Invalid("working memory exceeds 2 MiB")
			}
			vars[n.ID] = value
		case "read", "call":
			if call == nil {
				return nil, errors.New("tool boundary missing")
			}
			if n.Kind == "call" {
				mutated = true
			}
			result, err := call(ctx, n, data)
			if err != nil {
				return nil, err
			}
			if len(result) > 1<<20 {
				return nil, automation.Invalid("tool output limit")
			}
			v, err := automation.Decode(result)
			if err != nil {
				return nil, err
			}
			memory += len(result)
			if memory > 2<<20 {
				return nil, automation.Invalid("working memory exceeds 2 MiB")
			}
			vars[n.ID] = v
		case "guard":
			b, ok := value.(bool)
			if !ok {
				return nil, automation.Invalid("guard expects boolean")
			}
			if !b {
				i = indices[n.Otherwise]
				continue
			}
		case "fallback":
			return nil, &Fallback{n.Reason}
		case "return":
			if e = automation.Validate(p.OutputSchema, data); e != nil {
				if !mutated {
					return nil, &Fallback{"OUTPUT_SCHEMA"}
				}
				return nil, e
			}
			return data, nil
		}
		i = indices[n.Next]
	}
	return nil, fmt.Errorf("program ended without result")
}
