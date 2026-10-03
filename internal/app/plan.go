package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/pushguard/pushguard/internal/detector"
	"github.com/pushguard/pushguard/internal/model"
)

// buildPlan discovers languages/project boundaries and builds the verification
// plan (configured checks remain authoritative when present).
func (a *App) buildPlan(ctx context.Context) error {
	plan, err := detector.Build(ctx, a.Root, a.Cfg, detector.Options{Git: a.Repo.GitDir != "", Targets: a.Targets, Tools: a.Tools})
	if err != nil {
		return fail(ExitConfig, err.Error())
	}
	a.Plan = plan
	a.ContextBuilder.Files = plan.Discovery.Files
	a.Checks = plan.Checks
	a.Repo.Project.Boundaries = plan.Discovery.Boundaries
	a.Repo.Project.SourceFiles = 0
	for _, n := range plan.Discovery.Languages {
		a.Repo.Project.SourceFiles += n
	}
	for _, lang := range plan.Discovery.Summary() {
		known := false
		for _, existing := range a.Repo.Project.Languages {
			known = known || existing == lang
		}
		if !known {
			a.Repo.Project.Languages = append(a.Repo.Project.Languages, lang)
		}
	}
	return nil
}

// repairState binds repair approvals to repository state: the Git fingerprint
// when available, otherwise (standalone verification) a content hash of every
// discovered source file plus the configuration.
func (a *App) repairState(ctx context.Context) (model.StateFingerprint, error) {
	if a.Repo.GitDir != "" && a.Repo.HEAD != "" {
		return a.fingerprint(ctx)
	}
	h := sha256.New()
	fmt.Fprintln(h, a.Report.ConfigHash)
	// Re-enumerate names: a new untracked source/config file must invalidate an
	// approval just as modifying an existing file does. Contents are never cached.
	discovery := detector.Discover(ctx, a.Root, false)
	if ctx.Err() != nil {
		return model.StateFingerprint{}, ctx.Err()
	}
	if discovery.Truncated {
		return model.StateFingerprint{}, fail(ExitStateChanged, "standalone state discovery is incomplete")
	}
	files := append([]string(nil), discovery.Files...)
	files = append(files, a.Targets...)
	sort.Strings(files)
	for _, f := range files {
		hash, err := FileHash(a.Root, f)
		if err != nil {
			return model.StateFingerprint{}, fail(ExitStateChanged, err.Error())
		}
		fmt.Fprintln(h, f, hash)
	}
	return model.StateFingerprint{Value: hex.EncodeToString(h.Sum(nil))}, nil
}
