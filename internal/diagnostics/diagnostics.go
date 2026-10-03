package diagnostics

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/pushguard/pushguard/internal/model"
	"github.com/pushguard/pushguard/internal/security"
)

var (
	tsParen = regexp.MustCompile(`^(.+?)\((\d+),(\d+)\):\s*(?:error|warning)\s+(TS\d+):\s*(.*)$`)
	tsColon = regexp.MustCompile(`^(.+?):(\d+):(\d+)\s+-\s+(?:error|warning)\s+(TS\d+):\s*(.*)$`)
	locLine = regexp.MustCompile(`^(.+?):(\d+)(?::(\d+))?:\s*(.*)$`)
)

const MaxDiagnostics = 1000
const maxDiagnostics = MaxDiagnostics

// Full captured output lives in CommandResult. Normalized evidence is bounded
// independently so a noisy compiler cannot multiply log memory or receipt size.
func Parse(check model.Check, result model.CommandResult) []model.Diagnostic {
	out := parseInput(check, result)
	if len(out) > maxDiagnostics {
		out = out[:maxDiagnostics]
	}
	classification := Classify(check, result)
	for i := range out {
		out[i].Classification = classification
		out[i].Message = truncate(out[i].Message, 2000)
		out[i].RawOutput = truncate(out[i].RawOutput, 512)
		if len(out[i].Location.File) > 4096 {
			out[i].Location = model.SourceLocation{}
			out[i].ReportedExact = false
		}
	}
	return Normalize(check, out)
}
func parseInput(check model.Check, result model.CommandResult) []model.Diagnostic {
	raw := result.Stdout
	if result.Stderr != "" {
		if raw != "" {
			raw += "\n"
		}
		raw += result.Stderr
	}
	raw = security.Terminal(raw)
	tool := toolName(check)
	if ds, ok := parseSyntaxJSON(check, result); ok {
		return ds
	}
	if ds, ok := parsePyright(check, result); ok {
		return ds
	}
	if result.ExitCode != 0 {
		if ds, ok := parseMypy(check, result, raw); ok {
			return ds
		}
		if ds, ok := parsePytest(check, result, raw); ok {
			return ds
		}
	}
	if ds, ok := parseStructured(check, result); ok {
		return ds
	}
	if check.Category == "lint" && (tool == "eslint" || strings.Contains(raw, "[{") || strings.HasPrefix(strings.TrimSpace(raw), "[")) {
		if ds := parseESLint(raw, check, result); len(ds) > 0 {
			return ds
		}
	}
	if ds := parseAnnotations(raw, check, result); len(ds) > 0 {
		return ds
	}
	if result.ExitCode == 0 {
		return nil
	}
	if check.Category == "test" {
		if ds := parseJestText(raw, check, result); len(ds) > 0 {
			return ds
		}
	}
	if ds, ok := parseTraceback(check, result, raw); ok {
		return ds
	}
	lines := strings.Split(raw, "\n")
	out := make([]model.Diagnostic, 0)
	currentFile := ""
	for _, line := range lines {
		if len(out) >= maxDiagnostics {
			break
		}
		line = strings.TrimSpace(line)
		line = strings.TrimSpace(strings.TrimPrefix(line, "-->"))
		line = strings.TrimPrefix(line, "test at ")
		// ESLint's "✖ N problems" summary repeats counts, not new evidence.
		if line == "" || strings.HasPrefix(line, "✖") {
			continue
		}
		if strings.HasPrefix(line, "node:") || strings.HasPrefix(line, "at ") && strings.Contains(line, "(node:") {
			continue
		}
		if looksLikeFile(line) && !strings.ContainsAny(line, " \t:") {
			currentFile = cleanPath(line)
		}
		if strings.HasPrefix(line, "=== RUN") || strings.HasPrefix(line, "=== PAUSE") || strings.HasPrefix(line, "=== CONT") || strings.HasPrefix(line, "--- PASS:") || strings.HasPrefix(line, "--- SKIP:") {
			continue
		}
		if frame, ok := parseStack(line); ok {
			if len(out) == 0 {
				out = append(out, model.Diagnostic{Tool: tool, Category: check.Category, Severity: "error", Message: "failure stack reported by the tool", Command: result.Command, ExitCode: result.ExitCode, RawOutput: truncate(raw, 512)})
			}
			last := &out[len(out)-1]
			if len(last.StackTrace) < 30 {
				last.StackTrace = append(last.StackTrace, frame)
			}
			if last.Location.File == "" {
				last.Location = model.SourceLocation{File: frame.File, Line: frame.Line, Column: frame.Column, Exact: frame.Line > 0}
				last.ReportedExact = last.Location.Exact
			}
			continue
		}
		var d *model.Diagnostic
		if m := stylish.FindStringSubmatch(line); len(m) > 0 && currentFile != "" {
			d = &model.Diagnostic{Tool: "ESLint", Category: check.Category, Severity: m[3], Location: location(currentFile, m[1], m[2]), Message: m[4], Rule: m[5], Code: m[5]}
		}
		if m := tsParen.FindStringSubmatch(line); len(m) > 0 {
			lineNo, _ := strconv.Atoi(m[2])
			col, _ := strconv.Atoi(m[3])
			d = &model.Diagnostic{Tool: "TypeScript", Category: check.Category, Severity: severity(line), Location: model.SourceLocation{File: cleanPath(m[1]), Line: lineNo, Column: col, Exact: true}, Code: m[4], Message: m[5]}
		}
		if d == nil {
			if m := tsColon.FindStringSubmatch(line); len(m) > 0 {
				lineNo, _ := strconv.Atoi(m[2])
				col, _ := strconv.Atoi(m[3])
				d = &model.Diagnostic{Tool: "TypeScript", Category: check.Category, Severity: severity(line), Location: model.SourceLocation{File: cleanPath(m[1]), Line: lineNo, Column: col, Exact: true}, Code: m[4], Message: m[5]}
			}
		}
		if d == nil {
			if m := plainParen.FindStringSubmatch(line); len(m) > 0 && looksLikeFile(m[1]) {
				d = &model.Diagnostic{Tool: tool, Category: check.Category, Severity: severity(line), Location: location(m[1], m[2], m[3]), Message: m[4]}
			}
		}
		if d == nil {
			if m := stackLocation.FindStringSubmatch(line); len(m) > 0 && looksLikeFile(m[1]) {
				d = &model.Diagnostic{Tool: tool, Category: check.Category, Severity: "error", Location: location(m[1], m[2], m[3]), Message: "location reported by the failed tool"}
			}
		}
		if d == nil {
			if m := locLine.FindStringSubmatch(line); len(m) > 0 && looksLikeFile(m[1]) {
				lineNo, _ := strconv.Atoi(m[2])
				col := 0
				if m[3] != "" {
					col, _ = strconv.Atoi(m[3])
				}
				d = &model.Diagnostic{Tool: tool, Category: check.Category, Severity: "error", Location: model.SourceLocation{File: cleanPath(m[1]), Line: lineNo, Column: col, Exact: lineNo > 0}, Message: m[4]}
			}
		}
		if d == nil && (strings.Contains(strings.ToLower(line), "error") || strings.Contains(strings.ToLower(line), "failed") || npmCode.MatchString(line)) {
			d = &model.Diagnostic{Tool: tool, Category: check.Category, Severity: "error", Message: line}
		}
		if d != nil {
			if d.Tool == "Ruff" {
				if m := ruffCode.FindStringSubmatch(d.Message); len(m) > 0 {
					d.Code, d.Rule, d.Message = m[1], m[1], m[2]
				}
			}
			if m := npmCode.FindStringSubmatch(line); len(m) > 0 {
				d.Tool, d.Code = "npm", m[1]
			}
			d.Command = result.Command
			d.ExitCode = result.ExitCode
			d.RawOutput = truncate(raw, 512)
			d.ReportedExact = d.Location.Exact
			out = appendUnique(out, *d)
		}
	}
	if len(out) == 0 && result.ExitCode != 0 {
		out = append(out, model.Diagnostic{Tool: tool, Category: check.Category, Severity: "error", Message: fmt.Sprintf("%s exited with code %d", check.Name, result.ExitCode), Command: result.Command, ExitCode: result.ExitCode, RawOutput: truncate(raw, 512)})
	}
	return out
}

