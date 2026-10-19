package semantic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

const maxResponseBytes = 2 << 20

type OpenRouterConfig struct {
	APIKey       string
	Model        string
	DecisionsURL string
	ZDRURL       string
	Timeout      time.Duration
	MaxRetries   int
	Client       *http.Client
}

type OpenRouter struct {
	apiKey       string
	model        string
	decisionsURL string
	zdrURL       string
	maxRetries   int
	client       *http.Client
}

func NewOpenRouter(cfg OpenRouterConfig) *OpenRouter {
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: cfg.Timeout}
	}
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 0
	}
	if cfg.MaxRetries > 10 {
		cfg.MaxRetries = 10
	}
	return &OpenRouter{
		apiKey:       cfg.APIKey,
		model:        cfg.Model,
		decisionsURL: cfg.DecisionsURL,
		zdrURL:       cfg.ZDRURL,
		maxRetries:   cfg.MaxRetries,
		client:       client,
	}
}

func (o *OpenRouter) Provider() string       { return ProviderOpenRouter }
func (o *OpenRouter) RequestedModel() string { return o.model }
func (o *OpenRouter) PrivacyEligible(ctx context.Context) (bool, error) {
	return o.ZDREligible(ctx)
}

func (o *OpenRouter) Evaluate(ctx context.Context, input Request) (Response, error) {
	payload, err := json.Marshal(struct {
		Model string `json:"model"`
		Request
	}{Model: o.model, Request: input})
	if err != nil {
		return Response{}, fmt.Errorf("encode semantic request: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt <= o.maxRetries; attempt++ {
		if attempt > 0 {
			delay := time.Duration(1<<(attempt-1)) * 100 * time.Millisecond
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return Response{}, ctx.Err()
			case <-timer.C:
			}
		}
		response, retry, callErr := o.call(ctx, payload)
		if callErr == nil {
			return response, nil
		}
		lastErr = callErr
		if !retry {
			break
		}
	}
	return Response{}, lastErr
}

func (o *OpenRouter) call(ctx context.Context, payload []byte) (Response, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.decisionsURL, bytes.NewReader(payload))
	if err != nil {
		return Response{}, false, err
	}
	req.Header.Set("Authorization", "Bearer "+o.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-OpenRouter-Title", "Lumni")

	resp, err := o.client.Do(req)
	if err != nil {
		return Response{}, ctx.Err() == nil, fmt.Errorf("OpenRouter decisions request: %w", err)
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if readErr != nil {
		return Response{}, true, fmt.Errorf("read OpenRouter decisions response: %w", readErr)
	}
	if len(body) > maxResponseBytes {
		return Response{}, false, errors.New("OpenRouter decisions response exceeds 2 MiB")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// A provider may echo request state in a validation error. Keep response
		// bodies out of logs and the semantic failure audit row.
		return Response{}, retryableStatus(resp.StatusCode), fmt.Errorf("OpenRouter decisions returned %d", resp.StatusCode)
	}
	var result Response
	if err := json.Unmarshal(body, &result); err != nil {
		return Response{}, false, fmt.Errorf("decode OpenRouter decisions response: %w", err)
	}
	if result.Model == "" || result.Answers == nil {
		return Response{}, false, errors.New("OpenRouter decisions response is missing model or answers")
	}
	result.CostMicros = usageCostMicros(result.Model, result.Usage)
	return result, false, nil
}

func (o *OpenRouter) ZDREligible(ctx context.Context) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.zdrURL, nil)
	if err != nil {
		return false, err
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("fetch OpenRouter ZDR catalog: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, fmt.Errorf("OpenRouter ZDR catalog returned %d", resp.StatusCode)
	}
	var catalog struct {
		Data []struct {
			ModelID string `json:"model_id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&catalog); err != nil {
		return false, fmt.Errorf("decode OpenRouter ZDR catalog: %w", err)
	}
	for _, endpoint := range catalog.Data {
		if endpoint.ModelID == o.model {
			return true, nil
		}
		if o.model == "~typesafe/jev-latest" && strings.HasPrefix(endpoint.ModelID, "typesafe/jev-") {
			return true, nil
		}
	}
	return false, nil
}

func retryableStatus(code int) bool {
	return code == http.StatusTooManyRequests || code == 529 || code >= 500
}

func usageCostMicros(model string, usage Usage) int64 {
	if usage.CostUSD > 0 {
		return int64(math.Round(usage.CostUSD * 1_000_000))
	}
	if model == "typesafe/jev-1.13" || strings.HasPrefix(model, "typesafe/jev-1.13-") {
		// $0.042 per million input tokens = 0.042 microdollars/token.
		return int64(math.Round(float64(usage.EffectiveInputTokens()) * 0.042))
	}
	return 0
}
