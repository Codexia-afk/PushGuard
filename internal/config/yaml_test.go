package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadYAMLCommandConfig(t *testing.T) {
	root := t.TempDir()
	yaml := "version: 1\nchecks:\n  - name: test\n    category: test\n    command: go test ./...\n    required: true\npush:\n  requireExplicitConfirmation: true\n"
	if err := os.WriteFile(filepath.Join(root, ".pushguard.yaml"), []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Checks) != 1 || len(cfg.Checks[0].Args) != 3 {
		t.Fatalf("unexpected YAML checks: %+v", cfg.Checks)
	}
	if !cfg.Push.RequireExplicitConfirmation {
		t.Fatal("push approval was not parsed")
	}
}
