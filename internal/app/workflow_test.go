package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/pushguard/pushguard/internal/config"
	"github.com/pushguard/pushguard/internal/model"
	"github.com/pushguard/pushguard/internal/testutil"
	"github.com/pushguard/pushguard/internal/ui"
)

type countedProvider struct {
	calls     int
	proposals int
	p         model.RepairProposal
}

func (p *countedProvider) Analyze(context.Context, model.ContextBundle) (*model.Analysis, error) {
	p.calls++
	return &model.Analysis{Summary: "Evidence observed", RootCause: "Implementation bug"}, nil
}
func (p *countedProvider) ProposeFix(context.Context, model.ContextBundle) (*model.RepairProposal, error) {
	p.proposals++
	return &p.p, nil
}

func TestDenialBeforeInvestigationAndPatchAreIndependent(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		calls       int
	}{
		{"repair denied", "allow\nwhat failed?\nshow the code\nshow the logs\nwhat happens if I deny?\ndeny\n", 0},
		{"unknown not approval", "allow\nmaybe\n", 0},
		{"patch denied", "allow\nallow\nshow the diff\nwhy is this fix safe?\ndeny\n", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, remote := fixture(t, "annotation", "pass")
			p := &countedProvider{p: proposal("func Value() int { return 0 }", "func Value() int { return 1 }", 3)}
			before, _ := os.ReadFile(filepath.Join(root, "value.go"))
			a, code, out := execute(t, root, tc.input, p, false)
			after, _ := os.ReadFile(filepath.Join(root, "value.go"))
			if code != ExitCancelled || p.calls != tc.calls || p.proposals != tc.calls || !bytes.Equal(before, after) || len(a.Report.History) != 1 {
				t.Fatalf("approval boundary failed: %d calls=%d/%d\n%s", code, p.calls, p.proposals, out)
			}
			if !strings.Contains(out, "value.go:3:26") || !strings.Contains(out, "PUSH NOT STARTED") {
				t.Fatal(out)
			}
			if len(a.Report.RepairAudit) != 1 {
				t.Fatal("missing denial audit")
			}
			remoteEmpty(t, root, remote)
		})
	}
}

func TestConfiguredProviderGetsNoHTTPBeforeInvestigationApproval(t *testing.T) {
	root, remote := fixture(t, "first")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	cfg, _, _ := config.Load(root)
	cfg.AI.Provider, cfg.AI.Model, cfg.AI.Endpoint = "ollama", "test-model", server.URL
	data, _ := json.Marshal(cfg)
	testutil.Write(t, root, ".pushguard.json", string(data))
	testutil.Commit(t, root)
	_, code, out := execute(t, root, "allow\ndeny\n", nil, false)
	if code != ExitCancelled || calls.Load() != 0 {
		t.Fatalf("denial contacted provider: %d %s", code, out)
	}
	_, code, out = execute(t, root, "allow\nallow\n", nil, false)
	if code != ExitAI || calls.Load() != 1 || !strings.Contains(out, "Selected configuration:") || !strings.Contains(out, "HTTP 503") {
		t.Fatalf("availability check failed: %d %s", code, out)
	}
	remoteEmpty(t, root, remote)
}

func TestCheckRepairUsesConfiguredHTTPProviderAndNeverPushes(t *testing.T) {
	root, remote := fixture(t, "first", "pass")
	p := proposal("func Value() int { return 0 }", "func Value() int { return 1 }", 3)
	p.Patch = ""
	p.Edits = []model.TextEdit{{File: "value.go", OldText: "func Value() int { return 0 }", NewText: "func Value() int { return 1 }"}}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []any{map[string]string{"name": "test-only-http-provider:latest"}}})
			return
		}
		requests.Add(1)
		if r.URL.Path != "/api/chat" {
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
		}
		data, _ := os.ReadFile(filepath.Join(root, "value.go"))
		if strings.Contains(string(data), "return 1") {
			t.Error("provider observed unapproved filesystem edits")
		}
		var request struct {
			Messages []struct{ Role, Content string }
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		var response any = model.Analysis{Summary: "Actual check failed", RootCause: "Incorrect implementation"}
		if len(request.Messages) > 1 && strings.Contains(request.Messages[1].Content, "Return only JSON") {
			response = p
		}
		content, _ := json.Marshal(response)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": string(content)}})
	}))
	defer server.Close()
	cfg, _, _ := config.Load(root)
	cfg.AI.Provider, cfg.AI.Endpoint, cfg.AI.Model = "ollama", server.URL, "test-only-http-provider"
	data, _ := json.Marshal(cfg)
	testutil.Write(t, root, ".pushguard.json", string(data))
	testutil.Commit(t, root)
	var out bytes.Buffer
	a := New(ui.New(strings.NewReader("allow\nallow\nallow\nallow\n"), &out, &out, false), false)
	a.Workdir = root
	if code := a.RunCheck(context.Background(), false); code != 0 {
		t.Fatalf("check failed %d\n%s", code, out.String())
	}
	if requests.Load() != 1 || a.Report.PushResult != nil || len(a.Report.History) != 5 || a.Report.Fingerprint == nil {
		t.Fatalf("incomplete HTTP/check workflow: requests=%d history=%d", requests.Load(), len(a.Report.History))
	}
	m := a.Report.RepairMetrics
	if m.AIRequests != 1 || m.Attempts != 1 || m.Applied != 1 || m.Rejected != 0 || len(m.Checks) != 2 || m.Checks[0].Initial != 1 || m.Checks[0].Current != 0 || len(m.FilesModified) != 1 || a.Report.RepairAudit[0].Progress.Resolved != 1 || a.Report.RepairAudit[0].ContextEstimate == 0 {
		t.Fatalf("metrics differ from actual execution: %+v", m)
	}
	for _, s := range a.Report.StateHistory {
		if strings.Contains(s, "PUSH_AUTHORIZATION") || s == "PUSHING" {
			t.Fatal("check entered push state")
		}
	}
	remoteEmpty(t, root, remote)
}

