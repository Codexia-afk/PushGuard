package receipt

import (
	"context"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pushguard/pushguard/internal/runner"
)

// PriorRepairs preserves historical approved operations when later developer
// edits change a repaired file's current hash. It never restores a patch or
// grants authority: only intact locally trusted receipts on the same branch's
// verified ancestry are considered. Current checks still verify the new commit.
func priorRepairs(ctx context.Context, root, identity, branch, head string, key ed25519.PublicKey, r runner.Runner) []AIRepairRecord {
	dir, err := repositoryDir(root)
	if err != nil {
		return nil
	}
	dir = filepath.Join(dir, "delivery")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	type candidate struct {
		id string
		at time.Time
	}
	var candidates []candidate
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, e := entry.Info()
		if e != nil || !info.Mode().IsRegular() || info.Size() > 256<<10 {
			continue
		}
		candidates = append(candidates, candidate{strings.TrimSuffix(entry.Name(), ".json"), info.ModTime()})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].at.After(candidates[j].at) })
	if len(candidates) > 100 {
		candidates = candidates[:100]
	}
	keys := map[string]ed25519.PublicKey{Digest(key): key}
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			return nil
		}
		v, e := LoadVerification(root, candidate.id)
		if e != nil || v.Repository != identity || v.Branch != branch || len(v.AIRepairs) == 0 {
			continue
		}
		if len(v.CommitSHA) != 40 && len(v.CommitSHA) != 64 || strings.Trim(v.CommitSHA, "0123456789abcdefABCDEF") != "" {
			continue
		}
		valid := ValidateVerification(v, Expectation{Repository: identity, CommitSHA: v.CommitSHA, Branch: branch, TreeHash: v.TreeHash, ConfigHash: v.ConfigHash, Keys: keys})
		if valid.Status != Valid {
			continue
		}
		if result := r.Run(ctx, root, []string{"git", "merge-base", "--is-ancestor", v.CommitSHA, head}, 10*time.Second); result.ExitCode == 0 {
			return v.AIRepairs
		}
	}
	return nil
}
