package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"lumni/internal/composio"
	"lumni/pkg/protocol"
)

type Config struct {
	Composio            composio.Options
	DatabaseURL         string
	HTTPAddr            string
	OTLPHTTPAddr        string
	OTLPGRPCAddr        string
	BootstrapName       string
	BootstrapSlug       string
	BootstrapAdminKey   string
	DefaultMode         protocol.EnforcementMode
	PayloadRetention    time.Duration
	LeaseTTL            time.Duration
	RequestBodyLimit    int64
	GitHubWebhookSecret string
	GitHubAPIToken      string
	GitHubAPIURL        string
	SecretEncryptionKey string
	LogLevel            string
	WorkerInterval      time.Duration
	WebhookTimeout      time.Duration
	ConsoleDir          string
	SessionIdleTTL      time.Duration
	SessionAbsoluteTTL  time.Duration
	AnalyticsBackend    string
	ClickHouseURL       string
	ClickHouseDatabase  string
	ClickHouseUser      string
	ClickHousePassword  string
	AnalyticsTimeout    time.Duration
	SemanticProvider    string
	OpenRouterAPIKey    string
	JEVModel            string
	SemanticTimeout     time.Duration
	SemanticMaxRetries  int
	SemanticCandidates  int
	OpenRouterDecisions string
	OpenRouterZDR       string

	// PlatformKeys may create tenants (POST /v1/tenants); none turns the route off.
	PlatformKeys []string

	// Limits for workspaces made by sign-up (0: none).
	TenantChecksPerDay    int
	TenantChecksPerSecond int
}

