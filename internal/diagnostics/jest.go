package diagnostics

import (
	"regexp"
	"strings"

	"github.com/pushguard/pushguard/internal/model"
)

var jestSuite = regexp.MustCompile(`^(FAIL|PASS)\s+(.+?\.[cm]?[jt]sx?)(?:\s|$)`)
var jestCodeFrame = regexp.MustCompile(`^(?:>?\s*\d+\s*\||\|)`)

// Jest's default reporter goes to stderr even when invoked through npm. Each
// bullet starts a distinct failure; passing test names and totals are not errors.
func parseJestText(raw string, check model.Check, result model.CommandResult) []model.Diagnostic {
	var out []model.Diagnostic
	var current *model.Diagnostic
	var evidence strings.Builder
	var message strings.Builder
	suite := ""
	flush := func() {
		if current != nil && len(out) < maxDiagnostics {
			current.Message = strings.TrimSpace(message.String())
			current.RawOutput = truncate(strings.TrimSpace(evidence.String()), 512)
			out = append(out, *current)
		}
		current = nil
		evidence.Reset()
		message.Reset()
	}
	for _, line := range strings.Split(raw, "\n") {
		text := strings.TrimSpace(line)
		if m := jestSuite.FindStringSubmatch(text); m != nil {
			flush()
			suite = ""
			if m[1] == "FAIL" {
				suite = cleanPath(m[2])
			}
			continue
		}
		if strings.HasPrefix(text, "Test Suites:") || strings.HasPrefix(text, "Tests:") || strings.HasPrefix(text, "Snapshots:") || strings.HasPrefix(text, "Ran all test suites") {
			flush()
			suite = ""
			continue
		}
		if suite != "" && strings.HasPrefix(text, "● ") {
			flush()
			current = &model.Diagnostic{Tool: "Jest", Category: check.Category, Severity: "error", TestName: strings.TrimPrefix(text, "● "), Location: model.SourceLocation{File: suite}, Command: result.Command, ExitCode: result.ExitCode}
		}
		if current == nil {
			continue
		}
		if frame, ok := parseStack(text); ok {
			if len(current.StackTrace) < 30 {
				current.StackTrace = append(current.StackTrace, frame)
			}
			if !current.ReportedExact && frame.Line > 0 {
				current.Location = model.SourceLocation{File: frame.File, Line: frame.Line, Column: frame.Column, Exact: true}
				current.ReportedExact = true
			}
		}
		if evidence.Len() < 4000 {
			evidence.WriteString(line + "\n")
		}
		// Keep locations and numbered code frames out of the diagnostic identity:
		// moving an unchanged assertion must not look like repair progress.
		if message.Len() < 2000 && text != "" && !strings.HasPrefix(text, "at ") && !jestCodeFrame.MatchString(text) {
			message.WriteString(text + "\n")
		}
	}
	flush()
	return out
}
