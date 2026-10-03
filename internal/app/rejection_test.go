package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pushguard/pushguard/internal/model"
	"github.com/pushguard/pushguard/internal/ui"
)

type correctingProvider struct {
	sequenceProvider
	sawRejection bool
}

func (p *correctingProvider) Analyze(ctx context.Context, input model.ContextBundle) (*model.Analysis, error) {
	if p.next > 0 {
		p.sawRejection = strings.Contains(input.Metadata["previousProposalRejected"], "eslint-disable")
	}
	return p.sequenceProvider.Analyze(ctx, input)
}

func TestRejectedSuppressionRequiresNewInvestigationAndApplyDecisions(t *testing.T) {
	for _, tc := range []struct {
		name, input             string
		code, requests, repairs int
	}{
		{"stop", "allow\nallow\ndeny\n", ExitPatch, 1, 0},
		{"EOF stops", "allow\nallow\n", ExitPatch, 1, 0},
		{"reject correction", "allow\nallow\nallow\ndeny\n", ExitCancelled, 2, 0},
		{"apply correction", "allow\nallow\nallow\nallow\nallow\n", ExitOK, 2, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, remote := fixture(t, "first")
			bad := proposal("func Value() int { return 0 }", "func Value() int { return 1 } // eslint-disable", 3)
			good := proposal("func Value() int { return 0 }", "func Value() int { return 1 }", 3)
			p := &correctingProvider{sequenceProvider: sequenceProvider{proposals: []model.RepairProposal{bad, good}}}
			var out bytes.Buffer
			a := New(ui.New(strings.NewReader(tc.input), &out, &out, false), false)
			a.Workdir, a.Provider = root, p
			code := a.RunCheck(context.Background(), false)
			if code != tc.code || p.next != tc.requests || a.Report.RepairCycles != tc.repairs {
				t.Fatalf("retry boundary: code=%d requests=%d repairs=%d\n%s", code, p.next, a.Report.RepairCycles, out.String())
			}
			data, _ := os.ReadFile(filepath.Join(root, "value.go"))
			if strings.Contains(string(data), "eslint-disable") || (strings.Contains(string(data), "return 1")) != (tc.repairs == 1) {
				t.Fatal("unapproved or suppressed source was applied")
			}
			if a.Report.RepairAudit[0].PatchDecision != "REJECTED_BY_POLICY" || a.Report.RepairAudit[0].Rejection == "" {
				t.Fatal("rejection audit missing")
			}
			if tc.requests == 2 && !p.sawRejection {
				t.Fatal("correction lacked policy feedback")
			}
			if tc.repairs == 1 && len(a.Report.History) != 3 {
				t.Fatal("real failed check/full verification not rerun")
			}
			remoteEmpty(t, root, remote)
		})
	}
}
