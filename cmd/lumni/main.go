package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/lumnihq/lumni/internal/auth"
	"github.com/lumnihq/lumni/internal/config"
	"github.com/lumnihq/lumni/internal/store"
	"github.com/lumnihq/lumni/pkg/protocol"
)

func main() {
	root := &cobra.Command{Use: "lumni", Short: "Lumni coordination runtime CLI"}
	root.AddCommand(migrateCommand(), bootstrapCommand(), modeCommand(), getCommand(), repoCommand(), benchmarkCommand(), initCommand())
	root.AddCommand(productCommands()...)
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func benchmarkCommand() *cobra.Command {
	var baseURL, key string
	var requests, concurrency int
	cmd := &cobra.Command{Use: "benchmark", Short: "Measure local shadow admission latency", RunE: func(cmd *cobra.Command, args []string) error {
		if requests < 1 || concurrency < 1 || concurrency > requests {
			return errors.New("requests must be positive and concurrency must be between 1 and requests")
		}
		actorID, workID := "lumni-benchmark", "lumni-benchmark-work"
		if err := request(cmd.Context(), baseURL, key, http.MethodPost, "/v1/actors", map[string]any{"id": actorID, "type": "AGENT", "runtime": "benchmark"}, io.Discard); err != nil {
			return err
		}
		if err := request(cmd.Context(), baseURL, key, http.MethodPost, "/v1/work", map[string]any{"id": workID, "actor_id": actorID, "goal": "Local admission benchmark", "risk": "LOW"}, io.Discard); err != nil {
			return err
		}
		if err := request(cmd.Context(), baseURL, key, http.MethodPost, "/v1/resources/resolve", map[string]any{"namespace": "benchmark", "type": "benchmark", "external_id": "shared"}, io.Discard); err != nil {
			return err
		}

		jobs := make(chan int)
		durations := make(chan time.Duration, requests)
		errorsSeen := make(chan error, requests)
		client := &http.Client{Timeout: 10 * time.Second}
		var workers sync.WaitGroup
		for range concurrency {
			workers.Add(1)
			go func() {
				defer workers.Done()
				for sequence := range jobs {
					payload, _ := json.Marshal(map[string]any{"work_id": workID, "actor_id": actorID, "operation": "benchmark.observe", "arguments": map[string]any{"sequence": sequence}, "resources": []map[string]any{{"namespace": "benchmark", "type": "benchmark", "external_id": "shared", "mode": "READ"}}, "risk": "LOW"})
					req, err := http.NewRequestWithContext(cmd.Context(), http.MethodPost, strings.TrimRight(baseURL, "/")+"/v1/actions/observe", bytes.NewReader(payload))
					if err != nil {
						errorsSeen <- err
						continue
					}
					req.Header.Set("Authorization", "Bearer "+key)
					req.Header.Set("Content-Type", "application/json")
					started := time.Now()
					resp, err := client.Do(req)
					durations <- time.Since(started)
					if err != nil {
						errorsSeen <- err
						continue
					}
					_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
					_ = resp.Body.Close()
					if resp.StatusCode != http.StatusOK {
						errorsSeen <- fmt.Errorf("benchmark request returned HTTP %d", resp.StatusCode)
					}
				}
			}()
		}
		for i := range requests {
			jobs <- i
		}
		close(jobs)
		workers.Wait()
		close(durations)
		close(errorsSeen)
		for err := range errorsSeen {
			if err != nil {
				return err
			}
		}
		values := make([]time.Duration, 0, requests)
		for duration := range durations {
			values = append(values, duration)
		}
		sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
		percentile := func(p float64) float64 {
			index := int(float64(len(values)-1) * p)
			return float64(values[index].Microseconds()) / 1000
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"requests": requests, "concurrency": concurrency, "p50_ms": percentile(.50), "p95_ms": percentile(.95), "p99_ms": percentile(.99)})
	}}
	commonHTTPFlags(cmd, &baseURL, &key)
	cmd.Flags().IntVar(&requests, "requests", 1000, "number of observed admissions")
	cmd.Flags().IntVar(&concurrency, "concurrency", 4, "parallel requests")
	return cmd
}

func migrateCommand() *cobra.Command {
	var direction string
	cmd := &cobra.Command{Use: "migrate", Short: "Apply or inspect database migrations", RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		return store.Migrate(cmd.Context(), cfg.DatabaseURL, direction)
	}}
	cmd.Flags().StringVar(&direction, "direction", "up", "up, down, down-to-zero, or status")
	return cmd
}

