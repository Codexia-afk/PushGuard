package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestInitNeverOverwritesAnyConfiguration(t *testing.T) {
	for _, name := range []string{".pushguard.json", ".pushguard.yaml", ".pushguard.yml"} {
		root := t.TempDir()
		path := filepath.Join(root, name)
		os.WriteFile(path, []byte("original"), 0600)
		if err := Save(root, Default()); err == nil {
			t.Fatalf("overwrote %s", name)
		}
		data, _ := os.ReadFile(path)
		if string(data) != "original" {
			t.Fatal("config changed")
		}
	}
}
func TestCommandParsingIsPortableAndRejectsMalformedInput(t *testing.T) {
	for _, v := range []struct {
		input string
		want  []string
	}{{`"C:\Program Files\Tool\tool.exe" "a b" ""`, []string{`C:\Program Files\Tool\tool.exe`, "a b", ""}}, {`go test ./...`, []string{"go", "test", "./..."}}, {`echo '$HOME' '$(whoami)'`, []string{"echo", "$HOME", "$(whoami)"}}} {
		got, err := ParseCommand(v.input)
		if err != nil || !reflect.DeepEqual(got, v.want) {
			t.Fatalf("%s: %v %v", v.input, got, err)
		}
	}
	for _, input := range []string{"", `go "unterminated`, "go\nrun"} {
		if _, err := ParseCommand(input); err == nil {
			t.Fatalf("malformed command accepted: %q", input)
		}
	}
}
func TestInvalidPolicyAndUnknownKeysFailClosed(t *testing.T) {
	for _, data := range []string{`{"version":99}`, `{"push":{"requireExplicitConfirmation":false}}`, `{"repair":{"maxAttempts":-1}}`, `{"unknown":true}`, `{"checks":[{"name":"test","args":[]}]}`, `{"checks":[{"name":"test","args":["go"],"unknown":true}]}`} {
		root := t.TempDir()
		os.WriteFile(filepath.Join(root, ".pushguard.json"), []byte(data), 0600)
		if _, _, err := Load(root); err == nil {
			t.Fatalf("invalid configuration accepted: %s", data)
		}
	}
}

func TestMultipleOrLinkedConfigurationsFailClosed(t *testing.T) {
	t.Run("multiple", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, ".pushguard.json"), []byte(`{"version":1}`), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".pushguard.yaml"), []byte("version: 1\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Load(root); err == nil {
			t.Fatal("multiple configuration formats were accepted")
		}
	})
	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "real.json")
		if err := os.WriteFile(target, []byte(`{"version":1}`), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, ".pushguard.json")); err != nil {
			t.Skipf("symbolic links unavailable: %v", err)
		}
		if _, _, err := Load(root); err == nil {
			t.Fatal("symbolic-link configuration was accepted")
		}
	})
}

func TestChecksAreRequiredByDefault(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, ".pushguard.json"), []byte(`{"checks":[{"name":"one","command":"go test ./..."}]}`), 0600)
	cfg, _, err := Load(root)
	if err != nil || !cfg.Checks[0].Required {
		t.Fatal("missing required flag silently disabled verification")
	}
}
func TestYAMLErrorsAreVisible(t *testing.T) {
	for _, data := range []string{"push:\n  typo: true\n", "repair:\n  enabled: maybe\n", "checks:\n - name: test\n", "checks:\n  - name: test\n    command: go 'bad\n"} {
		root := t.TempDir()
		os.WriteFile(filepath.Join(root, ".pushguard.yaml"), []byte(data), 0600)
		if _, _, err := Load(root); err == nil {
			t.Fatalf("invalid YAML accepted: %s", data)
		}
	}
}
