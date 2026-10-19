package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"lumni/pkg/protocol"
)

type initOptions struct {
	URL, Key, Code, Dir, Actor, Gateway, Home string
	Dry, NoMCP, NoHello, URLExplicit          bool
}
type initProject struct{ Languages, Libraries, MCP []string }

func initCommand() *cobra.Command {
	var o initOptions
	cmd := &cobra.Command{Use: "init", Short: "Connect an agent project using a one-time code or environment key", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if !o.NoMCP {
			o.Home, _ = os.UserHomeDir()
		}
		if o.Key == "" {
			o.Key = os.Getenv("LUMNI_API_KEY")
		}
		if o.Code == "" {
			o.Code = os.Getenv("LUMNI_CODE")
		}
		o.URLExplicit = cmd.Flags().Changed("url") || os.Getenv("LUMNI_URL") != ""
		client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		return runInit(cmd.Context(), o, cmd.OutOrStdout(), client)
	}}
	cmd.Flags().StringVar(&o.URL, "url", env("LUMNI_URL", "http://localhost:8080"), "Lumni API URL")
	cmd.Flags().StringVar(&o.Key, "key", "", "Lumni API key (defaults to LUMNI_API_KEY without printing it)")
	cmd.Flags().StringVar(&o.Code, "code", "", "Single-use onboarding code (defaults to LUMNI_CODE)")
	cmd.Flags().StringVar(&o.Dir, "dir", ".", "Agent project directory")
	cmd.Flags().StringVar(&o.Actor, "actor", "", "Actor ID (defaults to the code hint or project name)")
	cmd.Flags().StringVar(&o.Gateway, "gateway", "", "Gateway executable (also detected next to lumni or on PATH)")
	cmd.Flags().BoolVar(&o.Dry, "dry-run", false, "Describe setup without network requests or file changes")
	cmd.Flags().BoolVar(&o.NoMCP, "no-mcp", false, "Skip MCP configuration wrapping")
	cmd.Flags().BoolVar(&o.NoHello, "no-hello", false, "Skip registering the agent and its hello check")
	return cmd
}

func initURL(value string) (string, error) {
	u, err := url.Parse(strings.TrimRight(value, "/"))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("Lumni URL must be an HTTP(S) origin or base path without credentials, query, or fragment")
	}
	return u.String(), nil
}

func initCall(ctx context.Context, client *http.Client, base, key, method, path string, in, out any) error {
	var body []byte
	var err error
	if in != nil {
		body, err = json.Marshal(in)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach the Lumni API: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s returned HTTP %d", method, path, resp.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
	}
	_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<20))
	return err
}

func optionalFile(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return b, err
}

func detectInitProject(dir, home string) initProject {
	p := initProject{}
	if b, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		var pkg struct{ Dependencies, DevDependencies map[string]json.RawMessage }
		_ = json.Unmarshal(b, &pkg)
		lang := "javascript"
		if _, err := os.Stat(filepath.Join(dir, "tsconfig.json")); err == nil || pkg.DevDependencies["typescript"] != nil || pkg.Dependencies["typescript"] != nil {
			lang = "typescript"
		}
		p.Languages = append(p.Languages, lang)
		for _, lib := range []string{"@langchain/langgraph", "langchain", "openai", "@anthropic-ai/sdk", "ai", "@modelcontextprotocol/sdk", "@temporalio/client", "@restatedev/restate-sdk"} {
			if pkg.Dependencies[lib] != nil || pkg.DevDependencies[lib] != nil {
				p.Libraries = append(p.Libraries, lib)
			}
		}
	}
	var python strings.Builder
	for _, name := range []string{"pyproject.toml", "requirements.txt", "Pipfile"} {
		if b, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			python.Write(b)
			python.WriteByte('\n')
		}
	}
	if python.Len() > 0 {
		p.Languages = append(p.Languages, "python")
		for _, lib := range []string{"langgraph", "langchain", "openai", "anthropic", "crewai", "autogen", "temporalio", "restate"} {
			if strings.Contains(python.String(), lib) {
				p.Libraries = append(p.Libraries, lib)
			}
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil {
		p.Languages = append(p.Languages, "go")
		for _, lib := range []string{"go.temporal.io/sdk", "github.com/sashabaranov/go-openai", "github.com/anthropics/anthropic-sdk-go"} {
			if strings.Contains(string(b), lib) {
				p.Libraries = append(p.Libraries, lib)
			}
		}
	}
	paths := []string{filepath.Join(dir, ".mcp.json"), filepath.Join(dir, ".cursor", "mcp.json"), filepath.Join(dir, ".vscode", "mcp.json")}
	if home != "" {
		paths = append(paths, filepath.Join(home, ".cursor", "mcp.json"), filepath.Join(home, "Library", "Application Support", "Claude", "claude_desktop_config.json"), filepath.Join(home, ".config", "Claude", "claude_desktop_config.json"))
	}
	seen := map[string]bool{}
	for _, path := range paths {
		if seen[path] {
			continue
		}
		seen[path] = true
		if b, err := os.ReadFile(path); err == nil {
			var cfg map[string]json.RawMessage
			if json.Unmarshal(b, &cfg) == nil && (cfg["mcpServers"] != nil || cfg["servers"] != nil) {
				p.MCP = append(p.MCP, path)
			}
		}
	}
	sort.Strings(p.Libraries)
	return p
}

func initEnvValue(text, name string) (string, bool) {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
		key, value, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(key) == name {
			value = strings.TrimSpace(value)
			if unquoted, err := strconv.Unquote(value); err == nil {
				value = unquoted
			} else if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
				value = value[1 : len(value)-1]
			}
			return value, true
		}
	}
	return "", false
}