func parseESLint(raw string, check model.Check, result model.CommandResult) []model.Diagnostic {
	var entries []struct {
		FilePath string `json:"filePath"`
		Messages []struct {
			RuleID   string `json:"ruleId"`
			Severity int    `json:"severity"`
			Message  string `json:"message"`
			Line     int    `json:"line"`
			Column   int    `json:"column"`
		} `json:"messages"`
	}
	payload := strings.TrimSpace(raw)
	if start := strings.Index(payload, "["); start > 0 {
		if end := strings.LastIndex(payload, "]"); end > start {
			payload = payload[start : end+1]
		}
	}
	if json.Unmarshal([]byte(payload), &entries) != nil {
		return nil
	}
	var out []model.Diagnostic
	for _, e := range entries {
		for _, m := range e.Messages {
			if len(out) >= maxDiagnostics {
				return out
			}
			sev := "error"
			if m.Severity == 1 {
				sev = "warning"
			}
			d := model.Diagnostic{Tool: "ESLint", Category: check.Category, Severity: sev, Location: model.SourceLocation{File: cleanPath(e.FilePath), Line: m.Line, Column: m.Column, Exact: m.Line > 0}, Code: m.RuleID, Rule: m.RuleID, Message: m.Message, Command: result.Command, ExitCode: result.ExitCode, RawOutput: truncate(raw, 512), ReportedExact: m.Line > 0}
			out = append(out, d)
		}
	}
	return out
}