func bootstrapCommand() *cobra.Command {
	var name, slug, key, mode string
	cmd := &cobra.Command{Use: "bootstrap", Short: "Create the first tenant and admin API key", RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		generated := false
		if key == "" {
			key, err = auth.Generate()
			if err != nil {
				return err
			}
			generated = true
		}
		if err := store.Migrate(cmd.Context(), cfg.DatabaseURL, "up"); err != nil {
			return err
		}
		pools, err := store.Open(cmd.Context(), cfg.DatabaseURL)
		if err != nil {
			return err
		}
		defer pools.Close()
		id, err := auth.New(pools.Query).Bootstrap(cmd.Context(), name, slug, key, protocol.EnforcementMode(strings.ToUpper(mode)))
		if err == nil {
			fmt.Printf("tenant_id=%s tenant_slug=%s\n", id, slug)
			if generated {
				fmt.Printf("api_key=%s\n", key)
				fmt.Println("store this key now; it will not be shown again")
			}
		}
		return err
	}}
	cmd.Flags().StringVar(&name, "name", "Local Development", "tenant name")
	cmd.Flags().StringVar(&slug, "slug", "local", "tenant slug")
	cmd.Flags().StringVar(&key, "key", "", "initial admin key (generated when omitted)")
	cmd.Flags().StringVar(&mode, "mode", "SHADOW", "SHADOW, WARN, or ENFORCE")
	return cmd
}

func modeCommand() *cobra.Command {
	var baseURL, key, mode string
	cmd := &cobra.Command{Use: "mode", Short: "Change the tenant enforcement mode", RunE: func(cmd *cobra.Command, args []string) error {
		return request(cmd.Context(), baseURL, key, http.MethodPut, "/v1/config/mode", map[string]any{"mode": strings.ToUpper(mode)}, os.Stdout)
	}}
	commonHTTPFlags(cmd, &baseURL, &key)
	cmd.Flags().StringVar(&mode, "set", "SHADOW", "mode to set")
	return cmd
}

func getCommand() *cobra.Command {
	var baseURL, key string
	cmd := &cobra.Command{Use: "get [actors|agents|work|resources|actions/ID|replay/ID]", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return request(cmd.Context(), baseURL, key, http.MethodGet, "/v1/"+strings.TrimPrefix(args[0], "/"), nil, os.Stdout)
	}}
	commonHTTPFlags(cmd, &baseURL, &key)
	return cmd
}

func repoCommand() *cobra.Command {
	var baseURL, key, path string
	cmd := &cobra.Command{Use: "repo-import", Short: "Import local Git identity without source contents", RunE: func(cmd *cobra.Command, args []string) error {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		head, err := git(absolute, "rev-parse", "HEAD")
		if err != nil {
			return err
		}
		origin, _ := git(absolute, "remote", "get-url", "origin")
		branch, _ := git(absolute, "branch", "--show-current")
		name := filepath.Base(absolute)
		external := origin
		if external == "" {
			external = absolute
		}
		metadata := map[string]any{"head_sha": head, "local_path_hash_only": true, "branch": branch}
		input := map[string]any{"provider": "git", "external_id": external, "name": name, "default_branch": branch, "url": origin, "metadata": metadata}
		return request(cmd.Context(), baseURL, key, http.MethodPost, "/v1/repositories", input, os.Stdout)
	}}
	commonHTTPFlags(cmd, &baseURL, &key)
	cmd.Flags().StringVar(&path, "path", ".", "repository path")
	return cmd
}

func commonHTTPFlags(cmd *cobra.Command, baseURL, key *string) {
	cmd.Flags().StringVar(baseURL, "url", env("LUMNI_URL", "http://localhost:8080"), "Lumni API URL")
	cmd.Flags().StringVar(key, "key", os.Getenv("LUMNI_API_KEY"), "Lumni API key")
}
func request(ctx context.Context, baseURL, key, method, path string, input any, out io.Writer) error {
	var body []byte
	var err error
	if input != nil {
		body, err = json.Marshal(input)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(baseURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, data)
	}
	var pretty bytes.Buffer
	if json.Indent(&pretty, data, "", "  ") == nil {
		data = pretty.Bytes()
	}
	_, err = out.Write(append(data, '\n'))
	return err
}
func git(path string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = path
	data, err := cmd.Output()
	return strings.TrimSpace(string(data)), err
}
func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
