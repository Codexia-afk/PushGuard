package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pushguard/pushguard/internal/config"
	"github.com/pushguard/pushguard/internal/detector"
	"github.com/pushguard/pushguard/internal/model"
	"github.com/pushguard/pushguard/internal/patch"
	"github.com/pushguard/pushguard/internal/repository"
	"github.com/pushguard/pushguard/internal/testutil"
)

func cli(t *testing.T, input string, args ...string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code := runWith(context.Background(), args, strings.NewReader(input), &out, &out)
	return code, out.String()
}
func configured(t *testing.T) string {
	t.Helper()
	root, _ := testutil.Repository(t)
	testutil.Write(t, root, "implementation.txt", "original\n")
	cfg := config.Default()
	cfg.Checks = []model.Check{{Name: "whitespace", Required: true, Args: []string{"git", "diff", "--check"}}}
	if err := config.Save(root, cfg); err != nil {
		t.Fatal(err)
	}
	testutil.Commit(t, root)
	return root
}
func TestEveryReadOnlyCommandAndStrictFlags(t *testing.T) {
	root := configured(t)
	for _, command := range []string{"version", "config", "doctor", "status", "check", "ready", "review", "explain"} {
		t.Run(command, func(t *testing.T) {
			if command == "explain" {
				cli(t, "", "check", "--repo", root)
			}
			args := []string{command, "--repo", root}
			if command == "check" {
				args = append(args, "--non-interactive")
			}
			code, out := cli(t, "", args...)
			if code != 0 {
				t.Fatalf("%s: %d %s", command, code, out)
			}
		})
	}
	for _, args := range [][]string{{"push", "--typo"}, {"ready", "unexpected"}, {"config", "--non-interactive"}, {"unknown"}} {
		if code, out := cli(t, "y\np\ny\n", args...); code != 2 {
			t.Fatalf("unknown arguments ran workflow: %v %d %s", args, code, out)
		}
	}
}

