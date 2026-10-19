package config

import (
	"strings"
	"testing"
	"time"
)

func TestSemanticDefaultsOff(t *testing.T) {
	for _, key := range []string{"LUMNI_SEMANTIC_PROVIDER", "LUMNI_OPENROUTER_API_KEY", "LUMNI_JEV_MODEL", "LUMNI_SEMANTIC_TIMEOUT", "LUMNI_SEMANTIC_MAX_RETRIES", "LUMNI_SEMANTIC_MAX_CANDIDATES"} {
		t.Setenv(key, "")
	}
	config, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if config.SemanticProvider != "off" || config.JEVModel != "typesafe/jev-1.13" || config.SemanticTimeout != 5*time.Second || config.SemanticMaxRetries != 3 || config.SemanticCandidates != 20 {
		t.Fatalf("unexpected semantic defaults: %+v", config)
	}
}

func TestOpenRouterRequiresKey(t *testing.T) {
	t.Setenv("LUMNI_SEMANTIC_PROVIDER", "openrouter")
	t.Setenv("LUMNI_OPENROUTER_API_KEY", "")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "LUMNI_OPENROUTER_API_KEY") {
		t.Fatalf("error=%v", err)
	}
}

func TestComposioDefaultsOffAndRequiresAuthConfig(t *testing.T) {
	t.Setenv("LUMNI_COMPOSIO_API_KEY", "")
	config, err := Load()
	if err != nil || config.Composio.APIKey != "" || config.Composio.Timeout != 15*time.Second {
		t.Fatalf("unexpected Composio default: %v", err)
	}
	t.Setenv("LUMNI_COMPOSIO_API_KEY", "test-key")
	t.Setenv("LUMNI_COMPOSIO_GITHUB_AUTH_CONFIG_ID", "")
	if _, err = Load(); err == nil {
		t.Fatal("enabled Composio must require an auth config")
	}
}
