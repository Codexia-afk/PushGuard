package diagnostics

import (
	"strings"
	"testing"

	"github.com/pushguard/pushguard/internal/model"
)

func TestReportedLocationAdapters(t *testing.T) {
	for _, tc := range []struct {
		name, text, file, tool, rule string
		line, column                 int
	}{
		{"CI multiline", "::error file=src/lintErrors.ts,line=4,col=1::no-var:\nUnexpected var, use let or const instead.", "src/lintErrors.ts", "ESLint", "no-var", 4, 1},
		{"colon", "file.ts:12:19: invalid value", "file.ts", "ESLint", "", 12, 19},
		{"bare colon", "file.ts:12:19", "file.ts", "ESLint", "", 12, 19},
		{"parentheses", "file.ts(12,19): error TS2339: invalid property", "file.ts", "TypeScript", "", 12, 19},
		{"generic parentheses", "file.ts(12,19)", "file.ts", "ESLint", "", 12, 19},
		{"properties", "file=foo.ts,line=12,col=19", "foo.ts", "ESLint", "", 12, 19},
		{"stack", "at foo.ts:12:19", "foo.ts", "ESLint", "", 12, 19},
		{"Node test", "test at tests/cart.test.mjs:6:1", "tests/cart.test.mjs", "ESLint", "", 6, 1},
		{"Node file URL", "at TestContext.<anonymous> (file:///tmp/project/tests/cart.test.mjs:7:10)", "/tmp/project/tests/cart.test.mjs", "ESLint", "", 7, 10},
		{"stylish", "src/foo.ts\n  4:1  error  Unexpected var  no-var", "src/foo.ts", "ESLint", "no-var", 4, 1},
		{"CI escapes", "::error col=19,file=src/a%2Cb.ts,line=12::no-var: Unexpected%0Avar", "src/a,b.ts", "ESLint", "no-var", 12, 19},
		{"Windows CI", `::error file=C:\repo\foo.ts,line=12,col=19::no-var: Unexpected var`, `C:\repo\foo.ts`, "ESLint", "no-var", 12, 19},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ds := Parse(model.Check{Name: "lint", Category: "lint"}, model.CommandResult{ExitCode: 1, Stdout: tc.text})
			if len(ds) != 1 {
				t.Fatalf("diagnostics: %+v", ds)
			}
			d := ds[0]
			if d.Location.File != tc.file || d.Location.Line != tc.line || d.Location.Column != tc.column || d.Tool != tc.tool || d.Rule != tc.rule || !d.ReportedExact {
				t.Fatalf("wrong evidence: %+v", d)
			}
			if strings.Contains(Render(t.TempDir(), d, 2), "Exact reported location: unavailable") {
				t.Fatal("present evidence hidden")
			}
			if tc.name == "CI multiline" && d.Message != "Unexpected var, use let or const instead." {
				t.Fatal(d.Message)
			}
		})
	}
}

func TestInfrastructureAndSecurityClassification(t *testing.T) {
	for text, want := range map[string]string{
		"Missing required environment variable: DATABASE_ENCRYPTION_KEY": "ENVIRONMENT",
		"npm error Missing script: lint":                                 "CONFIGURATION",
		"ECONNREFUSED":                                                   "NETWORK", "no space left on device": "RESOURCE",
		"expired token was accepted": "SECURITY", "assertion failed": "TEST",
	} {
		if got := Classify(model.Check{Category: "test"}, model.CommandResult{Stderr: text, ExitCode: 1}); got != want {
			t.Fatalf("%q: %s want %s", text, got, want)
		}
	}
}

const lintWithSourceLiteral = `[{"filePath":"src/index.ts","messages":[{"line":1,"column":1,"severity":2,"ruleId":"quotes","message":"Use single quotes in \"Missing required environment variable: KEY\""}],"source":"throw new Error('Missing required environment variable: KEY')"}]`

func TestStructuredLintDoesNotMisclassifySourceAsRuntimeFailure(t *testing.T) {
	for _, phrase := range []string{
		"Missing required environment variable: KEY", "no space left on device",
		"out of memory", "rate limit exceeded", "ECONNREFUSED",
		"authentication failed", "ESLint configuration error: example",
	} {
		t.Run(phrase, func(t *testing.T) {
			raw := strings.ReplaceAll(lintWithSourceLiteral, "Missing required environment variable: KEY", phrase)
			result := model.CommandResult{ExitCode: 1, Stdout: "> example lint\n> eslint src --format json\n\n" + raw}
			ds := Parse(model.Check{Name: "lint", Category: "lint"}, result)
			if len(ds) != 1 || ds[0].Classification != "CODE" || ds[0].Rule != "quotes" || !ds[0].ReportedExact {
				t.Fatalf("incorrect lint evidence: %+v", ds)
			}
			if got := Infrastructure(result); got != "" {
				t.Fatalf("lint misclassified: %s", got)
			}
		})
	}
}

func TestStructuredLintRetainsRuntimeFailureEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, stderr, terminated, classification string
		timedOut, truncated                      bool
	}{
		{name: "stderr environment", stderr: "Missing required environment variable: KEY", classification: "ENVIRONMENT"},
		{name: "stderr resource", stderr: "no space left on device", classification: "RESOURCE"},
		{name: "termination resource", terminated: "disk quota exceeded", classification: "RESOURCE"},
		{name: "stderr network", stderr: "ECONNREFUSED", classification: "NETWORK"},
		{name: "stderr configuration", stderr: "ESLint configuration error: invalid options", classification: "CONFIGURATION"},
		{name: "timeout", timedOut: true, classification: "RESOURCE"},
		{name: "truncated", truncated: true, classification: "RESOURCE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := model.CommandResult{ExitCode: 1, Stdout: lintWithSourceLiteral, Stderr: tc.stderr, Terminated: tc.terminated, TimedOut: tc.timedOut, Truncated: tc.truncated}
			if got := Classify(model.Check{Category: "lint"}, result); got != tc.classification {
				t.Fatalf("classification = %s, want %s", got, tc.classification)
			}
			if Infrastructure(result) == "" {
				t.Fatal("runtime failure lost alongside lint evidence")
			}
		})
	}
}

func TestLintJSONStreamsPreserveOnlyRuntimeEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, stdout, stderr, want string
	}{
		{"stderr JSON", "", lintWithSourceLiteral, "CODE"},
		{"prefixed JSON", "[lint] running\n" + lintWithSourceLiteral, "", "CODE"},
		{"stdout runtime suffix", lintWithSourceLiteral + "\nMissing required environment variable: KEY", "", "ENVIRONMENT"},
		{"stdout runtime prefix", "Missing required environment variable: KEY\n" + lintWithSourceLiteral, "", "ENVIRONMENT"},
		{"stderr runtime suffix", "", lintWithSourceLiteral + "\nECONNREFUSED", "NETWORK"},
		{"unrelated JSON", `[{"message":"Missing required environment variable: KEY"}]`, "", "ENVIRONMENT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := model.CommandResult{ExitCode: 1, Stdout: tc.stdout, Stderr: tc.stderr}
			if got := Classify(model.Check{Category: "lint"}, result); got != tc.want {
				t.Fatalf("classification = %s, want %s", got, tc.want)
			}
			if got := Infrastructure(result); (got == "") != (tc.want == "CODE") {
				t.Fatalf("infrastructure = %q for %s", got, tc.want)
			}
		})
	}
}
