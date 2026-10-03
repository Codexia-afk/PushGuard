package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/pushguard/pushguard/internal/llm"
	"github.com/pushguard/pushguard/internal/model"
)

func (a *App) providerFailure(err error) error {
	var budget *llm.ContextBudgetError
	if errors.As(err, &budget) {
		return fail(ExitAI, "CONTEXT BUDGET EXCEEDED. "+err.Error()+". Select a smaller diagnostic group or adjust the configured input budget before a fresh investigation.")
	}
	if llm.IsResponseError(err) {
		return fail(ExitAI, "INVALID AI RESPONSE. "+err.Error()+". No proposal was applied; verification remains failed.")
	}
	if a.Cfg.AI.Provider == "ollama" {
		return fail(ExitAI, fmt.Sprintf("LOCAL AI REQUEST FAILED.\nProvider: Ollama\nModel: %s\nEndpoint: %s\nSelected configuration: %s\nProblem: %s\nCheck the installed Ollama service, model availability and request timeout with pushguard ai status --test. No cloud fallback or unapproved source edit occurred.", a.Cfg.AI.Model, a.Cfg.AI.Endpoint, a.CfgPath, err))
	}
	return fail(ExitAI, fmt.Sprintf("AI repair is configured but unavailable or returned an invalid response.\nProvider: %s\nEndpoint: %s\nModel: %s\nSelected configuration: %s\nProblem: %s\nCheck the selected configuration (repository settings override user defaults), then run pushguard ai status --test. See docs/ai-repair.md.\nNo proposed changes were applied in this attempt. Verification remains failed. PUSH NOT STARTED.", a.Cfg.AI.Provider, a.Cfg.AI.Endpoint, a.Cfg.AI.Model, a.CfgPath, err))
}

// recoverProvider explains a provider failure and lets the developer retry
// (a new request still needs the normal repair approval) or exit safely. The
// repository is never modified by a provider failure; there is no cloud fallback.
func (a *App) recoverProvider(cause error, audit *model.RepairAudit, originalState string) (bool, error) {
	if err := a.unappliedAttempt(audit, originalState, cause.Error(), "PROVIDER_FAILED", "AI STEP FAILED"); err != nil {
		return false, err
	}
	err := a.providerFailure(cause)
	if a.NonInteractive {
		return false, err
	}
	a.UI.Say(err.Error())
	for {
		switch a.UI.Choice("AI step failed; the repository is unchanged.", "[R] Retry", "[D] Details", "[E] Exit safely") {
		case "r", "retry":
			return true, nil
		case "d", "details":
			a.UI.Say("Deterministic verification results remain valid; only AI assistance is unavailable. Underlying error: " + cause.Error())
		default:
			return false, err
		}
	}
}

// infrastructureMenu handles failures source repair must not touch: the
// developer can fix the cause and rerun the same check, inspect it, or exit.
func (a *App) infrastructureMenu(ctx context.Context, failure model.CheckResult, message string) (bool, error) {
	if a.NonInteractive {
		return false, fail(ExitVerification, message)
	}
	a.UI.Say(message)
	for {
		switch a.UI.Choice("Fix the cause outside PushGuard, then retry.", "[R] Retry this check", "[D] Details", "[E] Exit safely") {
		case "r", "retry":
			if _, err := a.runOne(ctx, failure.Check); err != nil {
				return false, err
			}
			return true, nil
		case "d", "details":
			a.UI.Say(failure.Command.Stdout + "\n" + failure.Command.Stderr)
		default:
			return false, fail(ExitVerification, message)
		}
	}
}
