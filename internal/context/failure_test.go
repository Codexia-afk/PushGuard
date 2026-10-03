package context

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/pushguard/pushguard/internal/model"
	"github.com/pushguard/pushguard/internal/testutil"
)

func TestGroupedDiagnosticsIncludeDistantLocationsAndReadOnlyRules(t *testing.T) {
	root, _ := testutil.Repository(t)
	var source strings.Builder
	for i := 1; i <= 500; i++ {
		fmt.Fprintf(&source, "const value%d = 'long enough to require source windows';\n", i)
	}
	testutil.Write(t, root, "src/lint.ts", source.String())
	testutil.Write(t, root, "src/cart.ts", "var cart = 0;\n")
	testutil.Write(t, root, "eslint.config.mjs", "export default [{ rules: { 'no-var': 'error' } }];\n")
	testutil.Write(t, root, ".prettierrc", `{"semi":true}`)
	testutil.Write(t, root, "tsconfig.json", `{"compilerOptions":{"strict":true}}`)
	testutil.Write(t, root, "package.json", `{"scripts":{"lint":"eslint src"},"unrelated":"omit me"}`)
	testutil.Write(t, root, ".env", "API_KEY=do-not-send")
	testutil.Commit(t, root)
	ds := []model.Diagnostic{
		{Message: "no-var", Location: model.SourceLocation{File: "src/lint.ts", Line: 4}},
		{Message: "no-var", Location: model.SourceLocation{File: "src/lint.ts", Line: 450}},
		{Message: "no-var", Location: model.SourceLocation{File: "src/cart.ts", Line: 1}},
	}
	bundle := (Builder{Redact: true}).BuildFailure(context.Background(), root, &model.Repository{}, ds[0], ds)
	seen := map[string]bool{}
	var contents strings.Builder
	for _, f := range bundle.SourceFiles {
		seen[f.File] = true
		contents.WriteString(f.Content)
		if !strings.HasPrefix(f.File, "src/") && f.Editable {
			t.Fatal("configuration offered as editable")
		}
	}
	if !strings.Contains(contents.String(), "value450") || !strings.Contains(contents.String(), "value4 ") || len(bundle.Diagnostics) != 3 {
		t.Fatal("group lost evidence")
	}
	for _, name := range []string{"src/cart.ts", "eslint.config.mjs", ".prettierrc", "tsconfig.json", "package.json"} {
		if !seen[name] {
			t.Fatalf("missing %s", name)
		}
	}
	if strings.Contains(contents.String(), "do-not-send") || strings.Contains(contents.String(), "omit me") || len(contents.String()) > sourceBudget+16<<10 {
		t.Fatal("context not minimized")
	}
}

func TestESMTestContextResolvesTypeScriptImplementationImports(t *testing.T) {
	root, _ := testutil.Repository(t)
	testutil.Write(t, root, "src/cart.ts", "export function add(a, b) { return a - b; }\n")
	testutil.Write(t, root, "src/unrelated.mjs", "export function addUnrelated() { return 0; }\n")
	testutil.Write(t, root, "tests/cart.test.mjs", "import { add } from '../src/cart.ts';\nassert.equal(add(10, 20), 30);\n")
	testutil.Commit(t, root)
	d := model.Diagnostic{Message: "Expected values to be strictly equal", Location: model.SourceLocation{File: "tests/cart.test.mjs", Line: 2}}
	b := (Builder{Redact: true}).BuildFailure(context.Background(), root, &model.Repository{}, d, []model.Diagnostic{d})
	if len(b.EditableFiles) != 1 || b.EditableFiles[0] != "src/cart.ts" {
		t.Fatalf("implementation omitted: %+v", b.EditableFiles)
	}
}

func TestLintContextExcludesUnrelatedImportedImplementations(t *testing.T) {
	root, _ := testutil.Repository(t)
	testutil.Write(t, root, "src/main.ts", "import { Service } from './service.js';\nvar service = new Service();\n")
	testutil.Write(t, root, "src/service.ts", "export class Service {}\n")
	testutil.Commit(t, root)
	d := model.Diagnostic{Category: "lint", Rule: "no-var", Location: model.SourceLocation{File: "src/main.ts", Line: 2}}
	bundle := (Builder{Redact: true}).BuildFailure(context.Background(), root, &model.Repository{}, d, []model.Diagnostic{d})
	if len(bundle.EditableFiles) != 1 || bundle.EditableFiles[0] != "src/main.ts" {
		t.Fatalf("unrelated source sent: %v", bundle.EditableFiles)
	}
}

func TestRunnerDiagnosticsResolveSecondaryTestImports(t *testing.T) {
	root, _ := testutil.Repository(t)
	testutil.Write(t, root, "package.json", `{"scripts":{"test":"node scripts/verify.cjs"}}`)
	testutil.Write(t, root, "scripts/verify.cjs", "// Test launcher\n")
	testutil.Write(t, root, "tests/cart.test.ts", "import { add } from '../src/cart.js';\nassert.equal(add(10,20),30);\n")
	testutil.Write(t, root, "src/cart.ts", "export function add(a,b) { return a-b; }\n")
	testutil.Commit(t, root)
	ds := []model.Diagnostic{
		{Category: "test", Location: model.SourceLocation{File: "scripts/verify.cjs", Line: 1}},
		{Category: "test", Location: model.SourceLocation{File: "tests/cart.test.ts", Line: 2}},
	}
	bundle := (Builder{Redact: true}).BuildFailure(context.Background(), root, &model.Repository{}, ds[0], ds)
	if len(bundle.EditableFiles) != 1 || bundle.EditableFiles[0] != "src/cart.ts" {
		t.Fatalf("incorrect targets: %v", bundle.EditableFiles)
	}
}

func TestLongTestResolvesMultilineAliasedImportAndDistantImplementation(t *testing.T) {
	root, _ := testutil.Repository(t)
	testutil.Write(t, root, "src/rates.ts", strings.Repeat("// unrelated declaration padding\n", 100)+"export function calculateRate(n: number) { return 100 / n; }\n")
	testutil.Write(t, root, "tests/rates.test.ts", "import {\n  calculateRate as rate,\n} from '../src/rates.js';\n"+strings.Repeat("// fixture data\n", 130)+"expect(rate(4)).toBe(40);\n")
	testutil.Commit(t, root)
	d := model.Diagnostic{Category: "test", Message: "Expected: 40, Received: 25", Location: model.SourceLocation{File: "tests/rates.test.ts", Line: 134}}
	b := (Builder{Redact: true}).BuildFailure(context.Background(), root, &model.Repository{}, d, []model.Diagnostic{d})
	if len(b.EditableFiles) != 1 || b.EditableFiles[0] != "src/rates.ts" {
		t.Fatalf("implementation omitted: %+v", b.EditableFiles)
	}
	found := false
	for _, file := range b.SourceFiles {
		if file.File == "src/rates.ts" && file.Editable && strings.Contains(file.Content, "return 100 / n") {
			found = true
		}
	}
	if !found {
		t.Fatal("related implementation window lost the relevant declaration")
	}
}
