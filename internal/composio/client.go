// Package composio provides the opt-in cloud transport for the GitHub pilot.
// It never exposes provider credentials, remote sessions, or arbitrary tools.
package composio

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const ReadTool = "GITHUB_GET_A_PULL_REQUEST"
const UpdateTool = "GITHUB_UPDATE_A_PULL_REQUEST"

type Options struct {
	APIKey, BaseURL, AuthConfigID, SlackAuthConfigID, CallbackURL, WebhookSecret string
	Timeout                                                                      time.Duration
}

type Client struct {
	options Options
	http    *http.Client
}

func NewClient(options Options) (*Client, error) {
	if options.APIKey == "" {
		return nil, nil
	}
	if options.AuthConfigID == "" && options.SlackAuthConfigID == "" {
		return nil, errors.New("Composio requires a GitHub or Slack auth config")
	}
	if options.BaseURL == "" {
		options.BaseURL = "https://backend.composio.dev/api/v3.1"
	}
	u, err := url.Parse(options.BaseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"))) {
		return nil, errors.New("Composio base URL must use HTTPS (HTTP is allowed only on loopback)")
	}
	if options.CallbackURL != "" {
		callback, e := url.Parse(options.CallbackURL)
		if e != nil || callback.Host == "" || callback.User != nil || callback.Scheme != "https" {
			return nil, errors.New("Composio callback URL must use HTTPS")
		}
	}
	if options.Timeout <= 0 {
		options.Timeout = 15 * time.Second
	}
	if options.Timeout > 20*time.Second {
		return nil, errors.New("Composio timeout must be at most 20 seconds")
	}
	options.BaseURL = strings.TrimRight(options.BaseURL, "/")
	return &Client{options: options, http: &http.Client{Timeout: options.Timeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// do deliberately makes one request: mutations are never automatically retried.
// Provider error bodies can contain credentials and are not included in errors.
func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.options.BaseURL+path, body)
	if err != nil {
		return errors.New("COMPOSIO_UNAVAILABLE: invalid request")
	}
	req.Header.Set("x-api-key", c.options.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return errors.New("COMPOSIO_UNAVAILABLE: transport failure; execution may have occurred")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("COMPOSIO_UNAVAILABLE: upstream HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil || len(data) > 2<<20 {
		return errors.New("COMPOSIO_UNAVAILABLE: incomplete or oversized response")
	}
	if out != nil && json.Unmarshal(data, out) != nil {
		return errors.New("COMPOSIO_UNAVAILABLE: invalid response")
	}
	return nil
}

type Link struct {
	ConnectedAccountID string `json:"connected_account_id"`
	RedirectURL        string `json:"redirect_url"`
	ExpiresAt          string `json:"expires_at"`
}

func (c *Client) Connect(ctx context.Context, user string) (Link, error) {
	return c.ConnectToolkit(ctx, user, "github")
}
func (c *Client) ConnectToolkit(ctx context.Context, user, toolkit string) (Link, error) {
	var link Link
	config := c.options.AuthConfigID
	if toolkit == "slack" {
		config = c.options.SlackAuthConfigID
	}
	if config == "" {
		return link, errors.New("COMPOSIO_DISABLED: toolkit auth config is missing")
	}
	in := map[string]any{"auth_config_id": config, "user_id": user}
	if c.options.CallbackURL != "" {
		in["callback_url"] = c.options.CallbackURL
	}
	err := c.do(ctx, "POST", "/connected_accounts/link", in, &link)
	if err != nil {
		return link, err
	}
	u, e := url.Parse(link.RedirectURL)
	if link.ConnectedAccountID == "" || e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return Link{}, errors.New("COMPOSIO_UNAVAILABLE: invalid connection link")
	}
	return link, nil
}

type Account struct {
	ID       string `json:"id"`
	UserID   string `json:"user_id"`
	Status   string `json:"status"`
	Disabled bool   `json:"is_disabled"`
	Toolkit  struct {
		Slug string `json:"slug"`
	} `json:"toolkit"`
	AuthConfig struct {
		ID string `json:"id"`
	} `json:"auth_config"`
}

func (c *Client) Status(ctx context.Context, id, user string) (Account, error) {
	return c.StatusToolkit(ctx, id, user, "github")
}
func (c *Client) StatusToolkit(ctx context.Context, id, user, toolkit string) (Account, error) {
	var account Account
	err := c.do(ctx, "GET", "/connected_accounts/"+url.PathEscape(id), nil, &account)
	config := c.options.AuthConfigID
	if toolkit == "slack" {
		config = c.options.SlackAuthConfigID
	}
	if err == nil && (account.ID != id || account.UserID != user || account.Toolkit.Slug != toolkit || account.AuthConfig.ID != config) {
		err = errors.New("COMPOSIO_ACCOUNT_MISMATCH: unexpected account ownership")
	}
	if account.Disabled {
		account.Status = "DISABLED"
	}
	return account, err
}
func (c *Client) Disconnect(ctx context.Context, id string) error {
	return c.do(ctx, "DELETE", "/connected_accounts/"+url.PathEscape(id), nil, nil)
}

func (c *Client) Session(ctx context.Context, user, account string) (string, error) {
	return c.SessionToolkit(ctx, user, account, "github")
}
func (c *Client) SessionToolkit(ctx context.Context, user, account, toolkit string) (string, error) {
	config := c.options.AuthConfigID
	tools := []string{ReadTool, UpdateTool}
	if toolkit == "slack" {
		config = c.options.SlackAuthConfigID
		tools = []string{"SLACK_SEND_MESSAGE"}
	}
	// These are the REST settings behind the SDK's DIRECT_TOOLS preset.
	in := map[string]any{
		"user_id": user, "toolkits": map[string]any{"enable": []string{toolkit}},
		"auth_configs":       map[string]string{toolkit: config},
		"connected_accounts": map[string]any{toolkit: []string{account}},
		"tools":              map[string]any{toolkit: map[string]any{"enable": tools}},
		"instant":            false, "manage_connections": map[string]bool{"enable": false},
		"workbench": map[string]bool{"enable": false, "enable_proxy_execution": false},
		"search":    map[string]bool{"enable": false}, "execute": map[string]bool{"enable_multi_execute": false},
		"preload": map[string]string{"tools": "all"},
	}
	var out struct {
		ID string `json:"session_id"`
	}
	if err := c.do(ctx, "POST", "/tool_router/session", in, &out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", errors.New("COMPOSIO_UNAVAILABLE: session ID missing")
	}
	return out.ID, nil
}

func (c *Client) Execute(ctx context.Context, session, account, tool string, args any) (json.RawMessage, error) {
	if tool != ReadTool && tool != UpdateTool {
		return nil, errors.New("COMPOSIO_TOOL_DENIED: tool is outside the pilot allowlist")
	}
	return c.executeTool(ctx, session, account, tool, args)
}
func (c *Client) executeTool(ctx context.Context, session, account, tool string, args any) (json.RawMessage, error) {
	var out struct {
		Data       json.RawMessage `json:"data"`
		Error      json.RawMessage `json:"error"`
		Successful *bool           `json:"successful"`
	}
	err := c.do(ctx, "POST", "/tool_router/session/"+url.PathEscape(session)+"/execute", map[string]any{"tool_slug": tool, "arguments": args, "account": account, "enable_auto_workbench_offload": false}, &out)
	if err != nil {
		return nil, err
	}
	if (out.Successful != nil && !*out.Successful) || (len(out.Error) > 0 && string(out.Error) != "null" && string(out.Error) != `""`) || len(out.Data) == 0 || string(out.Data) == "null" {
		return nil, errors.New("COMPOSIO_UNAVAILABLE: tool failed or returned no data")
	}
	return out.Data, nil
}

// VerifyWebhook authenticates the unmodified V3 body and bounds replay age.
func VerifyWebhook(secret string, headers http.Header, body []byte, now time.Time) bool {
	id, stamp := headers.Get("webhook-id"), headers.Get("webhook-timestamp")
	seconds, err := strconv.ParseInt(stamp, 10, 64)
	if secret == "" || id == "" || err != nil || seconds < now.Unix()-300 || seconds > now.Unix()+300 {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(id + "." + stamp + "."))
	_, _ = mac.Write(body)
	for _, candidate := range strings.Fields(headers.Get("webhook-signature")) {
		version, encoded, ok := strings.Cut(candidate, ",")
		if !ok || version != "v1" {
			continue
		}
		received, e := base64.StdEncoding.DecodeString(encoded)
		if e == nil && hmac.Equal(received, mac.Sum(nil)) {
			return true
		}
	}
	return false
}
