package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pushguard/pushguard/internal/config"
	"github.com/pushguard/pushguard/internal/llm"
	"github.com/pushguard/pushguard/internal/model"
	"github.com/pushguard/pushguard/internal/receipt"
	"github.com/pushguard/pushguard/internal/testutil"
	"github.com/pushguard/pushguard/internal/ui"
)

func TestToolHelper(t *testing.T) {
	index := -1
	for i, arg := range os.Args {
		if arg == "--" {
			index = i
			break
		}
	}
	if index < 0 {
		return
	}
	args := os.Args[index+1:]
	if len(args) < 2 {
		os.Exit(2)
	}
	root, mode := args[0], args[1]
	data, _ := os.ReadFile(filepath.Join(root, "value.go"))
	text := string(data)
	if mode == "environment" {
		fmt.Println("Missing required environment variable: DATABASE_ENCRYPTION_KEY")
		os.Exit(1)
	}
	if mode == "security" {
		fmt.Println("tests/auth.test.ts:20:11: expired token was accepted")
		os.Exit(1)
	}
	if mode == "annotation" && !strings.Contains(text, "return 1") {
		fmt.Println("::error file=value.go,line=3,col=26::no-var:\nUnexpected implementation value.")
		os.Exit(1)
	}
	if strings.HasPrefix(mode, "lint-json") && !strings.Contains(text, "return 1") {
		output := os.Stdout
		if mode == "lint-json-stderr" {
			output = os.Stderr
		}
		fmt.Fprintln(output, `[{"filePath":"value.go","messages":[{"line":3,"column":26,"severity":2,"ruleId":"no-console","message":"Unexpected console statement."}],"source":"Missing required environment variable: DATABASE_ENCRYPTION_KEY"}]`)
		os.Exit(1)
	}
	if mode == "full-fail" {
		counter := os.Getenv("PUSHGUARD_TEST_COUNTER")
		previous, _ := os.ReadFile(counter)
		_ = os.WriteFile(counter, append(previous, 'x'), 0600)
		if len(previous) > 0 {
			fmt.Println("value.go:3:26: second pipeline invocation failed")
			os.Exit(1)
		}
	}
	bad := mode == "fail" || mode == "first" && !strings.Contains(text, "return 1") || mode == "second" && strings.Contains(text, "return 1") && !strings.Contains(text, "return 2")
	if mode == "mutate" {
		_ = os.WriteFile(filepath.Join(root, "changed.txt"), []byte("changed during verification"), 0600)
	}
	if bad {
		fmt.Println("value.go:3:26: check failed: expected a verified result")
		os.Exit(1)
	}
	os.Exit(0)
}
func fixture(t *testing.T, modes ...string) (string, string) {
	t.Helper()
	root, remote := testutil.Repository(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Repair.MaxAttempts = 3
	for i, mode := range modes {
		cfg.Checks = append(cfg.Checks, model.Check{Name: fmt.Sprintf("check-%d", i), Category: "test", Required: true, Args: []string{exe, "-test.run=^TestToolHelper$", "--", root, mode}})
	}
	for i, mode := range modes {
		if strings.HasPrefix(mode, "lint-json") {
			cfg.Checks[i].Category = "lint"
		}
	}
	if err = config.Save(root, cfg); err != nil {
		t.Fatal(err)
	}
	testutil.Write(t, root, "value.go", "package example\n\nfunc Value() int { return 0 }\n\nfunc Other() int { return 0 }\n")
	testutil.Commit(t, root)
	return root, remote
}
func execute(t *testing.T, root, input string, provider llm.Provider, non bool) (*App, int, string) {
	t.Helper()
	var output bytes.Buffer
	u := ui.New(strings.NewReader(input), &output, &output, non)
	a := New(u, non)
	a.Workdir = root
	a.Provider = provider
	code := a.RunPush(context.Background(), false)
	return a, code, output.String()
}
func proposal(old, new string, line int) model.RepairProposal {
	patch := fmt.Sprintf("--- a/value.go\n+++ b/value.go\n@@ -%d,2 +%d,2 @@\n \n-%s\n+%s\n", line-1, line-1, old, new)
	if line == 3 {
		patch = fmt.Sprintf("--- a/value.go\n+++ b/value.go\n@@ -2,3 +2,3 @@\n \n-%s\n+%s\n \n", old, new)
	}
	return model.RepairProposal{Summary: "Repair implementation only", RootCause: "The implementation returned an incorrect value", Files: []string{"value.go"}, Patch: patch}
}
func fake(p model.RepairProposal) llm.Provider {
	return llm.FakeProvider{Analysis: model.Analysis{Summary: "The check provides failure evidence", RootCause: "The implementation value is incorrect"}, Proposal: p}
}
func remoteEmpty(t *testing.T, root, remote string) {
	t.Helper()
	if text := testutil.Git(t, root, "ls-remote", "--heads", remote); strings.TrimSpace(text) != "" {
		t.Fatalf("unexpected remote mutation: %s", text)
	}
}

func TestPushApprovalsAndRemoteOutcome(t *testing.T) {
	for _, test := range []struct {
		name, input string
		non         bool
		code        int
		push        bool
	}{{"start denied", "n\n", false, ExitCancelled, false}, {"review denied", "y\nx\n", false, ExitCancelled, false}, {"push denied", "y\np\nn\n", false, ExitCancelled, false}, {"noninteractive ignores scripted yes", "y\np\ny\n", true, ExitCancelled, false}, {"authorized", "y\np\ny\n", false, 0, true}} {
		t.Run(test.name, func(t *testing.T) {
			root, remote := fixture(t, "pass")
			a, code, out := execute(t, root, test.input, nil, test.non)
			if code != test.code {
				t.Fatalf("exit %d want %d\n%s", code, test.code, out)
			}
			if test.push {
				remoteTip := strings.Fields(testutil.Git(t, root, "ls-remote", "--heads", remote, "main"))
				if len(remoteTip) == 0 || remoteTip[0] != a.Verified.HEAD {
					t.Fatal("remote received the wrong commit")
				}
				if a.Report.PushResult == nil || !a.Report.ReviewComplete {
					t.Fatal("push evidence incomplete")
				}
			} else {
				remoteEmpty(t, root, remote)
			}
		})
	}
}
func TestRepairPermissionsAreIndependent(t *testing.T) {
	p := proposal("func Value() int { return 0 }", "func Value() int { return 1 }", 3)
	for _, input := range []string{"allow\ndeny\n", "allow\nallow\ndeny\n"} {
		t.Run(input, func(t *testing.T) {
			root, remote := fixture(t, "first")
			before, _ := FileHash(root, "value.go")
			_, code, out := execute(t, root, input, fake(p), false)
			if code != ExitCancelled {
				t.Fatalf("exit %d\n%s", code, out)
			}
			after, _ := FileHash(root, "value.go")
			if before != after {
				t.Fatal("denied repair changed source")
			}
			remoteEmpty(t, root, remote)
		})
	}
}
func TestApprovedRepairIsReviewedBeforeCommitBlocker(t *testing.T) {
	root, remote := fixture(t, "first")
	p := proposal("func Value() int { return 0 }", "func Value() int { return 1 }", 3)
	a, code, out := execute(t, root, "allow\nallow\nallow\ncontinue\n", fake(p), false)
	if code != ExitPreflight || !a.Report.ReviewComplete || !strings.Contains(out, "Final review") {
		t.Fatalf("repair did not reach human review: %d\n%s", code, out)
	}
	if len(a.Report.History) != 3 || len(a.Report.ApprovalEvents) != 4 {
		t.Fatalf("missing verification/approval events: %+v", a.Report)
	}
	remoteEmpty(t, root, remote)
	testutil.Commit(t, root)
	b, code, out := execute(t, root, "y\np\ny\n", nil, false)
	if code != 0 {
		t.Fatalf("committed repair did not push: %d\n%s", code, out)
	}
	if len(b.Review) != 1 || b.Report.RepairCycles != 1 {
		t.Fatal("repair provenance was lost across commit")
	}
}

type sequenceProvider struct {
	proposals []model.RepairProposal
	next      int
}

func (s *sequenceProvider) Analyze(context.Context, model.ContextBundle) (*model.Analysis, error) {
	return &model.Analysis{Summary: "Tool evidence identifies the failure", RootCause: "Incorrect implementation"}, nil
}
func (s *sequenceProvider) ProposeFix(context.Context, model.ContextBundle) (*model.RepairProposal, error) {
	if s.next >= len(s.proposals) {
		return nil, fmt.Errorf("no proposal")
	}
	p := s.proposals[s.next]
	s.next++
	return &p, nil
}
func TestSecondErrorRequiresNewApprovalAndFullPipeline(t *testing.T) {
	root, remote := fixture(t, "first", "second")
	provider := &sequenceProvider{proposals: []model.RepairProposal{proposal("func Value() int { return 0 }", "func Value() int { return 1 }", 3), proposal("func Other() int { return 0 }", "func Other() int { return 2 }", 5)}}
	a, code, out := execute(t, root, "allow\nallow\nallow\nallow\nallow\ncontinue\n", provider, false)
	if code != ExitPreflight || a.Report.RepairCycles != 2 || len(a.Report.History) != 6 {
		t.Fatalf("repair sequence failed: %d cycles=%d checks=%d\n%s", code, a.Report.RepairCycles, len(a.Report.History), out)
	}
	count := 0
	for _, e := range a.Report.ApprovalEvents {
		if e.Scope == "analyze" {
			count++
		}
	}
	if count != 2 {
		t.Fatal("each failure needs independent investigation approval")
	}
	remoteEmpty(t, root, remote)
}
func TestRepairLimitAndRejectedPatch(t *testing.T) {
	t.Run("limit", func(t *testing.T) {
		root, remote := fixture(t, "fail")
		cfg, _, _ := config.Load(root)
		cfg.Repair.MaxAttempts = 1
		data, _ := json.Marshal(cfg)
		testutil.Write(t, root, ".pushguard.json", string(data))
		testutil.Commit(t, root)
		p := proposal("func Value() int { return 0 }", "func Value() int { return 1 }", 3)
		a, code, out := execute(t, root, "y\ny\ny\nk\n", fake(p), false)
		if code != ExitRepairLimit || a.Report.RepairCycles != 1 {
			t.Fatalf("limit failed: %d\n%s", code, out)
		}
		remoteEmpty(t, root, remote)
	})
	t.Run("invalid patch", func(t *testing.T) {
		root, remote := fixture(t, "first")
		_, code, out := execute(t, root, "y\ny\nn\n", fake(model.RepairProposal{Patch: "garbage"}), false)
		if code != ExitPatch {
			t.Fatalf("invalid patch accepted: %d\n%s", code, out)
		}
		remoteEmpty(t, root, remote)
	})
	t.Run("provider unavailable", func(t *testing.T) {
		root, remote := fixture(t, "first")
		_, code, out := execute(t, root, "y\ny\n", nil, false)
		if code != ExitAI {
			t.Fatalf("wrong provider failure: %d\n%s", code, out)
		}
		remoteEmpty(t, root, remote)
	})
}

type triggerWriter struct {
	bytes.Buffer
	match  string
	action func()
	fired  bool
}

func (w *triggerWriter) Write(p []byte) (int, error) {
	if !w.fired && strings.Contains(string(p), w.match) {
		w.fired = true
		w.action()
	}
	return w.Buffer.Write(p)
}
func TestStateChangeAfterReviewInvalidatesAuthorization(t *testing.T) {
	for _, mode := range []string{"worktree", "configuration", "remote"} {
		t.Run(mode, func(t *testing.T) {
			root, remote := fixture(t, "pass")
			tip := strings.TrimSpace(testutil.Git(t, root, "rev-parse", "HEAD"))
			w := &triggerWriter{match: "Final push authorization", action: func() {
				switch mode {
				case "worktree":
					testutil.Write(t, root, "human.txt", "changed")
				case "configuration":
					data, _ := os.ReadFile(filepath.Join(root, ".pushguard.json"))
					testutil.Write(t, root, ".pushguard.json", string(data)+"\n")
				case "remote":
					testutil.Git(t, root, "push", remote, tip+":refs/heads/main")
				}
			}}
			a := New(ui.New(strings.NewReader("y\np\ny\n"), w, w, false), false)
			a.Workdir = root
			code := a.RunPush(context.Background(), false)
			want := ExitStateChanged
			if mode != "remote" {
				want = ExitPreflight
			}
			if code != want || a.Report.PushResult != nil {
				t.Fatalf("state change not blocked: %d\n%s", code, w.String())
			}
			if mode != "remote" {
				if !strings.Contains(w.String(), "Re-running required verification") || len(a.Report.History) != 4 {
					t.Fatal("stale state was not reverified")
				}
				remoteEmpty(t, root, remote)
			}
		})
	}
}
func TestChangesDuringVerificationAndRemoteRejection(t *testing.T) {
	t.Run("check mutates", func(t *testing.T) {
		root, remote := fixture(t, "mutate")
		_, code, out := execute(t, root, "y\n", nil, false)
		if code != ExitStateChanged {
			t.Fatalf("changed verification accepted: %d\n%s", code, out)
		}
		remoteEmpty(t, root, remote)
	})
	t.Run("remote rejects", func(t *testing.T) {
		root, remote := fixture(t, "pass")
		hook := filepath.Join(remote, "hooks", "pre-receive")
		if err := os.WriteFile(hook, []byte("#!/bin/sh\necho rejected-by-test >&2\nexit 1\n"), 0755); err != nil {
			t.Fatal(err)
		}
		a, code, out := execute(t, root, "y\np\ny\n", nil, false)
		if code != ExitPush || a.Report.PushResult == nil {
			t.Fatalf("remote rejection mishandled: %d\n%s", code, out)
		}
		remoteEmpty(t, root, remote)
	})
}
func TestJSONIsOneDocumentAndPushNeverSucceedsWithoutApproval(t *testing.T) {
	root, remote := fixture(t, "pass")
	for _, push := range []bool{false, true} {
		var out bytes.Buffer
		a := New(ui.New(strings.NewReader("y\np\ny\n"), &out, &out, true), true)
		a.Workdir = root
		code := 0
		if push {
			code = a.RunPush(context.Background(), true)
		} else {
			code = a.RunCheck(context.Background(), true)
		}
		var report model.SessionReport
		if err := json.Unmarshal(out.Bytes(), &report); err != nil {
			t.Fatalf("invalid JSON: %v\n%s", err, out.String())
		}
		if push && code != ExitCancelled {
			t.Fatal("JSON push implied authorization")
		}
		if !push && (code != 0 || report.Fingerprint == nil || report.ConfigHash == "" || report.Version == "") {
			t.Fatalf("receipt incomplete: %d %+v", code, report)
		}
		remoteEmpty(t, root, remote)
	}
}
func TestSourceArchiveCanRunAllChecks(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PUSHGUARD_CACHE_DIR", t.TempDir())
	exe, _ := os.Executable()
	cfg := config.Default()
	cfg.Checks = []model.Check{{Name: "archive-check", Required: true, Args: []string{exe, "-test.run=^TestToolHelper$", "--", root, "pass"}}}
	if err := config.Save(root, cfg); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	a := New(ui.New(strings.NewReader(""), &out, &out, true), true)
	a.Workdir = root
	if code := a.RunCheck(context.Background(), true); code != 0 {
		t.Fatalf("archive failed: %d %s", code, out.String())
	}
	r, err := receipt.Latest(root)
	if err != nil || r.Fingerprint != nil || len(r.Warnings) == 0 {
		t.Fatal("archive claimed Git evidence")
	}
}
func TestCancellationStopsPipeline(t *testing.T) {
	root, remote := fixture(t, "pass")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	a := New(ui.New(strings.NewReader("y\np\ny\n"), &out, &out, false), false)
	a.Workdir = root
	code := a.RunPush(ctx, false)
	if code == 0 {
		t.Fatal("cancelled workflow succeeded")
	}
	remoteEmpty(t, root, remote)
}

func TestPatchApprovalIsInvalidatedByConcurrentEdit(t *testing.T) {
	root, remote := fixture(t, "first")
	p := proposal("func Value() int { return 0 }", "func Value() int { return 1 }", 3)
	w := &triggerWriter{match: "Proposed repair", action: func() { testutil.Write(t, root, "human.txt", "concurrent edit") }}
	a := New(ui.New(strings.NewReader("y\ny\ny\ny\n"), w, w, false), false)
	a.Workdir = root
	a.Provider = fake(p)
	if code := a.RunPush(context.Background(), false); code != ExitStateChanged {
		t.Fatalf("stale patch approved: %d %s", code, w.String())
	}
	data, _ := os.ReadFile(filepath.Join(root, "value.go"))
	if strings.Contains(string(data), "return 1") {
		t.Fatal("stale patch was applied")
	}
	remoteEmpty(t, root, remote)
}
func TestEvidenceStorageFailureStopsBeforePush(t *testing.T) {
	root, remote := fixture(t, "pass")
	cache := filepath.Join(t.TempDir(), "not-a-directory")
	testutil.Write(t, filepath.Dir(cache), filepath.Base(cache), "file")
	t.Setenv("PUSHGUARD_CACHE_DIR", cache)
	a, code, out := execute(t, root, "y\np\ny\n", nil, false)
	if code != ExitConfig || a.Report.PushResult != nil {
		t.Fatalf("push without receipt: %d %s", code, out)
	}
	remoteEmpty(t, root, remote)
}
func TestDeniedContinuationKeepsNewDiagnostic(t *testing.T) {
	root, remote := fixture(t, "first", "second")
	p := proposal("func Value() int { return 0 }", "func Value() int { return 1 }", 3)
	a, code, out := execute(t, root, "y\ny\ny\nn\n", fake(p), false)
	if code != ExitCancelled || a.Report.Checks[0].Status != model.StatusPass || a.Report.Checks[1].Status != model.StatusFail {
		t.Fatalf("second error evidence lost: %d %s", code, out)
	}
	remoteEmpty(t, root, remote)
}

func TestSeparatePushURLIsTheObservedAndExecutedDestination(t *testing.T) {
	root, fetchRemote := fixture(t, "pass")
	destination := filepath.Join(t.TempDir(), "destination.git")
	testutil.Git(t, root, "init", "-q", "--bare", destination)
	testutil.Git(t, root, "remote", "set-url", "--push", "origin", destination)
	a, code, out := execute(t, root, "y\np\ny\n", nil, false)
	if code != 0 {
		t.Fatalf("different push URL failed: %d %s", code, out)
	}
	remoteEmpty(t, root, fetchRemote)
	tip := strings.Fields(testutil.Git(t, root, "ls-remote", "--heads", destination, "main"))
	if len(tip) == 0 || tip[0] != a.Verified.HEAD {
		t.Fatal("push reached the wrong destination")
	}
}
func TestFinalAuthorizationSupportsReviewAndDetails(t *testing.T) {
	root, _ := fixture(t, "pass")
	_, code, out := execute(t, root, "y\np\nd\nr\np\ny\n", nil, false)
	if code != 0 || strings.Count(out, "Final review") != 2 {
		t.Fatalf("review-again failed: %d %s", code, out)
	}
}
