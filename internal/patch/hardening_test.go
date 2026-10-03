package patch

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/pushguard/pushguard/internal/model"
)

func TestValidatorRejectsUnsupportedDiffMetadataAndFileMismatch(t *testing.T) {
	root := t.TempDir()
	cases := []string{
		"diff --git a/main.go b/main.go\nold mode 100644\nnew mode 100755\n--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-a\n+b\n",
		"--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-a\n+b\n",
	}
	if _, err := (Validator{}).Validate(root, model.RepairProposal{Patch: cases[0]}); err == nil {
		t.Fatal("mode metadata was accepted")
	}
	if _, err := (Validator{}).Validate(root, model.RepairProposal{Files: []string{"other.go"}, Patch: cases[1]}); err == nil {
		t.Fatal("mismatched proposal file list was accepted")
	}
}

func TestValidatorRejectsSymlinkedParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	p := model.RepairProposal{Patch: "--- a/linked/file.go\n+++ b/linked/file.go\n@@ -1 +1 @@\n-a\n+b\n"}
	if _, err := (Validator{}).Validate(root, p); err == nil {
		t.Fatal("symlinked parent was accepted")
	}
}

func TestSnapshotRestoreRefusesStateDrift(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	store := SnapshotStore{Base: t.TempDir()}
	snap, err := store.Create(root, []string{"main.go"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("after"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.Seal(root, snap); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("external"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.Restore(root, snap); err == nil {
		t.Fatal("rollback ignored external state drift")
	}
}