func TestFailedReverificationAsksAgainWithoutAnotherProviderCall(t *testing.T) {
	root, remote := fixture(t, "fail", "pass")
	p := &countedProvider{p: proposal("func Value() int { return 0 }", "func Value() int { return 1 }", 3)}
	a, code, out := execute(t, root, "allow\nallow\nallow\ndeny\n", p, false)
	if code != ExitCancelled || p.calls != 1 || len(a.Report.History) != 2 || strings.Count(out, "investigate these failures") != 2 || !strings.Contains(out, "Verification still FAILED") {
		t.Fatalf("failed repair bypassed approval: %d\n%s", code, out)
	}
	remoteEmpty(t, root, remote)
}

func TestFullVerificationFailureStopsBeforePreflightAndPush(t *testing.T) {
	root, remote := fixture(t, "full-fail", "pass")
	t.Setenv("PUSHGUARD_TEST_COUNTER", filepath.Join(t.TempDir(), "counter"))
	p := &countedProvider{}
	a, code, out := execute(t, root, "allow\ndeny\n", p, false)
	if code != ExitCancelled || len(a.Report.History) != 3 || p.calls != 0 || a.Report.Preflight != nil || !strings.Contains(out, "FULL VERIFICATION") {
		t.Fatalf("full failure bypassed gate: %d\n%s", code, out)
	}
	remoteEmpty(t, root, remote)
}

func TestEnvironmentBypassesAIAndSecurityProtectsTests(t *testing.T) {
	for _, mode := range []string{"environment", "security"} {
		t.Run(mode, func(t *testing.T) {
			root, remote := fixture(t, mode)
			if mode == "security" {
				testutil.Write(t, root, "tests/auth.test.ts", "import { isTokenValid } from '../src/auth.ts';\nexpect(isTokenValid()).toBe(false);\n")
				testutil.Write(t, root, "src/auth.ts", "export function isTokenValid() { return true; }\n")
				testutil.Commit(t, root)
			}
			p := &countedProvider{p: model.RepairProposal{Files: []string{"tests/auth.test.ts"}, Patch: "--- a/tests/auth.test.ts\n+++ b/tests/auth.test.ts\n@@ -1 +1 @@\n-expect(false)\n+expect(true)\n"}}
			a, code, out := execute(t, root, "allow\nallow\ndeny\n", p, false)
			want, calls := ExitVerification, 0
			if mode == "security" {
				want, calls = ExitPatch, 1
			}
			if code != want || p.calls != calls || a.Report.PushResult != nil || a.Report.RepairCycles != 0 {
				t.Fatalf("classification boundary: %d\n%s", code, out)
			}
			remoteEmpty(t, root, remote)
		})
	}
}

func TestRepairLimitRollbackPreservesDeveloperWork(t *testing.T) {
	root, remote := fixture(t, "fail")
	cfg, _, _ := config.Load(root)
	cfg.Repair.MaxAttempts = 1
	data, _ := json.Marshal(cfg)
	testutil.Write(t, root, ".pushguard.json", string(data))
	testutil.Commit(t, root)
	testutil.Write(t, root, "staged.txt", "developer staged work")
	testutil.Git(t, root, "add", "staged.txt")
	testutil.Write(t, root, "untracked.txt", "developer untracked work")
	p := &countedProvider{p: proposal("func Value() int { return 0 }", "func Value() int { return 1 }", 3)}
	before := testutil.Git(t, root, "status", "--porcelain")
	_, code, out := execute(t, root, "allow\nallow\nallow\nr\n", p, false)
	if code != ExitRepairLimit || testutil.Git(t, root, "status", "--porcelain") != before {
		t.Fatalf("rollback damaged work: %d\n%s", code, out)
	}
	source, _ := os.ReadFile(filepath.Join(root, "value.go"))
	if strings.Contains(string(source), "return 1") {
		t.Fatal("repair not rolled back")
	}
	remoteEmpty(t, root, remote)
}

