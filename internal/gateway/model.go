package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/lumnihq/lumni/pkg/protocol"
	lumni "github.com/lumnihq/lumni/sdk/go"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// ModelHandler is an opt-in OpenAI-compatible Responses/Chat proxy. It stores a
// result only after the provider's complete terminal event and intact transport.
func (p *Proxy) ModelHandler(upstream string) (http.Handler, error) {
	target, err := url.Parse(upstream)
	if err != nil {
		return nil, err
	}
	if target.Host == "" {
		return nil, errors.New("model upstream URL required")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || (!strings.HasSuffix(r.URL.Path, "/responses") && !strings.HasSuffix(r.URL.Path, "/chat/completions")) {
			http.Error(w, "supported model endpoint required", 404)
			return
		}
		input, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<20))
		if err != nil {
			http.Error(w, "invalid model request", 400)
			return
		}
		var config struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		if json.Unmarshal(input, &config) != nil {
			http.Error(w, "invalid model JSON", 400)
			return
		}
		id := r.Header.Get("X-Lumni-Call")
		if id == "" {
			id = "model_" + uuid.NewString()
		}
		work := r.Header.Get("X-Lumni-Work")
		if work == "" {
			work = p.cfg.WorkID
		}
		capture := map[string]any{"id": id, "work_id": work, "provider": "openai-compatible", "model": config.Model, "input": json.RawMessage(input), "status": "PENDING"}
		if step := r.Header.Get("X-Lumni-Step"); step != "" {
			capture["step_id"] = step
		}
		var admitted lumni.ModelCallAdmission
		if err = p.ensureContext(r.Context(), protocol.ActionCheckInput{ActorID: p.cfg.ActorID, WorkID: work, Risk: protocol.RiskLow}); err == nil {
			admitted, err = p.cfg.Client.BeginModelCall(r.Context(), capture)
		}
		if err != nil {
			http.Error(w, "model capture unavailable", 503)
			return
		}
		if !admitted.Created {
			if admitted.Status == "COMPLETE" {
				if result, e := p.cfg.Client.ModelResult(r.Context(), id); e == nil {
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("X-Lumni-Deduplicated", "true")
					_, _ = w.Write(result)
					return
				}
			}
			http.Error(w, "MODEL_RESULT_UNAVAILABLE: prior call will not be repeated", 409)
			return
		}
		w.Header().Set("X-Lumni-Call", id)
		proxy := httputil.NewSingleHostReverseProxy(target)
		proxy.FlushInterval = -1
		finish := func(status string, output, usage json.RawMessage) {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
			defer cancel()
			capture["status"] = status
			if output != nil {
				capture["output"] = output
			}
			if usage != nil {
				capture["usage"] = usage
			}
			if err := p.cfg.Client.RecordModelCall(ctx, capture); err != nil {
				p.degraded("model_result_persistence_unavailable")
			}
		}
		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, e error) {
			finish("INCOMPLETE", nil, nil)
			http.Error(w, "model upstream unavailable", 502)
		}
		proxy.ModifyResponse = func(resp *http.Response) error {
			var result, usage json.RawMessage
			terminal := false
			observerOverflow := false
			events := []json.RawMessage{}
			totalBytes := 0
			responses := strings.HasSuffix(r.URL.Path, "/responses")
			observer := newWireObserver(config.Stream, func(data []byte) {
				if !config.Stream {
					if json.Valid(data) {
						result = append([]byte(nil), data...)
						terminal = true
						var parsed struct {
							Usage json.RawMessage `json:"usage"`
						}
						_ = json.Unmarshal(data, &parsed)
						usage = parsed.Usage
					}
					return
				}
				if string(data) == "[DONE]" {
					if !responses {
						terminal = true
					}
					return
				}
				var event struct {
					Type     string          `json:"type"`
					Response json.RawMessage `json:"response"`
					Usage    json.RawMessage `json:"usage"`
				}
				if json.Unmarshal(data, &event) != nil {
					return
				}
				if event.Type == "response.completed" {
					result = event.Response
					var response struct {
						Usage json.RawMessage `json:"usage"`
					}
					_ = json.Unmarshal(result, &response)
					usage = response.Usage
					terminal = json.Valid(result)
				}
				if len(event.Usage) > 0 {
					usage = event.Usage
				}
				totalBytes += len(data)
				if totalBytes <= 16<<20 && len(events) < 20000 {
					events = append(events, append([]byte(nil), data...))
				} else {
					terminal = false
					observerOverflow = true
				}
			})
			resp.Body = &captureBody{ReadCloser: resp.Body, observe: observer.feed, done: func(complete bool) {
				observer.finish(complete)
				if complete && terminal && !observer.over && !observerOverflow && resp.StatusCode >= 200 && resp.StatusCode < 300 {
					if len(result) == 0 {
						result, err = assembleChat(events)
						if err != nil {
							finish("INCOMPLETE", nil, usage)
							return
						}
					}
					finish("COMPLETE", result, usage)
				} else {
					finish("INCOMPLETE", nil, usage)
				}
			}}
			return nil
		}
		r.Body = io.NopCloser(bytes.NewReader(input))
		proxy.ServeHTTP(w, r)
	}), nil
}
