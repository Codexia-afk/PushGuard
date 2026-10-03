package detector

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pushguard/pushguard/internal/config"
	"github.com/pushguard/pushguard/internal/runner"
)

func TestPythonSyntaxDoesNotImportRepositoryModules(t *testing.T) {
	python := SystemTools{}.Python("")
	if len(python) == 0 {
		t.Skip("Python 3 not installed")
	}
	root := tree(t, map[string]string{"foo.py": "raise RuntimeError('never execute application')\n", "json.py": "open('executed', 'w').write('unsafe')\n", "sitecustomize.py": "open('executed', 'w').write('unsafe')\n"})
	plan := build(t, root, fakeTools{python: python}, "foo.py")
	c, _ := find(plan, "python syntax")
	r := (runner.Runner{}).RunInput(context.Background(), root, c.Args, 10*time.Second, c.Input)
	if _, err := os.Stat(filepath.Join(root, "executed")); !os.IsNotExist(err) {
		t.Fatal("syntax discovery executed repository code")
	}
	if r.ExitCode != 0 {
		t.Fatalf("syntax check: %s%s", r.Stdout, r.Stderr)
	}
}

func TestNestedInvalidManifestAndIncompleteDiscoveryCannotPass(t *testing.T) {
	root := tree(t, map[string]string{"service/package.json": "{broken"})
	if _, err := Build(context.Background(), root, config.Default(), Options{Tools: allTools(), Git: true}); err == nil {
		t.Fatal("malformed nested manifest fell through to Git sanity")
	}
	d := Discovery{Truncated: true}
	if _, err := Build(context.Background(), t.TempDir(), config.Default(), Options{Discovery: &d, Tools: allTools(), Git: true}); err == nil {
		t.Fatal("partial discovery produced a certifiable automatic plan")
	}
}

func TestWorkspaceMemberChecksAreNotAssumedFromRootScript(t *testing.T) {
	root := tree(t, map[string]string{
		"package.json":              `{"workspaces":["packages/*"],"scripts":{"test":"node --test root.test.js"}}`,
		"packages/api/package.json": `{"scripts":{"lint":"eslint ."}}`,
	})
	if _, ok := find(build(t, root, allTools()), "packages/api: lint"); !ok {
		t.Fatal("root test script suppressed member lint")
	}
}
