package context

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/pushguard/pushguard/internal/model"
	"github.com/pushguard/pushguard/internal/security"
)

type DiagnosticGroup struct {
	ID          string
	Family      string
	Files       []string
	Diagnostics []model.Diagnostic
}

var relativeImport = regexp.MustCompile(`(?:from\s+|require\(\s*|import\s*)["'](\.[^"']+)["']`)

func diagnosticFamily(d model.Diagnostic) string {
	if d.Category == "test" {
		return "test:" + d.TestName
	}
	rule := d.Rule
	if rule == "" {
		rule = d.Code
	}
	switch rule {
	case "no-var", "prefer-const", "quotes", "semi", "prettier/prettier", "indent":
		return "style"
	case "no-unused-vars", "@typescript-eslint/no-unused-vars", "TS6133", "TS6196":
		return "unused-symbol"
	}
	if strings.HasPrefix(rule, "TS23") || strings.HasPrefix(rule, "TS27") {
		return "type-compatibility"
	}
	if rule == "" {
		return d.Category
	}
	return rule
}
func relative(root, file string) string {
	if filepath.IsAbs(file) {
		if rel, err := filepath.Rel(root, file); err == nil {
			file = rel
		}
	}
	return filepath.ToSlash(filepath.Clean(file))
}

// PlanGroups is deterministic: check -> file/region -> family, then bounded
// joining of directly importing files in the same family. It never infers tool
// locations or assumes a diagnostic count is the number of underlying bugs.
func PlanGroups(root string, ds []model.Diagnostic, limit int) []DiagnosticGroup {
	if limit < 1 {
		limit = 10
	}
	limit = min(limit, 50)
	ordered := append([]model.Diagnostic(nil), ds...)
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if a.Check != b.Check {
			return a.Check < b.Check
		}
		// A summary without a path cannot identify a repair target. Keep it as
		// evidence, after diagnostics that actually identify source files.
		if (a.Location.File == "") != (b.Location.File == "") {
			return a.Location.File != ""
		}
		if a.Location.File != b.Location.File {
			return a.Location.File < b.Location.File
		}
		if diagnosticFamily(a) != diagnosticFamily(b) {
			return diagnosticFamily(a) < diagnosticFamily(b)
		}
		return a.Location.Line < b.Location.Line
	})
	var groups []DiagnosticGroup
	for _, d := range ordered {
		file, family := relative(root, d.Location.File), diagnosticFamily(d)
		join := -1
		if len(groups) > 0 {
			g := groups[len(groups)-1]
			last := g.Diagnostics[len(g.Diagnostics)-1]
			if len(g.Diagnostics) < limit && last.Check == d.Check && g.Family == family && g.Files[0] == file && d.Location.Line-last.Location.Line <= 60 {
				join = len(groups) - 1
			}
		}
		if join < 0 {
			groups = append(groups, DiagnosticGroup{Family: family, Files: []string{file}, Diagnostics: []model.Diagnostic{d}})
		} else {
			groups[join].Diagnostics = append(groups[join].Diagnostics, d)
		}
	}
	imports := map[string]map[string]bool{}
	for _, g := range groups {
		for _, file := range g.Files {
			if _, ok := imports[file]; ok {
				continue
			}
			imports[file] = map[string]bool{}
			data, err := security.RepositoryFile(root, file)
			if err != nil || len(data) > 128<<10 {
				continue
			}
			for _, m := range relativeImport.FindAllStringSubmatch(string(data), 40) {
				target := filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(file), m[1])))
				imports[file][target] = true
				imports[file][strings.TrimSuffix(target, filepath.Ext(target))+".ts"] = true
			}
		}
	}
	for i := 0; i < len(groups); i++ {
		if len(groups[i].Diagnostics) == 0 {
			continue
		}
		for j := i + 1; j < len(groups); j++ {
			a, b := &groups[i], &groups[j]
			if len(b.Diagnostics) == 0 || a.Family != b.Family || a.Diagnostics[0].Check != b.Diagnostics[0].Check || len(a.Diagnostics)+len(b.Diagnostics) > limit || len(a.Files) >= 3 {
				continue
			}
			linked := false
			for _, left := range a.Files {
				for _, right := range b.Files {
					linked = linked || imports[left][right] || imports[right][left]
				}
			}
			if linked {
				a.Diagnostics = append(a.Diagnostics, b.Diagnostics...)
				a.Files = append(a.Files, b.Files...)
				b.Diagnostics = nil
			}
		}
	}
	var out []DiagnosticGroup
	for _, g := range groups {
		if len(g.Diagnostics) == 0 {
			continue
		}
		var ids []string
		for _, d := range g.Diagnostics {
			ids = append(ids, d.ID+"/"+d.Location.File+"/"+d.Message)
		}
		sum := sha256.Sum256([]byte(strings.Join(ids, "\x00")))
		g.ID = "G-" + hex.EncodeToString(sum[:6])
		out = append(out, g)
	}
	return out
}