func toolName(c model.Check) string {
	s := strings.ToLower(c.Name + " " + strings.Join(c.Args, " "))
	switch {
	case strings.Contains(s, "cargo"):
		return "Cargo"
	case strings.Contains(s, "ruff"):
		return "Ruff"
	case strings.Contains(s, "typescript") || strings.Contains(s, "typecheck"):
		return "TypeScript"
	case strings.Contains(s, "eslint") || strings.Contains(s, "lint"):
		if strings.Contains(s, "ruff") {
			return "Ruff"
		}
		return "ESLint"
	case strings.Contains(s, "jest"):
		return "Jest"
	case strings.Contains(s, "vitest"):
		return "Vitest"
	case strings.Contains(s, "go test"):
		return "go test"
	case strings.Contains(s, "vet"):
		return "go vet"
	case strings.Contains(s, "ruff"):
		return "Ruff"
	case strings.Contains(s, "pytest"):
		return "pytest"
	case strings.Contains(s, "cargo"):
		return "Cargo"
	default:
		if c.Category == "lint" {
			return "ESLint"
		}
		if c.Category == "typecheck" {
			return "TypeScript"
		}
		return c.Name
	}
}
func cleanPath(s string) string {
	s = strings.Trim(strings.TrimSpace(s), "\"'")
	if strings.HasPrefix(s, "file://") {
		if u, err := url.Parse(s); err == nil && (u.Host == "" || u.Host == "localhost") {
			s = u.Path
			if len(s) > 2 && s[0] == '/' && s[2] == ':' {
				s = s[1:]
			}
		}
	}
	return s
}
func looksLikeFile(s string) bool {
	s = strings.TrimSpace(s)
	return strings.Contains(s, "/") || strings.Contains(s, "\\") || filepath.Ext(s) != ""
}
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n… output truncated"
}
func appendUnique(ds []model.Diagnostic, d model.Diagnostic) []model.Diagnostic {
	for _, x := range ds {
		if x.Location.File == d.Location.File && x.Location.Line == d.Location.Line && x.Code == d.Code && x.Message == d.Message {
			return ds
		}
	}
	return append(ds, d)
}

