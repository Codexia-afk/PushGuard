package security

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pushguard/pushguard/internal/model"
)

var providerKey = regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]{20,}|gsk_[A-Za-z0-9_-]{20,}|AKIA[A-Z0-9]{16}|gh[opusr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})\b`)

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)--(?:token|password|api-key|api_key|secret|client-secret)(?:=|\s+)(?:"[^"]*"|'[^']*'|[^\s]+)`),
	regexp.MustCompile(`(?i)\b[A-Z0-9_]*(?:API_KEY|TOKEN|SECRET|PASSWORD|ENCRYPTION_KEY|ACCESS_KEY|CLIENT_SECRET)[A-Z0-9_]*\s*[:=]\s*(?:"[^"]*"|'[^']*'|[^\s,;]+)`),
	regexp.MustCompile(`(?i)["']?(api[_-]?key|token|secret|password|private[_-]?key)["']?\s*[:=]\s*(?:"[^"]*"|'[^']*'|[^\s,;]+)`),
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=-]+`),
	regexp.MustCompile(`-----BEGIN [A-Z ]+-----[\s\S]*?-----END [A-Z ]+-----`),
	providerKey,
	regexp.MustCompile(`(?i)((?:https?|postgres(?:ql)?|mysql|mongodb(?:\+srv)?|redis)://[^\s/@:]+:)[^\s/@]+@`),
	regexp.MustCompile(`(?i)authorization\s*:\s*(?:basic|token)\s+[^\s,;]+`),
}

func Redact(s string) string {
	for i, p := range secretPatterns {
		s = redactMatches(s, p, func(v string) string {
			if providerKey.FindString(v) == v {
				return "[REDACTED]"
			}
			// Assignment patterns: keep code expressions (calls, member access,
			// comparisons) so displayed and model-proposed source stays truthful;
			// literal values are still redacted.
			if i == 1 || i == 2 {
				value := assignedValue(v)
				quoted := strings.HasPrefix(value, `"`) || strings.HasPrefix(value, "'")
				if !quoted && expression(value) {
					return v
				}
			}
			l := strings.ToLower(strings.TrimSpace(v))
			if strings.HasPrefix(l, "bearer ") {
				return "Bearer [REDACTED]"
			}
			if strings.Contains(l, "begin ") {
				return "[REDACTED PRIVATE KEY]"
			}
			if strings.Contains(v, "@") && strings.Contains(l, "://") {
				return p.ReplaceAllString(v, "$1[REDACTED]@")
			}
			if m := regexp.MustCompile(`(?i)^([a-z0-9_-]+)`).FindStringSubmatch(v); len(m) > 1 {
				return m[1] + "=[REDACTED]"
			}
			return "[REDACTED]"
		})
	}
	return s
}

var primitiveParameter = regexp.MustCompile(`(?i)^[a-z_$][a-z0-9_$]*\??\s*:\s*(?:string|number|boolean|unknown|any|never)(?:\[\])?(?:\)[^\s]*)?$`)
var parameterPrefix = regexp.MustCompile(`[A-Za-z_$][A-Za-z0-9_$]*\s*\([^)]*$`)

