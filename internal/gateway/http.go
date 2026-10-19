package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/lumnihq/lumni/pkg/protocol"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

type HTTPRoute struct {
	Method    string             `json:"method"`
	Path      string             `json:"path"`
	Operation string             `json:"operation"`
	Tool      string             `json:"tool"`
	Source    string             `json:"source"`
	Risk      protocol.RiskLevel `json:"risk"`
}

// OpenAPIRoutes discovers operationIds without guessing mutation semantics.
// Resource/effect mappings must be confirmed separately through Setup or the API.
func OpenAPIRoutes(document []byte) ([]HTTPRoute, error) {
	var spec struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(document, &spec); err != nil {
		return nil, err
	}
	var routes []HTTPRoute
	for path, methods := range spec.Paths {
		for method, raw := range methods {
			if !strings.Contains(" get post put patch delete head options ", " "+method+" ") {
				continue
			}
			var op struct {
				ID string `json:"operationId"`
			}
			if err := json.Unmarshal(raw, &op); err != nil {
				return nil, err
			}
			if op.ID == "" {
				continue
			}
			routes = append(routes, HTTPRoute{Method: strings.ToUpper(method), Path: path, Operation: op.ID, Tool: op.ID, Source: "openapi", Risk: protocol.RiskHigh})
		}
	}
	return routes, nil
}
func matchPath(pattern, path string) (map[string]string, bool) {
	a, b := strings.Split(strings.Trim(pattern, "/"), "/"), strings.Split(strings.Trim(path, "/"), "/")
	if len(a) != len(b) {
		return nil, false
	}
	params := map[string]string{}
	for i, v := range a {
		if strings.HasPrefix(v, "{") && strings.HasSuffix(v, "}") {
			params[strings.Trim(v, "{}")] = b[i]
		} else if v != b[i] {
			return nil, false
		}
	}
	return params, true
}

type retainedHTTP struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    []byte            `json:"body"`
}

// RESTHandler gates mapped HTTP/OpenAPI operations. Unmapped traffic remains
// observational in shadow and fails closed for consequential enforced requests.
func (p *Proxy) RESTHandler(upstream string, routes []HTTPRoute) (http.Handler, error) {
	target, err := url.Parse(upstream)
	if err != nil {
		return nil, err
	}
	if target.Host == "" {
		return nil, errors.New("upstream URL required")
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.FlushInterval = -1
	type callKey struct{}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		if c, ok := r.Context().Value(callKey{}).(activeCall); ok {
			p.report(r.Context(), c, protocol.ActionResultInput{Status: "UNKNOWN"})
		}
		http.Error(w, "upstream unavailable", 502)
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		c, ok := resp.Request.Context().Value(callKey{}).(activeCall)
		if !ok {
			return nil
		}
		var captured bytes.Buffer
		over := false
		resp.Body = &captureBody{ReadCloser: resp.Body, observe: func(b []byte) {
			if captured.Len()+len(b) > 16<<20 {
				over = true
				captured.Reset()
			}
			if !over {
				_, _ = captured.Write(b)
			}
		}, done: func(complete bool) {
			status := "UNKNOWN"
			var output json.RawMessage
			if complete && !over {
				output, _ = json.Marshal(retainedHTTP{Status: resp.StatusCode, Headers: map[string]string{"Content-Type": resp.Header.Get("Content-Type"), "ETag": resp.Header.Get("ETag")}, Body: captured.Bytes()})
				if resp.StatusCode >= 200 && resp.StatusCode < 300 {
					status = "SUCCEEDED"
				}
			}
			p.report(resp.Request.Context(), c, protocol.ActionResultInput{Status: status, Output: output})
		}}
		return nil
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var route *HTTPRoute
		var pathParams map[string]string
		for _, candidate := range routes {
			if candidate.Method != r.Method {
				continue
			}
			if params, ok := matchPath(candidate.Path, r.URL.Path); ok {
				route = &candidate
				pathParams = params
				break
			}
		}
		mode := p.operationMode(r.Context(), "*")
		if route != nil {
			mode = p.operationMode(r.Context(), route.Operation)
		}
		if route == nil {
			if mode == protocol.ModeEnforce && r.Method != http.MethodGet && r.Method != http.MethodHead {
				http.Error(w, "RESOURCE_UNRESOLVED: operation mapping required", 422)
				return
			}
			w.Header().Set("X-Lumni-Coverage", "unmapped")
			proxy.ServeHTTP(w, r)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<20))
		if err != nil {
			http.Error(w, "request too large", 413)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		var payload any
		if len(body) > 0 {
			if json.Unmarshal(body, &payload) != nil {
				payload = string(body)
			}
		}
		args, _ := json.Marshal(map[string]any{"body": payload, "path": pathParams, "query": r.URL.Query()})
		in := protocol.ActionCheckInput{ActionID: r.Header.Get("X-Lumni-Action"), ActorID: p.cfg.ActorID, WorkID: p.cfg.WorkID, Operation: route.Operation, Tool: route.Tool, Source: route.Source, Arguments: args, Risk: route.Risk, ObserveOnly: !p.cfg.ManagedPolicy && mode == protocol.ModeShadow}
		if in.ActionID == "" {
			in.ActionID = "http_" + uuid.NewString()
		}
		if v := r.Header.Get("X-Lumni-Work"); v != "" {
			in.WorkID = v
		}
		d, err := p.cfg.Client.PrepareExternal(r.Context(), in, "")
		if err != nil {
			if mode != protocol.ModeShadow {
				http.Error(w, "COORDINATOR_UNAVAILABLE", 503)
				return
			}
			p.degraded("http_admission_unavailable")
			w.Header().Set("X-Lumni-Coverage", "degraded")
			proxy.ServeHTTP(w, r)
			return
		}
		if d.Decision == protocol.DecisionDeduplicate && d.ExistingActionID != nil {
			data, e := p.cfg.Client.PreviousResult(r.Context(), *d.ExistingActionID)
			var retained retainedHTTP
			if e != nil || json.Unmarshal(data, &retained) != nil || retained.Status == 0 {
				http.Error(w, "RESULT_UNAVAILABLE", 409)
				return
			}
			for k, v := range retained.Headers {
				w.Header().Set(k, v)
			}
			w.Header().Set("X-Lumni-Deduplicated", "true")
			w.WriteHeader(retained.Status)
			_, _ = w.Write(retained.Body)
			return
		}
		if !shouldForward(d) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(409)
			_ = json.NewEncoder(w).Encode(d)
			return
		}
		token := ""
		if d.LeaseToken != nil {
			token = *d.LeaseToken
		}
		started, e := p.cfg.Client.StartActionOnce(r.Context(), d.ActionID, token)
		if e != nil || !started {
			if mode != protocol.ModeShadow {
				http.Error(w, "ACTION_PENDING: existing invocation requires evidence", 409)
				return
			}
			p.degraded("http_start_unavailable")
		}
		w.Header().Set("X-Lumni-Action", d.ActionID)
		r = r.WithContext(context.WithValue(r.Context(), callKey{}, activeCall{ActionID: d.ActionID, LeaseToken: token}))
		proxy.ServeHTTP(w, r)
	}), nil
}

