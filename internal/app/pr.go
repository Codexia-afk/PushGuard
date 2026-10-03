package app

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/pushguard/pushguard/internal/approval"
	"github.com/pushguard/pushguard/internal/codehost"
	"github.com/pushguard/pushguard/internal/delivery"
	"github.com/pushguard/pushguard/internal/model"
	"github.com/pushguard/pushguard/internal/preflight"
	"github.com/pushguard/pushguard/internal/receipt"
	"github.com/pushguard/pushguard/internal/state"
)

type PROptions struct {
	Title, Description, Base string
	Draft                    *bool
	Number                   int
	Wait                     time.Duration
}
type prSession struct {
	Options PROptions
	Remote  codehost.RemoteRepository
	Receipt *receipt.VerificationReceipt
}

func (a *App) RunPR(ctx context.Context, jsonOut bool, options PROptions) int {
	a.Report.Operation = "pr"
	var output io.Writer
	if jsonOut {
		a.NonInteractive, a.UI.NonInteractive = true, true
		output = a.UI.Out
		a.UI.Out, a.UI.Err = io.Discard, io.Discard
	}
	err := a.prepare(ctx, true)
	if err == nil {
		err = a.preparePR(ctx, options)
	}
	if err == nil {
		unlock, e := a.lockRepository()
		err = e
		if err == nil {
			defer unlock()
			err = a.push(ctx)
			if err == nil {
				err = a.afterBranchPush(ctx)
			}
		}
	}
	if jsonOut {
		a.UI.Out = output
	}
	if ctx.Err() != nil {
		err = fail(ExitCancelled, "PR workflow interrupted")
	}
	return a.finish(jsonOut, err)
}
func (a *App) preparePR(ctx context.Context, o PROptions) error {
	if !a.Cfg.GitHub.Enabled || !a.Cfg.GitHub.PR.Enabled {
		return fail(ExitConfig, "Enable github.enabled and github.pr.enabled in the repository's committed PushGuard configuration")
	}
	if a.CodeHost == nil {
		a.CodeHost = &codehost.GitHubProvider{Runner: a.Runner, Workdir: a.Root}
	}
	remote, err := a.CodeHost.DetectRepository(ctx, *a.Repo)
	if err != nil {
		return err
	}
	if !remote.CanPush {
		return fail(ExitPreflight, "GitHub repository write access is unavailable; PR branch push cannot proceed")
	}
	if o.Base == "" {
		o.Base = a.Cfg.GitHub.PR.BaseBranch
	}
	if o.Base == "" {
		o.Base = remote.DefaultBranch
	}
	if a.Repo.Target.DetachedHEAD || a.Repo.Branch == o.Base || a.Repo.Branch == remote.DefaultBranch {
		return fail(ExitPreflight, "Switch to a feature branch before PR delivery; the base/default branch is not a PR push target")
	}
	if r := a.Runner.Run(ctx, a.Root, []string{"git", "check-ref-format", "refs/heads/" + o.Base}, 10*time.Second); r.ExitCode != 0 {
		return fail(ExitConfig, "invalid PR base branch")
	}
	if err = a.RepoService.FeatureTarget(ctx, a.Repo); err != nil {
		return err
	}
	if o.Number > 0 {
		pr, e := a.CodeHost.GetPullRequest(ctx, o.Number)
		if e != nil {
			return e
		}
		if pr == nil || pr.Head != a.Repo.Branch || pr.HeadRepository != remote.Identity() || pr.State != "open" {
			return fail(ExitPreflight, "PR repair requires checking out this open PR's feature branch in the same repository")
		}
		a.UI.Say("Hosted repair opportunity for " + pr.URL + ". Hosted diagnostics are observations; local verification and approvals determine any repair.")
		checks, e := a.CodeHost.GetChecks(ctx, pr.HeadSHA)
		if e != nil {
			return e
		}
		for _, check := range checks {
			if check.State == codehost.Failure {
				a.UI.Status("FAIL", check.Name, "hosted check")
				for _, d := range check.Diagnostics {
					if annotation, ok := delivery.Annotation(d); ok {
						a.UI.Say(fmt.Sprintf("%s:%d — %s", annotation.Path, annotation.StartLine, annotation.Message))
					}
				}
			}
		}
		o.Base = pr.Base
		if o.Title == "" {
			o.Title = pr.Title
		}
	}
	if o.Title == "" {
		o.Title = strings.ReplaceAll(a.Repo.Branch, "-", " ")
	}
	a.pr = &prSession{Options: o, Remote: *remote}
	if o.Title == "" || len(o.Title) > 256 || strings.ContainsAny(o.Title, "\r\n\x00") || len(o.Description) > 16000 {
		return fail(ExitConfig, "PR title/description exceeds metadata limits or contains invalid characters")
	}
	a.UI.Title("PushGuard — verified delivery for your pull request")
	a.UI.Say(remote.Identity() + "\n" + a.Repo.Branch + " -> " + o.Base + "\nPR creation requires separate authorization after branch push.")
	return nil
}