func Render(root string, d model.Diagnostic, radius int) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s\n", d.Tool))
	if d.Location.File != "" && d.Location.Line > 0 {
		b.WriteString("REPORTED LOCATION (tool evidence)\n")
		b.WriteString(fmt.Sprintf("%s:%d", d.Location.File, d.Location.Line))
		if d.Location.Column > 0 {
			b.WriteString(fmt.Sprintf(":%d", d.Location.Column))
		}
		b.WriteString("\n\n")
		path := d.Location.File
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		data, err := security.RepositoryFile(root, path)
		if err == nil {
			lines := strings.Split(string(data), "\n")
			start := d.Location.Line - radius
			if start < 1 {
				start = 1
			}
			end := d.Location.Line + radius
			if end > len(lines) {
				end = len(lines)
			}
			width := len(strconv.Itoa(end))
			for i := start; i <= end; i++ {
				b.WriteString(fmt.Sprintf("%*d │ %s\n", width, i, lines[i-1]))
				if i == d.Location.Line && d.Location.Column > 0 {
					col := d.Location.Column - 1
					if col > len(lines[i-1]) {
						col = len(lines[i-1])
					}
					b.WriteString(strings.Repeat(" ", width) + " │ " + strings.Repeat(" ", col) + "^\n")
				}
			}
		} else {
			b.WriteString("Exact source context unavailable: " + err.Error() + "\n")
		}
	} else {
		b.WriteString("Exact reported location: unavailable\n\n")
	}
	if d.Rule != "" {
		b.WriteString("Rule: " + d.Rule + "\n")
	} else if d.Code != "" {
		b.WriteString(d.Code + "\n")
	}
	b.WriteString(d.Message + "\n")
	return b.String()
}

func severity(line string) string {
	if strings.Contains(line, "warning") {
		return "warning"
	}
	return "error"
}

