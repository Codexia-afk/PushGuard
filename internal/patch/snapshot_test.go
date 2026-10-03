package patch

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSnapshotRestoresOnlyAffectedFilesAndMode(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "src", "file.go")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("before"), 0750); err != nil {
		t.Fatal(err)
	}
	store := SnapshotStore{Base: t.TempDir()}
	snap, err := store.Create(root, []string{"src/file.go", "src/new.go"})
	if err != nil {
		t.Fatal(err)
	}
	latest, err := store.Latest()
	if err != nil || latest.ID != snap.ID {
		t.Fatalf("latest snapshot = %+v, err=%v", latest, err)
	}
	if err := os.WriteFile(path, []byte("after"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "new.go"), []byte("new"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "unrelated.txt"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.Seal(root, snap); err != nil {
		t.Fatal(err)
	}
	if err := store.Restore(root, snap); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "before" {
		t.Fatalf("restored content = %q", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0750 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(root, "src/new.go")); !os.IsNotExist(err) {
		t.Fatalf("new file was not removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "unrelated.txt")); err != nil {
		t.Fatalf("unrelated file lost: %v", err)
	}
}
