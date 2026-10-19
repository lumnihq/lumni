package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"lumni/pkg/protocol"
	lumni "lumni/sdk/go"
)

type Config struct {
	Client                     *lumni.Client
	ActorID, SessionID, WorkID string
	Risk                       protocol.RiskLevel
	FailClosed                 bool
	Mode                       protocol.EnforcementMode
	OperationModes             map[string]protocol.EnforcementMode
	ManagedPolicy              bool
}
type Proxy struct {
	cfg              Config
	mu               sync.Mutex
	active           map[string]activeCall
	policyMu         sync.Mutex
	policyAt         time.Time
	policyMode       protocol.EnforcementMode
	policyOperations map[string]protocol.EnforcementMode
}
type activeCall struct {
	ActionID, LeaseToken, Method string
	WorkID, SessionID            string
	StepID                       *string
}
type sessionKey struct{}

func session(ctx context.Context) string                    { v, _ := ctx.Value(sessionKey{}).(string); return v }
func rpcKey(ctx context.Context, id json.RawMessage) string { return session(ctx) + ":" + string(id) }

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}
type toolParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Meta      map[string]any  `json:"_meta,omitempty"`
}

func New(cfg Config) *Proxy {
	if cfg.Risk == "" {
		cfg.Risk = protocol.RiskMedium
	}
	if cfg.Mode == "" {
		cfg.Mode = protocol.ModeShadow
	}
	return &Proxy{cfg: cfg, active: map[string]activeCall{}}
}

func (p *Proxy) Stdio(ctx context.Context, command string, args ...string) error {
	if command == "" {
		return errors.New("upstream command is required")
	}
	cmd := exec.CommandContext(ctx, command, args...)
	upstreamIn, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	upstreamOut, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	errCh := make(chan error, 2)
	go func() { errCh <- p.forwardRequests(ctx, os.Stdin, upstreamIn, os.Stdout) }()
	go func() { errCh <- p.forwardResponses(ctx, upstreamOut, os.Stdout) }()
	err = <-errCh
	p.abandon(ctx, "")
	_ = cmd.Process.Kill()
	_ = upstreamIn.Close()
	_ = cmd.Wait()
	return err
}

func (p *Proxy) forwardRequests(ctx context.Context, src io.Reader, dst io.Writer, clientOut io.Writer) error {
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		forward, response := p.intercept(ctx, line)
		if response != nil {
			p.write(clientOut, response)
			continue
		}
		if forward {
			if _, err := dst.Write(append(line, '\n')); err != nil {
				return err
			}
		}
	}
	return scanner.Err()
}
func (p *Proxy) forwardResponses(ctx context.Context, src io.Reader, dst io.Writer) error {
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		p.recordResponse(ctx, line)
		p.write(dst, append(line, '\n'))
	}
	return scanner.Err()
}
func (p *Proxy) write(dst io.Writer, data []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, _ = dst.Write(data)
	if len(data) == 0 || data[len(data)-1] != '\n' {
		_, _ = dst.Write([]byte{'\n'})
	}
}

func (p *Proxy) intercept(ctx context.Context, body []byte) (bool, []byte) {
	var msg rpcMessage
	if json.Unmarshal(body, &msg) != nil || len(msg.ID) == 0 {
		return true, nil
	}
	if msg.Method == "tools/list" {
		p.mu.Lock()
		p.active[rpcKey(ctx, msg.ID)] = activeCall{Method: msg.Method}
		p.mu.Unlock()
		return true, nil
	}
	if msg.Method != "tools/call" {
		return true, nil
	}
	var params toolParams
	if json.Unmarshal(msg.Params, &params) != nil {
		return true, nil
	}
	in := p.actionInput(params)
	if sc := session(ctx); sc != "" {
		in.SessionID = stringPtr(sc)
		if in.WorkID == p.cfg.WorkID {
			in.WorkID += "_" + sc
		}
	}
	mode := p.operationMode(ctx, in.Operation)
	in.ObserveOnly = !p.cfg.ManagedPolicy && mode == protocol.ModeShadow
	if err := p.ensureContext(ctx, in); err != nil {
		if mode != protocol.ModeShadow && (p.cfg.FailClosed || p.cfg.Risk != protocol.RiskLow) {
			return false, rpcError(msg.ID, "COORDINATOR_UNAVAILABLE", err.Error(), nil)
		}
		p.degraded("context_unavailable")
		return true, nil
	}
	decision, err := p.cfg.Client.CheckAction(ctx, in)
	if err != nil {
		if mode != protocol.ModeShadow && (p.cfg.FailClosed || p.cfg.Risk != protocol.RiskLow) {
			return false, rpcError(msg.ID, "COORDINATOR_UNAVAILABLE", err.Error(), nil)
		}
		p.degraded("admission_unavailable")
		return true, nil
	}
	if shouldForward(decision) {
		token := ""
		if decision.LeaseToken != nil {
			token = *decision.LeaseToken
		}
		{
			if err := p.cfg.Client.StartAction(ctx, decision.ActionID, token); err != nil {
				if mode != protocol.ModeShadow {
					return false, rpcError(msg.ID, "ACTION_START_FAILED", err.Error(), decision)
				}
				p.degraded("call_start_unavailable")
			}
		}
		p.mu.Lock()
		p.active[rpcKey(ctx, msg.ID)] = activeCall{ActionID: decision.ActionID, LeaseToken: token, Method: msg.Method, WorkID: in.WorkID, SessionID: session(ctx), StepID: in.StepID}
		p.mu.Unlock()
		return true, nil
	}
	if decision.Decision == protocol.DecisionDeduplicate {
		if decision.ExistingActionID != nil {
			if output, e := p.cfg.Client.PreviousResult(ctx, *decision.ExistingActionID); e == nil {
				return false, rpcResult(msg.ID, output)
			}
		}
		return false, rpcError(msg.ID, "RESULT_UNAVAILABLE", "The existing effect cannot be repeated; its retained result is unavailable", decision)
	}
	return false, rpcError(msg.ID, string(decision.ReasonCode), "Lumni did not admit the tool call", decision)
}

