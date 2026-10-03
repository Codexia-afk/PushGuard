package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pushguard/pushguard/internal/approval"
	"github.com/pushguard/pushguard/internal/codehost"
	"github.com/pushguard/pushguard/internal/config"
	internalcontext "github.com/pushguard/pushguard/internal/context"
	"github.com/pushguard/pushguard/internal/detector"
	"github.com/pushguard/pushguard/internal/diagnostics"
	"github.com/pushguard/pushguard/internal/integrity"
	"github.com/pushguard/pushguard/internal/llm"
	"github.com/pushguard/pushguard/internal/model"
	"github.com/pushguard/pushguard/internal/patch"
	"github.com/pushguard/pushguard/internal/preflight"
	"github.com/pushguard/pushguard/internal/receipt"
	"github.com/pushguard/pushguard/internal/repository"
	"github.com/pushguard/pushguard/internal/runner"
	"github.com/pushguard/pushguard/internal/security"
	"github.com/pushguard/pushguard/internal/state"
	"github.com/pushguard/pushguard/internal/ui"
	"github.com/pushguard/pushguard/internal/verify"
)

const (
	ExitOK           = 0
	ExitVerification = 1
	ExitConfig       = 2
	ExitCancelled    = 3
	ExitPreflight    = 4
	ExitRepairLimit  = 5
	ExitPush         = 6
	ExitAI           = 7
	ExitPatch        = 8
	ExitStateChanged = 9
)

type failure struct {
	code    int
	message string
}

func (e *failure) Error() string          { return e.message }
func fail(code int, message string) error { return &failure{code, message} }

type App struct {
	UI             *ui.UI
	Runner         runner.Runner
	RepoService    repository.Service
	Verifier       verify.Service
	ContextBuilder internalcontext.Builder
	Cfg            config.Config
	CfgPath        string
	Machine        *state.Machine
	Provider       llm.Provider
	CodeHost       codehost.CodeHostProvider
	pr             *prSession
	Root, Workdir  string
	// TargetPaths are absolute files named on the command line; prepare turns
	// them into Targets (relative to Root) for file-level checks.
	TargetPaths    []string
	Targets        []string
	Plan           detector.Plan
	Tools          detector.Tools
	Repo           *model.Repository
	Checks         []model.Check
	Results        []model.CheckResult
	Report         model.SessionReport
	Snapshots      []model.Snapshot
	Review         []model.ReviewEntry
	NonInteractive bool
	Approvals      *approval.Ledger
	Verified       model.StateFingerprint
	Planned        model.StateFingerprint
	attempts       int
	seenPatches    map[string]bool
	seenStates     map[string]bool
	seenEdits      map[string]bool
	seenProposals  map[string]bool
	stale          bool
}

