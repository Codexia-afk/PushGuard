package repository

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pushguard/pushguard/internal/testutil"
)

func TestInitialPushCountsHistoryAndNULRenames(t *testing.T) {
	root, _ := testutil.Repository(t)
	testutil.Write(t, root, "old name.txt", "content\n")
	testutil.Commit(t, root)
	testutil.Write(t, root, "more.txt", "more\n")
	testutil.Commit(t, root)
	testutil.Git(t, root, "mv", "old name.txt", "new name.txt")
	r, err := (Service{}).Discover(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if r.Changes.Commits != 2 || len(r.Changes.Files) != 2 {
		t.Fatalf("initial outgoing history incorrect: %+v", r.Changes)
	}
	if len(r.Changes.All) != 1 || r.Changes.All[0] != "new name.txt" {
		t.Fatalf("NUL rename parsing failed: %+v", r.Changes)
	}
}
func TestPushRemoteAndDifferentUpstreamBranch(t *testing.T) {
	root, remote := testutil.Repository(t)
	testutil.Write(t, root, "file.txt", "one")
	testutil.Commit(t, root)
	testutil.Git(t, root, "push", "-u", "origin", "main:delivery")
	r, err := (Service{}).Discover(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if r.Target.Branch != "delivery" || r.Target.Base == "" || r.Changes.Commits != 0 {
		t.Fatalf("upstream plan wrong: %+v", r)
	}
	testutil.Git(t, root, "remote", "add", "release", remote)
	testutil.Git(t, root, "config", "branch.main.pushRemote", "release")
	r, err = (Service{}).Discover(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if r.Target.Remote != "release" || r.Target.Branch != "main" {
		t.Fatalf("pushRemote ignored: %+v", r.Target)
	}
}
func TestWorktreeUsesActualGitDirectory(t *testing.T) {
	root, _ := testutil.Repository(t)
	testutil.Write(t, root, "file.txt", "one")
	testutil.Commit(t, root)
	work := filepath.Join(t.TempDir(), "secondary")
	testutil.Git(t, root, "worktree", "add", "-q", "-b", "secondary", work)
	r, err := (Service{}).Discover(context.Background(), work)
	if err != nil {
		t.Fatal(err)
	}
	head := strings.TrimSpace(testutil.Git(t, work, "rev-parse", "HEAD"))
	testutil.Write(t, r.GitDir, "MERGE_HEAD", head+"\n")
	r, err = (Service{}).Discover(context.Background(), work)
	if err != nil || !r.MergeState {
		t.Fatalf("worktree merge was missed: %+v %v", r, err)
	}
}

func TestInitialOutgoingReviewContainsEarlierCommits(t *testing.T) {
	root, _ := testutil.Repository(t)
	testutil.Write(t, root, "earlier.txt", "earlier-content\n")
	testutil.Commit(t, root)
	testutil.Write(t, root, "latest.txt", "latest-content\n")
	testutil.Commit(t, root)
	service := Service{}
	repo, err := service.Discover(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	diff, err := service.OutgoingDiff(context.Background(), repo)
	if err != nil || !strings.Contains(diff, "+earlier-content") || !strings.Contains(diff, "+latest-content") {
		t.Fatalf("incomplete initial review: %s %v", diff, err)
	}
}

func TestProjectDiscoveryReportsWorkspacePackageManagerAndCISteps(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{
  "name": "payments-api",
  "packageManager": "pnpm@9.0.0",
  "workspaces": ["apps/*", "packages/*"],
  "scripts": {"lint": "eslint .", "test": "vitest run"},
  "dependencies": {"fastify": "1.0.0", "typescript": "1.0.0"},
  "devDependencies": {"vitest": "1.0.0"}
}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".github", "workflows"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".github", "workflows", "ci.yml"), []byte("jobs:\n  verify:\n    steps:\n      - run: pnpm test\n      - run: pnpm deploy-production\n"), 0600); err != nil {
		t.Fatal(err)
	}
	project := detectProject(root)
	if project.Name != "payments-api" || project.PackageManager != "pnpm" || !project.Workspace {
		t.Fatalf("project metadata incomplete: %+v", project)
	}
	for _, want := range []string{"lint", "test"} {
		if !contains(project.Scripts, want) {
			t.Fatalf("script %q missing: %+v", want, project.Scripts)
		}
	}
	for _, want := range []string{"Fastify", "TypeScript", "Vitest"} {
		if !contains(project.Frameworks, want) {
			t.Fatalf("framework %q missing: %+v", want, project.Frameworks)
		}
	}
	if !contains(project.CILocalSteps, "pnpm test") || !contains(project.CIRemoteSteps, "pnpm deploy-production") {
		t.Fatalf("CI step classification incomplete: local=%v remote=%v", project.CILocalSteps, project.CIRemoteSteps)
	}
}
