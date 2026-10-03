// Package delivery composes portable local evidence and remotely observed CI.
// It is shared by the CLI and a separately trusted hosted publisher, not by AI.
package delivery

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"path"
	"strings"

	"github.com/pushguard/pushguard/internal/codehost"
	"github.com/pushguard/pushguard/internal/model"
	"github.com/pushguard/pushguard/internal/receipt"
	"github.com/pushguard/pushguard/internal/security"
)

const marker = "<!-- pushguard-receipt-v1:"

type Snapshot struct {
	PR          *codehost.PullRequest        `json:"pullRequest"`
	Receipt     *receipt.VerificationReceipt `json:"receipt,omitempty"`
	Local       receipt.Validation           `json:"local"`
	Hosted      codehost.CheckState          `json:"hosted"`
	Overall     codehost.CheckState          `json:"overall"`
	Checks      []codehost.HostedCheck       `json:"checks"`
	Published   codehost.CheckState          `json:"publishedCheck"`
	Files       []string                     `json:"files,omitempty"`
	Warnings    []string                     `json:"warnings,omitempty"`
	Summary     string                       `json:"summary"`
	Annotations []codehost.Annotation        `json:"annotations,omitempty"`
}
type Service struct {
	Provider   codehost.CodeHostProvider
	Repository codehost.RemoteRepository
	Keys       map[string]ed25519.PublicKey
	Required   []string
}

func Description(description string, v *receipt.VerificationReceipt) (string, error) {
	if v == nil || v.ID != v.Hash() {
		return "", fmt.Errorf("valid receipt required for PR description")
	}
	if strings.Contains(description, marker) {
		return "", fmt.Errorf("description must not supply a receipt block")
	}
	var b strings.Builder
	if description != "" {
		b.WriteString(security.Redact(description) + "\n\n")
	}
	b.WriteString("## PushGuard Verification\n\nLocal verification completed before this branch was pushed.\n\n### Checks\n\n")
	for _, c := range v.Checks {
		fmt.Fprintf(&b, "- %s: %s\n", markdown(c.Name), c.Status)
	}
	files := map[string]bool{}
	for _, r := range v.AIRepairs {
		for _, f := range r.Files {
			files[f] = true
		}
	}
	fmt.Fprintf(&b, "\n### AI-assisted repair\n\n%d approved repair operations; %d AI-assisted files. Recorded repairs were shown, approved, applied by PushGuard, and followed by deterministic verification.\n\n### Verification\n\nPushGuard version: %s\n\nReceipt: `%s`\n\nCommit: `%s`\n\n### Important\n\nLocal verification does not replace hosted CI. GitHub Actions and repository-required checks must still pass. The signed local receipt is an issuer claim; hosted verification requires a maintainer-trusted signing key.\n", len(v.AIRepairs), len(files), markdown(v.PushGuardVersion), v.ID, v.CommitSHA)
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	b.WriteString("\n" + marker + base64.StdEncoding.EncodeToString(data) + " -->\n")
	if b.Len() > 60000 {
		return "", fmt.Errorf("PR verification description exceeds 60000 bytes")
	}
	return b.String(), nil
}

func Extract(body string) (*receipt.VerificationReceipt, error) {
	if len(body) > 65536 || strings.Count(body, marker) != 1 {
		return nil, fmt.Errorf("exactly one bounded signed receipt block is required")
	}
	start := strings.Index(body, marker) + len(marker)
	end := strings.Index(body[start:], " -->")
	if end < 0 {
		return nil, fmt.Errorf("receipt block is incomplete")
	}
	data, err := base64.StdEncoding.DecodeString(body[start : start+end])
	if err != nil {
		return nil, fmt.Errorf("receipt block is not valid base64")
	}
	var v receipt.VerificationReceipt
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&v); err != nil {
		return nil, fmt.Errorf("receipt schema is invalid")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("receipt has trailing data")
	}
	return &v, nil
}