func (a *App) deliveryReceipt(ctx context.Context) error {
	if a.pr.Receipt != nil && a.pr.Receipt.LocalStateHash == a.Verified.Value {
		return nil
	}
	v, err := receipt.NewVerification(ctx, a.Root, a.pr.Remote.Identity(), a.Report, a.Runner)
	if err != nil {
		return fail(ExitConfig, err.Error())
	}
	if err = receipt.SaveVerification(a.Root, v); err != nil {
		return err
	}
	a.pr.Receipt = v
	a.Report.DeliveryReceiptID = v.ID
	a.event("verification.completed", v.ID)
	a.UI.Say("Local verification receipt: " + v.ID + "\nReceipt is commit-bound and signed. Hosted CI remains unverified.")
	return nil
}
func (a *App) validateDelivery(ctx context.Context) error {
	if a.pr.Receipt == nil || a.Report.DeliveryReceiptID != a.pr.Receipt.ID || a.pr.Receipt.LocalStateHash != a.Verified.Value {
		return fail(ExitStateChanged, "delivery receipt is missing or stale; verification must run again")
	}
	tree, err := a.RepoService.Git(ctx, a.Root, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return err
	}
	keys, err := receipt.PublicTrust()
	if err != nil {
		return err
	}
	validation := receipt.ValidateVerification(a.pr.Receipt, receipt.Expectation{Repository: a.pr.Remote.Identity(), CommitSHA: a.Verified.HEAD, Branch: a.Repo.Branch, TreeHash: strings.TrimSpace(tree), ConfigHash: a.Report.ConfigHash, Keys: keys})
	if validation.Status != receipt.Valid {
		return fail(ExitStateChanged, string(validation.Status)+": "+validation.Reason)
	}
	return nil
}