// Go, Rust and Ruff expose structured evidence. Their message text still passes
// through the deterministic location adapters, never through an LLM extractor.
func parseStructured(check model.Check, result model.CommandResult) ([]model.Diagnostic, bool) {
	raw := strings.TrimSpace(result.Stdout)
	if strings.HasPrefix(raw, "{") {
		var suite struct {
			TestResults []struct {
				Name             string
				FailureMessage   string
				AssertionResults []struct {
					Status, FullName string
					FailureMessages  []string
				}
			}
		}
		if json.Unmarshal([]byte(raw), &suite) == nil && suite.TestResults != nil {
			var out []model.Diagnostic
			for _, test := range suite.TestResults {
				if len(out) >= maxDiagnostics {
					break
				}
				messages := []string{}
				if test.FailureMessage != "" {
					messages = append(messages, test.FailureMessage)
				}
				for _, assertion := range test.AssertionResults {
					if assertion.Status == "failed" {
						plain := result
						plain.Stdout, plain.Stderr = strings.Join(assertion.FailureMessages, "\n"), ""
						ds := Parse(check, plain)
						for i := range ds {
							ds[i].TestName = assertion.FullName
							if ds[i].Location.File == "" {
								ds[i].Location.File = test.Name
							}
						}
						out = append(out, ds...)
					}
				}
				if len(messages) > 0 {
					plain := result
					plain.Stdout = strings.Join(messages, "\n")
					plain.Stderr = ""
					for _, d := range Parse(check, plain) {
						out = appendUnique(out, d)
					}
				}
			}
			return out, true
		}
	}
	if strings.HasPrefix(raw, "[") && (strings.Contains(check.Name, "ruff") || len(check.Args) > 0 && check.Args[0] == "ruff") {
		var entries []struct {
			Filename, Message, Code string
			Location                struct{ Row, Column int }
		}
		if json.Unmarshal([]byte(raw), &entries) != nil {
			return nil, false
		}
		var out []model.Diagnostic
		for _, v := range entries {
			if len(out) >= maxDiagnostics {
				break
			}
			out = append(out, model.Diagnostic{Tool: "Ruff", Category: check.Category, Severity: "error", Location: model.SourceLocation{File: v.Filename, Line: v.Location.Row, Column: v.Location.Column, Exact: v.Location.Row > 0}, Code: v.Code, Message: v.Message, Command: result.Command, ExitCode: result.ExitCode, RawOutput: truncate(raw, 512), ReportedExact: v.Location.Row > 0})
		}
		return out, true
	}
	var out []model.Diagnostic
	recognized := false
	var text strings.Builder
	goOutput := map[string]*strings.Builder{}
	goStatus := map[string]string{}
	var goOrder []string
	for _, line := range strings.Split(raw, "\n") {
		if len(out) >= maxDiagnostics {
			break
		}
		if !strings.HasPrefix(strings.TrimSpace(line), "{") {
			continue
		}
		var event struct {
			Action, Output, Test, Package, Reason string
			Message                               struct {
				Message, Level string
				Code           *struct{ Code string }
				Spans          []struct {
					FileName    string `json:"file_name"`
					LineStart   int    `json:"line_start"`
					ColumnStart int    `json:"column_start"`
					IsPrimary   bool   `json:"is_primary"`
				}
			}
		}
		if json.Unmarshal([]byte(line), &event) != nil {
			continue
		}
		if event.Action != "" && event.Package != "" {
			recognized = true
			key := event.Package + "\x00" + event.Test
			if goOutput[key] == nil {
				goOutput[key] = &strings.Builder{}
				goOrder = append(goOrder, key)
			}
			goOutput[key].WriteString(event.Output)
			if event.Action == "pass" || event.Action == "skip" || event.Action == "fail" {
				goStatus[key] = event.Action
			}
			if event.Action == "fail" && event.Test != "" {
				out = append(out, model.Diagnostic{Tool: "go test", Category: check.Category, Severity: "error", TestName: event.Test, Message: "test failed: " + event.Package + "/" + event.Test, Command: result.Command, ExitCode: result.ExitCode})
			}
		}
		if event.Reason == "compiler-message" {
			recognized = true
			d := model.Diagnostic{Tool: "Cargo", Category: check.Category, Severity: event.Message.Level, Message: event.Message.Message, Command: result.Command, ExitCode: result.ExitCode, RawOutput: truncate(line, 512)}
			if event.Message.Code != nil {
				d.Code = event.Message.Code.Code
			}
			for _, span := range event.Message.Spans {
				if span.IsPrimary {
					d.Location = model.SourceLocation{File: span.FileName, Line: span.LineStart, Column: span.ColumnStart, Exact: span.LineStart > 0}
					d.ReportedExact = d.Location.Exact
					break
				}
			}
			out = append(out, d)
		}
	}
	if !recognized {
		return nil, false
	}
	for _, key := range goOrder {
		// Passed/skipped test messages are not failure evidence, even when a
		// different package fails in the same go test invocation.
		if goStatus[key] != "pass" && goStatus[key] != "skip" {
			text.WriteString(goOutput[key].String())
		}
	}
	if text.Len() > 0 {
		plain := result
		plain.Stdout = text.String()
		plain.Stderr = result.Stderr
		out = append(Parse(check, plain), out...)
	}
	if len(out) == 0 && result.ExitCode != 0 {
		out = append(out, model.Diagnostic{Tool: toolName(check), Category: check.Category, Severity: "error", Message: fmt.Sprintf("%s exited with code %d", check.Name, result.ExitCode), RawOutput: truncate(raw+result.Stderr, 512)})
	}
	return out, true
}

