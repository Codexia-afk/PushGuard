package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pushguard/pushguard/internal/llm"
	"github.com/pushguard/pushguard/internal/model"
	"github.com/pushguard/pushguard/internal/testutil"
	"github.com/pushguard/pushguard/internal/ui"
)

func TestRepairDiagnosticsBoundsLargeLintRequests(t *testing.T) {
	var group []model.Diagnostic
	for i := 0; i < 71; i++ {
		file := "src/a.ts"
		if i%2 == 1 {
			file = "src/b.ts"
		}
		group = append(group, model.Diagnostic{Location: model.SourceLocation{File: file, Line: i + 1}})
	}
	f := model.CheckResult{Check: model.Check{Category: "lint"}, Diagnostics: group}
	selected := repairDiagnostics(f, 2048)
	if len(selected) != 12 {
		t.Fatalf("batch size %d", len(selected))
	}
	for _, d := range selected {
		if d.Location.File != "src/a.ts" {
			t.Fatal("mixed target files")
		}
	}
	if len(f.Diagnostics) != 71 {
		t.Fatal("original evidence lost")
	}
	if len(repairDiagnostics(f, 256)) != 1 {
		t.Fatal("small budget not respected")
	}
	f.Check.Category = "test"
	if len(repairDiagnostics(f, 2048)) != 71 {
		t.Fatal("test evidence split")
	}
}

type rateLimitedProvider struct {
	countedProvider
	calls int
}

func (p *rateLimitedProvider) InvestigateAndPropose(_ context.Context, input model.ContextBundle) (*model.Analysis, *model.RepairProposal, error) {
	p.calls++
	if p.calls == 1 {
		return nil, nil, &llm.RateLimitError{Problem: "HTTP 429", RetryAfter: time.Millisecond}
	}
	return &model.Analysis{Summary: "Fix result", RootCause: "Incorrect result"}, &p.p, nil
}

func TestRateLimitRecoveryRequiresFreshApproval(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		calls, code int
	}{
		{"deny", "allow\nallow\ndeny\n", 1, ExitPatch},
		{"EOF", "allow\nallow\n", 1, ExitPatch},
		{"approve", "allow\nallow\nallow\nallow\nallow\n", 2, ExitOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, remote := fixture(t, "first")
			p := &rateLimitedProvider{countedProvider: countedProvider{p: proposal("func Value() int { return 0 }", "func Value() int { return 1 }", 3)}}
			var out bytes.Buffer
			a := New(ui.New(strings.NewReader(tc.input), &out, &out, false), false)
			a.Workdir, a.Provider = root, p
			code := a.RunCheck(context.Background(), false)
			if code != tc.code || p.calls != tc.calls {
				t.Fatalf("code=%d calls=%d\n%s", code, p.calls, out.String())
			}
			if a.Report.RepairAudit[0].PatchDecision != "PROVIDER_RATE_LIMITED" {
				t.Fatal("rate limit was not recorded")
			}
			remoteEmpty(t, root, remote)
		})
	}
}