func (p *Proxy) actionInput(params toolParams) protocol.ActionCheckInput {
	actorID, sessionID, workID := p.cfg.ActorID, p.cfg.SessionID, p.cfg.WorkID
	if lm, ok := params.Meta["lumni"].(map[string]any); ok {
		if v, ok := lm["actor_id"].(string); ok {
			actorID = v
		}
		if v, ok := lm["session_id"].(string); ok {
			sessionID = v
		}
		if v, ok := lm["work_id"].(string); ok {
			workID = v
		}
	}
	in := protocol.ActionCheckInput{WorkID: workID, ActorID: actorID, SessionID: stringPtr(sessionID), Operation: params.Name, Tool: params.Name, Source: "mcp", Arguments: params.Arguments, Risk: p.cfg.Risk}
	if lm, ok := params.Meta["lumni"].(map[string]any); ok {
		// Explicit metadata supplies confirmed semantics; ordinary tool text is never interpreted.
		if action, ok := lm["action"]; ok {
			b, _ := json.Marshal(action)
			var explicit protocol.ActionCheckInput
			if json.Unmarshal(b, &explicit) == nil {
				in.Resources = explicit.Resources
				in.Verification = explicit.Verification
				in.EffectKey = explicit.EffectKey
				in.StepID = explicit.StepID
				if explicit.Operation != "" {
					in.Operation = explicit.Operation
				}
			}
		}
	}
	return in
}

func (p *Proxy) ensureContext(ctx context.Context, in protocol.ActionCheckInput) error {
	if _, err := p.cfg.Client.PutActor(ctx, protocol.Actor{ID: in.ActorID, Type: protocol.ActorAgent, Runtime: "mcp", DisplayName: in.ActorID}); err != nil {
		return err
	}
	if in.SessionID != nil {
		if _, err := p.cfg.Client.StartSession(ctx, protocol.Session{ID: *in.SessionID, ActorID: in.ActorID, Runtime: "mcp"}); err != nil {
			return err
		}
	}
	_, err := p.cfg.Client.StartWork(ctx, protocol.Work{ID: in.WorkID, ActorID: in.ActorID, SessionID: in.SessionID, Goal: "MCP tool coordination", Risk: in.Risk})
	return err
}