func New(u *ui.UI, non bool) *App {
	r := runner.Runner{}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	u.NonInteractive = non
	return &App{UI: u, Runner: r, RepoService: repository.Service{Runner: r}, Verifier: verify.Service{Runner: r}, ContextBuilder: internalcontext.Builder{Runner: r, Redact: true}, Machine: state.New(), NonInteractive: non, Approvals: &approval.Ledger{}, Report: model.SessionReport{SchemaVersion: "2", Version: model.Version, SessionID: hex.EncodeToString(id), GeneratedAt: time.Now().UTC()}}
}
func (a *App) move(next state.State) error {
	if err := a.Machine.Move(next); err != nil {
		return fail(ExitConfig, err.Error())
	}
	a.Report.State = string(next)
	a.Report.StateHistory = append(a.Report.StateHistory, string(next))
	a.stateEvent(next)
	return nil
}
func (a *App) prepare(ctx context.Context, requireGit bool) error {
	a.UI.BindContext(ctx)
	if err := a.move(state.Discovering); err != nil {
		return err
	}
	cwd := a.Workdir
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return fail(ExitConfig, err.Error())
		}
	}
	repo, err := a.RepoService.Discover(ctx, cwd)
	if err != nil {
		if requireGit {
			return fail(ExitConfig, err.Error()+"\npushguard push requires a Git repository; use pushguard check for standalone verification.")
		}
		// A source archive can be verified without pretending it has a push target.
		root, e := filepath.Abs(cwd)
		if e != nil {
			return fail(ExitConfig, e.Error())
		}
		root, e = filepath.EvalSymlinks(root)
		if e != nil {
			return fail(ExitConfig, e.Error())
		}
		probe := a.Runner.Run(ctx, cwd, []string{"git", "rev-parse", "--is-inside-work-tree"}, 10*time.Second)
		if probe.ExitCode == 0 {
			return fail(ExitConfig, err.Error())
		}
		_, gitErr := runner.GitPath()
		if ctx.Err() != nil || gitErr == nil && !strings.Contains(probe.Stderr, "not a git repository") {
			return fail(ExitConfig, err.Error())
		}
		repo = &model.Repository{Root: root, CWD: root, Project: model.Project{Name: filepath.Base(root), Root: root}}
		a.Report.Mode = "standalone"
		a.Report.Warnings = append(a.Report.Warnings, "Standalone verification (source archive): Git operations unavailable; push operations disabled.")
	} else {
		a.Report.Mode = "repository"
	}
	a.Repo = repo
	a.Root = repo.Root
	a.Report.Root = repo.Root
	for _, target := range a.TargetPaths {
		rel, e := filepath.Rel(a.Root, target)
		if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return fail(ExitConfig, "file "+target+" is outside the verified directory "+a.Root)
		}
		a.Targets = append(a.Targets, filepath.ToSlash(rel))
	}
	a.Report.Repository = repo
	cfg, path, err := config.Load(repo.Root)
	if err != nil {
		return fail(ExitConfig, err.Error())
	}
	a.Cfg, a.CfgPath = cfg, path
	if cfg.AI.APIKeyEnv != "" {
		a.Verifier.Runner.Remove = append(a.Verifier.Runner.Remove, cfg.AI.APIKeyEnv)
		a.Runner.Remove = append(a.Runner.Remove, cfg.AI.APIKeyEnv)
		a.ContextBuilder.Runner.Remove = append(a.ContextBuilder.Runner.Remove, cfg.AI.APIKeyEnv)
	}
	a.Report.ConfigHash = config.Hash(cfg)
	if err := a.buildPlan(ctx); err != nil {
		return err
	}
	if len(a.Checks) == 0 {
		langs := strings.Join(a.Plan.Discovery.Summary(), ", ")
		if langs == "" {
			langs = "none"
		}
		return fail(ExitConfig, "no verification checks could be planned (languages detected: "+langs+"). "+strings.Join(a.Plan.Notes, " ")+" Configure checks with pushguard init.")
	}
	if a.Provider == nil {
		a.Provider, err = llm.NewRepairProvider(cfg.AI, cfg.Privacy)
		if err != nil {
			a.Provider = llm.Unavailable{Reason: err.Error()}
		}
	}
	if repo.GitDir != "" && repo.HEAD != "" {
		a.Planned, err = a.fingerprint(ctx)
		if err != nil {
			return err
		}
	}
	a.restoreProvenance()
	return nil
}
func (a *App) restoreProvenance() {
	previous, err := receipt.Latest(a.Root)
	if err != nil {
		return
	}
	valid := true
	for _, entry := range previous.Review {
		hash, e := FileHash(a.Root, entry.File)
		if e != nil || entry.AfterHash == "" || hash != entry.AfterHash {
			valid = false
			break
		}
	}
	if valid && len(previous.Review) > 0 {
		a.Review = previous.Review
		a.Report.Repairs = previous.Repairs
		a.Report.RepairCycles = previous.RepairCycles
		a.Report.Review = previous.Review
		a.Report.Snapshots = previous.Snapshots
		a.Report.RepairAudit = previous.RepairAudit
	}
}
func FileHash(root, file string) (string, error) {
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	root, err = filepath.Abs(canonical)
	if err != nil {
		return "", err
	}
	full := filepath.Join(root, filepath.FromSlash(file))
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe review path")
	}
	parent, parentErr := filepath.EvalSymlinks(filepath.Dir(full))
	if parentErr == nil {
		relative, e := filepath.Rel(root, parent)
		if e != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("review path leaves repository")
		}
	}
	info, err := os.Lstat(full)
	if os.IsNotExist(err) {
		return "absent", nil
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("review file is not regular")
	}
	f, err := os.Open(full)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	fmt.Fprint(h, info.Mode().String())
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func (a *App) discovery() {
	a.UI.Title("PushGuard Agent " + model.Version)
	if a.Repo.GitDir == "" {
		a.UI.Say(a.Repo.Project.Name + " | source archive (verification only)")
	} else {
		target := a.Repo.Target.Remote + "/" + a.Repo.Target.Branch
		if a.Repo.Target.Remote == "" {
			target = "not configured"
		}
		a.UI.Say(fmt.Sprintf("%s | branch %s | target %s", a.Repo.Project.Name, a.Repo.Branch, target))
	}
	if a.Repo.GitDir == "" {
		a.UI.Say("Mode: Standalone verification | Git operations: Unavailable | Push operations: Disabled")
	}
	a.UI.Title("DISCOVERY")
	langs := a.Plan.Discovery.Summary()
	if len(langs) == 0 {
		a.UI.Status("WARN", "Languages", "no recognised source files")
	}
	for _, lang := range langs {
		a.UI.Status("PASS", lang+" detected", fmt.Sprintf("%d source file(s)", a.Plan.Discovery.Languages[lang]))
	}
	for _, b := range a.Plan.Discovery.Boundaries {
		dir := b.Dir
		if dir == "." {
			dir = "(root)"
		}
		a.UI.Say(fmt.Sprintf("  project %s: %s via %s", dir, b.Kind, strings.Join(b.Indicators, ", ")))
	}
	if len(a.Targets) > 0 {
		a.UI.Say("Targets: " + strings.Join(a.Targets, ", "))
	}
	a.UI.Title("VERIFICATION PLAN")
	for i, check := range a.Checks {
		label := ""
		if !check.Required {
			label = " (optional)"
		}
		detail := detector.Display(check)
		if check.Description != "" {
			detail = check.Name + ": " + check.Description
			if check.WorkingDir != "" {
				detail = "(in " + check.WorkingDir + ") " + detail
			}
		}
		a.UI.Say(fmt.Sprintf("%d. %s%s", i+1, detail, label))
		if check.Unavailable != "" {
			a.UI.Say("   UNAVAILABLE: " + check.Unavailable)
		}
	}
	for _, note := range a.Plan.Notes {
		a.UI.Say("Note: " + note)
	}
	a.UI.Say("Nothing has been modified. Nothing has been pushed.")
	if a.Cfg.AI.IsEnabled() {
		a.UI.Say("AI repair: " + a.Cfg.AI.Provider + " / " + a.Cfg.AI.Model + " (availability checked after approval)")
	} else {
		a.UI.Say("AI repair: disabled by " + a.CfgPath + ". See pushguard ai status for setup.")
	}
	if a.Repo.Project.PackageManager != "" {
		a.UI.Say("Package manager: " + a.Repo.Project.PackageManager)
	}
	if len(a.Repo.Project.Frameworks) > 0 {
		a.UI.Say("Frameworks: " + strings.Join(a.Repo.Project.Frameworks, ", "))
	}
	if a.Repo.Project.Workspace {
		a.UI.Say("Workspace/monorepo detected; checks remain conservative and root-owned unless configured explicitly.")
	}
	if len(a.Repo.Project.CI) > 0 {
		a.UI.Say("CI configuration detected; hosted services and secrets remain external.")
	}
	if len(a.Repo.Project.CILocalSteps) > 0 {
		a.UI.Say("Local CI-compatible steps: " + strings.Join(a.Repo.Project.CILocalSteps, "; "))
	}
	if len(a.Repo.Project.CIRemoteSteps) > 0 {
		a.UI.Say("Remote-only CI steps not executed: " + strings.Join(a.Repo.Project.CIRemoteSteps, "; "))
	}
}
func (a *App) RunCheck(ctx context.Context, jsonOut bool) int {
	a.Report.Operation = "check"
	a.Machine.CheckOnly = true
	var output io.Writer
	if jsonOut {
		a.NonInteractive, a.UI.NonInteractive = true, true
		output = a.UI.Out
		a.UI.Out = io.Discard
		a.UI.Err = io.Discard
	}
	err := a.prepare(ctx, false)
	if err == nil {
		if !jsonOut {
			a.discovery()
		}
		if a.NonInteractive {
			err = a.verifyFull(ctx)
		} else {
			var unlock func()
			unlock, err = a.lockRepository()
			if err == nil {
				defer unlock()
				err = a.beginVerification(ctx)
			}
		}
		if err == nil && !a.NonInteractive && len(a.Report.Repairs) > 0 {
			a.UI.Title("PushGuard Change Review")
			for _, proposal := range a.Report.Repairs {
				a.UI.Say(proposal.Summary + "\n" + proposal.Patch)
			}
			a.printResults()
			err = a.reviewCheck(ctx)
		}
		if err == nil && len(verify.Blocking(a.Results)) > 0 {
			err = fail(ExitVerification, "required verification failed")
			if !jsonOut {
				a.explainFailures()
			}
		}
		if err == nil {
			err = a.move(state.Complete)
			a.Report.Status = model.StatusPass
			if optionalFailure(a.Results) {
				a.Report.Status = model.StatusWarning
			}
		}
	}
	if !jsonOut && err == nil {
		a.UI.Say("\nVerification complete. pushguard check never performs git push.")
	}
	if jsonOut {
		a.UI.Out = output
	}
	if ctx.Err() != nil {
		err = fail(ExitCancelled, "workflow interrupted")
	}
	return a.finish(jsonOut, err)
}
func (a *App) RunPush(ctx context.Context, jsonOut bool) int {
	a.Report.Operation = "push"
	var output io.Writer
	if jsonOut {
		output = a.UI.Out
		a.UI.Out = io.Discard
		a.UI.Err = io.Discard
		a.NonInteractive = true
		a.UI.NonInteractive = true
	}
	err := a.prepare(ctx, true)
	if err == nil {
		unlock, lockErr := a.lockRepository()
		if lockErr != nil {
			err = lockErr
		} else {
			defer unlock()
			err = a.push(ctx)
		}
	}
	if jsonOut {
		a.UI.Out = output
	}
	if ctx.Err() != nil {
		err = fail(ExitCancelled, "workflow interrupted")
	}
	return a.finish(jsonOut, err)
}
func (a *App) push(ctx context.Context) error {
	a.discovery()
	if a.Repo.HEAD == "" {
		return fail(ExitPreflight, "create a commit before pushing")
	}
	if err := a.beginVerification(ctx); err != nil {
		return err
	}
	if len(verify.Blocking(a.Results)) > 0 {
		if a.NonInteractive || !a.Cfg.Repair.Enabled {
			a.explainFailures()
			return fail(ExitVerification, "verification failed; push remains stopped")
		}
		if err := a.repairLoop(ctx); err != nil {
			return err
		}
	}
	for restarts := 0; ; restarts++ {
		err := a.pushGates(ctx)
		if !a.stale || a.NonInteractive || restarts >= 3 || ctx.Err() != nil {
			return err
		}
		a.stale = false
		a.UI.Say("Repository changed after verification. Previous verification and push approval are now stale. PUSH NOT STARTED. Re-running required verification...")
		if err := a.refreshVerification(ctx); err != nil {
			return err
		}
	}
}

