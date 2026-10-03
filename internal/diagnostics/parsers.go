package diagnostics

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/pushguard/pushguard/internal/model"
)

// parseSyntaxJSON reads the JSON-lines emitted by PushGuard's built-in Python
// and JavaScript syntax checkers.
func parseSyntaxJSON(check model.Check, result model.CommandResult) ([]model.Diagnostic, bool) {
	if check.Category != "syntax" || !strings.Contains(result.Stdout, `"checked"`) {
		return nil, false
	}
	tool := "Python syntax"
	if check.Language == "JavaScript" || strings.Contains(strings.ToLower(check.Name), "javascript") {
		tool = "node --check"
	}
	var out []model.Diagnostic
	recognized := false
	for _, line := range strings.Split(result.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var v struct {
			File    string `json:"file"`
			Line    int    `json:"line"`
			Column  int    `json:"column"`
			Type    string `json:"type"`
			Message string `json:"message"`
			Text    string `json:"text"`
			Checked *int   `json:"checked"`
		}
		if json.Unmarshal([]byte(line), &v) != nil {
			continue
		}
		if v.Checked != nil {
			recognized = true
			continue
		}
		if v.File == "" || len(out) >= maxDiagnostics {
			continue
		}
		recognized = true
		message := v.Message
		if v.Text != "" {
			message += " (source: " + strings.TrimSpace(v.Text) + ")"
		}
		out = append(out, model.Diagnostic{Tool: tool, Category: "syntax", Severity: "error", Location: model.SourceLocation{File: v.File, Line: v.Line, Column: v.Column, Exact: v.Line > 0}, Code: v.Type, Message: message, Command: result.Command, ExitCode: result.ExitCode, RawOutput: truncate(line, 512), ReportedExact: v.Line > 0})
	}
	return out, recognized
}

var (
	pytestHeader    = regexp.MustCompile(`^_{3,} (.+?) _{3,}$`)
	pytestLocation  = regexp.MustCompile(`^(\S+?\.py):(\d+): (?:in (\S+)|(\w+(?:Error|Exception|Warning|Exit)\w*))$`)
	pytestSummary   = regexp.MustCompile(`^(FAILED|ERROR) (\S+?\.py)(?:::(\S+?))?(?: - (.*))?$`)
	pythonFrame     = regexp.MustCompile(`^\s*(?:E\s+)?File "(.+?)", line (\d+)(?:, in (.+))?$`)
	pythonException = regexp.MustCompile(`^(?:E\s+)?([A-Za-z_][\w.]*(?:Error|Exception|Warning|Exit|Interrupt))(?::\s*(.*))?$`)
	mypyLine        = regexp.MustCompile(`^(.+?\.pyi?):(\d+):(?:(\d+):)? (error|warning|note): (.*?)(?:\s+\[([\w-]+)\])?$`)
)

// parsePytest understands --tb=short failure sections, collection errors and
// the -rfE short summary. Each failing test or collection error becomes one
// diagnostic whose stack keeps both test and implementation frames.
func parsePytest(check model.Check, result model.CommandResult, raw string) ([]model.Diagnostic, bool) {
	if !strings.Contains(raw, "short test summary info") && !strings.Contains(raw, "= FAILURES =") && !strings.Contains(raw, "= ERRORS =") && !strings.Contains(strings.Join(check.Args, " "), "pytest") {
		return nil, false
	}
	lines := strings.Split(raw, "\n")
	var out []model.Diagnostic
	var current *model.Diagnostic
	var evidence []string
	flush := func() {
		if current == nil {
			return
		}
		if len(evidence) > 0 {
			current.Message = strings.Join(evidence, " | ")
		}
		if current.Message == "" {
			current.Message = "test failed"
		}
		if len(out) < maxDiagnostics {
			out = append(out, *current)
		}
		current, evidence = nil, nil
	}
	inSections := false
	for _, rawLine := range lines {
		line := strings.TrimRight(rawLine, "\r")
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "=") && strings.HasSuffix(trimmed, "=") {
			flush()
			upper := strings.ToUpper(trimmed)
			inSections = strings.Contains(upper, " FAILURES ") || strings.Contains(upper, " ERRORS ")
			continue
		}
		if !inSections {
			continue
		}
		if m := pytestHeader.FindStringSubmatch(trimmed); m != nil {
			flush()
			name := m[1]
			current = &model.Diagnostic{Tool: "pytest", Category: check.Category, Severity: "error", Code: "TestFailure", Command: result.Command, ExitCode: result.ExitCode, RawOutput: truncate(raw, 512)}
			if strings.HasPrefix(name, "ERROR collecting ") {
				current.Code = "CollectionError"
				file := strings.TrimSpace(strings.TrimPrefix(name, "ERROR collecting "))
				current.Location = model.SourceLocation{File: file}
			} else {
				current.StackTrace = append(current.StackTrace, model.StackFrame{Function: name})
			}
			continue
		}
		if current == nil {
			continue
		}
		if m := pytestLocation.FindStringSubmatch(trimmed); m != nil {
			n, _ := strconv.Atoi(m[2])
			frame := model.StackFrame{File: m[1], Line: n, Function: m[3]}
			if len(current.StackTrace) < 30 {
				current.StackTrace = append(current.StackTrace, frame)
			}
			// The last reported location in --tb=short is where the failure was raised.
			current.Location = model.SourceLocation{File: m[1], Line: n, Exact: true}
			current.ReportedExact = true
			if m[4] != "" {
				current.Code = m[4]
			}
			continue
		}
		if m := pythonFrame.FindStringSubmatch(trimmed); m != nil {
			n, _ := strconv.Atoi(m[2])
			if len(current.StackTrace) < 30 {
				current.StackTrace = append(current.StackTrace, model.StackFrame{File: m[1], Line: n, Function: m[3]})
			}
			if m[3] == "" {
				// A frame without a function is a SyntaxError location in the named file.
				current.Location = model.SourceLocation{File: m[1], Line: n, Exact: true}
				current.ReportedExact = true
			}
			continue
		}
		if strings.HasPrefix(trimmed, "E ") || trimmed == "E" {
			text := strings.TrimSpace(strings.TrimPrefix(trimmed, "E"))
			if m := pythonException.FindStringSubmatch(text); m != nil && !strings.HasPrefix(text, "assert") {
				current.Code = shortException(m[1])
			}
			if text != "" && len(evidence) < 6 && !strings.HasPrefix(text, "^") {
				evidence = append(evidence, text)
			}
		}
	}
	flush()
	// Without failure sections (e.g. -q only), use the short summary.
	if len(out) == 0 {
		for _, line := range lines {
			m := pytestSummary.FindStringSubmatch(strings.TrimSpace(line))
			if m == nil || len(out) >= maxDiagnostics {
				continue
			}
			code := "TestFailure"
			if m[1] == "ERROR" {
				code = "CollectionError"
			}
			message := m[4]
			if message == "" {
				message = strings.ToLower(m[1]) + " " + m[2]
				if m[3] != "" {
					message += "::" + m[3]
				}
			}
			out = append(out, model.Diagnostic{Tool: "pytest", Category: check.Category, Severity: "error", Location: model.SourceLocation{File: m[2]}, Code: code, Message: message, Command: result.Command, ExitCode: result.ExitCode, RawOutput: truncate(raw, 512)})
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

func shortException(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i+1:]
	}
	return name
}