func initEnvPlan(text, base, key string) (string, error) {
	add := []string{}
	for _, pair := range [][2]string{{"LUMNI_URL", base}, {"LUMNI_API_KEY", key}} {
		if existing, ok := initEnvValue(text, pair[0]); ok {
			if existing != pair[1] {
				return "", fmt.Errorf(".env already contains a different %s; existing credentials were preserved", pair[0])
			}
			continue
		}
		add = append(add, pair[0]+"="+strconv.Quote(pair[1]))
	}
	if len(add) == 0 {
		return text, nil
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return text + "\n# Lumni, added by lumni init\n" + strings.Join(add, "\n") + "\n", nil
}

func initAtomicWrite(path string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".lumni-init-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

func initMCPPlan(data []byte, gateway, base, key, actor string) ([]byte, []string, error) {
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, nil, err
	}
	servers, ok := cfg["mcpServers"].(map[string]any)
	if !ok {
		servers, ok = cfg["servers"].(map[string]any)
	}
	if !ok {
		return nil, nil, errors.New("MCP server configuration is missing")
	}
	wrapped := []string{}
	for name, raw := range servers {
		s, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		command, ok := s["command"].(string)
		if !ok || command == "" {
			continue
		}
		if filepath.Base(command) == "gateway" || filepath.Base(command) == "lumni-gateway" {
			continue
		}
		args := []any{"--transport", "stdio", "--command", command, "--"}
		if old, ok := s["args"].([]any); ok {
			args = append(args, old...)
		}
		environment, ok := s["env"].(map[string]any)
		if !ok {
			environment = map[string]any{}
		}
		environment["LUMNI_URL"] = base
		environment["LUMNI_API_KEY"] = key
		environment["LUMNI_GATEWAY_ACTOR"] = actor + "-" + name
		s["command"] = gateway
		s["args"] = args
		s["env"] = environment
		wrapped = append(wrapped, name)
	}
	sort.Strings(wrapped)
	b, err := json.MarshalIndent(cfg, "", "  ")
	return append(b, '\n'), wrapped, err
}

func initActorName(project string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(project) {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			b.WriteRune(c)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteByte('-')
		}
	}
	if value := strings.Trim(b.String(), "-"); value != "" {
		return value
	}
	return "my-agent"
}

func initIgnore(dir string) error {
	path := filepath.Join(dir, ".gitignore")
	old, err := optionalFile(path)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(old), "\n") {
		if containsString([]string{".env", ".env*", "*.env", "/.env"}, strings.TrimSpace(line)) {
			return nil
		}
	}
	text := string(old)
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return initAtomicWrite(path, []byte(text+".env\n"), 0644)
}