func (p *Proxy) recordResponse(ctx context.Context, body []byte) {
	var msg rpcMessage
	if json.Unmarshal(body, &msg) != nil || len(msg.ID) == 0 || (len(msg.Result) == 0 && len(msg.Error) == 0) {
		return
	}
	p.mu.Lock()
	active, ok := p.active[rpcKey(ctx, msg.ID)]
	if ok {
		delete(p.active, rpcKey(ctx, msg.ID))
	}
	p.mu.Unlock()
	if !ok {
		return
	}
	if len(msg.Result) > 0 && active.WorkID != "" {
		var result struct {
			Meta struct {
				Lumni struct {
					Observations []protocol.ObservationInput `json:"observations"`
				} `json:"lumni"`
			} `json:"_meta"`
		}
		if json.Unmarshal(msg.Result, &result) == nil {
			for i, observation := range result.Meta.Lumni.Observations {
				observation.WorkID = active.WorkID
				observation.StepID = active.StepID
				observation.ID = fmt.Sprintf("gateway:%s:observation:%d", active.ActionID, i)
				if active.SessionID != "" {
					observation.SessionID = &active.SessionID
				}
				if _, err := p.cfg.Client.Observe(ctx, observation); err != nil {
					p.degraded("observation_capture_unavailable")
				}
			}
		}
	}
	if active.Method == "tools/list" {
		var result struct {
			Tools []struct {
				Name   string          `json:"name"`
				Schema json.RawMessage `json:"inputSchema"`
			} `json:"tools"`
		}
		if json.Unmarshal(msg.Result, &result) == nil {
			for _, tool := range result.Tools {
				if e := p.cfg.Client.DiscoverTool(ctx, "mcp", tool.Name, tool.Schema); e != nil {
					p.degraded("schema_capture_unavailable")
				}
			}
		}
		return
	}
	p.recordHTTPResponse(ctx, active, body)
}
func (p *Proxy) degraded(reason string) {
	slog.Warn("gateway coverage degraded", "reason", reason, "mode", p.cfg.Mode)
}
func (p *Proxy) abandon(ctx context.Context, scope string) {
	p.mu.Lock()
	calls := []activeCall{}
	for key, call := range p.active {
		if strings.HasPrefix(key, scope+":") {
			calls = append(calls, call)
			delete(p.active, key)
		}
	}
	p.mu.Unlock()
	for _, call := range calls {
		if call.ActionID != "" {
			p.report(ctx, call, protocol.ActionResultInput{Status: "UNKNOWN"})
		}
	}
}
func (p *Proxy) report(ctx context.Context, call activeCall, input protocol.ActionResultInput) {
	c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if call.LeaseToken != "" {
		input.LeaseToken = &call.LeaseToken
	}
	if err := p.cfg.Client.ReportResult(c, call.ActionID, input); err != nil {
		p.degraded("result_persistence_unavailable")
	}
}
func (p *Proxy) HTTPHandler(upstream string) (http.Handler, error) {
	return p.httpHandler(upstream, nil)
}
func (p *Proxy) httpHandler(upstream string, transport http.RoundTripper) (http.Handler, error) {
	target, err := url.Parse(upstream)
	if err != nil {
		return nil, err
	}
	if target.Host == "" || (target.Scheme != "http" && target.Scheme != "https") {
		return nil, errors.New("HTTP upstream required")
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.FlushInterval = -1
	if transport != nil {
		proxy.Transport = transport
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		ctx := resp.Request.Context()
		scope := session(ctx)
		isSSE := strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream")
		if resp.StatusCode == http.StatusAccepted {
			return nil
		}
		observer := newWireObserver(isSSE, func(data []byte) { p.recordResponse(ctx, data) })
		resp.Body = &captureBody{ReadCloser: resp.Body, observe: observer.feed, done: func(complete bool) {
			observer.finish(complete)
			if id, ok := ctx.Value(activeKey{}).(string); ok {
				p.mu.Lock()
				call, found := p.active[id]
				if found {
					delete(p.active, id)
				}
				p.mu.Unlock()
				if found && call.ActionID != "" {
					p.report(ctx, call, protocol.ActionResultInput{Status: "UNKNOWN"})
				}
			}
		}}
		if resp.Request.Method == http.MethodDelete && resp.StatusCode < 300 {
			p.abandon(ctx, scope)
		}
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, e error) {
		if key, ok := r.Context().Value(activeKey{}).(string); ok {
			p.mu.Lock()
			call, found := p.active[key]
			delete(p.active, key)
			p.mu.Unlock()
			if found && call.ActionID != "" {
				p.report(r.Context(), call, protocol.ActionResultInput{Status: "UNKNOWN"})
			}
		}
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scope := r.Header.Get("Mcp-Session-Id")
		if scope == "" {
			scope = "request_" + uuid.NewString()
		}
		r = r.WithContext(context.WithValue(r.Context(), sessionKey{}, scope))
		if r.Method != http.MethodPost {
			proxy.ServeHTTP(w, r)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<20))
		if err != nil {
			http.Error(w, "invalid body", 400)
			return
		}
		_ = r.Body.Close()
		forward, response := p.intercept(r.Context(), body)
		if !forward {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(response)
			return
		}
		var msg rpcMessage
		_ = json.Unmarshal(body, &msg)
		r = r.WithContext(context.WithValue(r.Context(), activeKey{}, rpcKey(r.Context(), msg.ID)))
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		proxy.ServeHTTP(w, r)
	}), nil
}

type activeKey struct{}