// parseTraceback handles plain Python tracebacks (scripts, unittest, tools).
func parseTraceback(check model.Check, result model.CommandResult, raw string) ([]model.Diagnostic, bool) {
	if !strings.Contains(raw, "Traceback (most recent call last)") && !(strings.Contains(raw, `File "`) && strings.Contains(raw, "Error")) {
		return nil, false
	}
	var out []model.Diagnostic
	var frames []model.StackFrame
	for _, rawLine := range strings.Split(raw, "\n") {
		line := strings.TrimRight(rawLine, "\r")
		if m := pythonFrame.FindStringSubmatch(line); m != nil {
			n, _ := strconv.Atoi(m[2])
			if len(frames) < 30 {
				frames = append(frames, model.StackFrame{File: m[1], Line: n, Function: m[3]})
			}
			continue
		}
		if len(frames) == 0 || strings.HasPrefix(line, " ") {
			continue
		}
		if m := pythonException.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			last := frames[len(frames)-1]
			d := model.Diagnostic{Tool: toolName(check), Category: check.Category, Severity: "error", Location: model.SourceLocation{File: last.File, Line: last.Line, Exact: last.Line > 0}, Code: shortException(m[1]), Message: strings.TrimSpace(m[1] + ": " + m[2]), Command: result.Command, ExitCode: result.ExitCode, StackTrace: frames, RawOutput: truncate(raw, 512), ReportedExact: last.Line > 0}
			if len(out) < maxDiagnostics {
				out = append(out, d)
			}
			frames = nil
		}
	}
	return out, len(out) > 0
}

func parseMypy(check model.Check, result model.CommandResult, raw string) ([]model.Diagnostic, bool) {
	if !strings.Contains(strings.Join(check.Args, " "), "mypy") {
		return nil, false
	}
	var out []model.Diagnostic
	for _, line := range strings.Split(raw, "\n") {
		m := mypyLine.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil || m[4] == "note" || len(out) >= maxDiagnostics {
			continue
		}
		n, _ := strconv.Atoi(m[2])
		col, _ := strconv.Atoi(m[3])
		out = append(out, model.Diagnostic{Tool: "mypy", Category: check.Category, Severity: m[4], Location: model.SourceLocation{File: m[1], Line: n, Column: col, Exact: n > 0}, Code: m[6], Message: m[5], Command: result.Command, ExitCode: result.ExitCode, RawOutput: truncate(line, 512), ReportedExact: n > 0})
	}
	return out, len(out) > 0
}

func parsePyright(check model.Check, result model.CommandResult) ([]model.Diagnostic, bool) {
	raw := strings.TrimSpace(result.Stdout)
	if !strings.HasPrefix(raw, "{") || !strings.Contains(raw, "generalDiagnostics") {
		return nil, false
	}
	var report struct {
		GeneralDiagnostics []struct {
			File     string `json:"file"`
			Severity string `json:"severity"`
			Message  string `json:"message"`
			Rule     string `json:"rule"`
			Range    struct {
				Start struct{ Line, Character int } `json:"start"`
			} `json:"range"`
		} `json:"generalDiagnostics"`
	}
	if json.Unmarshal([]byte(raw), &report) != nil {
		return nil, false
	}
	var out []model.Diagnostic
	for _, d := range report.GeneralDiagnostics {
		if len(out) >= maxDiagnostics || d.Severity == "information" {
			continue
		}
		out = append(out, model.Diagnostic{Tool: "pyright", Category: check.Category, Severity: d.Severity, Location: model.SourceLocation{File: d.File, Line: d.Range.Start.Line + 1, Column: d.Range.Start.Character + 1, Exact: true}, Code: d.Rule, Message: d.Message, Command: result.Command, ExitCode: result.ExitCode, ReportedExact: true})
	}
	return out, true
}
