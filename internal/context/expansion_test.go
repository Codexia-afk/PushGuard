package context

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pushguard/pushguard/internal/model"
)

func TestBoundedExpansionRejectsTraversalSecretsAndCommands(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{"core.py": "def subtotal():\n    return 1\n", ".env": "TOKEN=private\n", "secret.py": "TOKEN=private\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	b := Builder{Files: []string{"core.py", ".env", "secret.py"}}
	for _, file := range []string{"../core.py", "/etc/passwd", ".env", "secret.py", "node_modules/x.py", "*.py", "core.py\nother.py"} {
		if _, err := b.Expand(context.Background(), root, model.ContextBundle{}, model.ContextRequest{Files: []string{file}, Reason: "needed"}); err == nil {
			t.Errorf("unsafe path accepted: %q", file)
		}
	}
	if _, err := b.Expand(context.Background(), root, model.ContextBundle{}, model.ContextRequest{Symbols: []string{"$(touch owned)"}, Reason: "needed"}); err == nil {
		t.Fatal("command accepted as symbol")
	}
	out, err := b.Expand(context.Background(), root, model.ContextBundle{}, model.ContextRequest{Symbols: []string{"subtotal"}, Reason: "locate implementation"})
	if err != nil || len(out.EditableFiles) != 1 || out.EditableFiles[0] != "core.py" || !strings.Contains(out.SourceFiles[0].Content, "def subtotal") {
		t.Fatalf("expansion: %+v %v", out, err)
	}
}

func TestStandalonePythonTestResolvesImplementationAndFocus(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "tests"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tests/test_total.py"), []byte("from core import subtotal\n\ndef test_total():\n    assert subtotal() == 2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	source := strings.Repeat("# unrelated prefix\n", 100) + "def subtotal():\n    return 1\n"
	if err := os.WriteFile(filepath.Join(root, "core.py"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	d := model.Diagnostic{Category: "test", Location: model.SourceLocation{File: "tests/test_total.py", Line: 4}, Message: "AssertionError: 1 != 2"}
	bundle := (Builder{Files: []string{"core.py", "tests/test_total.py"}, Redact: true}).BuildFailure(context.Background(), root, &model.Repository{Root: root}, d, []model.Diagnostic{d})
	if len(bundle.EditableFiles) != 1 || bundle.EditableFiles[0] != "core.py" {
		t.Fatalf("implementation missing: %+v", bundle)
	}
	for _, file := range bundle.SourceFiles {
		if file.File == "core.py" && (!strings.Contains(file.Content, "def subtotal") || len(file.FocusLines) == 0) {
			t.Fatal("late implementation focus lost")
		}
	}
}
