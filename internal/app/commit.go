package app

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/pushguard/pushguard/internal/approval"
	"github.com/pushguard/pushguard/internal/runner"
	"github.com/pushguard/pushguard/internal/state"
)

// commitRepairs is a human-authorized Git operation, never an agent capability.
// --only limits the commit to displayed, tracked repair paths and leaves unrelated
// staged work alone. Unrelated local changes remain a push blocker.
func (a *App) commitRepairs(ctx context.Context) error {
	if a.Report.Operation != "push" && a.Report.Operation != "pr" || !a.Report.ReviewComplete || !a.allRequiredPassed() {
		return fail(ExitPreflight, "reviewed and verified repairs are required before committing")
	}
	if err := a.requireVerified(ctx); err != nil {
		return err
	}
	seen := map[string]bool{}
	var files []string
	for _, entry := range a.Review {
		if entry.PatchApplied && !seen[entry.File] {
			seen[entry.File] = true
			files = append(files, entry.File)
		}
	}
	if len(files) == 0 {
		return fail(ExitPreflight, "no approved repair files to commit")
	}
	sort.Strings(files)
	tracked := a.Runner.Run(ctx, a.Root, append([]string{"git", "ls-files", "--error-unmatch", "--"}, files...), 10*time.Second)
	if tracked.ExitCode != 0 {
		return fail(ExitPreflight, "new repair files require developer staging before commit; existing index preserved")
	}
	diff := a.Runner.Run(ctx, a.Root, append([]string{"git", "diff", "--no-color", "--no-ext-diff", "--no-textconv", "HEAD", "--"}, files...), 30*time.Second)
	if diff.ExitCode != 0 || diff.Truncated {
		return fail(ExitPreflight, "complete commit diff unavailable")
	}
	if strings.TrimSpace(diff.Stdout) == "" {
		return fail(ExitPreflight, "no repair changes to commit")
	}
	a.UI.Title("Commit review")
	a.UI.Say(diff.Stdout)
	args := append([]string{"git", "commit", "--only", "-m", "Apply approved PushGuard repairs", "--"}, files...)
	a.UI.Say("Exact local operation: " + runner.CommandString(args))
	if err := a.move(state.WaitingForCommit); err != nil {
		return err
	}
	binding := approval.Bind(struct {
		State, Diff string
		Args        []string
	}{a.Verified.Value, diff.Stdout, args})
	if err := a.consent(approval.Commit, binding, "Commit this displayed diff locally, then rerun verification? Push still needs separate approval."); err != nil {
		return err
	}
	if err := a.requireVerified(ctx); err != nil {
		return err
	}
	if err := a.move(state.Committing); err != nil {
		return err
	}
	result := a.Runner.Run(ctx, a.Root, args, 2*time.Minute)
	a.Report.CommitResult = &result
	if result.ExitCode != 0 {
		return fail(ExitPreflight, "Git commit did not complete: "+result.Stderr+result.Stdout)
	}
	a.UI.Say("Reviewed repairs committed locally. Reverification required before push.")
	if err := a.move(state.StateCheck); err != nil {
		return err
	}
	return a.saveEvidence()
}