func TestOscillatingRepairStopsBeforeThirdModelCall(t *testing.T) {
	root, remote := fixture(t, "fail")
	p := &sequenceProvider{proposals: []model.RepairProposal{
		proposal("func Value() int { return 0 }", "func Value() int { return 1 }", 3),
		proposal("func Value() int { return 1 }", "func Value() int { return 0 }", 3),
	}}
	_, code, out := execute(t, root, "allow\nallow\nallow\nallow\nallow\nallow\n", p, false)
	if code != ExitRepairLimit || p.next != 2 || !strings.Contains(out, "oscillating repair detected") {
		t.Fatalf("oscillation: %d\n%s", code, out)
	}
	remoteEmpty(t, root, remote)
}

func TestHumanCommitResumesSameSessionAndPushesExactVerifiedCommit(t *testing.T) {
	root, remote := fixture(t, "first")
	p := &countedProvider{p: proposal("func Value() int { return 0 }", "func Value() int { return 1 }", 3)}
	w := &triggerWriter{match: "Commit the reviewed repairs here", action: func() { testutil.Commit(t, root) }}
	a := New(ui.New(strings.NewReader("allow\nallow\nallow\ncontinue\ncontinue\ncontinue\nallow\n"), w, w, false), false)
	a.Workdir, a.Provider = root, p
	if code := a.RunPush(context.Background(), false); code != 0 {
		t.Fatalf("same session commit: %d\n%s", code, w.String())
	}
	if len(a.Report.History) != 5 || a.Report.PushResult == nil || a.Report.PushResult.ExitCode != 0 || p.calls != 1 {
		t.Fatal("verification or push evidence incomplete")
	}
	tip := strings.Fields(testutil.Git(t, root, "ls-remote", "--heads", remote, "main"))
	if len(tip) != 2 || tip[0] != a.Verified.HEAD {
		t.Fatal("wrong remote commit")
	}
	pushes := 0
	for _, e := range a.Report.ApprovalEvents {
		if e.Scope == "push" {
			pushes++
		}
	}
	if pushes != 1 {
		t.Fatal(fmt.Sprintf("push approval count: %d", pushes))
	}
}

func TestSingleCommandCanCommitApprovedRepairsAndPush(t *testing.T) {
	for _, allowCommit := range []bool{false, true} {
		t.Run(fmt.Sprintf("commit=%t", allowCommit), func(t *testing.T) {
			root, remote := fixture(t, "first")
			beforeHEAD := testutil.Git(t, root, "rev-parse", "HEAD")
			p := &countedProvider{p: proposal("func Value() int { return 0 }", "func Value() int { return 1 }", 3)}
			input := "allow\nallow\nallow\ncontinue\nc\ndeny\n"
			want := ExitCancelled
			if allowCommit {
				input = "allow\nallow\nallow\ncontinue\nc\nallow\ncontinue\nallow\n"
				want = ExitOK
			}
			a, code, out := execute(t, root, input, p, false)
			if code != want {
				t.Fatalf("single command: %d\n%s", code, out)
			}
			if !allowCommit {
				if testutil.Git(t, root, "rev-parse", "HEAD") != beforeHEAD || a.Report.CommitResult != nil {
					t.Fatal("denied commit executed")
				}
				remoteEmpty(t, root, remote)
			} else {
				if a.Report.CommitResult == nil || a.Report.CommitResult.ExitCode != 0 || a.Report.PushResult == nil || len(a.Report.History) != 5 {
					t.Fatalf("missing commit/verification/push evidence: %s", out)
				}
				if testutil.Git(t, root, "status", "--porcelain") != "" {
					t.Fatal("commit did not include verified repairs")
				}
			}
		})
	}
}

func TestLintSourceLiteralRepairApprovalAndReverification(t *testing.T) {
	for _, mode := range []string{"lint-json", "lint-json-stderr"} {
		for _, tc := range []struct {
			name, input string
			calls, code int
			applied     bool
		}{
			{"deny investigation", "allow\ndeny\n", 0, ExitCancelled, false},
			{"deny application", "allow\nallow\ndeny\n", 1, ExitCancelled, false},
			{"apply and reverify", "allow\nallow\nallow\nallow\n", 1, 0, true},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				root, remote := fixture(t, mode, "pass")
				p := &countedProvider{p: proposal("func Value() int { return 0 }", "func Value() int { return 1 }", 3)}
				var out bytes.Buffer
				a := New(ui.New(strings.NewReader(tc.input), &out, &out, false), false)
				a.Workdir, a.Provider = root, p
				code := a.RunCheck(context.Background(), false)
				if code != tc.code || p.calls != tc.calls || p.proposals != tc.calls || !strings.Contains(out.String(), "Failure classification: CODE") {
					t.Fatalf("repair flow: code=%d calls=%d/%d\n%s", code, p.calls, p.proposals, out.String())
				}
				data, err := os.ReadFile(filepath.Join(root, "value.go"))
				if err != nil || strings.Contains(string(data), "return 1") != tc.applied {
					t.Fatalf("unexpected source change: %s, %v", data, err)
				}
				if tc.applied && (len(a.Report.History) != 5 || a.Report.Fingerprint == nil) {
					t.Fatalf("repair was not fully reverified: history=%d", len(a.Report.History))
				}
				remoteEmpty(t, root, remote)
			})
		}
	}
}