func (a *App) pushGates(ctx context.Context) error {
	if err := a.move(state.GitPreflight); err != nil {
		return err
	}
	refreshed, err := a.RepoService.Discover(ctx, a.Root)
	if err != nil {
		return fail(ExitPreflight, err.Error())
	}
	a.Repo = refreshed
	if a.pr != nil {
		if err := a.RepoService.FeatureTarget(ctx, a.Repo); err != nil {
			return err
		}
	}
	a.Report.Repository = refreshed
	pf := preflight.Service{Runner: a.Runner}.Run(ctx, a.Repo, a.Cfg.Preflight.Remote, a.Cfg.Preflight.LFS, a.Cfg.Preflight.Resource, a.Cfg.Preflight.RepositoryPolicy, nil)
	a.Report.Preflight = &pf
	if err = a.move(state.ResourcePreflight); err != nil {
		return err
	}
	a.preflightSummary(pf)
	if err = a.move(state.StateCheck); err != nil {
		return err
	}
	if err = a.requireVerified(ctx); err != nil {
		return err
	}
	// Review repaired files even when the working tree still needs a human commit.
	if !pf.Passed && len(a.Review) == 0 {
		return fail(ExitPreflight, "Git preflight blocked the push; resolve the listed blockers and rerun")
	}
	if a.NonInteractive {
		if !pf.Passed {
			return fail(ExitPreflight, "Git preflight blocked the push")
		}
		return fail(ExitCancelled, "interactive human review and final authorization required; no push executed")
	}
	if a.pr != nil && pf.Passed {
		if err := a.deliveryReceipt(ctx); err != nil {
			return err
		}
	}
	if err = a.reviewGate(ctx); err != nil {
		return err
	}
	if !pf.Passed {
		if len(a.Repo.Changes.All) > 0 {
			a.UI.Say("Git pushes commits. The verified repairs are still local edits.")
			choice := a.UI.Choice("Commit the reviewed repairs here, or continue after your commit?", "[C] Review and commit repairs", "[R] Already committed: reverify", "[K] Keep and exit", "[U] Undo")
			if choice == "c" || choice == "commit" {
				if err := a.commitRepairs(ctx); err != nil {
					return err
				}
				a.stale = true
				return fail(ExitStateChanged, "approved repairs committed; fresh verification required")
			}
			if choice == "r" || choice == "continue" || choice == "reverify" {
				a.stale = true
				return fail(ExitStateChanged, "developer requested fresh verification after commit")
			}
			if choice == "u" || choice == "undo" {
				if err := a.rollbackRepairs(); err != nil {
					return err
				}
			}
		}
		return fail(ExitPreflight, "repairs reviewed and saved; push blocked until committed content passes verification")
	}
	return a.finalPush(ctx)
}
func (a *App) fingerprint(ctx context.Context) (model.StateFingerprint, error) {
	fp, err := integrity.Compute(ctx, a.Root, a.Report.ConfigHash, a.Runner)
	if err != nil {
		return fp, fail(ExitStateChanged, err.Error())
	}
	return fp, nil
}
func (a *App) requireVerified(ctx context.Context) error {
	cfg, _, err := config.Load(a.Root)
	if err != nil {
		return fail(ExitStateChanged, "configuration changed after verification: "+err.Error())
	}
	if config.Hash(cfg) != a.Report.ConfigHash {
		return fail(ExitStateChanged, "configuration changed after verification; rerun PushGuard")
	}
	fp, err := a.fingerprint(ctx)
	if err != nil {
		return err
	}
	if fp.Value != a.Verified.Value {
		a.stale = true
		a.event("receipt.invalidated", "repository state changed")
		return fail(ExitStateChanged, "Repository state changed after verification. Previous authorization is no longer valid. Verification must run again.")
	}
	return nil
}
func (a *App) verifyFull(ctx context.Context) error {
	if err := a.move(state.Verifying); err != nil {
		return err
	}
	if err := a.move(state.FullVerifying); err != nil {
		return err
	}
	var before model.StateFingerprint
	if a.Repo.GitDir != "" && a.Repo.HEAD != "" {
		var err error
		before, err = a.fingerprint(ctx)
		if err != nil {
			return err
		}
		if len(a.Report.History) == 0 && a.Planned.Value != "" && before.Value != a.Planned.Value {
			return fail(ExitStateChanged, "repository changed after check discovery; rerun to review a fresh command plan")
		}
	}
	a.UI.Title("Verification")
	a.Results = a.Verifier.RunAll(ctx, a.Root, a.Checks, false)
	for _, result := range a.Results {
		a.recordCheckMetrics(result)
	}
	a.Report.Checks = a.Results
	a.Report.Diagnostics = allDiagnostics(a.Results)
	a.appendHistory(a.Results...)
	a.printResults()
	if ctx.Err() != nil {
		return fail(ExitCancelled, "verification interrupted")
	}
	if before.Value != "" {
		after, err := a.fingerprint(ctx)
		if err != nil {
			return err
		}
		if before.Value != after.Value {
			return fail(ExitStateChanged, "repository changed while checks were running; rerun the complete pipeline on a stable state")
		}
		if len(verify.Blocking(a.Results)) == 0 {
			a.Verified = after
			a.Report.Fingerprint = &after
		}
	}
	return a.saveEvidence()
}
func (a *App) printResults() {
	for _, r := range a.Results {
		symbol := string(r.Status)
		if r.Status == model.StatusFail {
			symbol = "FAIL"
			if !r.Check.Required {
				symbol = "WARN"
			}
		}
		a.UI.Status(symbol, r.Check.Name, ui.Duration(r.Command.Duration))
	}
}
func (a *App) explainFailures() {
	for _, r := range verify.Blocking(a.Results) {
		a.UI.Say(diagnostics.Render(a.Root, firstDiagnostic(r), 3))
	}
}
func (a *App) consent(scope approval.Scope, binding, question string) error {
	if a.NonInteractive {
		return fail(ExitCancelled, "human authorization required")
	}
	if !a.UI.Prompt(question, "[Y] Allow", "[N] Deny") {
		return fail(ExitCancelled, "authorization denied; no push executed")
	}
	grant := a.Approvals.Grant(scope, binding)
	if err := a.Approvals.Consume(grant, scope, binding); err != nil {
		return fail(ExitCancelled, err.Error())
	}
	a.recordApproval(grant)
	return nil
}
func (a *App) recordApproval(grant approval.Approval) {
	a.Machine.Authorize(approval.DecisionAllow)
	a.Report.Approvals = append(a.Report.Approvals, string(grant.Scope)+":"+grant.ID)
	a.Report.ApprovalEvents = append(a.Report.ApprovalEvents, model.ApprovalEvent{ID: grant.ID, Scope: string(grant.Scope), Binding: grant.Binding, At: grant.At})
	switch grant.Scope {
	case approval.Analyze:
		a.event("repair.requested", "")
	case approval.Apply:
		a.event("repair.approved", "")
	case approval.Review:
		a.event("review.completed", "")
	case approval.Push:
		a.event("push.authorized", "")
	case approval.PullRequest:
		a.event("pr.authorized", "")
	}
}
func (a *App) repairLoop(ctx context.Context) error {
	lastRejection := ""
	previousVerification := ""
	var expansions []model.ContextRequest
	var retryAt time.Time
	for len(verify.Blocking(a.Results)) > 0 {
		if a.attempts >= a.Cfg.Repair.MaxAttempts {
			return a.repairLimit()
		}
		failure := verify.Blocking(a.Results)[0]
		if failure.DiagnosticsLimited {
			return fail(ExitVerification, "diagnostic capture limit reached; narrow the configured check to obtain complete evidence before AI repair")
		}
		d := firstDiagnostic(failure)
		if err := a.move(state.FailureDetected); err != nil {
			return err
		}
		if err := a.move(state.Diagnosing); err != nil {
			return err
		}
		a.UI.Title("Failure: " + failure.Check.Name)
		a.UI.Say(diagnostics.Render(a.Root, d, 3))
		a.UI.Say(fmt.Sprintf("%d diagnostic(s) captured for this check. PUSH NOT STARTED.", len(failure.Diagnostics)))
		classification := diagnostics.FailureClass(failure)
		a.UI.Say("Failure classification: " + classification)
		reason := ""
		if classification != model.ClassCode && classification != model.ClassTest {
			reason = diagnostics.Infrastructure(failure.Command)
		}
		if reason != "" || classification == "ENVIRONMENT" || classification == "CONFIGURATION" || classification == "NETWORK" || classification == "RESOURCE" || classification == "GIT" {
			message := "This appears to be an environment/configuration or infrastructure failure rather than a source-code defect. I will not rewrite application code to hide it. Resolve the reported tool, environment, connectivity or resource requirement, then verify again. " + reason
			if failure.Check.Unavailable != "" {
				message += "\nReason: " + failure.Check.Unavailable
			}
			retry, err := a.infrastructureMenu(ctx, failure, message)
			if err != nil {
				return err
			}
			if retry {
				continue
			}
		}
		if classification == "TEST" || classification == "SECURITY" {
			a.UI.Say("This is a test observation. Investigate the implementation first; tests and security policy are protected from AI patches.")
		}
		if a.NonInteractive || !a.Cfg.Repair.Enabled {
			return fail(ExitVerification, "verification remains failed; repair is disabled")
		}
		groups := repairGroups(a.Root, failure, a.Cfg.Repair.GroupLimit())
		if len(groups) == 0 {
			return fail(ExitPatch, "No editable source targets remain in this failure. The reported paths are protected, sensitive, or unavailable; resolve those diagnostics manually. No AI request was sent.")
		}
		selected := groups[0].Diagnostics
		a.UI.Say(fmt.Sprintf("Diagnostics grouped into %d bounded repair group(s). Selected %s: %d related diagnostics. At most %d investigation attempt(s) remain; request count depends on actual verification.", len(groups), groups[0].ID, len(selected), a.Cfg.Repair.MaxAttempts-a.attempts))
		bundle := a.ContextBuilder.BuildFailure(ctx, a.Root, a.Repo, selected[0], selected)
		if len(bundle.EditableFiles) == 0 && len(bundle.SourceFiles) == 0 {
			return fail(ExitPatch, "No editable implementation context was found for this failure. Inspect the reported location and project imports before retrying. No AI request was sent.")
		}
		d = selected[0]
		a.UI.Say("Available repair targets: " + strings.Join(bundle.EditableFiles, ", "))
		if err := a.move(state.WaitingForRepair); err != nil {
			return err
		}
		question := "Would you like the configured repair agent to investigate these failures and prepare a fix?"
		if lastRejection != "" {
			question = "The previous proposal was rejected. Allow the repair agent to prepare a corrected source-code fix? (One new AI request)"
		}
		if !retryAt.IsZero() {
			question = "The provider rate-limited the previous request. Allow waiting for its retry window and making one new request?"
		}
		if a.Cfg.Privacy.AllowRemoteAI && !a.Cfg.AI.LocalFirst {
			question += "\nRemote destination: " + a.Cfg.AI.Endpoint
		}
		a.Report.RepairAudit = append(a.Report.RepairAudit, model.RepairAudit{FailureID: approval.Bind(failure), Diagnostics: failure.Diagnostics, Provider: a.Cfg.AI.Provider, Model: a.Cfg.AI.Model, RepairDecision: "UNKNOWN", PatchDecision: "UNKNOWN", At: time.Now().UTC()})
		audit := &a.Report.RepairAudit[len(a.Report.RepairAudit)-1]
		audit.GroupID, audit.GroupCount = groups[0].ID, len(groups)
		if err := a.repairConsent(question, failure); err != nil {
			audit.RepairDecision = "DENY"
			a.UI.Say("Repair denied. No source files were modified by this repair cycle. Verification remains failed. PUSH NOT STARTED.")
			if lastRejection != "" {
				return fail(ExitPatch, "The previous attempt did not apply a patch. No additional AI request was authorized. Reason: "+lastRejection)
			}
			return err
		}
		audit.RepairDecision = "ALLOW"
		a.attempts++
		a.Report.RepairMetrics.Attempts++
		if a.Report.RepairMetrics.StartedAt.IsZero() {
			a.Report.RepairMetrics.StartedAt = time.Now().UTC()
		}
		if err := a.waitForProvider(ctx, retryAt); err != nil {
			return err
		}
		retryAt = time.Time{}
		providerState, err := a.repairState(ctx)
		if err != nil {
			return err
		}
		if a.seenStates == nil {
			a.seenStates = map[string]bool{}
		}
		if a.seenStates[providerState.Value] {
			return fail(ExitRepairLimit, "oscillating repair detected: repository returned to an already investigated state; no further model call")
		}
		a.seenStates[providerState.Value] = true
		if err := a.move(state.Analyzing); err != nil {
			return err
		}
		if provider, ok := a.Provider.(llm.RepairProvider); ok {
			probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			probeErr := provider.Available(probeCtx)
			cancel()
			if probeErr != nil {
				if retry, err := a.recoverProvider(probeErr, audit, providerState.Value); retry {
					continue
				} else {
					return err
				}
			}
			a.UI.Say("AI provider connected: " + provider.Name() + " / " + a.Cfg.AI.Model)
		}
		if a.Repo.GitDir != "" {
			fresh, err := a.RepoService.Discover(ctx, a.Root)
			if err != nil {
				return fail(ExitConfig, err.Error())
			}
			fresh.Project.Boundaries, fresh.Project.Languages = a.Repo.Project.Boundaries, a.Repo.Project.Languages
			a.Repo = fresh
		}
		// Refresh source after the decision/wait so exact anchors use current bytes.
		bundle = a.ContextBuilder.BuildFailure(ctx, a.Root, a.Repo, selected[0], selected)
		for _, request := range expansions {
			bundle, err = a.ContextBuilder.Expand(ctx, a.Root, bundle, request)
			if err != nil {
				lastRejection = err.Error()
				break
			}
		}
		if err != nil {
			expansions = nil
			if err := a.rejectProposal(audit, providerState.Value, lastRejection); err != nil {
				return err
			}
			continue
		}
		bundle.Metadata["totalDiagnostics"] = fmt.Sprint(len(failure.Diagnostics))
		bundle.Metadata["repairScope"] = "Repair the selected diagnostics with a small coherent batch; remaining failures will be verified and investigated in subsequent approved cycles."
		bundle.CheckName, bundle.Command = failure.Check.Name, runner.CommandString(failure.Check.Args)
		if previousVerification != "" {
			bundle.Metadata["previousVerification"] = previousVerification
		}
		if lastRejection != "" {
			bundle.Metadata["previousProposalRejected"] = lastRejection + ". Prepare a different compliant source repair; never add a suppression directive."
		}
		contextReduced := false
		if preparer, ok := a.Provider.(llm.ContextPreparer); ok {
			var usage model.InferenceMetrics
			bundle, usage, err = preparer.PrepareContext(bundle)
			if err != nil {
				if retry, err := a.recoverProvider(err, audit, providerState.Value); retry {
					continue
				} else {
					return err
				}
			}
			audit.ContextEstimate = usage.EstimatedInputTokens
			contextReduced = usage.ContextReduced
			if len(bundle.SourceFiles) == 0 {
				return fail(ExitAI, "context budget cannot retain source evidence; no inference request sent")
			}
			a.UI.Say(fmt.Sprintf("Context estimate: %d/%d input tokens; output reserved: %d; safety margin: %d. Model loading/generation is bounded by the provider timeout.", usage.EstimatedInputTokens, usage.MaxInputTokens, usage.ReservedOutputTokens, usage.SafetyMarginTokens))
		}
		a.UI.Say(fmt.Sprintf("Investigating %d of %d verified diagnostics (attempt %d/%d).", len(bundle.Diagnostics), len(failure.Diagnostics), a.attempts, a.Cfg.Repair.MaxAttempts))
		if _, ok := a.Provider.(llm.CombinedProvider); ok {
			a.UI.Say("One AI request for analysis and proposal; no automatic paid retries.")
		}
		a.Report.RepairMetrics.AIRequests++
		requestStarted := time.Now()
		analysis, proposal, err := a.investigate(ctx, bundle)
		audit.RequestDuration = time.Since(requestStarted)
		if err != nil {
			var needed *llm.ContextRequiredError
			if errors.As(err, &needed) && ctx.Err() == nil {
				audit.ContextRequest = &needed.Request
				if len(expansions) >= 2 {
					return fail(ExitRepairLimit, "context expansion limit reached; verification remains failed")
				}
				expansions = append(expansions, needed.Request)
				lastRejection = "Additional context requested: " + needed.Request.Reason + "; files: " + strings.Join(needed.Request.Files, ", ") + "; symbols: " + strings.Join(needed.Request.Symbols, ", ")
				if err := a.unappliedAttempt(audit, providerState.Value, lastRejection, "CONTEXT_REQUIRED", "CONTEXT REQUIRED"); err != nil {
					return err
				}
				continue
			}
			if delay, limited := llm.RetryDelay(err); limited && ctx.Err() == nil {
				lastRejection = err.Error()
				retryAt = time.Now().Add(delay)
				if err := a.unappliedAttempt(audit, providerState.Value, lastRejection, "PROVIDER_RATE_LIMITED", "AI RATE LIMITED"); err != nil {
					return err
				}
				continue
			}
			if llm.IsResponseError(err) && ctx.Err() == nil {
				lastRejection = err.Error()
				if err := a.rejectProposal(audit, providerState.Value, lastRejection); err != nil {
					return err
				}
				continue
			}
			if retry, err := a.recoverProvider(err, audit, providerState.Value); retry {
				continue
			} else {
				return err
			}
		}
		if analysis == nil || analysis.RootCause == "" {
			return fail(ExitAI, "AI returned no usable analysis")
		}
		a.UI.Say("AI hypothesis (unverified): " + analysis.RootCause)
		a.UI.Say("AI proposal summary (not applied): " + analysis.Summary)
		a.UI.Say("No source files have been modified by this attempt.")
		bundle.Analysis = analysis
		if err = a.move(state.GeneratingPatch); err != nil {
			return err
		}
		if proposal == nil {
			a.Report.RepairMetrics.AIRequests++
			err = a.providerProgress(10*time.Second, func() error {
				var requestErr error
				proposal, requestErr = a.Provider.ProposeFix(ctx, bundle)
				return requestErr
			})
		}
		if err != nil {
			if llm.IsResponseError(err) && ctx.Err() == nil {
				lastRejection = err.Error()
				if err := a.rejectProposal(audit, providerState.Value, lastRejection); err != nil {
					return err
				}
				continue
			}
			if retry, err := a.recoverProvider(err, audit, providerState.Value); retry {
				continue
			} else {
				return err
			}
		}
		if proposal == nil {
			return fail(ExitAI, "AI returned no proposal")
		}
		audit.Proposal = proposal
		if a.seenProposals == nil {
			a.seenProposals = map[string]bool{}
		}
		identity := approval.Bind(struct {
			State, Patch string
			Edits        []model.TextEdit
		}{providerState.Value, proposal.Patch, proposal.Edits})
		if a.seenProposals[identity] {
			return fail(ExitRepairLimit, "repeated proposal for unchanged source; stopping ineffective retries")
		}
		a.seenProposals[identity] = true
		audit.Inference = proposal.Inference
		if audit.Inference != nil {
			audit.Inference.ContextReduced = audit.Inference.ContextReduced || contextReduced
		}
		if proposal.Inference != nil && proposal.Inference.LoadDuration > 0 {
			a.UI.Say(fmt.Sprintf("Ollama model load: %s; inference request: %s; observed token counts (input/output): %d/%d", proposal.Inference.LoadDuration.Round(time.Millisecond), proposal.Inference.RequestDuration.Round(time.Millisecond), proposal.Inference.InputTokens, proposal.Inference.OutputTokens))
		}
		if err := validateDiagnosticIDs(proposal.DiagnosticsAddressed, bundle.Diagnostics); err != nil {
			lastRejection = err.Error()
			if err := a.rejectProposal(audit, providerState.Value, lastRejection); err != nil {
				return err
			}
			continue
		}
		if err := patch.Materialize(a.Root, proposal); err != nil {
			lastRejection = err.Error()
			if err := a.rejectProposal(audit, providerState.Value, lastRejection); err != nil {
				return err
			}
			continue
		}
		audit.PatchHash = approval.Bind(proposal.Patch)
		if a.seenPatches == nil {
			a.seenPatches = map[string]bool{}
		}
		if a.seenPatches[audit.PatchHash] {
			return fail(ExitRepairLimit, "repeated patch detected; stopping oscillating repairs")
		}
		a.seenPatches[audit.PatchHash] = true
		if err = a.move(state.ValidatingPatch); err != nil {
			return err
		}
		validator := patch.Validator{Sensitive: security.SensitivePath}
		files, err := validator.Validate(a.Root, *proposal)
		if err == nil {
			err = validateProposalTargets(files, bundle.EditableFiles)
		}
		if err != nil {
			lastRejection = err.Error()
			if err := a.rejectProposal(audit, providerState.Value, lastRejection); err != nil {
				return err
			}
			continue
		}
		if a.seenEdits == nil {
			a.seenEdits = map[string]bool{}
		}
		if a.seenEdits[patch.EditIdentity(proposal.Patch, true)] {
			return fail(ExitRepairLimit, "oscillating repair detected: proposed edits reverse an earlier repair")
		}
		if security.Redact(proposal.Patch) != proposal.Patch {
			lastRejection = "patch contains secret-like assignments; sensitive code must be edited manually"
			if err := a.rejectProposal(audit, providerState.Value, lastRejection); err != nil {
				return err
			}
			continue
		}
		if _, err = validator.Check(ctx, a.Root, *proposal, a.Runner); err != nil {
			lastRejection = "patch cannot be applied cleanly: " + err.Error()
			if err := a.rejectProposal(audit, providerState.Value, lastRejection); err != nil {
				return err
			}
			continue
		}
		if err := a.checkProposalSyntax(ctx, *proposal); err != nil {
			lastRejection = err.Error()
			if err := a.rejectProposal(audit, providerState.Value, lastRejection); err != nil {
				return err
			}
			continue
		}
		before, err := a.repairState(ctx)
		if err != nil {
			return err
		}
		if before.Value != providerState.Value {
			return fail(ExitStateChanged, "repository changed during provider investigation; proposal discarded")
		}
		a.UI.Title("Proposed repair")
		a.UI.Say(proposal.Summary)
		a.UI.Say("Likely root cause: " + proposal.RootCause)
		if proposal.Confidence != "" {
			a.UI.Say("Confidence (not verification): " + proposal.Confidence)
		}
		a.UI.Say("Files: " + strings.Join(files, ", "))
		if len(proposal.Risks) > 0 {
			a.UI.Say("Risks: " + strings.Join(proposal.Risks, "; "))
		}
		a.UI.Say(proposal.Patch)
		a.UI.Say("PATCH PROPOSED. No proposed changes have been applied.")
		if err = a.move(state.WaitingForPatch); err != nil {
			return err
		}
		binding := approval.Bind(struct {
			State, Patch string
			Files        []string
		}{before.Value, proposal.Patch, files})
		if err = a.patchConsent(binding, *proposal); err != nil {
			audit.PatchDecision = "DENY"
			a.UI.Say("Repair proposal rejected. No proposed changes were applied. Verification remains failed. PUSH NOT STARTED.")
			return err
		}
		audit.PatchDecision = "ALLOW"
		current, err := a.repairState(ctx)
		if err != nil {
			return err
		}
		if current.Value != before.Value {
			return fail(ExitStateChanged, "repository changed while the patch was being reviewed; generate a fresh proposal")
		}
		if err = a.move(state.Snapshotting); err != nil {
			return err
		}
		store := patch.SnapshotStore{}
		a.UI.Say("Creating repair snapshot...")
		snap, err := store.Create(a.Root, files)
		if err != nil {
			return fail(ExitPatch, "snapshot failed: "+err.Error())
		}
		a.Snapshots = append(a.Snapshots, snap)
		a.Report.Snapshots = append(a.Report.Snapshots, snap)
		a.UI.Say("Repository state captured. Applying approved repair...")
		current, err = a.repairState(ctx)
		if err != nil {
			return err
		}
		if current.Value != before.Value {
			return fail(ExitStateChanged, "repository changed after snapshot; patch approval invalidated")
		}
		if err = a.move(state.Applying); err != nil {
			return err
		}
		if _, err = patch.Apply(ctx, a.Root, *proposal, files, a.Runner); err != nil {
			return fail(ExitPatch, err.Error())
		}
		if err = store.Seal(a.Root, snap); err != nil {
			return fail(ExitPatch, "snapshot seal failed; stop and inspect affected files: "+err.Error())
		}
		a.Report.Repairs = append(a.Report.Repairs, *proposal)
		audit.Applied = true
		expansions = nil
		lastRejection = ""
		a.seenEdits[patch.EditIdentity(proposal.Patch, false)] = true
		a.UI.Say("PATCH APPLIED. Verification has not yet passed.")
		a.Report.RepairCycles++
		a.Report.RepairMetrics.Applied++
		for _, file := range files {
			found := false
			for _, old := range a.Report.RepairMetrics.FilesModified {
				found = found || old == file
			}
			if !found {
				a.Report.RepairMetrics.FilesModified = append(a.Report.RepairMetrics.FilesModified, file)
			}
			hash, err := FileHash(a.Root, file)
			if err != nil {
				return fail(ExitPatch, err.Error())
			}
			// The last content hash authenticates all recorded changes to this file.
			for i := range a.Review {
				if a.Review[i].File == file {
					a.Review[i].AfterHash = hash
				}
			}
			a.Review = append(a.Review, model.ReviewEntry{File: file, Reason: proposal.RootCause, Diagnostics: []string{d.Message}, Checks: []string{failure.Check.Name}, PatchApplied: true, AfterHash: hash})
		}
		a.Report.Review = a.Review
		if err = a.move(state.Reverifying); err != nil {
			return err
		}
		a.UI.Title("Verify repair")
		a.UI.Say("I will now rerun the actual failed check: " + runner.CommandString(failure.Check.Args))
		single, err := a.runOne(ctx, failure.Check)
		if err != nil {
			return err
		}
		audit.Verification = &single
		progress := diagnostics.Reconcile(failure, single)
		audit.Progress = &progress
		a.Report.RepairMetrics.NewDiagnostics += progress.New
		a.UI.Say(fmt.Sprintf("Diagnostic progress: %d -> %d; resolved: %d; persisting: %d; new: %d. Counts come from the real %s command.", progress.Before, progress.After, progress.Resolved, progress.Remaining, progress.New, failure.Check.Name))
		if !progress.Complete {
			a.UI.Say("Diagnostic evidence is incomplete; resolved/persisting/new counts are unknown, and cannot establish repair progress.")
		}
		if err := a.saveEvidence(); err != nil {
			return err
		}
		if single.Status != model.StatusPass {
			previousVerification = fmt.Sprintf("Previous approved patch: %s. Actual %s verification still failed: %d diagnostics before, %d after. Resolved %d; persisting %d; new %d. Do not repeat ineffective changes. Previous diff:\n%s", proposal.Summary, failure.Check.Name, len(failure.Diagnostics), len(single.Diagnostics), progress.Resolved, progress.Remaining, progress.New, proposal.Patch)
			if len(previousVerification) > 2500 {
				previousVerification = previousVerification[:2500]
			}
			a.UI.Say(fmt.Sprintf("Repair rechecked: %d diagnostics before, %d after. Verification still FAILED; remaining failures need another approved repair. PUSH NOT STARTED.", len(failure.Diagnostics), len(single.Diagnostics)))
		}
		if single.Status == model.StatusPass {
			a.UI.Say("Repair independently verified by the actual " + failure.Check.Name + " command. Continuing verification...")
		}
	}
	return nil
}
func (a *App) preflightSummary(pf model.PreflightResult) {
	a.UI.Title("Git and resource preflight")
	var unknown []string
	var warnings []string
	passed := 0
	for _, item := range pf.Items {
		if item.Blocking {
			a.UI.Status(string(item.Status), item.Name, item.Detail)
		}
		if item.Status == model.StatusPass {
			passed++
		}
		if item.Status == model.StatusWarning || item.Status == model.StatusUnknown {
			if item.Status == model.StatusWarning {
				warnings = append(warnings, item.Name+": "+item.Detail)
			} else {
				unknown = append(unknown, item.Name)
			}
			a.Report.Warnings = append(a.Report.Warnings, item.Name+": "+item.Detail)
		}
	}
	a.UI.Say(fmt.Sprintf("%d observed checks passed | %d outgoing commit(s)", passed, a.Repo.Changes.Commits))
	if len(unknown) > 0 {
		a.UI.Say("UNKNOWN: " + strings.Join(unknown, ", ") + ". The remote remains authoritative; details available in review.")
	}
	if len(warnings) > 0 {
		a.UI.Say("WARNING: " + strings.Join(warnings, "; "))
	}
}
func (a *App) reviewGate(ctx context.Context) error {
	if err := a.move(state.Reviewing); err != nil {
		return err
	}
	a.UI.Title("Final review")
	a.UI.Say(fmt.Sprintf("Repository: %s\nBranch: %s\nWarnings: %d\nReview completion is not push permission.", a.Repo.Project.Name, a.Repo.Branch, len(a.Report.Warnings)))
	a.printResults()
	if a.pr != nil && a.pr.Receipt != nil {
		a.UI.Say(fmt.Sprintf("Signed AI-assisted repair history: %d approved operation(s), including verified ancestors. Current checks verify the current commit.", len(a.pr.Receipt.AIRepairs)))
	}
	if len(a.Report.RepairAudit) > 0 {
		a.UI.Say(fmt.Sprintf("AI provider: %s / %s | investigation attempts: %d", a.Cfg.AI.Provider, a.Cfg.AI.Model, a.attempts))
	}
	a.UI.Say(fmt.Sprintf("%d outgoing commit(s), %d outgoing file(s), %d repair cycle(s)", a.Repo.Changes.Commits, len(a.Repo.Changes.Files), a.Report.RepairCycles))
	if len(a.Review) > 0 {
		for _, entry := range a.Review {
			a.UI.Say(fmt.Sprintf("%s\n  Reason: %s\n  Diagnostic: %s\n  Verified by: %s", entry.File, entry.Reason, strings.Join(entry.Diagnostics, "; "), strings.Join(entry.Checks, ", ")))
		}
		for _, proposal := range a.Report.Repairs {
			a.UI.Say(proposal.Patch)
		}
	}
	for {
		choice := a.UI.Choice("Review before push authorization:", "[R/D] Full Diff", "[F] Files", "[A] AI-assisted changes", "[T] Tests", "[G] Preflight", "[E] Explain", "[U] Undo PushGuard repairs", "[C] Continue", "[X] Exit")
		switch choice {
		case "d", "r", "review", "diff", "show the diff", "full diff", "review complete diff":
			if err := a.showDiff(ctx); err != nil {
				return err
			}
		case "f", "files", "review changed files":
			a.UI.Say("Outgoing files:\n" + strings.Join(a.Repo.Changes.Files, "\n") + "\nLocal edits:\n" + strings.Join(a.Repo.Changes.All, "\n"))
		case "a", "ai":
			if a.pr != nil && a.pr.Receipt != nil {
				for _, repair := range a.pr.Receipt.AIRepairs {
					a.UI.Say(fmt.Sprintf("%s / %s | files: %s | patch hash: %s | verified by: %s", repair.Provider, repair.Model, strings.Join(repair.Files, ", "), repair.PatchHash, strings.Join(repair.VerifiedBy, ", ")))
				}
			}
			for _, proposal := range a.Report.Repairs {
				a.UI.Say(proposal.Summary + "\n" + proposal.Patch)
			}
		case "t", "tests", "test results":
			a.printResults()
			for _, r := range a.Results {
				if r.Status == model.StatusFail {
					a.UI.Say(r.Command.Stdout + r.Command.Stderr)
				}
			}
		case "g", "details":
			if a.Report.Preflight != nil {
				for _, item := range a.Report.Preflight.Items {
					a.UI.Status(string(item.Status), item.Name, item.Detail)
				}
			}
		case "e", "explain", "why":
			for _, proposal := range a.Report.Repairs {
				a.UI.Say(proposal.Summary + "\nLikely root cause: " + proposal.RootCause)
			}
			if len(a.Report.Repairs) == 0 {
				a.UI.Say("The required checks verified the local state. Git will send only the displayed outgoing commits.")
			}
		case "u", "undo", "rollback":
			if len(a.Snapshots) == 0 {
				a.UI.Say("No repair snapshot was created in this session. Use pushguard rollback for the latest repository snapshot.")
				continue
			}
			if err := a.rollbackRepairs(); err != nil {
				return err
			}
			return fail(ExitCancelled, "PushGuard repairs restored; verify again before pushing")
		case "c", "p", "continue", "proceed":
			if err := a.requireVerified(ctx); err != nil {
				return err
			}
			grant := a.Approvals.Grant(approval.Review, a.Verified.Value)
			if err := a.Approvals.Consume(grant, approval.Review, a.Verified.Value); err != nil {
				return err
			}
			a.recordApproval(grant)
			a.Report.ReviewComplete = true
			if err := a.move(state.StateCheck); err != nil {
				return err
			}
			return nil
		case "x", "n", "no", "deny", "exit", "stop", "cancel", "":
			return fail(ExitCancelled, "review stopped; no push executed")
		default:
			if strings.HasPrefix(choice, "why") {
				for _, entry := range a.Review {
					a.UI.Say(entry.File + ": " + entry.Reason + " (" + strings.Join(entry.Diagnostics, "; ") + ")")
				}
			} else {
				a.UI.Say("Choose diff, files, tests, details, explain, undo, continue, or stop.")
			}
		}
	}
}
func (a *App) showDiff(ctx context.Context) error {
	diff, err := a.RepoService.OutgoingDiff(ctx, a.Repo)
	if err != nil {
		return fail(ExitConfig, "outgoing diff evidence incomplete: "+err.Error())
	}
	a.UI.Say("Outgoing commit diff:")
	a.UI.Say(diff)
	res := a.Runner.Run(ctx, a.Root, []string{"git", "diff", "--no-color", "--no-ext-diff", "--no-textconv", a.Repo.HEAD, "--"}, 30*time.Second)
	if res.ExitCode != 0 || res.Truncated {
		return fail(ExitConfig, "local diff evidence incomplete")
	}
	if res.Stdout != "" {
		a.UI.Say("Uncommitted diff:")
		a.UI.Say(res.Stdout)
	}
	return nil
}
func (a *App) finalPush(ctx context.Context) error {
	if a.Report.Operation != "push" && a.Report.Operation != "pr" || !a.allRequiredPassed() || !a.Report.ReviewComplete || a.Report.Preflight == nil || !a.Report.Preflight.Passed {
		return fail(ExitPreflight, "required gates have not passed")
	}
	if err := a.requireVerified(ctx); err != nil {
		return err
	}
	if a.pr != nil {
		if err := a.validateDelivery(ctx); err != nil {
			return err
		}
	}
	if err := a.move(state.WaitingForPush); err != nil {
		return err
	}
	refspec := a.Verified.HEAD + ":refs/heads/" + a.Repo.Target.Branch
	args := []string{"git", "push", "--", a.Repo.Target.RemoteURL, refspec}
	a.UI.Title("Final push authorization")
	a.UI.Say("No errors found in required local checks. State verified. Nothing pushed yet.")
	a.UI.Say(fmt.Sprintf("Code: %d required check(s) passed | Repairs: %d | Review: complete | Blocking failures: 0", requiredCount(a.Results), a.Report.RepairCycles))
	a.UI.Say(fmt.Sprintf("%s -> %s/%s\nVerified commit: %s\nExact operation: %s", a.Repo.Branch, a.Repo.Target.Remote, a.Repo.Target.Branch, a.Verified.HEAD, runner.CommandString(args)))
	// The receipt must be writable before an external mutation is authorized.
	if _, err := receipt.Save(a.Report); err != nil {
		return fail(ExitConfig, "cannot persist verification evidence: "+err.Error())
	}
	binding := approval.Bind(struct {
		Root, State, HEAD, URL, Remote, Branch, RemoteHEAD, Receipt string
		Outgoing                                                    []string
	}{a.Root, a.Verified.Value, a.Verified.HEAD, a.Repo.Target.RemoteURL, a.Repo.Target.Remote, a.Repo.Target.Branch, a.Report.Preflight.RemoteHEAD, a.Report.DeliveryReceiptID, a.Repo.Changes.Outgoing})
	for {
		choice := a.UI.Choice("Authorize this exact Git push?", "[Y] ALLOW PUSH", "[N] DENY", "[R] Review again", "[D] Details")
		if (a.pr == nil && approval.ParseDecision(choice) == approval.DecisionAllow) || (a.pr != nil && (choice == "y" || choice == "yes")) {
			grant := a.Approvals.Grant(approval.Push, binding)
			if err := a.Approvals.Consume(grant, approval.Push, binding); err != nil {
				return fail(ExitCancelled, err.Error())
			}
			a.recordApproval(grant)
			break
		}
		switch choice {
		case "r", "review":
			if err := a.reviewGate(ctx); err != nil {
				return err
			}
			if err := a.move(state.WaitingForPush); err != nil {
				return err
			}
		case "d", "details":
			a.printResults()
			for _, item := range a.Report.Preflight.Items {
				a.UI.Status(string(item.Status), item.Name, item.Detail)
			}
		case "n", "no", "deny", "x", "stop", "cancel", "":
			return fail(ExitCancelled, "Push denied. Your local verified changes remain. No git push was executed.")
		default:
			a.UI.Say("Choose yes, no, review, or details.")
		}
	}
	if err := a.requireVerified(ctx); err != nil {
		return err
	}
	remote, err := (preflight.Service{Runner: a.Runner}).RemoteHEAD(ctx, a.Root, a.Repo.Target.RemoteURL, a.Repo.Target.Branch)
	if err != nil {
		return fail(ExitPreflight, err.Error())
	}
	if remote != a.Report.Preflight.RemoteHEAD {
		return fail(ExitStateChanged, "remote branch changed after review; fetch and rerun verification")
	}
	// Remote probing may take time; check local state again immediately before execution.
	if err := a.requireVerified(ctx); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return fail(ExitCancelled, "push interrupted before execution")
	}
	if a.pr != nil {
		if err := a.validateDelivery(ctx); err != nil {
			return err
		}
	}
	if err = a.move(state.Pushing); err != nil {
		return err
	}
	a.UI.Say("Authorization received. Executing: " + runner.CommandString(args))
	res := a.Runner.Run(ctx, a.Root, args, 10*time.Minute)
	a.Report.PushResult = &res
	if res.ExitCode != 0 {
		a.UI.Say("PUSH FAILED. Local verification passed, but Git/remote rejected the operation.\nRemote response:\n" + res.Stderr + "\n" + res.Stdout)
		return fail(ExitPush, "Git/remote failure; no automatic source-code modifications were made after the push failure: "+firstLine(res.Stderr+" "+res.Stdout+" "+res.Terminated))
	}
	a.event("push.completed", a.Verified.HEAD)
	if a.pr != nil {
		if err = a.move(state.BranchPushed); err != nil {
			return err
		}
		a.UI.Say("BRANCH PUSH COMPLETE: " + a.Repo.Target.Remote + "/" + a.Repo.Target.Branch)
		return nil
	}
	if err = a.move(state.Complete); err != nil {
		return err
	}
	a.Report.Status = model.StatusPass
	a.UI.Say("\nPUSH COMPLETE. Remote: " + a.Repo.Target.Remote + " Branch: " + a.Repo.Target.Branch + ". All required local verification passed before human push authorization.")
	return nil
}
func (a *App) finish(jsonOut bool, err error) int {
	if !a.Report.RepairMetrics.StartedAt.IsZero() {
		a.Report.RepairMetrics.Duration = time.Since(a.Report.RepairMetrics.StartedAt)
	}
	if !jsonOut {
		a.repairMetricsSummary()
	}
	code := ExitOK
	if err != nil {
		code = ExitConfig
		var typed *failure
		if errors.As(err, &typed) {
			code = typed.code
		}
		a.Report.Error = err.Error()
		a.Report.Status = model.StatusFail
		if code == ExitCancelled {
			a.Report.Status = model.StatusSkipped
		} else if code == ExitConfig || code == ExitPreflight || code == ExitStateChanged {
			a.Report.Status = model.StatusBlocked
		}
		terminal := state.Aborted
		if code != ExitCancelled {
			terminal = state.Error
		}
		_ = a.Machine.Move(terminal)
		if !jsonOut {
			a.UI.Error(err.Error())
			if a.Report.PushResult == nil {
				a.UI.Say("PUSH NOT STARTED")
			}
		}
	} else if a.Report.Status == "" {
		a.Report.Status = model.StatusPass
	}
	a.Report.State = string(a.Machine.Current)
	a.Report.Review = a.Review
	if a.Report.Root != "" {
		if _, saveErr := receipt.Save(a.Report); saveErr != nil {
			a.Report.Warnings = append(a.Report.Warnings, "receipt unavailable: "+saveErr.Error())
			if !jsonOut {
				a.UI.Status("WARN", "receipt", saveErr.Error())
			}
			if code == ExitOK && a.Report.PushResult == nil {
				code = ExitConfig
				a.Report.Status = model.StatusBlocked
				a.Report.Error = "verification evidence could not be persisted"
			}
		}
	}
	if jsonOut {
		if err := ui.PrintJSON(a.UI.Out, security.SanitizeReport(a.Report)); err != nil {
			return ExitConfig
		}
	}
	return code
}
func allDiagnostics(results []model.CheckResult) []model.Diagnostic {
	var out []model.Diagnostic
	for _, r := range results {
		out = append(out, r.Diagnostics...)
	}
	return out
}
func firstDiagnostic(r model.CheckResult) model.Diagnostic {
	for _, d := range r.Diagnostics {
		if d.Location.File != "" && d.Location.Line > 0 {
			return d
		}
	}
	if len(r.Diagnostics) > 0 {
		return r.Diagnostics[0]
	}
	return model.Diagnostic{Tool: r.Check.Name, Category: r.Check.Category, Severity: "error", Message: fmt.Sprintf("%s exited with code %d", r.Check.Name, r.Command.ExitCode), RawOutput: r.Command.Stdout + "\n" + r.Command.Stderr, Command: r.Command.Command, ExitCode: r.Command.ExitCode}
}
func replaceResult(results []model.CheckResult, target model.CheckResult) []model.CheckResult {
	for i, r := range results {
		if r.Check.Name == target.Check.Name {
			results[i] = target
			return results
		}
	}
	return append(results, target)
}
func optionalFailure(results []model.CheckResult) bool {
	for _, r := range results {
		if !r.Check.Required && r.Status == model.StatusFail {
			return true
		}
	}
	return false
}
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

