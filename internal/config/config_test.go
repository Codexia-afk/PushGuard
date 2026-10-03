package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadJSONCommandConfig(t *testing.T) {
	root := t.TempDir()
	data := []byte(`{"version":1,"checks":[{"name":"test","category":"test","command":"go test ./...","required":true}],"ai":{"provider":"none"}}`)
	if err := os.WriteFile(filepath.Join(root, ".pushguard.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Checks) != 1 || len(cfg.Checks[0].Args) != 3 || cfg.Checks[0].Args[0] != "go" {
		t.Fatalf("unexpected config: %+v", cfg.Checks)
	}
}

func TestUserConfigurationIsUsedWhenRepositoryHasNoLocalConfig(t *testing.T) {
	root := t.TempDir()
	global := filepath.Join(t.TempDir(), "config.json")
	cfg := Default()
	cfg.AI.Provider = "openai-compatible"
	cfg.AI.Model = "local-model"
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(global, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PUSHGUARD_CONFIG_FILE", global)
	loaded, path, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if path != global || loaded.AI.Provider != cfg.AI.Provider || loaded.AI.Model != cfg.AI.Model {
		t.Fatalf("global configuration was not selected: path=%s cfg=%+v", path, loaded)
	}
}

func TestRepositoryConfigurationOverridesUserConfiguration(t *testing.T) {
	root := t.TempDir()
	global := filepath.Join(t.TempDir(), "config.json")
	globalData, _ := json.Marshal(Default())
	if err := os.WriteFile(global, globalData, 0600); err != nil {
		t.Fatal(err)
	}
	local := Default()
	local.AI.Provider = "ollama"
	local.AI.Model = "local-model"
	localData, _ := json.Marshal(local)
	if err := os.WriteFile(filepath.Join(root, ".pushguard.json"), localData, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PUSHGUARD_CONFIG_FILE", global)
	loaded, path, err := Load(root)
	if err != nil || path != filepath.Join(root, ".pushguard.json") || loaded.AI.Provider != "ollama" {
		t.Fatalf("repository configuration did not win: path=%s cfg=%+v err=%v", path, loaded, err)
	}
}
