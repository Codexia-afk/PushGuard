package diagnostics

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/pushguard/pushguard/internal/model"
)

func identity(d model.Diagnostic) string {
	return strings.Join([]string{d.Check, d.Tool, d.Category, d.Location.File, d.Rule, d.Code, d.TestName, d.Message}, "\x00")
}

// IDs intentionally exclude line/column: an insertion moving an unchanged
// diagnostic must not fabricate progress. Repeated equivalent occurrences get
// deterministic ordinals; reconciliation compares their multiplicity.
func Normalize(check model.Check, ds []model.Diagnostic) []model.Diagnostic {
	seen := map[string]int{}
	for i := range ds {
		d := &ds[i]
		d.Check = check.Name
		key := identity(*d)
		seen[key]++
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d", key, seen[key])))
		d.ID = "D-" + hex.EncodeToString(sum[:8])
	}
	return ds
}
func Reconcile(before, after model.CheckResult) model.DiagnosticProgress {
	p := model.DiagnosticProgress{Before: len(before.Diagnostics), After: len(after.Diagnostics), Complete: !before.DiagnosticsLimited && !after.DiagnosticsLimited && !before.Command.Truncated && !after.Command.Truncated}
	if !p.Complete {
		return p
	}
	counts := map[string]int{}
	for _, d := range before.Diagnostics {
		counts[identity(d)]++
	}
	for _, d := range after.Diagnostics {
		key := identity(d)
		if counts[key] > 0 {
			p.Remaining++
			counts[key]--
		} else {
			p.New++
		}
	}
	p.Resolved = p.Before - p.Remaining
	return p
}
