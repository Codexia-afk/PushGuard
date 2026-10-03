package patch

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/pushguard/pushguard/internal/model"
	"github.com/pushguard/pushguard/internal/testutil"
)

func TestVerificationSuppressionProposalsRejected(t *testing.T) {
	for _, line := range []string{"// eslint-disable", "// @ts-nocheck", "test.skip('auth')", "# noqa", "# type: ignore"} {
		p := model.RepairProposal{Patch: fmt.Sprintf("--- a/main.ts\n+++ b/main.ts\n@@ -1 +1 @@\n-original\n+%s\n", line)}
		if _, err := (Validator{}).Validate(t.TempDir(), p); err == nil {
			t.Fatalf("suppression accepted: %s", line)
		}
	}
}

func TestPackageVerificationLaunchersCannotBeRepairTargets(t *testing.T) {
	root := t.TempDir()
	testutil.Write(t, root, "package.json", `{"scripts":{"test":"node scripts/verify.cjs","lint":"eslint src/value.ts","start":"node src/server.js"}}`)
	for _, file := range []string{"scripts/verify.cjs", "node_modules/tool/index.js"} {
		if EditableFile(root, file) {
			t.Fatalf("verification/dependency file offered for editing: %s", file)
		}
		p := model.RepairProposal{Patch: fmt.Sprintf("--- a/%s\n+++ b/%s\n@@ -1 +1 @@\n-original\n+replacement\n", file, file)}
		if _, err := (Validator{}).Validate(root, p); err == nil {
			t.Fatalf("patch policy accepted %s", file)
		}
	}
	for _, file := range []string{"src/value.ts", "src/server.js"} {
		if !EditableFile(root, file) {
			t.Fatalf("application source was incorrectly protected: %s", file)
		}
	}
}

func TestTextDiffCannotModifyBinarySource(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "asset.dat"), []byte("old\x00value\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p := model.RepairProposal{Patch: "--- a/asset.dat\n+++ b/asset.dat\n@@ -1 +1 @@\n-old\n+new\n"}
	if _, err := (Validator{}).Validate(root, p); err == nil {
		t.Fatal("binary target accepted")
	}
}
