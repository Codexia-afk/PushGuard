package diagnostics

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pushguard/pushguard/internal/model"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "diagnostics", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestPythonSyntaxJSON(t *testing.T) {
	out := `{"file": "foo.py", "line": 1, "column": 7, "type": "SyntaxError", "message": "invalid syntax", "text": "def f(:"}` + "\n" + `{"checked": 3, "errors": 1}` + "\n"
	ds := Parse(model.Check{Name: "python syntax", Category: "syntax", Language: "Python"}, model.CommandResult{ExitCode: 1, Stdout: out})
	if len(ds) != 1 || ds[0].Location.File != "foo.py" || ds[0].Location.Line != 1 || ds[0].Code != "SyntaxError" || !ds[0].ReportedExact {
		t.Fatalf("syntax diagnostic: %+v", ds)
	}
	ok := Parse(model.Check{Name: "python syntax", Category: "syntax"}, model.CommandResult{ExitCode: 0, Stdout: `{"checked": 3, "errors": 0}`})
	if len(ok) != 0 {
		t.Fatalf("clean syntax run produced diagnostics: %+v", ok)
	}
}

func TestPytestFailuresFromRealOutput(t *testing.T) {
	check := model.Check{Name: "pytest", Category: "test", Args: []string{"python", "-m", "pytest"}}
	ds := Parse(check, model.CommandResult{ExitCode: 1, Stdout: fixture(t, "pytest_failures.txt")})
	if len(ds) != 2 {
		t.Fatalf("want 2 failing tests, got %+v", ds)
	}
	if !strings.Contains(filepath.ToSlash(ds[0].Location.File), "tests") || ds[0].Location.Line != 5 || !strings.Contains(ds[0].Message, "add(2, 3)") {
		t.Fatalf("pytest location/evidence lost: %+v", ds[0])
	}
	if ds[0].ID == "" || ds[0].ID == ds[1].ID {
		t.Fatal("distinct tests share a key")
	}
}

func TestPytestCollectionErrorPointsAtSource(t *testing.T) {
	check := model.Check{Name: "pytest", Category: "test", Args: []string{"python", "-m", "pytest"}}
	ds := Parse(check, model.CommandResult{ExitCode: 2, Stdout: fixture(t, "pytest_collection_error.txt")})
	if len(ds) != 1 {
		t.Fatalf("want 1 collection error, got %+v", ds)
	}
	d := ds[0]
	if !strings.HasSuffix(filepath.ToSlash(d.Location.File), "bad.py") || d.Location.Line != 1 || d.Code != "SyntaxError" {
		t.Fatalf("collection error should point at the broken module: %+v", d)
	}
}

func TestPythonTraceback(t *testing.T) {
	ds := Parse(model.Check{Name: "script", Category: "test"}, model.CommandResult{ExitCode: 1, Stderr: fixture(t, "python_traceback.txt")})
	if len(ds) != 1 || ds[0].Code != "NameError" || ds[0].Location.Line != 4 || len(ds[0].StackTrace) != 2 {
		t.Fatalf("traceback: %+v", ds)
	}
}

func TestMypyAndPyright(t *testing.T) {
	mypy := "app/models.py:12:5: error: Item \"None\" of \"Optional[User]\" has no attribute \"name\"  [union-attr]\napp/models.py:12:5: note: see docs\nFound 1 error"
	ds := Parse(model.Check{Name: "mypy", Category: "typecheck", Args: []string{"python", "-m", "mypy"}}, model.CommandResult{ExitCode: 1, Stdout: mypy})
	if len(ds) != 1 || ds[0].Code != "union-attr" || ds[0].Location.Column != 5 {
		t.Fatalf("mypy: %+v", ds)
	}
	pyright := `{"generalDiagnostics":[{"file":"/r/a.py","severity":"error","message":"\"x\" is not defined","rule":"reportUndefinedVariable","range":{"start":{"line":2,"character":4}}}]}`
	ds = Parse(model.Check{Name: "pyright", Category: "typecheck", Args: []string{"pyright", "--outputjson"}}, model.CommandResult{ExitCode: 1, Stdout: pyright})
	if len(ds) != 1 || ds[0].Location.Line != 3 || ds[0].Location.Column != 5 || ds[0].Code != "reportUndefinedVariable" {
		t.Fatalf("pyright: %+v", ds)
	}
}

