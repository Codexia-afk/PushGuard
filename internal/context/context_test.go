package context

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pushguard/pushguard/internal/model"
	"github.com/pushguard/pushguard/internal/runner"
	"github.com/pushguard/pushguard/internal/testutil"
)

func TestBuilderRedactsDiagnosticAndSkipsSensitiveFiles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("const token = \"secret-value\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	d := model.Diagnostic{Message: "token: secret-value", RawOutput: "Authorization: Bearer abc123", Location: model.SourceLocation{File: "main.go", Line: 1, Column: 1}}
	bundle := (Builder{Runner: runner.Runner{}, Redact: true}).Build(context.Background(), root, &model.Repository{}, d)
	joined := bundle.Diagnostic.Message + bundle.Diagnostic.RawOutput + bundle.Source
	if strings.Contains(joined, "secret-value") || strings.Contains(joined, "abc123") {
		t.Fatalf("secret was not redacted: %q", joined)
	}
}

func TestFailureContextSeparatesEditableImplementationFromTestEvidence(t *testing.T) {
	root, _ := testutil.Repository(t)
	testutil.Write(t, root, "add.go", "package example\nfunc Add(a, b int) int { return a - b }\n")
	testutil.Write(t, root, "add_test.go", "package example\nfunc TestAdd() { Add(2, 3) }\n")
	testutil.Commit(t, root)
	d := model.Diagnostic{Message: "Add: expected 5, got -1", Location: model.SourceLocation{File: "add_test.go", Line: 2}}
	b := (Builder{Redact: true}).BuildFailure(context.Background(), root, &model.Repository{}, d, []model.Diagnostic{d})
	if len(b.EditableFiles) != 1 || b.EditableFiles[0] != "add.go" {
		t.Fatalf("wrong edit scope: %+v", b.EditableFiles)
	}
	for _, file := range b.SourceFiles {
		if file.File == "add_test.go" && file.Editable {
			t.Fatal("test offered as editable")
		}
		if strings.Contains(file.Content, " | ") {
			t.Fatal("display line labels leaked into source fragments")
		}
	}
}
