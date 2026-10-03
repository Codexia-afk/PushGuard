package app

import (
	"github.com/pushguard/pushguard/internal/model"
	"github.com/pushguard/pushguard/internal/security"
	"github.com/pushguard/pushguard/internal/state"
)

// Rejected proposals never reach an apply gate. Returning to diagnosis requires
// a new explicit investigation decision before any further provider request.
func (a *App) rejectProposal(audit *model.RepairAudit, originalState, reason string) error {
	a.Report.RepairMetrics.Rejected++
	return a.unappliedAttempt(audit, originalState, reason, "REJECTED_BY_POLICY", "PROPOSAL REJECTED")
}

func (a *App) unappliedAttempt(audit *model.RepairAudit, originalState, reason, decision, title string) error {
	audit.PatchDecision = decision
	audit.Rejection = security.Redact(reason)
	delete(a.seenStates, originalState) // No patch was applied: this is not oscillation.
	a.UI.Say(title + ": " + reason)
	a.UI.Say("No proposal was applied in this attempt. Another request requires fresh permission. PUSH NOT STARTED.")
	if err := a.move(state.FailureDetected); err != nil {
		return err
	}
	return a.saveEvidence()
}