func runInit(ctx context.Context, o initOptions, out io.Writer, client *http.Client) error {
	var err error
	o.Dir, err = filepath.Abs(o.Dir)
	if err != nil {
		return err
	}
	if info, err := os.Stat(o.Dir); err != nil || !info.IsDir() {
		return errors.New("--dir must name an existing project directory")
	}
	envPath := filepath.Join(o.Dir, ".env")
	oldEnv, err := optionalFile(envPath)
	if err != nil {
		return err
	}
	storedURL, hasURL := initEnvValue(string(oldEnv), "LUMNI_URL")
	if hasURL && !o.URLExplicit {
		o.URL = storedURL
	}
	o.URL, err = initURL(o.URL)
	if err != nil {
		return err
	}
	if hasURL && storedURL != o.URL {
		return errors.New(".env already contains a different LUMNI_URL; existing configuration was preserved")
	}
	if len(o.Actor) > 200 || strings.IndexFunc(o.Actor, unicode.IsControl) >= 0 {
		return errors.New("invalid actor ID")
	}
	if o.Code != "" {
		if _, exists := initEnvValue(string(oldEnv), "LUMNI_API_KEY"); exists {
			return errors.New("project already has a Lumni key in .env; the onboarding code was not consumed")
		}
	}
	if o.Key == "" && o.Code == "" {
		o.Key, _ = initEnvValue(string(oldEnv), "LUMNI_API_KEY")
	}
	if !o.Dry && o.Key == "" && o.Code == "" {
		return errors.New("provide --code, LUMNI_CODE, or LUMNI_API_KEY")
	}
	if strings.ContainsAny(o.Key, "\r\n\x00") {
		return errors.New("invalid API key")
	}
	found := detectInitProject(o.Dir, o.Home)
	if len(found.Languages) > 0 {
		fmt.Fprintf(out, "Detected %s project", strings.Join(found.Languages, ", "))
		if len(found.Libraries) > 0 {
			fmt.Fprintf(out, " using %s", strings.Join(found.Libraries, ", "))
		}
		fmt.Fprintln(out)
	}
	if o.Dry {
		fmt.Fprintf(out, "Dry run: would configure .env and .gitignore, inspect %d MCP configurations, and register an agent. No files or network requests were made.\n", len(found.MCP))
		return nil
	}
	if o.Code != "" {
		host, _ := os.Hostname()
		var exchanged struct {
			Key   string `json:"api_key"`
			URL   string `json:"api_url"`
			Actor string `json:"actor_hint"`
		}
		if err = initCall(ctx, client, o.URL, "", "POST", "/v1/onboarding/exchange", map[string]string{"code": o.Code, "host": host, "project": filepath.Base(o.Dir)}, &exchanged); err != nil {
			return err
		}
		if exchanged.Key == "" {
			return errors.New("onboarding exchange returned no API key")
		}
		o.Key = exchanged.Key
		if exchanged.URL != "" && !hasURL {
			o.URL, err = initURL(exchanged.URL)
			if err != nil {
				return err
			}
		}
		if o.Actor == "" {
			o.Actor = exchanged.Actor
		}
		// Persist the one-time result before the next network call so a network
		// interruption cannot discard the only copy of the issued credential.
		planned, err := initEnvPlan(string(oldEnv), o.URL, o.Key)
		if err != nil {
			return err
		}
		if err = initIgnore(o.Dir); err != nil {
			return err
		}
		if err = initAtomicWrite(envPath, []byte(planned), 0600); err != nil {
			return err
		}
		oldEnv = []byte(planned)
	}
	if o.Actor == "" {
		o.Actor = initActorName(filepath.Base(o.Dir))
	}
	if len(o.Actor) > 200 || strings.IndexFunc(o.Actor, unicode.IsControl) >= 0 {
		return errors.New("actor ID must contain at most 200 characters and no control characters")
	}
	planned, err := initEnvPlan(string(oldEnv), o.URL, o.Key)
	if err != nil {
		return err
	}
	if err = initCall(ctx, client, o.URL, o.Key, "GET", "/v1/actors?limit=1", nil, nil); err != nil {
		return err
	}
	if planned != string(oldEnv) {
		if err = initAtomicWrite(envPath, []byte(planned), 0600); err != nil {
			return err
		}
	}
	if err = initIgnore(o.Dir); err != nil {
		return err
	}
	fmt.Fprintln(out, "Configured .env and .gitignore; existing unrelated values were preserved.")
	if !o.NoMCP && len(found.MCP) > 0 {
		if o.Gateway == "" {
			if exe, e := os.Executable(); e == nil {
				candidate := filepath.Join(filepath.Dir(exe), "gateway")
				if info, e := os.Stat(candidate); e == nil && !info.IsDir() {
					o.Gateway = candidate
				}
			}
		}
		if o.Gateway == "" {
			o.Gateway, _ = exec.LookPath("gateway")
		}
		if o.Gateway == "" {
			o.Gateway, _ = exec.LookPath("lumni-gateway")
		}
		if o.Gateway == "" {
			fmt.Fprintln(out, "Gateway not found. Install the gateway release binary, then rerun init to wrap MCP servers.")
		} else {
			o.Gateway, err = filepath.Abs(o.Gateway)
			if err != nil {
				return err
			}
			if info, e := os.Stat(o.Gateway); e != nil || info.IsDir() || info.Mode()&0111 == 0 {
				return errors.New("gateway must name an executable file")
			}
			for _, path := range found.MCP {
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				next, names, err := initMCPPlan(data, o.Gateway, o.URL, o.Key, o.Actor)
				if err != nil {
					return err
				}
				if len(names) == 0 {
					continue
				}
				backup, err := os.OpenFile(path+".lumni-backup", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
				if err == nil {
					_, err = backup.Write(data)
					if closeErr := backup.Close(); err == nil {
						err = closeErr
					}
				} else if os.IsExist(err) {
					err = nil
				}
				if err != nil {
					return err
				}
				if err = initAtomicWrite(path, next, 0600); err != nil {
					return err
				}
				fmt.Fprintf(out, "Wrapped %s; original configuration retained in %s.lumni-backup.\n", strings.Join(names, ", "), path)
			}
		}
	}
	if !o.NoHello {
		lang := "node"
		if len(found.Languages) > 0 {
			lang = found.Languages[0]
		}
		if err = initHello(ctx, client, o.URL, o.Key, o.Actor, lang, out); err != nil {
			fmt.Fprintf(out, "Credentials are saved; hello setup did not complete: %v\n", err)
			return err
		}
	}
	fmt.Fprintf(out, "Lumni is configured for actor %s. Credentials are stored in .env.\n", o.Actor)
	if len(found.Languages) > 0 {
		switch found.Languages[0] {
		case "python":
			fmt.Fprintln(out, "Install lumni-sdk, then create Client(os.environ[\"LUMNI_URL\"], os.environ[\"LUMNI_API_KEY\"]).")
		case "go":
			fmt.Fprintln(out, "Create lumni.New(os.Getenv(\"LUMNI_URL\"), os.Getenv(\"LUMNI_API_KEY\")) using lumni/sdk/go.")
		default:
			fmt.Fprintln(out, "Install @lumniverse/sdk, then create new LumniClient(process.env.LUMNI_URL, process.env.LUMNI_API_KEY).")
		}
	}
	return nil
}

func containsString(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

func initHello(ctx context.Context, client *http.Client, base, key, actor, runtime string, out io.Writer) error {
	if err := initCall(ctx, client, base, key, "POST", "/v1/actors", map[string]any{"id": actor, "type": "AGENT", "runtime": runtime, "display_name": actor, "metadata": map[string]string{"source": "lumni init"}}, nil); err != nil {
		return err
	}
	run := actor + "-hello-" + uuid.NewString()
	if err := initCall(ctx, client, base, key, "POST", "/v1/work", map[string]any{"id": run, "actor_id": actor, "goal": "Say hello to Lumni", "risk": "LOW"}, nil); err != nil {
		return err
	}
	var d protocol.Decision
	in := map[string]any{"action_id": run, "work_id": run, "actor_id": actor, "operation": "lumni.hello", "arguments": map[string]string{"from": "lumni init"}, "resources": []map[string]string{{"type": "lumni_hello", "external_id": run, "mode": "WRITE"}}, "risk": "LOW"}
	if err := initCall(ctx, client, base, key, "POST", "/v1/actions/check", in, &d); err != nil {
		return err
	}
	if d.LeaseToken != nil && *d.LeaseToken != "" {
		path := "/v1/actions/" + url.PathEscape(d.ActionID)
		if err := initCall(ctx, client, base, key, "POST", path+"/start", map[string]string{"lease_token": *d.LeaseToken}, nil); err != nil {
			return err
		}
		if err := initCall(ctx, client, base, key, "POST", path+"/result", protocol.ActionResultInput{Status: "SUCCEEDED", LeaseToken: d.LeaseToken, Output: json.RawMessage(`{"hello":true}`)}, nil); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "Registered %s and recorded a hello check: %s.\n", actor, d.Decision)
	return nil
}