func Load() (Config, error) {
	c := Config{
		Composio:            composio.Options{APIKey: os.Getenv("LUMNI_COMPOSIO_API_KEY"), BaseURL: env("LUMNI_COMPOSIO_BASE_URL", "https://backend.composio.dev/api/v3.1"), AuthConfigID: os.Getenv("LUMNI_COMPOSIO_GITHUB_AUTH_CONFIG_ID"), SlackAuthConfigID: os.Getenv("LUMNI_COMPOSIO_SLACK_AUTH_CONFIG_ID"), CallbackURL: os.Getenv("LUMNI_COMPOSIO_CALLBACK_URL"), WebhookSecret: os.Getenv("LUMNI_COMPOSIO_WEBHOOK_SECRET")},
		DatabaseURL:         env("LUMNI_DATABASE_URL", "postgres://lumni:lumni@localhost:5432/lumni?sslmode=disable"),
		HTTPAddr:            env("LUMNI_HTTP_ADDR", ":8080"),
		OTLPHTTPAddr:        env("LUMNI_OTLP_HTTP_ADDR", ":4318"),
		OTLPGRPCAddr:        env("LUMNI_OTLP_GRPC_ADDR", ":4317"),
		BootstrapName:       env("LUMNI_BOOTSTRAP_TENANT_NAME", "Local Development"),
		BootstrapSlug:       env("LUMNI_BOOTSTRAP_TENANT_SLUG", "local"),
		BootstrapAdminKey:   os.Getenv("LUMNI_BOOTSTRAP_ADMIN_KEY"),
		DefaultMode:         protocol.EnforcementMode(strings.ToUpper(env("LUMNI_MODE", "SHADOW"))),
		GitHubWebhookSecret: os.Getenv("LUMNI_GITHUB_WEBHOOK_SECRET"),
		GitHubAPIToken:      os.Getenv("LUMNI_GITHUB_API_TOKEN"),
		GitHubAPIURL:        env("LUMNI_GITHUB_API_URL", "https://api.github.com"),
		SecretEncryptionKey: env("LUMNI_SECRET_ENCRYPTION_KEY", "local-development-secret-change-me"),
		LogLevel:            env("LUMNI_LOG_LEVEL", "info"),
		ConsoleDir:          env("LUMNI_CONSOLE_DIR", ""),
		AnalyticsBackend:    strings.ToLower(env("LUMNI_ANALYTICS_BACKEND", "postgres")),
		ClickHouseURL:       env("LUMNI_CLICKHOUSE_URL", "http://localhost:8123"),
		ClickHouseDatabase:  env("LUMNI_CLICKHOUSE_DATABASE", "lumni"),
		ClickHouseUser:      env("LUMNI_CLICKHOUSE_USER", "default"),
		ClickHousePassword:  os.Getenv("LUMNI_CLICKHOUSE_PASSWORD"),
		SemanticProvider:    strings.ToLower(env("LUMNI_SEMANTIC_PROVIDER", "off")),
		OpenRouterAPIKey:    os.Getenv("LUMNI_OPENROUTER_API_KEY"),
		JEVModel:            env("LUMNI_JEV_MODEL", "typesafe/jev-1.13"),
		OpenRouterDecisions: env("LUMNI_OPENROUTER_DECISIONS_URL", "https://openrouter.ai/api/alpha/decisions"),
		OpenRouterZDR:       env("LUMNI_OPENROUTER_ZDR_URL", "https://openrouter.ai/api/v1/endpoints/zdr"),
	}
	c.PlatformKeys = list(os.Getenv("LUMNI_PLATFORM_KEY"))
	c.TenantChecksPerDay = int(int64Value("LUMNI_TENANT_CHECKS_PER_DAY", 2000))
	c.TenantChecksPerSecond = int(int64Value("LUMNI_TENANT_CHECKS_PER_SECOND", 10))
	var err error
	if c.Composio.Timeout, err = duration("LUMNI_COMPOSIO_TIMEOUT", 15*time.Second); err != nil || c.Composio.Timeout <= 0 {
		return Config{}, fmt.Errorf("invalid LUMNI_COMPOSIO_TIMEOUT")
	}
	if _, err = composio.NewClient(c.Composio); err != nil {
		return Config{}, err
	}
	if c.PayloadRetention, err = duration("LUMNI_PAYLOAD_RETENTION", 30*24*time.Hour); err != nil || c.PayloadRetention <= 0 {
		return Config{}, fmt.Errorf("invalid LUMNI_PAYLOAD_RETENTION")
	}
	if c.LeaseTTL, err = duration("LUMNI_LEASE_TTL", 30*time.Second); err != nil {
		return Config{}, err
	}
	if c.WorkerInterval, err = duration("LUMNI_WORKER_INTERVAL", 2*time.Second); err != nil {
		return Config{}, err
	}
	if c.WebhookTimeout, err = duration("LUMNI_WEBHOOK_TIMEOUT", 10*time.Second); err != nil {
		return Config{}, err
	}
	if c.SessionIdleTTL, err = duration("LUMNI_SESSION_IDLE_TTL", 8*time.Hour); err != nil {
		return Config{}, err
	}
	if c.SessionAbsoluteTTL, err = duration("LUMNI_SESSION_ABSOLUTE_TTL", 24*time.Hour); err != nil {
		return Config{}, err
	}
	if c.AnalyticsTimeout, err = duration("LUMNI_ANALYTICS_TIMEOUT", 5*time.Second); err != nil {
		return Config{}, err
	}
	if c.SemanticTimeout, err = duration("LUMNI_SEMANTIC_TIMEOUT", 5*time.Second); err != nil || c.SemanticTimeout <= 0 {
		return Config{}, fmt.Errorf("invalid LUMNI_SEMANTIC_TIMEOUT")
	}
	c.SemanticMaxRetries = int(int64Value("LUMNI_SEMANTIC_MAX_RETRIES", 3))
	c.SemanticCandidates = int(int64Value("LUMNI_SEMANTIC_MAX_CANDIDATES", 20))
	if c.SemanticCandidates > 20 {
		return Config{}, fmt.Errorf("LUMNI_SEMANTIC_MAX_CANDIDATES must be at most 20")
	}
	if c.SemanticMaxRetries > 10 {
		return Config{}, fmt.Errorf("LUMNI_SEMANTIC_MAX_RETRIES must be at most 10")
	}
	c.RequestBodyLimit = int64Value("LUMNI_REQUEST_BODY_LIMIT", 2<<20)
	switch c.DefaultMode {
	case protocol.ModeShadow, protocol.ModeWarn, protocol.ModeEnforce:
	default:
		return Config{}, fmt.Errorf("invalid LUMNI_MODE %q", c.DefaultMode)
	}
	if c.AnalyticsBackend != "postgres" && c.AnalyticsBackend != "clickhouse" && c.AnalyticsBackend != "auto" {
		return Config{}, fmt.Errorf("invalid LUMNI_ANALYTICS_BACKEND %q", c.AnalyticsBackend)
	}
	if c.SemanticProvider != "off" && c.SemanticProvider != "openrouter" {
		return Config{}, fmt.Errorf("invalid LUMNI_SEMANTIC_PROVIDER %q", c.SemanticProvider)
	}
	if c.SemanticProvider == "openrouter" && c.OpenRouterAPIKey == "" {
		return Config{}, fmt.Errorf("LUMNI_OPENROUTER_API_KEY is required when LUMNI_SEMANTIC_PROVIDER=openrouter")
	}
	return c, nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// list splits a comma-separated value, dropping blanks.
func list(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func duration(key string, fallback time.Duration) (time.Duration, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return d, nil
}

func int64Value(key string, fallback int64) int64 {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}