func TestESLintStylish(t *testing.T) {
	out := "\n/repo/src/app.ts\n  12:5  error    Unexpected console statement  no-console\n  14:1  warning  Missing return type           @typescript-eslint/explicit-function-return-type\n\n✖ 2 problems (1 error, 1 warning)\n"
	ds := Parse(model.Check{Name: "lint", Category: "lint", Args: []string{"npm", "run", "lint"}}, model.CommandResult{ExitCode: 1, Stdout: out})
	if len(ds) != 2 || ds[0].Code != "no-console" || ds[0].Location.Line != 12 || ds[1].Severity != "warning" {
		t.Fatalf("stylish: %+v", ds)
	}
}

// classify mirrors production: check-level Classify, then per-diagnostic Refine.
func classify(check model.Check, result model.CommandResult, ds []model.Diagnostic) []model.Diagnostic {
	for i := range ds {
		ds[i].Classification = Classify(check, result)
	}
	return Refine(check, result, ds)
}

func TestClassificationPriority(t *testing.T) {
	lint := model.Check{Name: "lint", Category: "lint"}
	// ESLint no-console with process.env in the message is CODE, not ENVIRONMENT.
	ds := classify(lint, model.CommandResult{ExitCode: 1, Stdout: "src/a.ts uses process.env.API_URL and connection refused text in a string"}, []model.Diagnostic{{Tool: "ESLint", Code: "no-console", Message: "Unexpected console statement in process.env.X handler", Location: model.SourceLocation{File: "src/a.ts", Line: 3}}})
	if ds[0].Classification != model.ClassCode {
		t.Fatalf("no-console: %s", ds[0].Classification)
	}
	ts := classify(model.Check{Name: "typecheck", Category: "typecheck"}, model.CommandResult{ExitCode: 2}, []model.Diagnostic{{Code: "TS2339", Message: "Property 'email' does not exist on type 'User'.", Location: model.SourceLocation{File: "src/user.ts", Line: 47}}})
	if ts[0].Classification != model.ClassCode {
		t.Fatalf("TS2339: %s", ts[0].Classification)
	}
	test := classify(model.Check{Name: "pytest", Category: "test"}, model.CommandResult{ExitCode: 1}, []model.Diagnostic{{Code: "TestFailure", Message: "assert FileNotFoundError raised", Location: model.SourceLocation{File: "tests/test_io.py", Line: 9}}})
	if test[0].Classification != model.ClassTest {
		t.Fatalf("pytest assertion: %s", test[0].Classification)
	}
	missing := classify(model.Check{Name: "lint", Category: "lint"}, model.CommandResult{ExitCode: 127, Terminated: `exec: "eslint": executable file not found in %PATH%`}, []model.Diagnostic{{Message: "lint exited with code 127"}})
	if missing[0].Classification != model.ClassEnvironment {
		t.Fatalf("missing runtime: %s", missing[0].Classification)
	}
	notRecognized := classify(model.Check{Name: "lint", Category: "lint"}, model.CommandResult{ExitCode: 1, Stderr: "'eslint' is not recognized as an internal or external command,\noperable program or batch file."}, []model.Diagnostic{{Message: "lint exited with code 1"}})
	if notRecognized[0].Classification != model.ClassEnvironment {
		t.Fatalf("not recognized: %s", notRecognized[0].Classification)
	}
	network := classify(model.Check{Name: "test", Category: "test"}, model.CommandResult{ExitCode: 1, Stderr: "fatal: unable to access 'https://x/': Could not resolve host: x"}, []model.Diagnostic{{Message: "test exited with code 1"}})
	if network[0].Classification != model.ClassNetwork {
		t.Fatalf("network: %s", network[0].Classification)
	}
	timeout := classify(model.Check{Name: "test", Category: "test"}, model.CommandResult{ExitCode: 124, TimedOut: true, Terminated: "timeout"}, []model.Diagnostic{{Message: "timed out"}})
	if timeout[0].Classification != model.ClassResource || Repairable(timeout[0]) {
		t.Fatalf("timeout: %s", timeout[0].Classification)
	}
	if !Repairable(model.Diagnostic{Classification: model.ClassCode}) || Repairable(model.Diagnostic{Classification: model.ClassEnvironment}) || Repairable(model.Diagnostic{Classification: model.ClassNetwork}) {
		t.Fatal("repairability is wrong")
	}
}