func TestDoctorRecognizesGoFormatter(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PUSHGUARD_CACHE_DIR", t.TempDir())
	testutil.Write(t, root, "go.mod", "module example.test/doctor\n\ngo 1.22\n")
	if code, out := cli(t, "", "doctor", "--repo", root); code != 0 || !strings.Contains(out, "Go formatter") {
		t.Fatalf("Go project doctor: %d %s", code, out)
	}
}
func TestInitAndStagedReview(t *testing.T) {
	root, _ := testutil.Repository(t)
	testutil.Write(t, root, "file.txt", "initial\n")
	testutil.Commit(t, root)
	if code, out := cli(t, "", "init", "--repo", root); code != 0 {
		t.Fatal(out)
	}
	before, _ := os.ReadFile(filepath.Join(root, ".pushguard.json"))
	if code, _ := cli(t, "", "init", "--repo", root); code != 2 {
		t.Fatal("init overwrote config")
	}
	after, _ := os.ReadFile(filepath.Join(root, ".pushguard.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("config changed")
	}
	testutil.Write(t, root, "file.txt", "staged-review-content\n")
	testutil.Git(t, root, "add", "file.txt")
	code, out := cli(t, "", "review", "--repo", root)
	if code != 0 || !strings.Contains(out, "+staged-review-content") {
		t.Fatalf("staged diff missing: %d %s", code, out)
	}
}
func TestRollbackConsentAndActualRestoration(t *testing.T) {
	root := configured(t)
	repo, err := (repository.Service{}).Discover(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	root = repo.Root
	store := patch.SnapshotStore{}
	snap, err := store.Create(root, []string{"implementation.txt"})
	if err != nil {
		t.Fatal(err)
	}
	testutil.Write(t, root, "implementation.txt", "approved repair\n")
	if err = store.Seal(root, snap); err != nil {
		t.Fatal(err)
	}
	if code, _ := cli(t, "y\n", "rollback", "--repo", root, "--non-interactive"); code != 3 {
		t.Fatal("noninteractive rollback changed files")
	}
	data, _ := os.ReadFile(filepath.Join(root, "implementation.txt"))
	if string(data) != "approved repair\n" {
		t.Fatal("denied rollback modified source")
	}
	if code, out := cli(t, "y\n", "rollback", "--repo", root); code != 0 {
		t.Fatalf("rollback failed %d %s", code, out)
	}
	data, _ = os.ReadFile(filepath.Join(root, "implementation.txt"))
	if string(data) != "original\n" {
		t.Fatal("snapshot was not restored")
	}
}
func TestHookRequiresCurrentReceiptAndVerifiedRef(t *testing.T) {
	root := configured(t)
	head := strings.TrimSpace(testutil.Git(t, root, "rev-parse", "HEAD"))
	if code, _ := cli(t, "", "hook-check", "--repo", root); code != 1 {
		t.Fatal("hook accepted no receipt")
	}
	if code, out := cli(t, "", "ready", "--repo", root); code != 0 {
		t.Fatal(out)
	}
	line := "refs/heads/main " + head + " refs/heads/main " + strings.Repeat("0", 40) + "\n"
	if code, out := cli(t, line, "hook-check", "--repo", root); code != 0 {
		t.Fatal(out)
	}
	if code, _ := cli(t, strings.Replace(line, head, strings.Repeat("0", 40), 1), "hook-check", "--repo", root); code != 1 {
		t.Fatal("hook accepted an unverified ref")
	}
	testutil.Write(t, root, "implementation.txt", "new state\n")
	if code, _ := cli(t, line, "hook-check", "--repo", root); code != 1 {
		t.Fatal("hook accepted stale evidence")
	}
}
func TestHookInstallationPreservesExistingHook(t *testing.T) {
	root, _ := testutil.Repository(t)
	testutil.Write(t, root, "file.txt", "initial")
	testutil.Commit(t, root)
	if code, out := cli(t, "", "init", "--repo", root, "--hook"); code != 0 {
		t.Fatal(out)
	}
	hook := filepath.Join(root, ".git", "hooks", "pre-push")
	data, err := os.ReadFile(hook)
	if err != nil || !strings.Contains(string(data), "hook-check") {
		t.Fatal("hook not installed")
	}
	os.Remove(filepath.Join(root, ".pushguard.json"))
	if code, _ := cli(t, "", "init", "--repo", root, "--hook"); code != 2 {
		t.Fatal("existing hook overwritten")
	}
}

func TestStandaloneFileCheckAndPushRequiresGit(t *testing.T) {
	if len((detector.SystemTools{}).Python("")) == 0 {
		t.Skip("Python 3 is not available")
	}
	dir := t.TempDir()
	t.Setenv("PUSHGUARD_CACHE_DIR", t.TempDir())
	testutil.Write(t, dir, "foo.py", "def f(:\n    pass\n")
	testutil.Write(t, dir, "ok.py", "x = 1\n")
	file := filepath.Join(dir, "foo.py")
	// Flags may follow the file argument.
	code, out := cli(t, "", "check", file, "--non-interactive")
	if code != 1 || !strings.Contains(out, "Standalone verification") || !strings.Contains(out, "foo.py:1") || strings.Contains(out, "ok.py") {
		t.Fatalf("standalone file check: %d\n%s", code, out)
	}
	code, out = cli(t, "", "check", filepath.Join(dir, "ok.py"), "--non-interactive")
	if code != 0 || !strings.Contains(out, "PASS python syntax") {
		t.Fatalf("valid file failed: %d\n%s", code, out)
	}
	if code, out := cli(t, "", "check", filepath.Join(dir, "missing.py")); code != 2 || !strings.Contains(out, "Cannot check") {
		t.Fatalf("missing file: %d %s", code, out)
	}
	if code, out := cli(t, "y\n", "push", "--repo", dir); code != 2 || !strings.Contains(out, "requires a Git repository") {
		t.Fatalf("push without Git: %d %s", code, out)
	}
	if code, _ := cli(t, "", "push", "foo.py"); code != 2 {
		t.Fatal("push accepted positional arguments")
	}
}