// A typed function parameter (token: string) is not a credential value. Preserve
// its exact bytes so proposals can match the source. Quoted literals, env
// assignments and ordinary log/JSON/YAML fields still use normal redaction.
func redactMatches(source string, pattern *regexp.Regexp, replace func(string) string) string {
	var out strings.Builder
	end := 0
	for _, match := range pattern.FindAllStringIndex(source, -1) {
		out.WriteString(source[end:match[0]])
		value := source[match[0]:match[1]]
		lineStart := strings.LastIndexByte(source[:match[0]], '\n') + 1
		if primitiveParameter.MatchString(value) && parameterPrefix.MatchString(source[lineStart:match[0]]) {
			out.WriteString(value)
		} else {
			out.WriteString(replace(value))
		}
		end = match[1]
	}
	out.WriteString(source[end:])
	return out.String()
}
func SensitivePath(path string) bool {
	b := strings.ToLower(filepath.Base(path))
	return b == ".env" || strings.HasPrefix(b, ".env.") || b == "id_rsa" || b == "id_ed25519" || strings.Contains(b, "secret") || strings.Contains(b, "credential") || strings.Contains(b, "token") || strings.Contains(b, "password") || strings.Contains(b, "private") || strings.HasSuffix(b, ".pem") || strings.HasSuffix(b, ".key") || strings.HasSuffix(b, ".p12") || strings.HasSuffix(b, ".pfx")
}
func SanitizeReport(in model.SessionReport) model.SessionReport {
	out := in
	out.RepairMetrics.VerificationCommands = append([]string(nil), in.RepairMetrics.VerificationCommands...)
	for i := range out.RepairMetrics.VerificationCommands {
		out.RepairMetrics.VerificationCommands[i] = Redact(out.RepairMetrics.VerificationCommands[i])
	}
	out.RepairMetrics.FilesModified = append([]string(nil), in.RepairMetrics.FilesModified...)
	for i := range out.RepairMetrics.FilesModified {
		out.RepairMetrics.FilesModified[i] = Redact(out.RepairMetrics.FilesModified[i])
	}
	out.Events = append([]model.Event(nil), in.Events...)
	for i := range out.Events {
		out.Events[i].Detail = Redact(out.Events[i].Detail)
	}
	if in.CommitResult != nil {
		sanitized := SanitizeReport(model.SessionReport{PushResult: in.CommitResult})
		out.CommitResult = sanitized.PushResult
	}
	out.RepairAudit = append([]model.RepairAudit(nil), in.RepairAudit...)
	for i := range out.RepairAudit {
		a := &out.RepairAudit[i]
		a.ContextRequest = sanitizeContextRequest(a.ContextRequest)
		a.Rejection = Redact(a.Rejection)
		a.Diagnostics = append([]model.Diagnostic(nil), a.Diagnostics...)
		for j := range a.Diagnostics {
			a.Diagnostics[j] = sanitizeDiagnostic(a.Diagnostics[j])
		}
		// Reuse the report sanitizer for nested proposal and verification records.
		if a.Proposal != nil {
			sanitized := SanitizeReport(model.SessionReport{Repairs: []model.RepairProposal{*a.Proposal}})
			a.Proposal = &sanitized.Repairs[0]
		}
		if a.Verification != nil {
			sanitized := SanitizeReport(model.SessionReport{Checks: []model.CheckResult{*a.Verification}})
			a.Verification = &sanitized.Checks[0]
		}
	}
	out.Checks = cloneChecks(in.Checks)
	out.History = cloneChecks(in.History)
	out.Diagnostics = append([]model.Diagnostic(nil), in.Diagnostics...)
	out.Repairs = append([]model.RepairProposal(nil), in.Repairs...)
	for i := range out.Repairs {
		out.Repairs[i].ContextRequired = sanitizeContextRequest(out.Repairs[i].ContextRequired)
		out.Repairs[i].Files = append([]string(nil), in.Repairs[i].Files...)
		out.Repairs[i].Verification = append([]string(nil), in.Repairs[i].Verification...)
		out.Repairs[i].Risks = append([]string(nil), in.Repairs[i].Risks...)
		out.Repairs[i].Edits = append([]model.TextEdit(nil), in.Repairs[i].Edits...)
	}
	out.Review = append([]model.ReviewEntry(nil), in.Review...)
	for i := range out.Review {
		out.Review[i].Diagnostics = append([]string(nil), in.Review[i].Diagnostics...)
	}
	out.Warnings = append([]string(nil), in.Warnings...)
	if in.PushResult != nil {
		p := *in.PushResult
		p.Args = append([]string(nil), p.Args...)
		out.PushResult = &p
	}
	if out.Repository != nil {
		r := *out.Repository
		r.Target.RemoteURL = Redact(r.Target.RemoteURL)
		out.Repository = &r
	}
	for i := range out.Checks {
		out.Checks[i].Check.Command = Redact(out.Checks[i].Check.Command)
		for j := range out.Checks[i].Check.Args {
			out.Checks[i].Check.Args[j] = Redact(out.Checks[i].Check.Args[j])
		}
		out.Checks[i].Command.Command = Redact(out.Checks[i].Command.Command)
		out.Checks[i].Command.WorkingDir = Redact(out.Checks[i].Command.WorkingDir)
		for j := range out.Checks[i].Command.Args {
			out.Checks[i].Command.Args[j] = Redact(out.Checks[i].Command.Args[j])
		}
		out.Checks[i].Command.Stdout = Redact(out.Checks[i].Command.Stdout)
		out.Checks[i].Command.Stderr = Redact(out.Checks[i].Command.Stderr)
		for j := range out.Checks[i].Diagnostics {
			out.Checks[i].Diagnostics[j] = sanitizeDiagnostic(out.Checks[i].Diagnostics[j])
		}
	}
	for i := range out.Diagnostics {
		out.Diagnostics[i] = sanitizeDiagnostic(out.Diagnostics[i])
	}
	for i := range out.Repairs {
		out.Repairs[i].Summary = Redact(out.Repairs[i].Summary)
		out.Repairs[i].RootCause = Redact(out.Repairs[i].RootCause)
		out.Repairs[i].Patch = Redact(out.Repairs[i].Patch)
		for j := range out.Repairs[i].Edits {
			out.Repairs[i].Edits[j].OldText = Redact(out.Repairs[i].Edits[j].OldText)
			out.Repairs[i].Edits[j].NewText = Redact(out.Repairs[i].Edits[j].NewText)
		}
		out.Repairs[i].Confidence = Redact(out.Repairs[i].Confidence)
		for j := range out.Repairs[i].Risks {
			out.Repairs[i].Risks[j] = Redact(out.Repairs[i].Risks[j])
		}
		for j := range out.Repairs[i].Verification {
			out.Repairs[i].Verification[j] = Redact(out.Repairs[i].Verification[j])
		}
		for j := range out.Repairs[i].Files {
			out.Repairs[i].Files[j] = Redact(out.Repairs[i].Files[j])
		}
	}
	if out.Preflight != nil {
		p := *out.Preflight
		p.Items = append([]model.PreflightItem(nil), p.Items...)
		for i := range p.Items {
			p.Items[i].Detail = Redact(p.Items[i].Detail)
		}
		out.Preflight = &p
	}
	for i := range out.Review {
		out.Review[i].Reason = Redact(out.Review[i].Reason)
		for j := range out.Review[i].Diagnostics {
			out.Review[i].Diagnostics[j] = Redact(out.Review[i].Diagnostics[j])
		}
	}
	for i := range out.Warnings {
		out.Warnings[i] = Redact(out.Warnings[i])
	}
	if out.PushResult != nil {
		out.PushResult.Command = Redact(out.PushResult.Command)
		out.PushResult.WorkingDir = Redact(out.PushResult.WorkingDir)
		for i := range out.PushResult.Args {
			out.PushResult.Args[i] = Redact(out.PushResult.Args[i])
		}
		out.PushResult.Stdout = Redact(out.PushResult.Stdout)
		out.PushResult.Stderr = Redact(out.PushResult.Stderr)
	}
	for i := range out.History {
		out.History[i].Check.Command = Redact(out.History[i].Check.Command)
		for j := range out.History[i].Check.Args {
			out.History[i].Check.Args[j] = Redact(out.History[i].Check.Args[j])
		}
		out.History[i].Command.Command = Redact(out.History[i].Command.Command)
		for j := range out.History[i].Command.Args {
			out.History[i].Command.Args[j] = Redact(out.History[i].Command.Args[j])
		}
		out.History[i].Command.Stdout = Redact(out.History[i].Command.Stdout)
		out.History[i].Command.Stderr = Redact(out.History[i].Command.Stderr)
		for j := range out.History[i].Diagnostics {
			out.History[i].Diagnostics[j] = sanitizeDiagnostic(out.History[i].Diagnostics[j])
		}
	}
	out.Error = Redact(out.Error)
	return out
}