// A2AHandler preserves opaque task messages. Only explicit lumni.action metadata
// can create an enforceable ActionIntent; task text is never interpreted as one.
func (p *Proxy) A2AHandler(upstream string) (http.Handler, error) {
	base, err := p.HTTPHandler(upstream)
	if err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			base.ServeHTTP(w, r)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<20))
		if err != nil {
			http.Error(w, "invalid task request", 400)
			return
		}
		var msg rpcMessage
		_ = json.Unmarshal(body, &msg)
		var params struct {
			Message struct {
				ContextID string `json:"contextId"`
				TaskID    string `json:"taskId"`
				Metadata  struct {
					Lumni struct {
						Action *protocol.ActionCheckInput `json:"action"`
					} `json:"lumni"`
				} `json:"metadata"`
			} `json:"message"`
		}
		_ = json.Unmarshal(msg.Params, &params)
		if in := params.Message.Metadata.Lumni.Action; in != nil && len(in.Resources) > 0 {
			in.Source = "a2a"
			in.Metadata, _ = json.Marshal(map[string]string{"a2a_context_id": params.Message.ContextID, "a2a_task_id": params.Message.TaskID})
			if in.ActionID == "" {
				in.ActionID = "a2a_" + uuid.NewString()
			}
			if in.WorkID == "" {
				in.WorkID = p.cfg.WorkID
			}
			if in.ActorID == "" {
				in.ActorID = p.cfg.ActorID
			}
			mode := p.operationMode(r.Context(), in.Operation)
			in.ObserveOnly = !p.cfg.ManagedPolicy && mode == protocol.ModeShadow
			d, e := p.cfg.Client.PrepareExternal(r.Context(), *in, "")
			if e != nil && mode != protocol.ModeShadow {
				w.WriteHeader(503)
				_, _ = w.Write(rpcError(msg.ID, "COORDINATOR_UNAVAILABLE", "admission unavailable", nil))
				return
			}
			if e == nil && !shouldForward(d) {
				if d.Decision == protocol.DecisionDeduplicate && d.ExistingActionID != nil {
					if output, e := p.cfg.Client.PreviousResult(r.Context(), *d.ExistingActionID); e == nil {
						_, _ = w.Write(rpcResult(msg.ID, output))
						return
					}
				}
				_, _ = w.Write(rpcError(msg.ID, string(d.ReasonCode), "A2A action was not admitted", d))
				return
			}
			if e == nil {
				token := ""
				if d.LeaseToken != nil {
					token = *d.LeaseToken
				}
				started, e := p.cfg.Client.StartActionOnce(r.Context(), d.ActionID, token)
				if (e != nil || !started) && mode != protocol.ModeShadow {
					_, _ = w.Write(rpcError(msg.ID, "ACTION_PENDING", "prior invocation requires evidence", nil))
					return
				}
				scope := r.Header.Get("Mcp-Session-Id")
				if scope == "" {
					scope = "a2a_" + uuid.NewString()
					r.Header.Set("Mcp-Session-Id", scope)
				}
				p.mu.Lock()
				p.active[scope+":"+string(msg.ID)] = activeCall{ActionID: d.ActionID, LeaseToken: token}
				p.mu.Unlock()
			}
		} else {
			w.Header().Set("X-Lumni-Coverage", "observational-task")
		}
		// The base transport does not intercept A2A methods, but records their result.
		r.Body = io.NopCloser(bytes.NewReader(body))
		base.ServeHTTP(w, r)
	}), nil
}
