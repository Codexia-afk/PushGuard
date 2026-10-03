package app

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"time"

	contextengine "github.com/pushguard/pushguard/internal/context"
	"github.com/pushguard/pushguard/internal/llm"
	"github.com/pushguard/pushguard/internal/model"
	"github.com/pushguard/pushguard/internal/patch"
)

// Large lint groups are repaired in bounded batches. Tests keep their grouped
// evidence because a reported test file may point to another implementation.
func repairDiagnostics(failure model.CheckResult, outputTokens int) []model.Diagnostic {
	group := failure.Diagnostics
	if failure.Check.Category != "lint" {
		return group
	}
	if outputTokens == 0 {
		outputTokens = 2048
	}
	limit := max(1, min(20, (outputTokens-512)/128))
	if len(group) <= limit {
		return group
	}
	selected := make([]model.Diagnostic, 0, limit)
	file := group[0].Location.File
	for _, d := range group {
		if d.Location.File == file {
			selected = append(selected, d)
		}
		if len(selected) == limit {
			break
		}
	}
	return selected
}

func repairGroups(root string, failure model.CheckResult, limit int) []contextengine.DiagnosticGroup {
	if failure.Check.Category == "lint" {
		var editable []model.Diagnostic
		for _, diagnostic := range failure.Diagnostics {
			file := diagnostic.Location.File
			if filepath.IsAbs(file) {
				var err error
				file, err = filepath.Rel(root, file)
				if err != nil {
					continue
				}
			}
			if file != "" && patch.EditableFile(root, filepath.ToSlash(file)) {
				editable = append(editable, diagnostic)
			}
		}
		failure.Diagnostics = editable
	}
	return contextengine.PlanGroups(root, failure.Diagnostics, limit)
}

func repairableDiagnostics(root string, failure model.CheckResult, outputTokens int) []model.Diagnostic {
	limit := max(1, min(10, (outputTokens-512)/128))
	if outputTokens == 0 {
		limit = 10
	}
	groups := repairGroups(root, failure, limit)
	if len(groups) == 0 {
		return nil
	}
	return groups[0].Diagnostics
}

func (a *App) waitForProvider(ctx context.Context, until time.Time) error {
	delay := time.Until(until)
	if delay <= 0 {
		return nil
	}
	a.UI.Say(fmt.Sprintf("Rate-limit retry authorized. Waiting %.0f seconds before the next request; Ctrl+C cancels.", math.Ceil(delay.Seconds())))
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fail(ExitCancelled, "rate-limit wait cancelled; no retry request sent")
	case <-timer.C:
		return nil
	}
}

func validateProposalTargets(files, editable []string) error {
	allowed := make(map[string]bool, len(editable))
	for _, file := range editable {
		allowed[file] = true
	}
	for _, file := range files {
		if !allowed[file] {
			return fmt.Errorf("proposal targets %s outside the supplied editable source context", file)
		}
	}
	return nil
}

func validateDiagnosticIDs(addressed []string, ds []model.Diagnostic) error {
	known := map[string]bool{}
	for _, d := range ds {
		if d.ID != "" {
			known[d.ID] = true
		}
	}
	seen := map[string]bool{}
	for _, id := range addressed {
		if !known[id] || seen[id] {
			return fmt.Errorf("proposal references an unknown or repeated diagnostic ID: %s", id)
		}
		seen[id] = true
	}
	return nil
}

func (a *App) investigate(ctx context.Context, bundle model.ContextBundle) (*model.Analysis, *model.RepairProposal, error) {
	var analysis *model.Analysis
	var proposal *model.RepairProposal
	err := a.providerProgress(10*time.Second, func() (err error) {
		if combined, ok := a.Provider.(llm.CombinedProvider); ok {
			analysis, proposal, err = combined.InvestigateAndPropose(ctx, bundle)
		} else {
			analysis, err = a.Provider.Analyze(ctx, bundle)
		}
		return err
	})
	return analysis, proposal, err
}

// Join the progress reporter before returning: it cannot write over a prompt
// or keep running after the request has completed.
func (a *App) providerProgress(interval time.Duration, request func() error) error {
	done, stopped := make(chan struct{}), make(chan struct{})
	start := time.Now()
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				a.UI.Say(fmt.Sprintf("AI is preparing a proposed repair (%ds elapsed). No proposal has been applied; Ctrl+C cancels.", int(time.Since(start).Seconds())))
			}
		}
	}()
	defer func() { close(done); <-stopped }()
	return request()
}