func TestRateLimitWaitCanBeCancelled(t *testing.T) {
	var out bytes.Buffer
	a := New(ui.New(strings.NewReader(""), &out, &out, false), false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := a.waitForProvider(ctx, time.Now().Add(time.Hour)); err == nil || time.Since(start) > time.Second {
		t.Fatal("wait did not cancel")
	}
}

func TestRepairBatchSkipsProtectedLintTargets(t *testing.T) {
	root := t.TempDir()
	testutil.Write(t, root, "src/value.ts", "var value = 1;\n")
	failure := model.CheckResult{Check: model.Check{Category: "lint"}, Diagnostics: []model.Diagnostic{
		{Location: model.SourceLocation{File: "src/security/secret.ts", Line: 1}},
		{Location: model.SourceLocation{File: "src/value.ts", Line: 1}},
	}}
	selected := repairableDiagnostics(root, failure, 2048)
	if len(selected) != 1 || selected[0].Location.File != "src/value.ts" || len(failure.Diagnostics) != 2 {
		t.Fatal("editable work was hidden by a protected file")
	}
}

type feedbackProvider struct {
	sequenceProvider
	received bool
}

func (p *feedbackProvider) Analyze(ctx context.Context, bundle model.ContextBundle) (*model.Analysis, error) {
	if p.next > 0 {
		p.received = strings.Contains(bundle.Metadata["previousVerification"], "Actual check-0 verification still failed") && strings.Contains(bundle.Metadata["previousVerification"], "return 2")
	}
	return p.sequenceProvider.Analyze(ctx, bundle)
}

func TestFailedRepairFeedsActualVerificationIntoNextRequest(t *testing.T) {
	root, _ := fixture(t, "first")
	p := &feedbackProvider{sequenceProvider: sequenceProvider{proposals: []model.RepairProposal{
		proposal("func Value() int { return 0 }", "func Value() int { return 2 }", 3),
		proposal("func Value() int { return 2 }", "func Value() int { return 1 }", 3),
	}}}
	var out bytes.Buffer
	a := New(ui.New(strings.NewReader("allow\nallow\nallow\nallow\nallow\nallow\n"), &out, &out, false), false)
	a.Workdir, a.Provider = root, p
	if code := a.RunCheck(context.Background(), false); code != ExitOK || !p.received {
		t.Fatalf("feedback=%t code=%d\n%s", p.received, code, out.String())
	}
}

func TestProposalTargetsAreBoundToSuppliedEditableFiles(t *testing.T) {
	if err := validateProposalTargets([]string{"src/unseen.ts"}, []string{"src/value.ts"}); err == nil {
		t.Fatal("unseen source was accepted")
	}
	if err := validateProposalTargets([]string{"src/value.ts"}, []string{"src/value.ts"}); err != nil {
		t.Fatal(err)
	}
}

type invalidThenCorrectProvider struct {
	calls    int
	feedback bool
	countedProvider
}

func (p *invalidThenCorrectProvider) InvestigateAndPropose(_ context.Context, input model.ContextBundle) (*model.Analysis, *model.RepairProposal, error) {
	p.calls++
	if p.calls == 1 {
		return nil, nil, &llm.ResponseError{Problem: "AI reached the output-token limit"}
	}
	p.feedback = strings.Contains(input.Metadata["previousProposalRejected"], "output-token limit")
	return &model.Analysis{Summary: "Fix source", RootCause: "Incorrect result"}, &p.p, nil
}

func TestInvalidAIResponseRequiresFreshApprovalAndCanRecover(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		calls, code int
		applied     bool
	}{
		{"deny retry", "allow\nallow\ndeny\n", 1, ExitPatch, false},
		{"EOF", "allow\nallow\n", 1, ExitPatch, false},
		{"deny apply", "allow\nallow\nallow\ndeny\n", 2, ExitCancelled, false},
		{"recover", "allow\nallow\nallow\nallow\nallow\n", 2, ExitOK, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, remote := fixture(t, "first")
			p := &invalidThenCorrectProvider{countedProvider: countedProvider{p: proposal("func Value() int { return 0 }", "func Value() int { return 1 }", 3)}}
			var out bytes.Buffer
			a := New(ui.New(strings.NewReader(tc.input), &out, &out, false), false)
			a.Workdir, a.Provider = root, p
			code := a.RunCheck(context.Background(), false)
			if code != tc.code || p.calls != tc.calls {
				t.Fatalf("code=%d calls=%d\n%s", code, p.calls, out.String())
			}
			if tc.calls == 2 && !p.feedback {
				t.Fatal("missing correction feedback")
			}
			data, _ := os.ReadFile(filepath.Join(root, "value.go"))
			if strings.Contains(string(data), "return 1") != tc.applied {
				t.Fatal("application approval bypass")
			}
			if a.Report.RepairAudit[0].Rejection == "" {
				t.Fatal("missing failure audit")
			}
			if tc.applied && len(a.Report.History) != 3 {
				t.Fatal("missing real verification")
			}
			remoteEmpty(t, root, remote)
		})
	}
}

func TestProviderProgressStopsBeforeReturning(t *testing.T) {
	var out bytes.Buffer
	a := New(ui.New(strings.NewReader(""), &out, &out, false), false)
	sentinel := &llm.ResponseError{Problem: "incomplete response"}
	err := a.providerProgress(time.Millisecond, func() error { time.Sleep(15 * time.Millisecond); return sentinel })
	if err != sentinel || !strings.Contains(out.String(), "AI is preparing") {
		t.Fatalf("missing progress or lost error: %v %s", err, out.String())
	}
	// Reporter must be joined: any write after return is a race with this read.
	saved := out.String()
	time.Sleep(5 * time.Millisecond)
	if out.String() != saved {
		t.Fatal("progress continued after request")
	}
}
