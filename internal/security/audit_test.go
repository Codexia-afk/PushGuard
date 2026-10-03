package security

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pushguard/pushguard/internal/model"
)

func TestRepairAuditAndCommitLogsAreRedactedWithoutMutation(t *testing.T) {
	secret := "postgres://user:private-password@localhost/db"
	in := model.SessionReport{
		CommitResult: &model.CommandResult{Stderr: secret},
		RepairAudit: []model.RepairAudit{{
			Diagnostics:  []model.Diagnostic{{Message: secret}},
			Proposal:     &model.RepairProposal{Summary: secret, Risks: []string{secret}},
			Verification: &model.CheckResult{Command: model.CommandResult{Stdout: secret}},
		}},
	}
	data, _ := json.Marshal(SanitizeReport(in))
	if strings.Contains(string(data), "private-password") {
		t.Fatalf("audit leaked secret: %s", data)
	}
	if in.RepairAudit[0].Proposal.Risks[0] != secret || in.RepairAudit[0].Diagnostics[0].Message != secret || in.CommitResult.Stderr != secret {
		t.Fatal("sanitization mutated source records")
	}
}
