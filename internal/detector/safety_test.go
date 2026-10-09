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

func TestFixturesDoNotBecomeProductionProjectBoundaries(t *testing.T) {
	root := tree(t, map[string]string{
		"go.mod":                         "module example.test/pushguard\n\ngo 1.22\n",
		"main.go":                        "package main\n",
		"testdata/broken/package.json":   `{"scripts":{"test":"node --test"}}`,
		"fixtures/sample/package.json":   `{"scripts":{"build":"tsc"}}`,
		"__fixtures__/case/package.json": `{"scripts":{"lint":"eslint ."}}`,
	})
	discovery := Discover(context.Background(), root, false)
	if len(discovery.Boundaries) != 1 || discovery.Boundaries[0].Dir != "." {
		t.Fatalf("fixture projects entered production verification: %+v", discovery.Boundaries)
	}
	for _, file := range discovery.Files {
		if Ignored(file) {
			t.Fatalf("ignored fixture was discovered: %s", file)
		}
	}
}

func TestGoBuildVerifiesLibrariesAndExecutablesWithoutArtifacts(t *testing.T) {
	for _, tc := range []struct {
		name        string
		files       map[string]string
		wantFailure bool
	}{
		{"library", map[string]string{"lib.go": "package sample\nfunc Value() int { return 1 }\n"}, false},
		{"executable", map[string]string{"main.go": "package main\nfunc main() {}\n"}, false},
		{"multiple executables", map[string]string{"cmd/one/main.go": "package main\nfunc main() {}\n", "cmd/two/main.go": "package main\nfunc main() {}\n"}, false},
		{"unreferenced broken library", map[string]string{"main.go": "package main\nfunc main() {}\n", "lib/lib.go": "package lib\nvar Value = undefined\n"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.files["go.mod"] = "module example.test/sample\n\ngo 1.22\n"
			root := tree(t, tc.files)
			c, ok := find(build(t, root, allTools()), "go build")
			if !ok {
				t.Fatal("missing Go build check")
			}
			result := (runner.Runner{}).RunInput(context.Background(), root, c.Args, time.Minute, c.Input)
			if (result.ExitCode != 0) != tc.wantFailure {
				t.Fatalf("unexpected build result: %d\n%s%s", result.ExitCode, result.Stdout, result.Stderr)
			}
			err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !entry.IsDir() {
					rel, err := filepath.Rel(root, path)
					if err != nil {
						return err
					}
					if _, exists := tc.files[filepath.ToSlash(rel)]; !exists {
						t.Errorf("build wrote artifact into repository: %s", rel)
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