func Aggregate(checks []codehost.HostedCheck, required []string) codehost.CheckState {
	if len(checks) == 0 {
		return codehost.Unknown
	}
	seen := map[string]codehost.CheckState{}
	pending, unknown, neutral, cancelled := false, false, false, false
	for _, c := range checks {
		if c.Source == "pushguard" {
			continue
		}
		if c.State == codehost.Failure {
			return codehost.Failure
		}
		seen[c.Name] = c.State
		switch c.State {
		case codehost.Queued, codehost.InProgress:
			pending = true
		case codehost.Unknown:
			unknown = true
		case codehost.Neutral:
			neutral = true
		case codehost.Cancelled:
			cancelled = true
		case codehost.Success:
		default:
			unknown = true
		}
	}
	if len(seen) == 0 {
		return codehost.Unknown
	}
	for _, name := range required {
		state, ok := seen[name]
		if !ok {
			pending = true
		} else if state == codehost.Neutral || state == codehost.Cancelled {
			return codehost.Failure
		}
	}
	if cancelled {
		return codehost.Cancelled
	}
	if pending {
		return codehost.InProgress
	}
	if unknown {
		return codehost.Unknown
	}
	if neutral {
		return codehost.Neutral
	}
	return codehost.Success
}

func (s Service) Inspect(ctx context.Context, number int) (*Snapshot, error) {
	pr, err := s.Provider.GetPullRequest(ctx, number)
	if err != nil {
		return nil, err
	}
	if pr == nil {
		return nil, fmt.Errorf("pull request unavailable")
	}
	if pr.HeadRepository != s.Repository.Identity() {
		return nil, fmt.Errorf("fork PR verification is not supported by this provider workflow")
	}
	state, err := s.Provider.GetCommitState(ctx, pr.HeadSHA)
	if err != nil {
		return nil, err
	}
	v, extractErr := Extract(pr.Body)
	local := receipt.ValidateVerification(v, receipt.Expectation{Repository: s.Repository.Identity(), CommitSHA: pr.HeadSHA, Branch: pr.Head, TreeHash: state.TreeHash, ConfigHash: state.ConfigHash, Keys: s.Keys})
	if extractErr != nil {
		local = receipt.Validation{Status: receipt.Unverified, Reason: extractErr.Error()}
	}
	checks, err := s.Provider.GetChecks(ctx, pr.HeadSHA)
	if err != nil {
		return nil, err
	}
	snapshot := &Snapshot{PR: pr, Receipt: v, Local: local, Hosted: Aggregate(checks, s.Required), Checks: checks}
	snapshot.Published = codehost.Unknown
	for _, c := range checks {
		if c.Source == "pushguard" && v != nil && c.ReceiptID == v.ID {
			snapshot.Published = c.State
		}
	}
	snapshot.Overall = snapshot.Hosted
	if local.Status != receipt.Valid {
		snapshot.Overall = codehost.Failure
	}
	if local.Status == receipt.Valid && snapshot.Hosted == codehost.Unknown {
		snapshot.Overall = codehost.Queued
	}
	for _, c := range checks {
		for _, d := range c.Diagnostics {
			if a, ok := Annotation(d); ok {
				snapshot.Annotations = append(snapshot.Annotations, a)
			}
		}
	}
	if len(snapshot.Annotations) > 50 {
		snapshot.Warnings = append(snapshot.Warnings, "Only the first 50 annotations are published; use the original CI check for all diagnostics")
		snapshot.Annotations = snapshot.Annotations[:50]
	}
	if len(s.Required) == 0 {
		snapshot.Warnings = append(snapshot.Warnings, "Status covers observed hosted checks. Configure github.checks.required (or publisher --require) for expected check names; GitHub branch protection remains authoritative")
	}
	snapshot.Summary = Summary(*snapshot)
	return snapshot, nil
}

func (s Service) Publish(ctx context.Context, snapshot *Snapshot) error {
	current, err := s.Provider.GetPullRequest(ctx, snapshot.PR.Number)
	if err != nil {
		return err
	}
	if current == nil || current.HeadSHA != snapshot.PR.HeadSHA || current.Body != snapshot.PR.Body {
		return fmt.Errorf("PR HEAD or receipt changed during hosted inspection; previous result is stale")
	}
	id := "unverified"
	if snapshot.Receipt != nil {
		id = snapshot.Receipt.ID
	}
	return s.Provider.PublishVerification(ctx, codehost.HostedVerification{HeadSHA: current.HeadSHA, ReceiptID: id, State: snapshot.Overall, Summary: snapshot.Summary, Annotations: snapshot.Annotations})
}

