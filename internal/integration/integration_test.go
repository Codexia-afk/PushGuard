package integration

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/pushguard/pushguard/internal/config"
	"github.com/pushguard/pushguard/internal/integrity"
	"github.com/pushguard/pushguard/internal/preflight"
	"github.com/pushguard/pushguard/internal/repository"
	"github.com/pushguard/pushguard/internal/runner"
)

func TestRepositoryDiscoveryAndFingerprintWithRealGit(t *testing.T) {
	git := findWorkingGit()
	if git == "" {
		t.Skip("working Git is unavailable")
	}
	if runtime.GOOS != "windows" {
		t.Setenv("PATH", filepath.Dir(git)+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	root := t.TempDir()
	runGit(t, root, "init", "-q", "-b", "main")
	runGit(t, root, "config", "user.email", "pushguard@example.test")
	runGit(t, root, "config", "user.name", "PushGuard Test")
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "main.go")
	runGit(t, root, "commit", "-q", "-m", "initial")
	runGit(t, root, "remote", "add", "origin", "file:///tmp/pushguard-integration-remote")
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\n// changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	repo, err := (repository.Service{Runner: runner.Runner{}}).Discover(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if repo.Branch != "main" || repo.Target.Remote != "origin" || repo.Target.Branch != "main" {
		t.Fatalf("unexpected repo: %+v", repo)
	}
	if len(repo.Changes.All) != 1 || repo.Changes.All[0] != "main.go" {
		t.Fatalf("unexpected changes: %+v", repo.Changes)
	}
	first, err := integrity.Compute(context.Background(), root, config.Hash(config.Default()), runner.Runner{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "untracked.txt"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := integrity.Compute(context.Background(), root, config.Hash(config.Default()), runner.Runner{})
	if err != nil {
		t.Fatal(err)
	}
	if first.Value == second.Value {
		t.Fatal("fingerprint did not change for untracked content")
	}
	pf := (preflight.Service{Runner: runner.Runner{}}).Run(context.Background(), repo, false, false, false, false, []string{"main.go"})
	foundAI := false
	for _, item := range pf.Items {
		if item.Name == "working tree" && item.Blocking {
			foundAI = true
		}
	}
	if !foundAI {
		t.Fatalf("expected uncommitted AI change blocker: %+v", pf.Items)
	}
}

func findWorkingGit() string {
	candidates := []string{}
	if p, err := exec.LookPath("git"); err == nil {
		candidates = append(candidates, p)
	}
	candidates = append(candidates, "/Library/Developer/CommandLineTools/usr/bin/git", "/Applications/Xcode.app/Contents/Developer/usr/bin/git")
	for _, p := range candidates {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		cmd := exec.Command(p, "--version")
		if cmd.Run() == nil {
			return p
		}
	}
	return ""
}
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	path := findWorkingGit()
	if path == "" {
		t.Skip("working Git is unavailable")
	}
	cmd := exec.Command(path, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}
