package diagnostics

import (
	"strings"
	"testing"

	"github.com/pushguard/pushguard/internal/model"
)

func TestJestTextKeepsFailuresSeparateFromPassingTestsAndSummaries(t *testing.T) {
	raw := `FAIL tests/analyticsService.test.ts
  analyticsService
    ✓ returns 0 when there are no tickets
    ✕ calculates resolution rate percentage correctly

  ● analyticsService › calculates resolution rate percentage correctly

    expect(received).toBe(expected) // Object.is equality
    Expected: 40
    Received: 250
      at Object.<anonymous> (tests/analyticsService.test.ts:137:18)

FAIL tests/assignmentService.test.ts
    ✓ throws an error when assigning a non-existent ticket
    ✓ throws an error when assigning to a non-existent agent
    ✕ rejects ticket assignment to an inactive agent

  ● assignmentService › rejects ticket assignment to an inactive agent

    Expected substring: "Cannot assign ticket to an inactive agent"
    Received function did not throw
      at Object.<anonymous> (tests/assignmentService.test.ts:54:8)

FAIL tests/sla.test.ts
  ● sla utility › calculates 1-hour SLA deadline for critical priority

    Expected: 1790852400000
    Received: 1790863200000
      at calculateSLADeadline (src/utils/sla.ts:12:5)
      at Object.<anonymous> (tests/sla.test.ts:10:32)

  ● sla utility › reports a second failure in the same suite

    Expected: false
    Received: true
      at Object.<anonymous> (tests/sla.test.ts:30:8)

PASS tests/ticketService.test.ts
    ✓ throws an error when updating a non-existent ticket
Test Suites: 3 failed, 1 passed, 4 total
Tests:       4 failed, 17 passed, 21 total
Ran all test suites.
`
	ds := Parse(model.Check{Name: "test", Category: "test"}, model.CommandResult{Command: "npm run test", ExitCode: 1, Stderr: raw})
	if len(ds) != 4 {
		t.Fatalf("expected four actual failures, got %+v", ds)
	}
	for i, file := range []string{"tests/analyticsService.test.ts", "tests/assignmentService.test.ts", "src/utils/sla.ts", "tests/sla.test.ts"} {
		d := ds[i]
		if d.Tool != "Jest" || d.Location.File != file || !d.ReportedExact || d.TestName == "" || !strings.Contains(d.Message, "Expected") {
			t.Fatalf("failure lost its identity/evidence: %+v", d)
		}
		if strings.Contains(d.Message+d.RawOutput, "✓") || strings.Contains(d.Message+d.RawOutput, "Test Suites:") {
			t.Fatalf("non-failure evidence leaked into block: %+v", d)
		}
	}
	if len(ds[0].StackTrace) != 1 || len(ds[2].StackTrace) != 2 || strings.Contains(ds[1].RawOutput, "Received: 250") {
		t.Fatalf("unrelated failures merged: %+v", ds)
	}
}

func TestJestTextWithoutStackRetainsSuiteAndDoesNotInventLine(t *testing.T) {
	ds := Parse(model.Check{Name: "test", Category: "test"}, model.CommandResult{ExitCode: 1, Stderr: "FAIL tests/cart.test.ts\n  ● cart › calculates total\n\n    Expected: 30\n    Received: 10\nTest Suites: 1 failed, 1 total\n"})
	if len(ds) != 1 || ds[0].Location.File != "tests/cart.test.ts" || ds[0].Location.Line != 0 || ds[0].ReportedExact {
		t.Fatalf("incorrect fallback location: %+v", ds)
	}
}

func TestJestTextDiagnosticIdentitySurvivesLineMovement(t *testing.T) {
	raw := "FAIL tests/cart.test.ts\n  ● cart › calculates total\n    Expected: 30\n    Received: 10\n    > 40 | expect(total).toBe(30);\n         |               ^\n      at Object.<anonymous> (tests/cart.test.ts:40:8)\n"
	check := model.Check{Name: "test", Category: "test"}
	before := Parse(check, model.CommandResult{ExitCode: 1, Stderr: raw})
	after := Parse(check, model.CommandResult{ExitCode: 1, Stderr: strings.ReplaceAll(raw, "40", "50")})
	if len(before) != 1 || len(after) != 1 || before[0].ID != after[0].ID || after[0].Location.Line != 50 {
		t.Fatalf("line movement invented progress: %+v -> %+v", before, after)
	}
}
