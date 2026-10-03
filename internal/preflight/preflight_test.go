package preflight

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pushguard/pushguard/internal/model"
	"github.com/pushguard/pushguard/internal/repository"
	"github.com/pushguard/pushguard/internal/testutil"
)

func inspect(t *testing.T, root string) model.PreflightResult {
	t.Helper()
	repo, err := (repository.Service{}).Discover(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	return (Service{}).Run(context.Background(), repo, true, true, true, true, nil)
}
func blocked(result model.PreflightResult, name string) bool {
	for _, item := range result.Items {
		if item.Name == name && item.Blocking {
			return true
		}
	}
	return false
}
func TestInitialLargeObjectIsBlockedFromHistory(t *testing.T) {
	root, _ := testutil.Repository(t)
	f, err := os.Create(filepath.Join(root, "large.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(51 * 1024 * 1024); err != nil {
		t.Fatal(err)
	}
	f.Close()
	testutil.Commit(t, root)
	os.Remove(filepath.Join(root, "large.bin"))
	testutil.Commit(t, root)
	result := inspect(t, root)
	if !blocked(result, "outgoing objects") || result.Bytes < 51*1024*1024 {
		t.Fatalf("historical initial-push blob escaped: %+v", result)
	}
}
func TestBadRemoteAndDetachedHead(t *testing.T) {
	root, _ := testutil.Repository(t)
	testutil.Write(t, root, "file.txt", "one")
	testutil.Commit(t, root)
	testutil.Git(t, root, "checkout", "--detach", "-q")
	if result := inspect(t, root); !blocked(result, "branch") {
		t.Fatalf("detached head accepted: %+v", result)
	}
	testutil.Git(t, root, "checkout", "-q", "main")
	testutil.Git(t, root, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "absent.git"))
	if result := inspect(t, root); !blocked(result, "remote reachability") {
		t.Fatalf("bad remote accepted: %+v", result)
	}
}
func TestNonFastForwardAndUnknownResources(t *testing.T) {
	root, remote := testutil.Repository(t)
	testutil.Write(t, root, "file.txt", "one")
	testutil.Commit(t, root)
	base := strings.TrimSpace(testutil.Git(t, root, "rev-parse", "HEAD"))
	testutil.Git(t, root, "checkout", "-q", "-b", "remote-line")
	testutil.Write(t, root, "remote.txt", "remote")
	testutil.Commit(t, root)
	testutil.Git(t, root, "push", remote, "HEAD:refs/heads/main")
	testutil.Git(t, root, "checkout", "-q", "main")
	testutil.Write(t, root, "local.txt", base)
	testutil.Write(t, root, ".github/workflows/test.yml", "name: CI\n")
	testutil.Commit(t, root)
	result := inspect(t, root)
	if !blocked(result, "fast-forward") {
		t.Fatalf("non-fast-forward passed: %+v", result)
	}
	unknown := false
	for _, item := range result.Items {
		if item.Name == "CI resources" && item.Status == model.StatusUnknown && !item.Blocking {
			unknown = true
		}
	}
	if !unknown {
		t.Fatal("unobservable resource state was not marked unknown")
	}
}
func TestMissingLFSIsClassified(t *testing.T) {
	root, _ := testutil.Repository(t)
	testutil.Write(t, root, "file.txt", "one")
	testutil.Commit(t, root)
	repo, err := (repository.Service{}).Discover(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	repo.LFSUsed = true
	repo.LFSInstalled = false
	result := (Service{}).Run(context.Background(), repo, false, true, false, false, nil)
	if !blocked(result, "Git LFS") {
		t.Fatal("missing LFS did not block")
	}
}
