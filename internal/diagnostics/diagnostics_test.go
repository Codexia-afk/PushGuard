package diagnostics

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pushguard/pushguard/internal/model"
)

func TestParseTypeScriptLocation(t *testing.T) {
	check := model.Check{Name: "typecheck", Category: "typecheck", Args: []string{"tsc"}}
	fixture, err := os.ReadFile(filepath.Join("..", "..", "testdata", "diagnostics", "typescript.txt"))
	if err != nil {
		t.Fatal(err)
	}
	result := model.CommandResult{Command: "tsc --noEmit", ExitCode: 2, Stderr: string(fixture)}
	ds := Parse(check, result)
	if len(ds) != 1 {
		t.Fatalf("got %d diagnostics", len(ds))
	}
	d := ds[0]
	if d.Location.File != "src/auth/user.ts" || d.Location.Line != 47 || d.Location.Column != 18 || d.Code != "TS2339" {
		t.Fatalf("unexpected diagnostic: %+v", d)
	}
	if !d.ReportedExact {
		t.Fatal("expected exact compiler location")
	}
}

func TestParseESLintJSONFixture(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("..", "..", "testdata", "diagnostics", "eslint.json"))
	if err != nil {
		t.Fatal(err)
	}
	ds := Parse(model.Check{Name: "eslint", Category: "lint"}, model.CommandResult{Command: "eslint -f json", ExitCode: 1, Stdout: string(fixture)})
	if len(ds) != 1 || ds[0].Tool != "ESLint" || ds[0].Location.Line != 47 {
		t.Fatalf("unexpected ESLint diagnostics: %+v", ds)
	}
}

func TestRenderDoesNotInventLocation(t *testing.T) {
	d := model.Diagnostic{Tool: "pytest", Message: "assertion failed"}
	got := Render(t.TempDir(), d, 2)
	if !strings.Contains(got, "Exact reported location: unavailable") {
		t.Fatalf("rendered output did not declare unknown location: %s", got)
	}
}

func TestRenderSourceCaret(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.ts")
	if err := os.WriteFile(path, []byte("const value = user.email\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got := Render(root, model.Diagnostic{Tool: "TypeScript", Location: model.SourceLocation{File: "main.ts", Line: 1, Column: 15, Exact: true}, Code: "TS2339", Message: "missing property"}, 1)
	if !strings.Contains(got, "main.ts:1:15") || !strings.Contains(got, "^") {
		t.Fatalf("missing location/caret: %s", got)
	}
}

func TestGenericColumnsAndGoJSON(t *testing.T) {
	check := model.Check{Name: "go test", Category: "test"}
	result := model.CommandResult{ExitCode: 1, Stdout: "src/main.go:19:7: undefined: missing\n"}
	ds := Parse(check, result)
	if len(ds) != 1 || ds[0].Location.Line != 19 || ds[0].Location.Column != 7 || ds[0].Location.File != "src/main.go" {
		t.Fatalf("column parser wrong: %+v", ds)
	}
	result.Stdout = `{"Action":"output","Package":"example","Output":"    add_test.go:8: expected 5\n"}` + "\n" + `{"Action":"fail","Package":"example","Test":"TestAdd"}`
	ds = Parse(check, result)
	if len(ds) < 2 || ds[0].Location.Line != 8 {
		t.Fatalf("Go JSON evidence lost: %+v", ds)
	}
}

func TestLocationLabelAndQuotedPathAreNormalized(t *testing.T) {
	result := model.CommandResult{ExitCode: 1, Stdout: "location: '/workspace/tests/cart.test.mjs:6:1'\n"}
	ds := Parse(model.Check{Name: "test", Category: "test"}, result)
	if len(ds) != 1 || ds[0].Location.File != "/workspace/tests/cart.test.mjs" || ds[0].Location.Line != 6 || ds[0].Location.Column != 1 {
		t.Fatalf("location label was treated as part of the path: %+v", ds)
	}
}
func TestSuccessfulLogsDoNotInventFailures(t *testing.T) {
	ds := Parse(model.Check{Name: "tool"}, model.CommandResult{ExitCode: 0, Stdout: "0 errors, no failed tests\n"})
	if len(ds) != 0 {
		t.Fatal("successful tool logs were interpreted as failure")
	}
}

func TestGoJSONSkipDoesNotHideActualFailure(t *testing.T) {
	raw := `{"Action":"output","Package":"example/patch","Test":"TestSymlink","Output":"    hardening_test.go:28: symlink permissions vary on Windows\n"}` + "\n" +
		`{"Action":"skip","Package":"example/patch","Test":"TestSymlink"}` + "\n" +
		`{"Action":"output","Package":"example/other","Test":"TestValue","Output":"    value_test.go:9: expected 5, got 2\n"}` + "\n" +
		`{"Action":"fail","Package":"example/other","Test":"TestValue"}`
	ds := Parse(model.Check{Name: "go test", Category: "test"}, model.CommandResult{ExitCode: 1, Stdout: raw})
	if len(ds) == 0 || ds[0].Location.File != "value_test.go" {
		t.Fatalf("actual failure hidden: %+v", ds)
	}
	for _, d := range ds {
		if strings.Contains(d.Message, "symlink permissions") {
			t.Fatal("skip became an error")
		}
	}
}

func TestReportedStackFramesAndJestJSON(t *testing.T) {
	check := model.Check{Name: "Jest", Category: "test"}
	raw := `{"testResults":[{"name":"src/add.test.js","assertionResults":[{"status":"failed","fullName":"adds","failureMessages":["TypeError: operation failed\n    at add (src/add.js:12:8)\n    at test (src/add.test.js:19:3)"]}]}]}`
	ds := Parse(check, model.CommandResult{ExitCode: 1, Stdout: raw})
	if len(ds) == 0 || ds[0].Location.File != "src/add.js" || ds[0].Location.Line != 12 || len(ds[0].StackTrace) != 2 {
		t.Fatalf("stack evidence lost: %+v", ds)
	}
}
