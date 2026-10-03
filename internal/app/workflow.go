package app

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pushguard/pushguard/internal/approval"
	"github.com/pushguard/pushguard/internal/config"
	"github.com/pushguard/pushguard/internal/model"
	"github.com/pushguard/pushguard/internal/patch"
	"github.com/pushguard/pushguard/internal/repolock"
	"github.com/pushguard/pushguard/internal/runner"
	"github.com/pushguard/pushguard/internal/state"
	"github.com/pushguard/pushguard/internal/ui"
	"github.com/pushguard/pushguard/internal/verify"
)

func (a *App) lockRepository() (func(), error) {
	path := filepath.Join(a.Repo.GitDir, "pushguard.lock")
	if a.Repo.GitDir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return nil, fail(ExitPreflight, err.Error())
		}
		if override := os.Getenv("PUSHGUARD_CACHE_DIR"); override != "" {
			base = override
		}
		dir := filepath.Join(base, "pushguard", "locks")
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, fail(ExitPreflight, err.Error())
		}
		path = filepath.Join(dir, fmt.Sprintf("%x.lock", sha256.Sum256([]byte(a.Root))))
	}
	unlock, err := repolock.AcquireFor(path, a.Report.SessionID, a.Report.Operation)
	if err != nil {
		return nil, fail(ExitPreflight, err.Error())
	}
	return unlock, nil
}

func (a *App) reviewCheck(ctx context.Context) error {
	before, err := a.repairState(ctx)
	if err != nil {
		return err
	}
	if err := a.consent(approval.Review, before.Value, "Acknowledge the verified local repairs?"); err != nil {
		return err
	}
	after, err := a.repairState(ctx)
	if err != nil {
		return err
	}
	if before.Value != after.Value {
		return fail(ExitStateChanged, "source changed during repair review; verify again")
	}
	a.Report.ReviewComplete = true
	return nil
}

func (a *App) beginVerification(ctx context.Context) error {
	if a.NonInteractive {
		return a.verifyFull(ctx)
	}
	if err := a.move(state.WaitingForChecks); err != nil {
		return err
	}
	if err := a.startConsent(); err != nil {
		return err
	}
	return a.controlledVerification(ctx)
}

func (a *App) refreshVerification(ctx context.Context) error {
	cfg, _, err := config.Load(a.Root)
	if err != nil || config.Hash(cfg) != a.Report.ConfigHash {
		return fail(ExitStateChanged, "verification policy changed; start a new session to approve the new command plan")
	}
	repo, err := a.RepoService.Discover(ctx, a.Root)
	if err != nil {
		return fail(ExitPreflight, err.Error())
	}
	a.Repo, a.Report.Repository = repo, repo
	a.Report.ReviewComplete = false
	a.Report.Preflight, a.Report.Fingerprint = nil, nil
	a.Verified = model.StateFingerprint{}
	if err := a.move(state.StateCheck); err != nil {
		return err
	}
	return a.controlledVerification(ctx)
}

// controlledVerification stops at the first required failure. A repair verifies
// that same check immediately, then resumes the remaining checks. Only a clean,
// complete second pass can certify the repository.
func (a *App) controlledVerification(ctx context.Context) error {
	if err := a.move(state.Verifying); err != nil {
		return err
	}
	if len(a.Report.History) == 0 && a.Planned.Value != "" {
		fp, err := a.fingerprint(ctx)
		if err != nil {
			return err
		}
		if fp.Value != a.Planned.Value {
			return fail(ExitStateChanged, "repository changed after discovery; command plan must be reviewed again")
		}
	}
	a.Results = nil
	full := false
	for {
		if full {
			a.UI.Title("FULL VERIFICATION")
			a.UI.Say("Running complete verification from the beginning to ensure repairs did not break another check.")
		}
		cycles := a.Report.RepairCycles
		var before model.StateFingerprint
		if full && a.Repo.GitDir != "" && a.Repo.HEAD != "" {
			var err error
			before, err = a.fingerprint(ctx)
			if err != nil {
				return err
			}
		}
		for i, check := range a.Checks {
			next := state.Verifying
			if full {
				next = state.FullVerifying
			}
			if err := a.move(next); err != nil {
				return err
			}
			a.UI.Say(fmt.Sprintf("[%d/%d] Running %s...", i+1, len(a.Checks), runner.CommandString(check.Args)))
			result, err := a.runOne(ctx, check)
			if err != nil {
				return err
			}
			if result.Blocking() {
				if err := a.repairLoop(ctx); err != nil {
					return err
				}
			}
		}
		if !full {
			a.UI.Say("All required individual checks passed.")
			full = true
			continue
		}
		if cycles != a.Report.RepairCycles {
			a.UI.Say("A repair changed the full-verification state. Restarting the complete pipeline.")
			continue
		}
		if err := a.move(state.FullVerifying); err != nil {
			return err
		}
		if before.Value != "" {
			after, err := a.fingerprint(ctx)
			if err != nil {
				return err
			}
			if after.Value != before.Value {
				return fail(ExitStateChanged, "repository changed during full verification")
			}
			a.Verified, a.Report.Fingerprint = after, &after
		}
		for i := range a.Review {
			a.Review[i].Checks = verifiedNames(a.Results)
		}
		a.Report.Review = a.Review
		return a.saveEvidence()
	}
}

