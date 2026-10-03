package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAIConfigurationAliasesAndExplicitDisable(t *testing.T) {
	for _, tc := range []struct {
		data string
		ok   bool
	}{
		{`{"ai":{"enabled":true,"provider":"ollama","model":"my-code-model","base_url":"http://127.0.0.1:11434","api_key_env":"MY_AI_KEY"}}`, true},
		{`{"ai":{"provider":"ollama","model":"model","endpoint":"http://localhost:11434"}}`, true},
		{`{"ai":{"provider":"ollama","api_key":"plaintext"}}`, false},
		{`{"ai":{"provider":"ollama","base_url":"http://localhost","endpoint":"http://localhost"}}`, false},
		{`{"ai":{"apiKeyEnv":"key has spaces"}}`, false},
	} {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, ".pushguard.json"), []byte(tc.data), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, _, err := Load(root)
		if (err == nil) != tc.ok {
			t.Fatalf("config %s: %v", tc.data, err)
		}
		if tc.ok && (!cfg.AI.IsEnabled() || cfg.AI.Endpoint == "") {
			t.Fatalf("config not normalized: %+v", cfg.AI)
		}
	}
	root := t.TempDir()
	global := filepath.Join(t.TempDir(), "config.json")
	_ = os.WriteFile(global, []byte(`{"ai":{"provider":"ollama","model":"global-code"}}`), 0600)
	t.Setenv("PUSHGUARD_CONFIG_FILE", global)
	_ = os.WriteFile(filepath.Join(root, ".pushguard.json"), []byte(`{"ai":{"provider":"none"}}`), 0600)
	cfg, path, err := Load(root)
	if err != nil || cfg.AI.IsEnabled() || path != filepath.Join(root, ".pushguard.json") {
		t.Fatal("repository disable did not override user AI")
	}
}
