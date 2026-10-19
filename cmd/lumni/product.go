package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/spf13/cobra"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func productCommands() []*cobra.Command {
	runtime := &cobra.Command{Use: "runtime", Short: "Submit, inspect and control native work"}
	runtime.AddCommand(jsonCommand("submit", "Submit versioned native work", "POST", "/v1/runtime/work", false), jsonCommand("inspect ID", "Inspect work and execution metadata", "GET", "/v1/activity/", true), jsonCommand("cancel ID", "Cancel work and its children", "POST", "/v1/runtime/work/", true), jsonCommand("signal ID", "Deliver an identified durable signal", "POST", "/v1/runtime/work/", true))
	verify := &cobra.Command{Use: "verify", Short: "Configure verifiers and inspect authoritative outcomes"}
	verify.AddCommand(jsonCommand("register", "Register a versioned verifier", "POST", "/v1/verifiers", false), jsonCommand("inspect ID", "Inspect an outcome", "GET", "/v1/outcomes/", true), jsonCommand("retry ID", "Queue verification without repeating the action", "POST", "/v1/outcomes/", true), jsonCommand("evidence ID", "Record authenticated verifier evidence", "POST", "/v1/outcomes/", true))
	replay := jsonCommand("replay ACTION_ID", "Reconstruct immutable evidence without side effects", "GET", "/v1/replay/", true)
	var execute bool
	var newWorkFile string
	replay.Flags().BoolVar(&execute, "execute", false, "Explicitly create new work through normal admission")
	replay.Flags().StringVar(&newWorkFile, "new-work", "", "Native work JSON with a new id, required with --execute")
	original := replay.RunE
	replay.RunE = func(cmd *cobra.Command, args []string) error {
		if len(args) != 1 {
			return errors.New("an action id is required")
		}
		if !execute {
			return original(cmd, args)
		}
		if newWorkFile == "" {
			return errors.New("--execute requires --new-work with a new work id")
		}
		b, err := os.ReadFile(newWorkFile)
		if err != nil {
			return err
		}
		var work map[string]any
		if err = json.Unmarshal(b, &work); err != nil {
			return err
		}
		base, _ := cmd.Flags().GetString("url")
		key, _ := cmd.Flags().GetString("key")
		id, ok := work["id"].(string)
		if !ok || strings.TrimSpace(id) == "" {
			return errors.New("execution replay requires an explicit new work id")
		}
		var source bytes.Buffer
		if err = request(cmd.Context(), base, key, "GET", "/v1/actions/"+url.PathEscape(args[0]), nil, &source); err != nil {
			return err
		}
		var action struct {
			WorkID string `json:"work_id"`
		}
		if err = json.Unmarshal(source.Bytes(), &action); err != nil {
			return err
		}
		if action.WorkID == id {
			return errors.New("execution replay must create a different work id")
		}
		return request(cmd.Context(), base, key, "POST", "/v1/runtime/work", work, cmd.OutOrStdout())
	}
	discover := &cobra.Command{Use: "discover [DIRECTORY]", Short: "Discover supported MCP client configurations without reading credentials", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		if len(args) > 0 {
			cwd = args[0]
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		paths := []string{filepath.Join(cwd, ".mcp.json"), filepath.Join(cwd, ".cursor", "mcp.json"), filepath.Join(home, "Library", "Application Support", "Claude", "claude_desktop_config.json"), filepath.Join(home, ".cursor", "mcp.json")}
		findings := []map[string]any{}
		for _, path := range paths {
			b, e := os.ReadFile(path)
			if os.IsNotExist(e) {
				continue
			}
			if e != nil {
				return e
			}
			var config struct {
				Servers map[string]json.RawMessage `json:"mcpServers"`
			}
			if e = json.Unmarshal(b, &config); e != nil {
				continue
			}
			names := []string{}
			for name := range config.Servers {
				names = append(names, name)
			}
			findings = append(findings, map[string]any{"path": path, "transport": "mcp", "servers": names})
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"integrations": findings})
	}}
	gateway := &cobra.Command{Use: "gateway", Short: "Run a gateway or connect an MCP client"}
	run := &cobra.Command{Use: "run [GATEWAY FLAGS]", DisableFlagParsing: true, RunE: func(cmd *cobra.Command, args []string) error {
		exe, e := os.Executable()
		if e != nil {
			return e
		}
		child := exec.CommandContext(cmd.Context(), filepath.Join(filepath.Dir(exe), "gateway"), args...)
		child.Stdin = os.Stdin
		child.Stdout = cmd.OutOrStdout()
		child.Stderr = cmd.ErrOrStderr()
		return child.Run()
	}}
	var configPath, serverName string
	connect := &cobra.Command{Use: "connect", Short: "Wrap one configured stdio MCP server with Lumni", RunE: func(cmd *cobra.Command, args []string) error {
		if configPath == "" || serverName == "" {
			return errors.New("--config and --server are required")
		}
		b, err := os.ReadFile(configPath)
		if err != nil {
			return err
		}
		var cfg map[string]any
		if err = json.Unmarshal(b, &cfg); err != nil {
			return err
		}
		servers, ok := cfg["mcpServers"].(map[string]any)
		if !ok {
			return errors.New("mcpServers configuration missing")
		}
		up, ok := servers[serverName].(map[string]any)
		if !ok {
			return errors.New("server not found")
		}
		command, ok := up["command"].(string)
		if !ok {
			return errors.New("use gateway run --transport http --upstream for Streamable HTTP")
		}
		if filepath.Base(command) == "gateway" {
			return errors.New("server is already wrapped")
		}
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		argv := []any{"--transport", "stdio", "--command", command, "--"}
		if old, ok := up["args"].([]any); ok {
			argv = append(argv, old...)
		}
		up["command"] = filepath.Join(filepath.Dir(exe), "gateway")
		up["args"] = argv
		encoded, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return err
		}
		backup := configPath + ".lumni-backup"
		f, err := os.OpenFile(backup, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		if _, err = f.Write(b); err != nil {
			f.Close()
			return err
		}
		if err = f.Close(); err != nil {
			return err
		}
		if err = os.WriteFile(configPath, encoded, 0600); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Connected %s. Original configuration saved to %s.\n", serverName, backup)
		return nil
	}}
	connect.Flags().StringVar(&configPath, "config", "", "MCP client configuration path")
	connect.Flags().StringVar(&serverName, "server", "", "Server entry to wrap")
	gateway.AddCommand(run, connect)
	version := &cobra.Command{Use: "version", Run: func(cmd *cobra.Command, args []string) { fmt.Fprintln(cmd.OutOrStdout(), "lumni 0.3.0") }}
	return []*cobra.Command{runtime, verify, replay, discover, gateway, version}
}
func jsonCommand(use, description, method, path string, entity bool) *cobra.Command {
	var base, key, file string
	cmd := &cobra.Command{Use: use, Short: description, RunE: func(cmd *cobra.Command, args []string) error {
		endpoint := path
		if entity {
			if len(args) != 1 {
				return errors.New("an entity id is required")
			}
			endpoint += url.PathEscape(args[0])
			switch cmd.Name() {
			case "cancel":
				endpoint += "/cancel"
			case "signal":
				endpoint += "/signals"
			case "retry":
				endpoint += "/retry"
			case "evidence":
				endpoint += "/evidence"
			}
		}
		var input any = map[string]any{}
		if method == http.MethodGet {
			input = nil
		} else if file != "" {
			b, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			if err = json.Unmarshal(b, &input); err != nil {
				return err
			}
		} else if strings.Contains(use, "submit") || strings.Contains(use, "register") || strings.Contains(use, "signal") || strings.Contains(use, "evidence") {
			return errors.New("--file JSON is required")
		}
		return request(cmd.Context(), base, key, method, endpoint, input, cmd.OutOrStdout())
	}}
	commonHTTPFlags(cmd, &base, &key)
	cmd.Flags().StringVar(&file, "file", "", "JSON request file")
	return cmd
}
