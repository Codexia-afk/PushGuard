package diagnostics

import (
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pushguard/pushguard/internal/model"
)

// Refine applies the per-diagnostic classification priority on top of the
// check-level Classify result:
//
//  1. structured diagnostic evidence (tool-reported code/rule at a source location)
//  2. tool identity (the tool itself could not run)
//  3. check category
//  4. process failure information (timeout, truncation)
//  5. infrastructure patterns in the tool's own output (Classify)
//
// Source text such as process.env.X, or a test asserting FileNotFoundError,
// therefore never turns a located source diagnostic into ENVIRONMENT.
func Refine(check model.Check, result model.CommandResult, ds []model.Diagnostic) []model.Diagnostic {
	tool := toolFailure(check, result)
	for i := range ds {
		d := &ds[i]
		switch {
		case structuredSource(*d):
			d.Classification = codeOrTest(check, *d)
		case tool != "":
			d.Classification = tool
		case check.Category == "security" || check.Category == "audit":
			d.Classification = model.ClassSecurity
		case result.TimedOut || result.Truncated:
			d.Classification = model.ClassResource
		case d.Classification == "" || d.Classification == model.ClassUnknown:
			if d.Location.File != "" && d.Location.Line > 0 && !outsideRepository(d.Location.File) {
				d.Classification = codeOrTest(check, *d)
			} else if d.Classification == "" {
				d.Classification = model.ClassUnknown
			}
		}
	}
	return ds
}

// FailureClass summarizes a failed check for routing: located source evidence
// wins over raw-output heuristics.
func FailureClass(r model.CheckResult) string {
	for _, d := range r.Diagnostics {
		if (d.Classification == model.ClassCode || d.Classification == model.ClassTest) && structuredSource(d) {
			return d.Classification
		}
	}
	if len(r.Diagnostics) > 0 && r.Diagnostics[0].Classification == model.ClassEnvironment {
		return model.ClassEnvironment
	}
	return Classify(r.Check, r.Command)
}

// Repairable reports whether source repair is an appropriate response.
func Repairable(d model.Diagnostic) bool {
	switch d.Classification {
	case model.ClassCode, model.ClassTest:
		return true
	case model.ClassUnknown, "":
		return d.Location.File != "" && d.Location.Line > 0 && !outsideRepository(d.Location.File)
	}
	return false
}

var sourceCode = regexp.MustCompile(`^(?:TS\d{4,5}|[A-Z]{1,4}\d{3,4}|SyntaxError|IndentationError|TabError|NameError|TypeError|AttributeError|ImportError|ModuleNotFoundError|ValueError|KeyError|IndexError|ZeroDivisionError|AssertionError|TestFailure|CollectionError|[a-z]+(?:-[a-z]+)+|@?[\w-]+/[\w-]+(?:/[\w-]+)?|[a-z]+)$`)

var missingTool = []string{"executable file not found", "is not recognized as an internal or external command", "command not found", "no module named pytest", "no module named mypy", "no module named ruff", "is not installed"}

func structuredSource(d model.Diagnostic) bool {
	code := d.Code
	if code == "" {
		code = d.Rule
	}
	if d.Location.File == "" || outsideRepository(d.Location.File) || code == "" || !sourceCode.MatchString(code) {
		return false
	}
	return d.Location.Line > 0 || code == "CollectionError" || code == "TestFailure"
}

func codeOrTest(check model.Check, d model.Diagnostic) string {
	if check.Category == "test" || d.Code == "TestFailure" || d.Code == "AssertionError" {
		if d.Code == "SyntaxError" || d.Code == "IndentationError" || d.Code == "CollectionError" && !IsTestFile(d.Location.File) {
			return model.ClassCode
		}
		return model.ClassTest
	}
	return model.ClassCode
}

func toolFailure(check model.Check, result model.CommandResult) string {
	if check.Unavailable != "" {
		return model.ClassEnvironment
	}
	if result.ExitCode == 127 && result.Terminated != "" && result.Terminated != "timeout" && result.Terminated != "canceled" {
		return model.ClassEnvironment
	}
	// Only the first lines of output describe the tool launch itself.
	head := strings.ToLower(firstLines(result.Stderr, 6) + "\n" + firstLines(result.Stdout, 3))
	for _, p := range missingTool {
		if strings.Contains(head, p) && (result.ExitCode == 1 || result.ExitCode == 2 || result.ExitCode == 127 || result.ExitCode == 9009) {
			return model.ClassEnvironment
		}
	}
	return ""
}

func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

func outsideRepository(file string) bool {
	f := filepath.ToSlash(file)
	return filepath.IsAbs(file) || strings.HasPrefix(f, "/") || strings.HasPrefix(f, "../") || strings.Contains(f, "/site-packages/") || strings.Contains(f, "/node_modules/") || strings.HasPrefix(f, "node_modules/") || strings.HasPrefix(f, "<")
}

// IsTestFile recognizes common test naming conventions across ecosystems.
func IsTestFile(file string) bool {
	f := strings.ToLower(filepath.ToSlash(file))
	base := path.Base(f)
	if strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py") || strings.HasSuffix(base, "_test.go") || strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") || base == "conftest.py" {
		return true
	}
	for _, part := range strings.Split(path.Dir(f), "/") {
		if part == "test" || part == "tests" || part == "__tests__" || part == "spec" {
			return true
		}
	}
	return false
}