func (a *App) afterBranchPush(ctx context.Context) error {
	if a.pr == nil || a.Report.PushResult == nil || a.Report.PushResult.ExitCode != 0 {
		return fail(ExitPreflight, "PR creation requires successful authorized branch push")
	}
	o := a.pr.Options
	existing, err := a.CodeHost.FindPullRequest(ctx, a.Repo.Branch, o.Base)
	if err != nil {
		return err
	}
	if o.Number > 0 && (existing == nil || existing.Number != o.Number) {
		return fail(ExitStateChanged, "the selected PR changed or closed before update")
	}
	if existing != nil && existing.HeadSHA != a.Verified.HEAD {
		existing, err = a.awaitPullRequestHead(ctx, existing)
		if err != nil {
			return err
		}
	}
	description := o.Description
	if existing != nil && description == "" {
		description = userDescription(existing.Body)
	}
	draft := a.Cfg.GitHub.PR.Draft
	if o.Draft != nil {
		draft = *o.Draft
	}
	if err = a.move(state.WaitingForPR); err != nil {
		return err
	}
	var body string
	for {
		body, err = delivery.Description(description, a.pr.Receipt)
		if err != nil {
			return err
		}
		a.UI.Title("PUSHGUARD PR")
		verb := "Create"
		if existing != nil {
			verb = fmt.Sprintf("Update PR #%d", existing.Number)
		}
		a.UI.Say(fmt.Sprintf("%s\nRepository: %s\nSource: %s\nTarget: %s\nTitle: %s\nDraft (new PR): %t\nLocal verification: PASS\nCommit: %s\nReceipt: %s", verb, a.pr.Remote.Identity(), a.Repo.Branch, o.Base, o.Title, draft, a.Verified.HEAD, a.pr.Receipt.ID))
		question := verb + " with this metadata and verification receipt?"
		if a.Cfg.GitHub.Checks.Publish {
			a.UI.Say("The configured hosted publisher will validate the signed receipt and publish the GitHub Check.")
		}
		choice := a.UI.Choice(question, "[Y] Create/update PR", "[N] Stop here", "[D] Full description", "[E] Edit details")
		switch choice {
		case "y", "yes":
			input := codehost.CreatePullRequestInput{Title: o.Title, Body: body, Base: o.Base, Head: a.Repo.Branch, Draft: draft}
			binding := approval.Bind(struct {
				Input           codehost.CreatePullRequestInput
				Commit, Receipt string
				Publish         bool
			}{input, a.Verified.HEAD, a.pr.Receipt.ID, a.Cfg.GitHub.Checks.Publish})
			grant := a.Approvals.Grant(approval.PullRequest, binding)
			if err = a.Approvals.Consume(grant, approval.PullRequest, binding); err != nil {
				return err
			}
			a.recordApproval(grant)
			if err = a.requireVerified(ctx); err != nil {
				return err
			}
			if err = a.validateDelivery(ctx); err != nil {
				return err
			}
			head, e := (preflight.Service{Runner: a.Runner}).RemoteHEAD(ctx, a.Root, a.Repo.Target.RemoteURL, a.Repo.Branch)
			if e != nil {
				return e
			}
			if head != a.Verified.HEAD {
				return fail(ExitStateChanged, "remote branch changed after push; PR authorization invalidated")
			}
			if err = a.move(state.CreatingPR); err != nil {
				return err
			}
			var pr *codehost.PullRequest
			if existing == nil {
				pr, err = a.CodeHost.CreatePullRequest(ctx, input)
			} else {
				err = a.CodeHost.UpdatePullRequest(ctx, codehost.UpdatePullRequestInput{Number: existing.Number, Title: o.Title, Body: body, Base: o.Base})
				if err == nil {
					pr, err = a.CodeHost.GetPullRequest(ctx, existing.Number)
				}
			}
			if err != nil {
				return err
			}
			if pr == nil {
				return fail(ExitStateChanged, "GitHub did not return PR metadata after creation/update")
			}
			a.Report.PullRequestURL, a.Report.PullRequestNumber = pr.URL, pr.Number
			if pr.HeadSHA != a.Verified.HEAD {
				pr, err = a.awaitPullRequestHead(ctx, pr)
				if err != nil {
					return err
				}
			}
			if err = a.move(state.PRCreated); err != nil {
				return err
			}
			a.event("pr.created", pr.URL)
			a.UI.Say(fmt.Sprintf("PULL REQUEST READY\nPR #%d\n%s\nLocal verification: PASS\nHosted verification: WAITING", pr.Number, pr.URL))
			return a.observeHosted(ctx, pr.Number, o.Wait)
		case "e", "edit":
			if title := a.UI.Text("PR title (empty keeps current):"); title != "" {
				o.Title = title
			}
			if text := a.UI.Text("Description (single line; use --body-file for multiple lines, empty keeps current):"); text != "" {
				description = text
			}
			if base := a.UI.Text("Base branch (empty keeps current):"); base != "" {
				o.Base = base
			}
			if o.Base == a.Repo.Branch {
				return fail(ExitConfig, "PR base and head must differ")
			}
		case "d", "details":
			a.UI.Say(body)
		default:
			return fail(ExitCancelled, "Branch was pushed. PR creation/update was not authorized; no PR mutation performed.")
		}
	}
}

