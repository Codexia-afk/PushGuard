package diagnostics

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/pushguard/pushguard/internal/model"
)

var (
	annotation     = regexp.MustCompile(`^::(error|warning|notice)\s+(.*?)::(.*)$`)
	properties     = regexp.MustCompile(`(?:^|,)\s*(file|line|col)=([^,]+)`)
	annotationRule = regexp.MustCompile(`(?s)^([@a-zA-Z0-9_/-]+):\s*(.*)$`)
	stylish        = regexp.MustCompile(`^(\d+):(\d+)\s+(error|warning)\s+(.+?)\s{2,}(\S+)\s*$`)
	plainParen     = regexp.MustCompile(`^(.+?)\((\d+),(\d+)\):?\s*(.*)$`)
	ruffCode       = regexp.MustCompile(`^([A-Z]+\d+)\s+(.*)$`)
	npmCode        = regexp.MustCompile(`(?i)^npm\s+(?:ERR!|error)\s+code\s+(\S+)`)
)

func location(file, line, column string) model.SourceLocation {
	l, _ := strconv.Atoi(line)
	c, _ := strconv.Atoi(column)
	return model.SourceLocation{File: cleanPath(file), Line: l, Column: c, Exact: l > 0}
}

// GitHub workflow-command escapes are decoded after splitting properties. In
// particular %2C in a filename is data, never a property separator.
func unescapeAnnotation(s string) string {
	return strings.NewReplacer("%0D", "\r", "%0A", "\n", "%3A", ":", "%2C", ",", "%25", "%").Replace(s)
}

func parseAnnotations(raw string, check model.Check, result model.CommandResult) []model.Diagnostic {
	lines := strings.Split(raw, "\n")
	var out []model.Diagnostic
	for i, text := range lines {
		line := strings.TrimSpace(text)
		m := annotation.FindStringSubmatch(line)
		props, message, sev := line, "", "error"
		if m != nil {
			props, message, sev = m[2], unescapeAnnotation(m[3]), m[1]
		}
		values := map[string]string{}
		for _, p := range properties.FindAllStringSubmatch(props, -1) {
			values[p[1]] = unescapeAnnotation(p[2])
		}
		if values["file"] == "" {
			continue
		}
		d := model.Diagnostic{Tool: toolName(check), Category: check.Category, Severity: sev, Location: location(values["file"], values["line"], values["col"]), Message: message, Command: result.Command, ExitCode: result.ExitCode, RawOutput: truncate(raw, 512)}
		if rule := annotationRule.FindStringSubmatch(message); rule != nil {
			if d.Tool == "ESLint" {
				d.Rule, d.Code, d.Message = rule[1], rule[1], rule[2]
			}
			if strings.HasPrefix(rule[1], "TS") {
				d.Tool, d.Code, d.Message = "TypeScript", rule[1], rule[2]
			}
		}
		if d.Message == "" && i+1 < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i+1]), "::") {
			d.Message = strings.TrimSpace(lines[i+1])
		}
		if d.Message == "" {
			d.Message = "diagnostic reported by the tool"
		}
		d.ReportedExact = d.Location.Exact
		out = appendUnique(out, d)
		if len(out) >= maxDiagnostics {
			break
		}
	}
	return out
}

// Classify describes deterministic evidence; it does not infer application causes.
func Classify(check model.Check, result model.CommandResult) string {
	text := runtimeEvidence(result)
	if result.TimedOut || result.Truncated || containsAny(text, "no space left", "quota exceeded", "out of memory", "rate limit") {
		return "RESOURCE"
	}
	if containsAny(text, "environment variable", "missing required secret", "executable file not found") {
		return "ENVIRONMENT"
	}
	if containsAny(text, "could not resolve host", "network is unreachable", "econnrefused", "enotfound") {
		return "NETWORK"
	}
	if containsAny(text, "missing script:", "configuration file not found", "invalid configuration", "eslint configuration error:") {
		return "CONFIGURATION"
	}
	if check.Category == "git" {
		return "GIT"
	}
	if check.Category == "security" || containsAny(text, "expired token", "authentication", "authorization", "sql injection", "csrf", "xss") {
		return "SECURITY"
	}
	if check.Category == "test" {
		return "TEST"
	}
	if containsAny(check.Category, "lint", "typecheck", "build", "vet", "format", "check", "syntax") {
		return "CODE"
	}
	return "UNKNOWN"
}

func containsAny(s string, values ...string) bool {
	for _, v := range values {
		if strings.Contains(s, v) {
			return true
		}
	}
	return false
}