func requiredCount(results []model.CheckResult) int {
	count := 0
	for _, r := range results {
		if r.Check.Required && r.Status == model.StatusPass {
			count++
		}
	}
	return count
}

func (a *App) startConsent() error {
	binding := approval.Bind(struct {
		Root, Config, State string
		Checks              []model.Check
	}{a.Root, a.Report.ConfigHash, a.Planned.Value, a.Checks})
	for {
		choice := a.UI.Choice("Start verification?", "[Y] Allow", "[D] Details", "[N] Deny")
		switch choice {
		case "y", "yes", "allow":
			grant := a.Approvals.Grant(approval.Start, binding)
			if err := a.Approvals.Consume(grant, approval.Start, binding); err != nil {
				return fail(ExitCancelled, err.Error())
			}
			a.recordApproval(grant)
			return nil
		case "d", "details", "commands":
			a.UI.Say(fmt.Sprintf("Project: %s\nFrameworks: %s\nPackage manager: %s\nRemote: %s\nUpstream: %s\nOutgoing commits: %d\nChanged files: %s\nGit LFS: used=%t, installed=%t\nCI: %s\nConfiguration: %s", strings.Join(a.Repo.Project.Languages, ", "), strings.Join(a.Repo.Project.Frameworks, ", "), a.Repo.Project.PackageManager, a.Repo.Target.Remote, a.Repo.Target.Upstream, a.Repo.Changes.Commits, strings.Join(a.Repo.Changes.All, ", "), a.Repo.LFSUsed, a.Repo.LFSInstalled, strings.Join(a.Repo.Project.CI, ", "), a.CfgPath))
			for _, check := range a.Checks {
				a.UI.Say(fmt.Sprintf("%s (required=%t): %s", check.Name, check.Required, runner.CommandString(check.Args)))
			}
		case "n", "no", "deny", "x", "stop", "cancel", "":
			return fail(ExitCancelled, "Verification cancelled. No files modified. No push performed.")
		default:
			a.UI.Say("Choose run, commands, or stop.")
		}
	}
}

// Historical records are previews; immutable cycle receipts retain complete logs.
func (a *App) appendHistory(results ...model.CheckResult) {
	for _, original := range results {
		r := original
		r.Preview = true
		r.Command.Stdout = logPreview(r.Command.Stdout)
		r.Command.Stderr = logPreview(r.Command.Stderr)
		ds := original.Diagnostics
		if len(ds) > 8 {
			ds = ds[:8]
		}
		r.Diagnostics = append([]model.Diagnostic(nil), ds...)
		for i := range r.Diagnostics {
			r.Diagnostics[i].RawOutput = ""
		}
		a.Report.History = append(a.Report.History, r)
	}
}
func logPreview(s string) string {
	if len(s) > 2000 {
		return s[:2000] + "\n[historical preview; complete evidence is in the cycle receipt]"
	}
	return s
}
func (a *App) saveEvidence() error {
	path, err := receipt.Save(a.Report)
	if err != nil {
		return fail(ExitConfig, "cannot persist verification evidence: "+err.Error())
	}
	a.Report.LogArtifacts = append(a.Report.LogArtifacts, path)
	return nil
}