// GitHub's PR index can lag a successful Git push. Retry only read operations
// while the actual remote tip is still the exact authorized commit. Never
// silently create/update a PR against a different commit or re-push the branch.
func (a *App) awaitPullRequestHead(ctx context.Context, pr *codehost.PullRequest) (*codehost.PullRequest, error) {
	a.UI.Say("GitHub is updating the PR's branch index. Waiting for the exact pushed commit...")
	for attempt := 0; attempt < 6; attempt++ {
		tip, err := (preflight.Service{Runner: a.Runner}).RemoteHEAD(ctx, a.Root, a.Repo.Target.RemoteURL, a.Repo.Branch)
		if err != nil {
			return nil, err
		}
		if tip != a.Verified.HEAD {
			return nil, fail(ExitStateChanged, "remote branch changed after push; PR authorization invalidated")
		}
		current, err := a.CodeHost.GetPullRequest(ctx, pr.Number)
		if err != nil {
			return nil, err
		}
		if current != nil && current.HeadSHA == a.Verified.HEAD {
			return current, nil
		}
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, fail(ExitCancelled, "PR synchronization wait cancelled")
		case <-timer.C:
		}
	}
	return nil, fail(ExitStateChanged, "GitHub PR HEAD has not caught up with the verified push; retry after synchronization. No new PR mutation was authorized by this wait")
}

func userDescription(body string) string {
	if i := strings.Index(body, "## PushGuard Verification"); i >= 0 {
		return strings.TrimSpace(body[:i])
	}
	return body
}

func (a *App) observeHosted(ctx context.Context, number int, wait time.Duration) error {
	keys, err := receipt.PublicTrust()
	if err != nil {
		return err
	}
	svc := delivery.Service{Provider: a.CodeHost, Repository: a.pr.Remote, Keys: keys, Required: a.Cfg.GitHub.Checks.Required}
	deadline := time.Now().Add(wait)
	for {
		if err = a.move(state.HostedVerifying); err != nil {
			return err
		}
		snapshot, e := svc.Inspect(ctx, number)
		if e != nil {
			return e
		}
		if snapshot.PR.HeadSHA != a.Verified.HEAD {
			return fail(ExitStateChanged, "PR HEAD changed; the previous local receipt is stale")
		}
		if a.Cfg.GitHub.Checks.Publish {
			delivery.RequirePublished(snapshot)
		}
		a.Report.HostedState = string(snapshot.Hosted)
		a.UI.Say(snapshot.Summary)
		if snapshot.Local.Status != receipt.Valid || snapshot.Overall == codehost.Failure || snapshot.Overall == codehost.Cancelled {
			_ = a.move(state.HostedFailed)
			return fail(ExitVerification, "Hosted delivery failed; the historical local receipt remains unchanged. Run pushguard pr repair "+fmt.Sprint(number)+" locally to investigate.")
		}
		if snapshot.Overall == codehost.Success {
			_ = a.move(state.HostedPassed)
			_ = a.move(state.ReadyForReview)
			a.Report.Status = model.StatusPass
			return a.move(state.Complete)
		}
		if err = a.move(state.HostedPending); err != nil {
			return err
		}
		if wait <= 0 || time.Now().After(deadline) {
			a.Report.Status = model.StatusUnknown
			a.UI.Say("Hosted verification is pending. Use pushguard pr status. No merge is performed.")
			return a.move(state.Complete)
		}
		timer := time.NewTimer(min(10*time.Second, time.Until(deadline)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return fail(ExitCancelled, "hosted wait cancelled; PR remains open")
		case <-timer.C:
		}
	}
}
