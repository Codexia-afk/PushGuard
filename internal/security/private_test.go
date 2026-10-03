package security

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSecureEvidenceDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := SecureDirectory(dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil || !PrivateMode(info.Mode()) {
		t.Fatalf("private directory: %v", err)
	}
	path := filepath.Join(dir, "evidence")
	if err := os.WriteFile(path, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "private" {
		t.Fatalf("private evidence unusable: %v", err)
	}
}