func sanitizeContextRequest(in *model.ContextRequest) *model.ContextRequest {
	if in == nil {
		return nil
	}
	out := *in
	out.Reason = Redact(in.Reason)
	out.Files, out.Symbols = append([]string(nil), in.Files...), append([]string(nil), in.Symbols...)
	for i := range out.Files {
		out.Files[i] = Redact(out.Files[i])
	}
	for i := range out.Symbols {
		out.Symbols[i] = Redact(out.Symbols[i])
	}
	return &out
}
func cloneChecks(in []model.CheckResult) []model.CheckResult {
	out := append([]model.CheckResult(nil), in...)
	for i := range out {
		out[i].Check.Args = append([]string(nil), in[i].Check.Args...)
		out[i].Command.Args = append([]string(nil), in[i].Command.Args...)
		out[i].Diagnostics = append([]model.Diagnostic(nil), in[i].Diagnostics...)
	}
	return out
}
func sanitizeDiagnostic(d model.Diagnostic) model.Diagnostic {
	d.Message = Redact(d.Message)
	d.RawOutput = Redact(d.RawOutput)
	d.Command = Redact(d.Command)
	return d
}

var ansi = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07]*(?:\x07|\x1b\\))`)

// Terminal prevents logs or repository text from issuing terminal escape commands.
func Terminal(s string) string {
	s = ansi.ReplaceAllString(s, "")
	return strings.Map(func(r rune) rune {
		if r < 32 && r != '\n' && r != '\t' && r != '\r' || r == 127 {
			return -1
		}
		return r
	}, s)
}

// RepositoryFile keeps diagnostic/context reads inside the repository and bounded.
func RepositoryFile(root, path string) ([]byte, error) {
	if SensitivePath(path) {
		return nil, fmt.Errorf("sensitive source context withheld")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(realRoot, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("source context is outside the repository")
	}
	if SensitivePath(rel) {
		return nil, fmt.Errorf("sensitive source context withheld")
	}
	info, err := os.Stat(real)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, fmt.Errorf("source context exceeds the 1 MiB file limit or is not regular")
	}
	return os.ReadFile(real)
}

func assignedValue(match string) string {
	index := strings.IndexAny(match, ":=")
	if index < 0 {
		return ""
	}
	return strings.TrimSpace(match[index+1:])
}

func expression(value string) bool {
	v := strings.TrimSpace(value)
	return v == "" || strings.HasPrefix(v, "=") || strings.HasPrefix(v, ">") || strings.ContainsAny(v, "()[]{}.$<>")
}
