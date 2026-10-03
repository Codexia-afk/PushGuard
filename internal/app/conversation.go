package app

import (
	"strings"

	"github.com/pushguard/pushguard/internal/approval"
	"github.com/pushguard/pushguard/internal/diagnostics"
	"github.com/pushguard/pushguard/internal/model"
)

// These typed, read-only conversation actions cannot grant approval or invoke a
// process. Provider text never enters this dispatcher as a developer decision.
type conversationAction int

const (
	unknownAction conversationAction = iota
	showEvidence
	showLogs
	showDiff
	showExplanation
	showPending
	showDenial
	showReverification
	rollbackAction
)

func parseAction(input string) conversationAction {
	s := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(input)), "?")
	switch s {
	case "d", "details", "explain more", "what failed", "where is the error", "show the code", "show code", "code", "what failed and where":
		return showEvidence
	case "l", "logs", "show logs", "show the logs":
		return showLogs
	case "diff", "full diff", "show diff", "show the diff", "what did the ai change", "what are you going to change":
		return showDiff
	case "e", "explain", "why", "why did it fail", "why is this fix safe":
		return showExplanation
	case "what still needs to pass":
		return showPending
	case "what happens if i deny":
		return showDenial
	case "why are you running this test again":
		return showReverification
	case "u", "undo", "rollback", "roll back the fix":
		return rollbackAction
	default:
		return unknownAction
	}
}

func (a *App) answer(action conversationAction, failure *model.CheckResult, proposal *model.RepairProposal) error {
	switch action {
	case showEvidence:
		if failure != nil {
			for _, d := range failure.Diagnostics {
				a.UI.Say(diagnostics.Render(a.Root, d, 3))
			}
		}
		if proposal != nil {
			a.UI.Say(proposal.Patch)
		}
	case showLogs:
		if failure != nil {
			a.UI.Say(failure.Command.Stdout + "\n" + failure.Command.Stderr)
		}
	case showDiff:
		if proposal != nil {
			a.UI.Say(proposal.Patch)
		} else {
			a.UI.Say("No repair proposal yet.")
			for _, p := range a.Report.Repairs {
				a.UI.Say(p.Patch)
			}
		}
	case showExplanation:
		if proposal != nil {
			a.UI.Say(proposal.Summary + "\nLIKELY ROOT CAUSE (AI inference): " + proposal.RootCause + "\nRisks: " + strings.Join(proposal.Risks, "; ") + "\nThe patch is path/policy validated; only real reverification can establish success.")
		} else if failure != nil {
			a.UI.Say(diagnostics.Render(a.Root, firstDiagnostic(*failure), 3))
			a.UI.Say("This is reported tool evidence. AI investigation has not been authorized for this failure.")
		}
	case showPending:
		a.UI.Say(a.pendingChecks())
	case showDenial:
		a.UI.Say("Deny stops this session. This proposal is not applied, earlier approved local changes remain, and Git push is not executed.")
	case showReverification:
		a.UI.Say("A proposed or applied patch is not proof of success. The failed command reruns immediately, followed by the complete pipeline.")
	case rollbackAction:
		if err := a.rollbackRepairs(); err != nil {
			return err
		}
		return fail(ExitCancelled, "PushGuard repairs rolled back; verification invalidated. PUSH NOT STARTED.")
	default:
		a.UI.Say("Unknown response is not approval. Choose Allow or Deny, or ask to show evidence, code, logs, diff, or remaining checks.")
	}
	return nil
}

func (a *App) repairConsent(question string, failure model.CheckResult) error {
	for {
		choice := a.UI.Choice(question, "[F/Y] Allow Fix", "[N] Deny", "[D] Explain More", "[L] Logs")
		if choice == "f" {
			choice = "allow"
		}
		decision := approval.ParseDecision(choice)
		if decision == approval.DecisionAllow {
			grant := a.Approvals.Grant(approval.Analyze, approval.Bind(failure))
			if err := a.Approvals.Consume(grant, approval.Analyze, approval.Bind(failure)); err != nil {
				return err
			}
			a.recordApproval(grant)
			return nil
		}
		if decision == approval.DecisionDeny || choice == "" || a.NonInteractive {
			return fail(ExitCancelled, "repair denied; no provider call authorized")
		}
		if err := a.answer(parseAction(choice), &failure, nil); err != nil {
			return err
		}
	}
}

func (a *App) patchConsent(binding string, proposal model.RepairProposal) error {
	for {
		choice := a.UI.Choice("Apply this exact diff to the listed files?", "[Y] Allow", "[N] Deny", "[E] Explain", "[D] Full Diff")
		decision := approval.ParseDecision(choice)
		if decision == approval.DecisionAllow {
			grant := a.Approvals.Grant(approval.Apply, binding)
			if err := a.Approvals.Consume(grant, approval.Apply, binding); err != nil {
				return err
			}
			a.recordApproval(grant)
			return nil
		}
		if decision == approval.DecisionDeny || choice == "" || a.NonInteractive {
			return fail(ExitCancelled, "patch denied; no proposed changes applied")
		}
		if err := a.answer(parseAction(choice), nil, &proposal); err != nil {
			return err
		}
	}
}
