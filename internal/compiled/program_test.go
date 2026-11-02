package compiled

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/lumnihq/lumni/pkg/protocol"
)

func lit(s string) protocol.Expression { return protocol.Expression{Literal: json.RawMessage(s)} }
func echoProgram() protocol.CompiledProgram {
	return protocol.CompiledProgram{Interpreter: Interpreter, InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`), Nodes: []protocol.ProgramNode{{ID: "done", Kind: "return", Value: protocol.Expression{Ref: "/input"}}}}
}
func TestBoundedProgramsRejectInvalidControlFlow(t *testing.T) {
	for _, kind := range []string{"read", "guard", "fallback"} {
		p := echoProgram()
		p.Nodes = []protocol.ProgramNode{{ID: "write", Kind: "call", BindingID: "write", BindingVersion: 1, Value: lit(`{}`), Next: "after"}, {ID: "after", Kind: kind, BindingID: "read", BindingVersion: 1, Value: lit(`true`), Next: "done", Otherwise: "done"}, {ID: "done", Kind: "return", Value: lit(`{}`)}}
		if kind != "guard" {
			p.Nodes[1].Otherwise = ""
		}
		if kind == "fallback" {
			p.Nodes[1].Next = ""
		}
		if ValidateProgram(p) == nil {
			t.Fatalf("accepted %s after mutation", kind)
		}
	}
	p := echoProgram()
	p.Nodes = []protocol.ProgramNode{{ID: "a", Kind: "let", Value: lit(`1`), Next: "a"}}
	if ValidateProgram(p) == nil {
		t.Fatal("cycle accepted")
	}
	p = echoProgram()
	p.Nodes[0].Value = protocol.Expression{Op: "exec", Args: []protocol.Expression{lit(`"rm"`)}}
	if ValidateProgram(p) == nil {
		t.Fatal("arbitrary source accepted")
	}
}
func TestProgramReturnsFallbackBeforeTools(t *testing.T) {
	p := echoProgram()
	out, e := Execute(context.Background(), p, []byte(`{"ok":true}`), time.Now(), nil)
	if e != nil || string(out) != `{"ok":true}` {
		t.Fatalf("echo %s %v", out, e)
	}
	_, e = Execute(context.Background(), p, []byte(`[]`), time.Now(), nil)
	var fallback *Fallback
	if !errors.As(e, &fallback) {
		t.Fatalf("schema mismatch did not fall back: %v", e)
	}
	p.Nodes = []protocol.ProgramNode{{ID: "gate", Kind: "guard", Value: lit(`false`), Next: "done", Otherwise: "off"}, {ID: "off", Kind: "fallback", Value: lit(`null`), Reason: "UNSUPPORTED"}, {ID: "done", Kind: "return", Value: lit(`{}`)}}
	_, e = Execute(context.Background(), p, []byte(`{}`), time.Now(), func(context.Context, protocol.ProgramNode, json.RawMessage) (json.RawMessage, error) {
		t.Fatal("tool called on off-path input")
		return nil, nil
	})
	if !errors.As(e, &fallback) || fallback.Reason != "UNSUPPORTED" {
		t.Fatalf("wrong fallback: %v", e)
	}
}
func TestArithmeticRejectsWrongTypesAndOverflow(t *testing.T) {
	for _, e := range []protocol.Expression{{Op: "div", Args: []protocol.Expression{lit(`1`), lit(`0`)}}, {Op: "add", Args: []protocol.Expression{lit(`"1"`), lit(`2`)}}, {Op: "mul", Args: []protocol.Expression{lit(`9007199254740991`), lit(`2`)}}} {
		if _, err := expression(e, map[string]any{}, time.Now()); err == nil {
			t.Fatalf("invalid arithmetic accepted: %+v", e)
		}
	}
}
