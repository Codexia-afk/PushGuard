package diagnostics

import (
	"testing"

	"github.com/pushguard/pushguard/internal/model"
)

func TestProgressTracksMultiplicityWithoutTreatingMovedLinesAsFixes(t *testing.T) {
	d := model.Diagnostic{Tool: "eslint", Category: "lint", Rule: "no-var", Message: "Unexpected var.", Location: model.SourceLocation{File: "src/a.js", Line: 3}}
	check := model.Check{Name: "lint"}
	before := Normalize(check, []model.Diagnostic{d, d, d})
	d.Location.Line = 100
	after := Normalize(check, []model.Diagnostic{d, d})
	if before[0].ID != after[0].ID || before[0].ID == before[1].ID {
		t.Fatal("IDs must survive line shifts and distinguish equivalent occurrences")
	}
	added := d
	added.Rule, added.Message = "no-undef", "Unknown variable."
	after = append(after, Normalize(check, []model.Diagnostic{added})...)
	p := Reconcile(model.CheckResult{Diagnostics: before}, model.CheckResult{Diagnostics: after})
	if p.Before != 3 || p.After != 3 || p.Resolved != 1 || p.Remaining != 2 || p.New != 1 || !p.Complete {
		t.Fatalf("equal total counts concealed actual changes: %+v", p)
	}
	p = Reconcile(model.CheckResult{Diagnostics: before}, model.CheckResult{Diagnostics: after, DiagnosticsLimited: true})
	if p.Complete || p.Resolved != 0 {
		t.Fatalf("incomplete evidence cannot certify resolution: %+v", p)
	}
}
