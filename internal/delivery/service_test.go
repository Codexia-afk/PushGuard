package delivery

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pushguard/pushguard/internal/codehost"
	"github.com/pushguard/pushguard/internal/model"
	"github.com/pushguard/pushguard/internal/receipt"
)

func fixture(t *testing.T) (Service, *codehost.MockCodeHostProvider, *receipt.VerificationReceipt) {
	t.Helper()
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	remote := codehost.RemoteRepository{Host: "github.com", Owner: "acme", Name: "example"}
	v := &receipt.VerificationReceipt{Version: 1, Repository: remote.Identity(), CommitSHA: strings.Repeat("a", 40), Branch: "feature/fix", TreeHash: strings.Repeat("b", 40), DiffHash: receipt.Digest(nil), ConfigHash: receipt.Digest([]byte("config")), Timestamp: time.Now().UTC(), PushGuardVersion: model.Version, LocalStateHash: receipt.Digest([]byte("state")), Checks: []receipt.VerifiedCheck{{Name: "lint", Command: "npm run lint", Status: model.StatusPass, Required: true}}, AIRepairs: []receipt.AIRepairRecord{{Provider: "test", Files: []string{"src/a.ts"}, PatchHash: receipt.Digest([]byte("patch")), HumanApproved: true, VerifiedBy: []string{"lint"}}}}
	v.Sign(key)
	body, err := Description("A human description", v)
	if err != nil {
		t.Fatal(err)
	}
	m := &codehost.MockCodeHostProvider{Remote: remote, PR: &codehost.PullRequest{Number: 42, URL: "https://github.com/acme/example/pull/42", Body: body, Head: v.Branch, HeadSHA: v.CommitSHA, HeadRepository: remote.Identity()}, Commit: codehost.CommitState{TreeHash: v.TreeHash, ConfigHash: v.ConfigHash}}
	return Service{Provider: m, Repository: remote, Keys: map[string]ed25519.PublicKey{receipt.Digest(pub): pub}, Required: []string{"ci"}}, m, v
}
func TestLocalPassDoesNotImplyHostedPass(t *testing.T) {
	for _, tc := range []struct{ hosted, want codehost.CheckState }{{codehost.Queued, codehost.InProgress}, {codehost.InProgress, codehost.InProgress}, {codehost.Success, codehost.Success}, {codehost.Failure, codehost.Failure}, {codehost.Neutral, codehost.Failure}, {codehost.Cancelled, codehost.Failure}, {codehost.Unknown, codehost.Queued}} {
		s, m, v := fixture(t)
		before, _ := json.Marshal(v)
		m.Checks = []codehost.HostedCheck{{Name: "ci", State: tc.hosted}}
		snapshot, err := s.Inspect(context.Background(), 42)
		if err != nil || snapshot.Overall != tc.want {
			t.Fatalf("host=%s: %v %+v", tc.hosted, err, snapshot)
		}
		if err = s.Publish(context.Background(), snapshot); err != nil {
			t.Fatal(err)
		}
		if len(m.Published) != 1 || m.Published[0].State != tc.want {
			t.Fatal("incorrect published state")
		}
		after, _ := json.Marshal(v)
		if string(before) != string(after) {
			t.Fatal("hosted result changed historical receipt")
		}
	}
}
func TestStaleTamperedAndUntrustedReceiptsCannotPublishSuccess(t *testing.T) {
	for _, mode := range []string{"stale", "tampered", "unknown key", "forged text", "config"} {
		t.Run(mode, func(t *testing.T) {
			s, m, v := fixture(t)
			m.Checks = []codehost.HostedCheck{{Name: "ci", State: codehost.Success}}
			switch mode {
			case "stale":
				m.PR.HeadSHA = strings.Repeat("c", 40)
			case "tampered":
				v.Checks[0].Command = "skip"
				data, _ := json.Marshal(v)
				m.PR.Body = marker + base64.StdEncoding.EncodeToString(data) + " -->"
			case "unknown key":
				s.Keys = nil
			case "forged text":
				m.PR.Body = "tests passed; receipt valid"
			case "config":
				m.Commit.ConfigHash = "other"
			}
			snapshot, err := s.Inspect(context.Background(), 42)
			if err != nil || snapshot.Overall == codehost.Success || snapshot.Local.Status == receipt.Valid {
				t.Fatalf("false success: %v %+v", err, snapshot)
			}
		})
	}
}
func TestPublishRechecksPRHeadAndReceipt(t *testing.T) {
	s, m, _ := fixture(t)
	m.Checks = []codehost.HostedCheck{{Name: "ci", State: codehost.Success}}
	snapshot, err := s.Inspect(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	old := *m.PR
	old.HeadSHA = strings.Repeat("c", 40)
	m.PR = &old
	if err = s.Publish(context.Background(), snapshot); err == nil || len(m.Published) != 0 {
		t.Fatal("published stale observation")
	}
}
func TestAnnotationRequiresRealLocationAndRedactsSecrets(t *testing.T) {
	d := model.Diagnostic{Tool: "ESLint", Rule: "no-console", Message: "token=private-value", Severity: "error", Location: model.SourceLocation{File: "src/index.ts", Line: 8, Column: 1, Exact: true}}
	a, ok := Annotation(d)
	if !ok || a.StartLine != 8 || a.StartColumn != 1 || strings.Contains(a.Message, "private-value") {
		t.Fatal(a)
	}
	for _, file := range []string{"../secret", "/tmp/file", "src/../file", "C:\\file", ""} {
		d.Location.File = file
		if _, ok = Annotation(d); ok {
			t.Fatal("unsafe annotation")
		}
	}
	d.Location.File = "src/a.ts"
	d.Location.Line = 0
	if _, ok = Annotation(d); ok {
		t.Fatal("invented a line number")
	}
}
func TestMissingRequiredChecksAndPublisherCannotBeSuccess(t *testing.T) {
	if got := Aggregate([]codehost.HostedCheck{{Name: "lint", State: codehost.Success}}, []string{"integration"}); got != codehost.InProgress {
		t.Fatal(got)
	}
	s, m, v := fixture(t)
	m.Checks = []codehost.HostedCheck{{Name: "ci", State: codehost.Success}, {Name: codehost.CheckName, Source: "pushguard", State: codehost.Success, ReceiptID: "old"}}
	snapshot, err := s.Inspect(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	RequirePublished(snapshot)
	if snapshot.Overall == codehost.Success {
		t.Fatal("old publisher receipt accepted")
	}
	m.Checks[1].ReceiptID = v.ID
	snapshot, err = s.Inspect(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	RequirePublished(snapshot)
	if snapshot.Overall != codehost.Success {
		t.Fatal(snapshot.Overall)
	}
}