func verifiedNames(results []model.CheckResult) []string {
	var names []string
	for _, r := range results {
		if r.Status == model.StatusPass {
			names = append(names, r.Check.Name)
		}
	}
	return names
}

func (a *App) runOne(ctx context.Context, check model.Check) (model.CheckResult, error) {
	var before model.StateFingerprint
	cfg, _, err := config.Load(a.Root)
	if err != nil || config.Hash(cfg) != a.Report.ConfigHash {
		return model.CheckResult{}, fail(ExitStateChanged, "configuration changed; command plan approval is stale")
	}
	if a.Repo.GitDir != "" && a.Repo.HEAD != "" {
		before, err = a.fingerprint(ctx)
		if err != nil {
			return model.CheckResult{}, err
		}
	}
	result := a.Verifier.RunCheck(ctx, a.Root, check)
	remaining := 16 << 20
	for _, previous := range a.Results {
		if previous.Check.Name != check.Name {
			remaining -= len(previous.Command.Stdout) + len(previous.Command.Stderr)
		}
	}
	if remaining < 0 {
		remaining = 0
	}
	if len(result.Command.Stdout)+len(result.Command.Stderr) > remaining {
		result.Status, result.Command.Truncated = model.StatusFail, true
		if len(result.Command.Stdout) > remaining {
			result.Command.Stdout, result.Command.Stderr = result.Command.Stdout[:remaining], ""
		} else {
			result.Command.Stderr = result.Command.Stderr[:remaining-len(result.Command.Stdout)]
		}
		result.Diagnostics = []model.Diagnostic{{Tool: check.Name, Category: "resource", Classification: "RESOURCE", Severity: "error", Message: "pipeline log budget of 16 MiB exceeded; evidence incomplete"}}
	}
	a.recordCheckMetrics(result)
	a.Results = replaceResult(a.Results, result)
	a.Report.Checks, a.Report.Diagnostics = a.Results, allDiagnostics(a.Results)
	a.appendHistory(result)
	a.UI.Status(string(result.Status), check.Name, ui.Duration(result.Command.Duration))
	if result.Blocking() {
		a.UI.Say("PUSH NOT STARTED")
	}
	if err := a.saveEvidence(); err != nil {
		return result, err
	}
	if ctx.Err() != nil {
		return result, fail(ExitCancelled, "verification interrupted")
	}
	if before.Value != "" {
		after, err := a.fingerprint(ctx)
		if err != nil {
			return result, err
		}
		if after.Value != before.Value {
			return result, fail(ExitStateChanged, "repository changed while checks were running; verification is stale")
		}
	}
	return result, nil
}

func (a *App) repairLimit() error {
	remaining := 0
	for _, r := range a.Results {
		remaining += len(r.Diagnostics)
	}
	message := fmt.Sprintf("Repair limit reached: %d/%d. Remaining captured diagnostics: %d. Verification still fails. PUSH NOT STARTED.", a.attempts, a.Cfg.Repair.MaxAttempts, remaining)
	a.UI.Say(message)
	for !a.NonInteractive {
		switch a.UI.Choice("Keep or roll back PushGuard repairs?", "[R] Roll back", "[K] Keep", "[D] Details", "[Q] Quit") {
		case "r", "rollback", "roll back":
			if err := a.rollbackRepairs(); err != nil {
				return err
			}
			return fail(ExitRepairLimit, "PushGuard repairs rolled back; verification remains failed")
		case "d", "details":
			a.explainFailures()
		default:
			return fail(ExitRepairLimit, message)
		}
	}
	return fail(ExitRepairLimit, message)
}

func (a *App) rollbackRepairs() error {
	store := patch.SnapshotStore{}
	for i := len(a.Snapshots) - 1; i >= 0; i-- {
		if err := store.Restore(a.Root, a.Snapshots[i]); err != nil {
			return fail(ExitPatch, err.Error())
		}
	}
	if len(a.Snapshots) > 0 {
		_ = store.ClearLatestFor(a.Root)
	}
	a.Verified = model.StateFingerprint{}
	a.Report.Fingerprint, a.Report.Repairs, a.Report.Review = nil, nil, nil
	a.Review = nil
	return nil
}

func (a *App) allRequiredPassed() bool {
	if len(a.Results) != len(a.Checks) || len(a.Checks) == 0 {
		return false
	}
	for _, c := range a.Checks {
		found := false
		for _, r := range a.Results {
			if r.Check.Name == c.Name && (!c.Required || r.Status == model.StatusPass) {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return len(verify.Blocking(a.Results)) == 0
}

func (a *App) pendingChecks() string {
	var names []string
	for _, c := range a.Checks {
		passed := false
		for _, r := range a.Results {
			if r.Check.Name == c.Name && r.Status == model.StatusPass {
				passed = true
			}
		}
		if c.Required && !passed {
			names = append(names, c.Name)
		}
	}
	return strings.Join(names, ", ") + "; then a complete clean pipeline and (for push) preflight, review and separate push authorization"
}
