package detector

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pushguard/pushguard/internal/config"
	"github.com/pushguard/pushguard/internal/model"
)

// fakeTools makes plans deterministic regardless of the host machine.
type fakeTools struct {
	present map[string]bool
	modules map[string]bool
	python  []string
}

func (f fakeTools) LookPath(name string) (string, error) {
	if f.present[name] {
		return "/usr/bin/" + name, nil
	}
	return "", fmt.Errorf("not found")
}
func (f fakeTools) Python(string) []string { return f.python }
func (f fakeTools) PythonModule(_ []string, module string) bool {
	return f.modules[module]
}

func allTools() fakeTools {
	return fakeTools{present: map[string]bool{"go": true, "gofmt": true, "npm": true, "node": true, "cargo": true, "ruff": true, "pytest": true, "gcc": true}, modules: map[string]bool{"pytest": true}, python: []string{"python3"}}
}

func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, text := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func build(t *testing.T, root string, tools Tools, targets ...string) Plan {
	t.Helper()
	plan, err := Build(context.Background(), root, config.Default(), Options{Tools: tools, Targets: targets})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func find(plan Plan, name string) (model.Check, bool) {
	for _, c := range plan.Checks {
		if c.Name == name {
			return c, true
		}
	}
	return model.Check{}, false
}

// Scenario A precondition: a repository with only foo.py is Python.
func TestSingleUnmanifestedPythonFileIsPython(t *testing.T) {
	for _, name := range []string{"foo.py", "100li.py", "nested/dir/whatever_name.py"} {
		root := tree(t, map[string]string{name: "print('x')\n"})
		plan := build(t, root, allTools())
		if plan.Discovery.Languages["Python"] != 1 {
			t.Fatalf("%s: Python not detected: %+v", name, plan.Discovery.Languages)
		}
		c, ok := find(plan, "python syntax")
		if !ok || !c.Required || !strings.Contains(c.Input, name) {
			t.Fatalf("%s: required Python syntax check missing: %+v", name, plan.Checks)
		}
		if c.Args[0] != "python3" || c.Args[1] != "-I" || c.Args[2] != "-S" || c.Args[3] != "-c" {
			t.Fatalf("syntax check must compile, not execute files: %v", c.Args)
		}
	}
}

func TestExtensionFallbackForEveryLanguage(t *testing.T) {
	for ext, lang := range map[string]string{".py": "Python", ".ts": "TypeScript", ".tsx": "TypeScript", ".js": "JavaScript", ".jsx": "JavaScript", ".go": "Go", ".rs": "Rust", ".java": "Java", ".c": "C", ".h": "C", ".cpp": "C++", ".hpp": "C++", ".cs": "C#"} {
		if got := LanguageOf("src/file" + ext); got != lang {
			t.Fatalf("%s: got %q want %q", ext, got, lang)
		}
		root := tree(t, map[string]string{"src/file" + ext: "x\n"})
		plan := build(t, root, allTools())
		found := false
		for _, b := range plan.Discovery.Boundaries {
			if b.Language == lang && b.Kind == "source" {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: no source boundary: %+v", ext, plan.Discovery.Boundaries)
		}
	}
}

func TestIgnoredDirectoriesAreNotSources(t *testing.T) {
	root := tree(t, map[string]string{"node_modules/a/index.js": "x", ".venv/lib/x.py": "x", "dist/out.js": "x", "build/gen.py": "x", "src/real.py": "x", "env/pyvenv.cfg": "", "env/lib/y.py": "x"})
	plan := build(t, root, allTools())
	if plan.Discovery.Languages["Python"] != 1 || plan.Discovery.Languages["JavaScript"] != 0 {
		t.Fatalf("ignored directories were scanned: %+v %v", plan.Discovery.Languages, plan.Discovery.Files)
	}
}

// Scenario M: multi-language boundaries with correct working directories.
func TestMultiLanguageBoundariesAndWorkingDirectories(t *testing.T) {
	root := tree(t, map[string]string{
		"frontend/package.json":  `{"name":"web","scripts":{"lint":"eslint .","test":"vitest run","build":"vite build"}}`,
		"frontend/tsconfig.json": "{}",
		"frontend/src/app.ts":    "export const x = 1\n",
		"backend/go.mod":         "module example.test/api\n\ngo 1.22\n",
		"backend/main.go":        "package main\n",
		"scripts/deploy.py":      "print('deploy')\n",
	})
	plan := build(t, root, allTools())
	want := map[string]string{"frontend: lint": "frontend", "frontend: test": "frontend", "frontend: build": "frontend", "backend: go vet": "backend", "backend: go test": "backend", "python syntax": ""}
	for name, wd := range want {
		c, ok := find(plan, name)
		if !ok {
			t.Fatalf("missing %q in %v", name, Names(plan.Checks))
		}
		if c.WorkingDir != wd {
			t.Fatalf("%s runs in %q, want %q", name, c.WorkingDir, wd)
		}
	}
	if c, _ := find(plan, "python syntax"); !strings.Contains(c.Input, "scripts/deploy.py") {
		t.Fatalf("loose script not syntax-checked: %q", c.Input)
	}
	if plan.Checks[0].Category != "syntax" {
		t.Fatalf("cheap syntax checks should run first: %v", Names(plan.Checks))
	}
}

func TestNativeWorkspaceMembersCollapseButNodeScriptsRemain(t *testing.T) {
	root := tree(t, map[string]string{
		"package.json":            `{"name":"mono","workspaces":["packages/*"],"scripts":{"test":"turbo test"}}`,
		"packages/a/package.json": `{"name":"a","scripts":{"test":"vitest"}}`,
		"packages/a/index.ts":     "export {}\n",
		"Cargo.toml":              "[workspace]\nmembers=[\"crates/x\"]\n",
		"crates/x/Cargo.toml":     "[package]\nname=\"x\"\n",
		"crates/x/src/lib.rs":     "pub fn x() {}\n",
	})
	plan := build(t, root, allTools())
	for _, c := range plan.Checks {
		if strings.HasPrefix(c.Name, "crates/x:") {
			t.Fatalf("workspace member duplicated: %v", Names(plan.Checks))
		}
	}
	if _, ok := find(plan, "test"); !ok {
		t.Fatalf("root workspace script missing: %v", Names(plan.Checks))
	}
	if _, ok := find(plan, "packages/a: test"); !ok {
		t.Fatal("Node member test was suppressed")
	}
}

func TestNodePlaceholderTestAndManifestPython(t *testing.T) {
	root := tree(t, map[string]string{
		"package.json":       `{"scripts":{"test":"echo \"Error: no test specified\" && exit 1","lint":"eslint ."}}`,
		"index.js":           "module.exports = 1\n",
		"py/pyproject.toml":  "[tool.ruff]\nline-length = 100\n[tool.pytest.ini_options]\n",
		"py/pkg/core.py":     "x = 1\n",
		"py/tests/test_c.py": "def test_x():\n    assert True\n",
	})
	plan := build(t, root, allTools())
	if _, ok := find(plan, "test"); ok {
		t.Fatal("npm placeholder test script was planned")
	}
	ruff, ok := find(plan, "py: ruff")
	if !ok || !ruff.Required || ruff.WorkingDir != "py" {
		t.Fatalf("configured ruff not required in its boundary: %+v", ruff)
	}
	pytest, ok := find(plan, "py: pytest")
	if !ok || pytest.Args[0] != "python3" || !strings.Contains(strings.Join(pytest.Args, " "), "no:cacheprovider") {
		t.Fatalf("pytest plan: %+v", pytest)
	}
}

func TestMissingToolchainIsUnavailableNotPass(t *testing.T) {
	root := tree(t, map[string]string{"go.mod": "module x\n", "main.go": "package main\n", "app.py": "x=1\n", "test_app.py": "def test(): pass\n"})
	plan := build(t, root, fakeTools{present: map[string]bool{}})
	vet, _ := find(plan, "go vet")
	if vet.Unavailable == "" || !vet.Required {
		t.Fatalf("missing Go toolchain not reported: %+v", vet)
	}
	syntax, _ := find(plan, "python syntax")
	if syntax.Unavailable == "" {
		t.Fatalf("missing Python interpreter not reported: %+v", syntax)
	}
	pytest, _ := find(plan, "pytest")
	if pytest.Unavailable == "" {
		t.Fatalf("missing pytest not reported: %+v", pytest)
	}
}

func TestStandaloneFileTargetsPlanFileChecksOnly(t *testing.T) {
	root := tree(t, map[string]string{"foo.py": "x = 1\n", "other.py": "y = 2\n", "tests/test_x.py": "def test(): pass\n", "lib.js": "1\n"})
	plan := build(t, root, allTools(), "foo.py")
	syntax, ok := find(plan, "python syntax")
	if !ok || strings.TrimSpace(syntax.Input) != "foo.py" {
		t.Fatalf("target-only syntax check: %+v", syntax)
	}
	if _, ok := find(plan, "pytest"); ok {
		t.Fatal("project-wide tests planned for a single-file check")
	}
	if _, ok := find(plan, "repository sanity"); ok {
		t.Fatal("Git sanity planned without Git")
	}
}

func TestConfiguredChecksAreAuthoritative(t *testing.T) {
	root := tree(t, map[string]string{"foo.py": "x\n"})
	cfg := config.Default()
	cfg.Checks = []model.Check{{Name: "custom", Args: []string{"make", "verify"}, Required: true}}
	plan, err := Build(context.Background(), root, cfg, Options{Tools: allTools()})
	if err != nil || len(plan.Checks) != 1 || plan.Checks[0].Name != "custom" {
		t.Fatalf("configured plan replaced: %+v %v", plan.Checks, err)
	}
}

func TestDisplayHidesEmbeddedScripts(t *testing.T) {
	c := model.Check{Args: []string{"py", "-3", "-c", PythonSyntaxScript}, WorkingDir: "svc"}
	text := Display(c)
	if strings.Contains(text, "compile(") || !strings.Contains(text, "<built-in checker>") || !strings.Contains(text, "(in svc)") {
		t.Fatalf("display: %s", text)
	}
}
