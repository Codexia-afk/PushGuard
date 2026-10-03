package detector

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pushguard/pushguard/internal/config"
)

func TestChecksIncludesAllDetectedEcosystems(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"package.json": `{"scripts":{"lint":"eslint .","test":"npm test"}}`,
		"go.mod":       "module example.test\n\ngo 1.22\n",
		"Cargo.toml":   "[package]\nname = \"example\"\nversion = \"0.1.0\"\n",
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}

	checks, err := Checks(root, config.Default())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"lint", "test", "gofmt", "go vet", "go test", "go build", "cargo check", "cargo clippy", "cargo test", "cargo build"}
	got := Names(checks)
	for _, name := range want {
		found := false
		for _, candidate := range got {
			if candidate == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("detected checks do not include %q: %v", name, got)
		}
	}
}
