package patch

import (
	"testing"

	"github.com/pushguard/pushguard/internal/model"
)

func TestPatchFiles(t *testing.T) {
	patch := "--- a/src/a.go\n+++ b/src/a.go\n@@ -1 +1 @@\n-old\n+new\n--- a/src/deleted.go\n+++ /dev/null\n@@ -1 +0,0 @@\n-old\n"
	files := PatchFiles(patch)
	if len(files) != 2 || files[0] != "src/a.go" || files[1] != "src/deleted.go" {
		t.Fatalf("files = %#v", files)
	}
}

func TestValidatorRejectsTraversalAndSensitiveFiles(t *testing.T) {
	v := Validator{Sensitive: func(path string) bool { return path == ".env" }}
	for _, tc := range []struct{ name, patch string }{
		{"traversal", "--- a/../secret\n+++ b/../secret\n@@ -1 +1 @@\n-a\n+b\n"},
		{"sensitive", "--- a/.env\n+++ b/.env\n@@ -1 +1 @@\n-a\n+b\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := v.Validate(t.TempDir(), structProposal(tc.patch)); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func structProposal(p string) model.RepairProposal { return model.RepairProposal{Patch: p} }