// CLI consumers also wait for the trusted publisher's check. The publisher
// itself evaluates receipt + CI without depending on its own previous result.
func RequirePublished(snapshot *Snapshot) {
	if snapshot.Overall == codehost.Success && snapshot.Published != codehost.Success {
		snapshot.Overall = codehost.InProgress
		if snapshot.Published == codehost.Failure || snapshot.Published == codehost.Cancelled {
			snapshot.Overall = snapshot.Published
		}
		snapshot.Warnings = append(snapshot.Warnings, "Waiting for the maintainer-configured PushGuard publisher to validate this receipt; local CLI trust is not hosted issuer trust")
	}
	snapshot.Summary = Summary(*snapshot)
}

func Annotation(d model.Diagnostic) (codehost.Annotation, bool) {
	file := d.Location.File
	if file == "" || d.Location.Line < 1 || !d.Location.Exact && !d.ReportedExact || strings.ContainsAny(file, "\\\x00\r\n:") || strings.HasPrefix(file, "/") || path.Clean(file) != file || file == ".." || strings.HasPrefix(file, "../") {
		return codehost.Annotation{}, false
	}
	message := security.Terminal(security.Redact(d.Message))
	if len(message) > 2000 {
		message = message[:2000]
	}
	rule := d.Rule
	if rule == "" {
		rule = d.Code
	}
	title := security.Terminal(security.Redact(d.Tool))
	if rule != "" {
		title += " / " + security.Terminal(rule)
	}
	if len(title) > 200 {
		title = title[:200]
	}
	level := "failure"
	switch d.Severity {
	case "warning":
		level = "warning"
	case "notice", "info":
		level = "notice"
	}
	a := codehost.Annotation{Path: file, StartLine: d.Location.Line, EndLine: d.Location.Line, Title: title, Message: message, Level: level}
	if d.Location.Column > 0 {
		a.StartColumn = d.Location.Column
		a.EndColumn = d.Location.Column
	}
	return a, true
}

func markdown(s string) string {
	return strings.NewReplacer("`", "\\`", "\n", " ", "\r", " ", "|", "\\|").Replace(html.EscapeString(security.Terminal(security.Redact(s))))
}
func Summary(s Snapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## PushGuard Verification\n\nPR #%d — Commit `%s`\n\n### Local verification\n\n%s: %s\n", s.PR.Number, s.PR.HeadSHA, s.Local.Status, markdown(s.Local.Reason))
	if s.Receipt != nil && s.Local.Status == receipt.Valid {
		for _, c := range s.Receipt.Checks {
			fmt.Fprintf(&b, "- %s: %s\n", markdown(c.Name), c.Status)
		}
		files := map[string]bool{}
		for _, r := range s.Receipt.AIRepairs {
			for _, f := range r.Files {
				files[f] = true
			}
		}
		fmt.Fprintf(&b, "\nAI-assisted repairs: %d; affected files: %d.\n\nReceipt: `%s`\n", len(s.Receipt.AIRepairs), len(files), s.Receipt.ID)
	}
	fmt.Fprintf(&b, "\n### Hosted verification: %s\n\n", s.Hosted)
	for _, c := range s.Checks {
		if c.Source != "pushguard" {
			fmt.Fprintf(&b, "- %s: %s\n", markdown(c.Name), c.State)
		}
	}
	if s.Hosted == codehost.Failure && s.Local.Status == receipt.Valid {
		b.WriteString("\nLocal verification did not reproduce this hosted failure. The historical local receipt is unchanged.\n")
	}
	fmt.Fprintf(&b, "\n### Overall: %s\n\n", s.Overall)
	if s.Overall == codehost.Success {
		b.WriteString("Required PushGuard checks passed. Ready for human review. Maintainers and GitHub policies decide whether to merge.\n")
	} else if s.Overall == codehost.InProgress || s.Overall == codehost.Queued {
		b.WriteString("WAITING FOR HOSTED CI. Hosted verification has not passed.\n")
	} else {
		b.WriteString("Verification is incomplete or failed. No merge is authorized.\n")
	}
	for _, warning := range s.Warnings {
		b.WriteString("\nWARNING: " + markdown(warning) + "\n")
	}
	return b.String()
}