var stackLocation = regexp.MustCompile(`^(.+?):(\d+)(?::(\d+))?$`)
var goStackLocation = regexp.MustCompile(`^(.+\.go):(\d+)(?: \+0x[0-9a-f]+)?$`)

func parseStack(line string) (model.StackFrame, bool) {
	frame := model.StackFrame{}
	candidate := ""
	if strings.HasPrefix(line, "at ") {
		candidate = strings.TrimPrefix(line, "at ")
		if index := strings.LastIndex(candidate, " ("); index >= 0 {
			frame.Function = candidate[:index]
			candidate = strings.TrimSuffix(candidate[index+2:], ")")
		}
	}
	var matches []string
	if candidate != "" {
		matches = stackLocation.FindStringSubmatch(candidate)
	} else {
		matches = goStackLocation.FindStringSubmatch(line)
	}
	if len(matches) < 3 || !looksLikeFile(matches[1]) {
		return frame, false
	}
	frame.File = cleanPath(matches[1])
	frame.Line, _ = strconv.Atoi(matches[2])
	if len(matches) > 3 {
		frame.Column, _ = strconv.Atoi(matches[3])
	}
	return frame, frame.Line > 0
}

// Structured lint messages and source literals are code evidence, not runtime
// failures. Remove only recognized ESLint JSON payloads, preserving surrounding
// process errors on either output stream and all termination evidence.
func runtimeEvidence(result model.CommandResult) string {
	stdout, stderr := result.Stdout, result.Stderr
	if result.ExitCode == 1 {
		stdout = withoutESLintPayloads(stdout)
		stderr = withoutESLintPayloads(stderr)
	}
	return strings.ToLower(stdout + "\n" + stderr + " " + result.Terminated)
}

func withoutESLintPayloads(raw string) string {
	var evidence strings.Builder
	for len(raw) > 0 {
		start := strings.IndexByte(raw, '[')
		if start < 0 {
			evidence.WriteString(raw)
			break
		}
		evidence.WriteString(raw[:start])
		raw = raw[start:]
		var entries []struct {
			FilePath string            `json:"filePath"`
			Messages []json.RawMessage `json:"messages"`
		}
		decoder := json.NewDecoder(strings.NewReader(raw))
		valid := decoder.Decode(&entries) == nil && len(entries) > 0
		hasMessages := false
		for _, entry := range entries {
			valid = valid && entry.FilePath != "" && entry.Messages != nil
			hasMessages = hasMessages || len(entry.Messages) > 0
		}
		if valid && hasMessages {
			evidence.WriteByte('\n')
			raw = raw[decoder.InputOffset():]
		} else {
			// Keep unrecognized text as evidence; continue at the next line so
			// nested brackets cannot cause repeated decoding of a large payload.
			end := strings.IndexByte(raw, '\n')
			if end < 0 {
				evidence.WriteString(raw)
				break
			}
			evidence.WriteString(raw[:end+1])
			raw = raw[end+1:]
		}
	}
	return evidence.String()
}

// Infrastructure evidence is not permission to edit source code.
func Infrastructure(result model.CommandResult) string {
	if result.Truncated {
		return "verification log evidence exceeded the configured bounds"
	}
	if result.TimedOut {
		return "the check timed out"
	}
	text := runtimeEvidence(result)
	for _, phrase := range []string{"no space left on device", "disk quota exceeded", "not enough space on the disk", "rate limit exceeded", "quota exceeded", "429 too many requests", "could not resolve host", "network is unreachable", "authentication failed", "executable file not found", "file does not exist", "missing required environment variable", "environment variable is not set", "missing environment variable", "missing required secret", "econnrefused", "enotfound", "missing script:", "configuration file not found", "eslint configuration error:"} {
		if strings.Contains(text, phrase) {
			return phrase
		}
	}
	return ""
}
