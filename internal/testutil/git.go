// Package testutil builds disposable repositories for real Git safety tests.
package testutil

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pushguard/pushguard/internal/runner"
)

func Git(t *testing.T, root string, args ...string) string {
	t.Helper()
	res := runner.Runner{}.Run(context.Background(), root, append([]string{"git"}, args...), 30*time.Second)
	if res.ExitCode != 0 {
		t.Fatalf("git %v: %s %s", args, res.Stderr, res.Terminated)
	}
	return res.Stdout
}
func Repository(t *testing.T) (string, string) {
	t.Helper()
	t.Setenv("PUSHGUARD_CACHE_DIR", t.TempDir())
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	probe := runner.Runner{}.Run(context.Background(), t.TempDir(), []string{"git", "--version"}, 10*time.Second)
	if probe.ExitCode != 0 {
		t.Skip("working Git unavailable")
	}
	parent := t.TempDir()
	root := filepath.Join(parent, "work")
	remote := filepath.Join(parent, "remote.git")
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	Git(t, parent, "init", "-q", "--bare", remote)
	Git(t, root, "init", "-q", "-b", "main")
	Git(t, root, "config", "user.name", "PushGuard Test")
	Git(t, root, "config", "user.email", "test@example.test")
	Git(t, root, "config", "commit.gpgsign", "false")
	Git(t, root, "config", "core.autocrlf", "false")
	Git(t, root, "remote", "add", "origin", remote)
	return root, remote
}
func Write(t *testing.T, root, name, text string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}
func Commit(t *testing.T, root string) {
	t.Helper()
	Git(t, root, "add", ".")
	Git(t, root, "commit", "-q", "-m", "test commit")
}