// Observers see the bytes without buffering or changing the forwarded stream.
// Bounded capture discards oversized evidence and leaves the attempt unknown.
type captureBody struct {
	io.ReadCloser
	once    sync.Once
	observe func([]byte)
	done    func(bool)
}

func (c *captureBody) Read(b []byte) (int, error) {
	n, e := c.ReadCloser.Read(b)
	if n > 0 {
		c.observe(b[:n])
	}
	if e != nil {
		c.once.Do(func() { c.done(e == io.EOF) })
	}
	return n, e
}
func (c *captureBody) Close() error { c.once.Do(func() { c.done(false) }); return c.ReadCloser.Close() }

type wireObserver struct {
	sse  bool
	buf  []byte
	data []byte
	over bool
	emit func([]byte)
}

func newWireObserver(sse bool, emit func([]byte)) *wireObserver {
	return &wireObserver{sse: sse, emit: emit}
}
func (o *wireObserver) feed(b []byte) {
	if o.over {
		return
	}
	if len(o.buf)+len(b) > 16<<20 {
		o.over = true
		o.buf = nil
		return
	}
	o.buf = append(o.buf, b...)
	if !o.sse {
		return
	}
	for {
		i := bytes.IndexByte(o.buf, '\n')
		if i < 0 {
			return
		}
		line := bytes.TrimSuffix(o.buf[:i], []byte{'\r'})
		o.buf = o.buf[i+1:]
		if len(line) == 0 {
			if len(o.data) > 0 {
				o.emit(bytes.TrimSuffix(o.data, []byte{'\n'}))
				o.data = nil
			}
		} else if bytes.HasPrefix(line, []byte("data:")) {
			part := bytes.TrimPrefix(line[5:], []byte{' '})
			if len(o.data)+len(part) > 16<<20 {
				o.over = true
				o.data = nil
				return
			}
			o.data = append(o.data, part...)
			o.data = append(o.data, '\n')
		}
	}
}
func (o *wireObserver) finish(complete bool) {
	if !o.sse && complete && !o.over {
		o.emit(o.buf)
	}
}
func (p *Proxy) recordHTTPResponse(ctx context.Context, call activeCall, body []byte) {
	var msg rpcMessage
	input := protocol.ActionResultInput{Status: "UNKNOWN"}
	if json.Unmarshal(body, &msg) == nil && len(msg.Result) > 0 && len(msg.Error) == 0 {
		var result struct {
			IsError bool `json:"isError"`
		}
		if json.Unmarshal(msg.Result, &result) == nil && !result.IsError {
			input.Status = "SUCCEEDED"
		}
		input.Output = msg.Result
	}
	p.report(ctx, call, input)
}

func shouldForward(d protocol.Decision) bool {
	switch d.Decision {
	case protocol.DecisionAllow, protocol.DecisionWarn, protocol.DecisionWouldAllow, protocol.DecisionWouldWait, protocol.DecisionWouldReplan, protocol.DecisionWouldDedup, protocol.DecisionWouldApproval, protocol.DecisionWouldReject:
		return true
	default:
		return false
	}
}
func rpcError(id json.RawMessage, code, message string, data any) []byte {
	out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "error": map[string]any{"code": -32042, "message": message, "data": map[string]any{"lumni_code": code, "decision": data}}})
	return out
}
func rpcResult(id json.RawMessage, result any) []byte {
	out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": result})
	return out
}
func stringPtr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

// Managed gateways remember the last service policy during outages. Before the
// first successful policy read, consequential calls fail closed.
func (p *Proxy) operationMode(ctx context.Context, operation string) protocol.EnforcementMode {
	if !p.cfg.ManagedPolicy {
		if mode, ok := p.cfg.OperationModes[operation]; ok {
			return mode
		}
		return p.cfg.Mode
	}
	p.policyMu.Lock()
	defer p.policyMu.Unlock()
	if time.Since(p.policyAt) > 2*time.Second {
		var config struct {
			Mode       protocol.EnforcementMode            `json:"mode"`
			Operations map[string]protocol.EnforcementMode `json:"operations"`
		}
		timeout, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := p.cfg.Client.GatewayConfig(timeout, &config)
		cancel()
		p.policyAt = time.Now()
		if err == nil {
			p.policyMode = config.Mode
			p.policyOperations = config.Operations
		} else {
			p.degraded("policy_unavailable")
		}
	}
	if mode, ok := p.policyOperations[operation]; ok {
		return mode
	}
	if mode, ok := p.policyOperations["*"]; ok {
		return mode
	}
	if p.policyMode != "" {
		return p.policyMode
	}
	return protocol.ModeEnforce
}
