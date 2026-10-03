package app

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/pushguard/pushguard/internal/llm"
	"github.com/pushguard/pushguard/internal/model"
	"github.com/pushguard/pushguard/internal/testutil"
	"github.com/pushguard/pushguard/internal/ui"
)

type contextRequestProvider struct {
	countedProvider
	expanded bool
}

func (p *contextRequestProvider) Analyze(ctx context.Context, in model.ContextBundle) (*model.Analysis, error) {
	if p.calls == 0 {
		p.calls++
		return nil, &llm.ContextRequiredError{Request: model.ContextRequest{Files: []string{"helper.go"}, Symbols: []string{"ExpectedValue"}, Reason: "need the caller contract"}}
	}
	for _, file := range in.SourceFiles {
		if file.File == "helper.go" && strings.Contains(file.Content, "ExpectedValue") {
			p.expanded = true
		}
	}
	return p.countedProvider.Analyze(ctx, in)
}

func TestContextExpansionNeedsFreshInvestigationApproval(t *testing.T) {
	for _, allow := range []bool{false, true} {
		root, remote := fixture(t, "first")
		testutil.Write(t, root, "helper.go", "package example\nconst ExpectedValue = 1\n")
		testutil.Commit(t, root)
		p := &contextRequestProvider{countedProvider: countedProvider{p: proposal("func Value() int { return 0 }", "func Value() int { return 1 }", 3)}}
		input, want := "allow\nallow\ndeny\n", ExitPatch
		if allow {
			input, want = "allow\nallow\nallow\nallow\nallow\n", ExitOK
		}
		var out bytes.Buffer
		a := New(ui.New(strings.NewReader(input), &out, &out, false), false)
		a.Workdir, a.Provider = root, p
		if code := a.RunCheck(context.Background(), false); code != want {
			t.Fatalf("allow=%t exit=%d\n%s", allow, code, out.String())
		}
		if p.expanded != allow || !allow && p.calls != 1 {
			t.Fatal("retrieval bypassed fresh approval")
		}
		remoteEmpty(t, root, remote)
	}
}

func TestRepeatedInvalidProposalStopsBeforeAttemptCap(t *testing.T) {
	root, remote := fixture(t, "first")
	p := &countedProvider{p: model.RepairProposal{Summary: "repair", RootCause: "incorrect value", Edits: []model.TextEdit{{File: "value.go", OldText: "does not exist", NewText: "anything"}}}}
	a, code, out := execute(t, root, "allow\nallow\nallow\nallow\n", p, false)
	if code != ExitRepairLimit || p.calls != 2 || a.Report.RepairCycles != 0 || !strings.Contains(out, "repeated proposal") {
		t.Fatalf("exit=%d calls=%d\n%s", code, p.calls, out)
	}
	remoteEmpty(t, root, remote)
}
