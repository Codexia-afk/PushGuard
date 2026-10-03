package context

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/pushguard/pushguard/internal/diagnostics"
	"github.com/pushguard/pushguard/internal/model"
	"github.com/pushguard/pushguard/internal/testutil"
)

func TestFiftyDiagnosticsAreBoundedDeterministicGroups(t *testing.T) {
	root := t.TempDir()
	var ds []model.Diagnostic
	for i := 0; i < 50; i++ {
		ds = append(ds, model.Diagnostic{Rule: "no-var", Category: "lint", Message: fmt.Sprintf("Unexpected var %d", i), Location: model.SourceLocation{File: "src/a.js", Line: i*4 + 1}})
	}
	ds = diagnostics.Normalize(model.Check{Name: "lint"}, ds)
	groups := PlanGroups(root, ds, 10)
	if len(groups) != 5 || !reflect.DeepEqual(groups, PlanGroups(root, ds, 10)) {
		t.Fatalf("unexpected groups: %+v", groups)
	}
	seen := map[string]bool{}
	for _, g := range groups {
		if len(g.Diagnostics) > 10 {
			t.Fatal("unbounded group")
		}
		for _, d := range g.Diagnostics {
			if seen[d.ID] {
				t.Fatal("duplicate diagnostic")
			}
			seen[d.ID] = true
		}
	}
	if len(seen) != 50 {
		t.Fatal("diagnostics lost")
	}
}

func TestGroupsJoinDirectImportsButSeparateChecks(t *testing.T) {
	root := t.TempDir()
	testutil.Write(t, root, "a.ts", "import { value } from './b.js';\nexport const a: number = value;\n")
	testutil.Write(t, root, "b.ts", "export const value = 'wrong';\n")
	ds := []model.Diagnostic{
		{Check: "types", Code: "TS2322", Location: model.SourceLocation{File: "a.ts", Line: 2}},
		{Check: "types", Code: "TS2345", Location: model.SourceLocation{File: "b.ts", Line: 1}},
		{Check: "build", Code: "TS2322", Location: model.SourceLocation{File: "a.ts", Line: 2}},
	}
	groups := PlanGroups(root, ds, 10)
	if len(groups) != 2 || len(groups[1].Diagnostics) != 2 || len(groups[1].Files) != 2 {
		t.Fatalf("dependency/check boundary lost: %+v", groups)
	}
}

func TestLocatedFailuresPrecedeLocationlessSummaries(t *testing.T) {
	ds := []model.Diagnostic{
		{Check: "test", Category: "test", Message: "3 tests failed"},
		{Check: "test", Category: "test", Message: "Expected: 40", Location: model.SourceLocation{File: "tests/rates.test.ts", Line: 137}},
	}
	groups := PlanGroups(t.TempDir(), ds, 10)
	if len(groups) != 2 || groups[0].Diagnostics[0].Location.File != "tests/rates.test.ts" {
		t.Fatalf("locationless summary selected over actionable failure: %+v", groups)
	}
}
