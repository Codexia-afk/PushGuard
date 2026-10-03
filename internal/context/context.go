package context

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/pushguard/pushguard/internal/model"
	"github.com/pushguard/pushguard/internal/runner"
	"github.com/pushguard/pushguard/internal/security"
)

type Builder struct {
	Files  []string // session discovery index; contents are always freshly read
	Runner runner.Runner
	Redact bool
	Radius int
}

func (b Builder) Build(ctx context.Context, root string, repo *model.Repository, d model.Diagnostic) model.ContextBundle {
	return b.build(ctx, root, repo, d, true)
}

func (b Builder) build(ctx context.Context, root string, repo *model.Repository, d model.Diagnostic, related bool) model.ContextBundle {
	if b.Radius <= 0 {
		b.Radius = 30
	}
	if b.Redact {
		d.RawOutput = redact(d.RawOutput, true)
		d.Message = redact(d.Message, true)
	}
	d.RawOutput = clip(d.RawOutput, 4000)
	d.Message = clip(d.Message, 1000)
	d.Command = clip(d.Command, 500)
	bundle := model.ContextBundle{Diagnostic: d, Metadata: map[string]string{"repository": repo.Project.Name, "reportedLocation": "unavailable", "languages": strings.Join(repo.Project.Languages, ","), "frameworks": strings.Join(repo.Project.Frameworks, ","), "packageManager": repo.Project.PackageManager, "changedFiles": clip(strings.Join(repo.Changes.All, ","), 2000)}}
	if d.Location.File != "" {
		bundle.Metadata["reportedLocation"] = fmt.Sprintf("%s:%d:%d", d.Location.File, d.Location.Line, d.Location.Column)
		if !sensitive(d.Location.File) {
			path := d.Location.File
			if !filepath.IsAbs(path) {
				path = filepath.Join(root, path)
			}
			if safeRepositoryPath(root, path) {
				if data, err := security.RepositoryFile(root, path); err == nil {
					bundle.Source = redact(limitContext(string(data), d.Location.Line, b.Radius), b.Redact)
				}
			}
		}
	}
	if d.Location.File != "" && !sensitive(d.Location.File) {
		res := b.Runner.Run(ctx, root, []string{"git", "diff", "--no-ext-diff", "--no-textconv", "HEAD", "--", d.Location.File}, 15_000_000_000)
		bundle.Diff = redact(res.Stdout, b.Redact)
	}
	// Lint diagnostics point to their repair targets directly. Sending imported
	// services for a quotes/no-var error wastes tokens and invites unrelated edits.
	if related && d.Category != "lint" {
		if data, err := security.RepositoryFile(root, d.Location.File); err == nil {
			for _, file := range b.findRelated(ctx, root, repo, d, string(data)) {
				bundle.Related = append(bundle.Related, file.file+" (candidate related definition)\n"+file.snippet)
			}
		}
	}
	if len(bundle.Source) > 8000 {
		bundle.Source = bundle.Source[:8000]
	}
	if len(bundle.Diff) > 4000 {
		bundle.Diff = bundle.Diff[:4000]
	}
	return bundle
}
func limitContext(src string, line, radius int) string {
	lines := strings.Split(src, "\n")
	if line <= 0 {
		return strings.Join(lines[:min(len(lines), radius*2+1)], "\n")
	}
	start := line - radius - 1
	if start < 0 {
		start = 0
	}
	if start >= len(lines) {
		return "Reported line is outside the available source."
	}
	end := line + radius
	if end > len(lines) {
		end = len(lines)
	}
	out := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		out = append(out, fmt.Sprintf("%4d | %s", i+1, lines[i]))
	}
	return strings.Join(out, "\n")
}

type relatedFile struct {
	file, snippet string
	line          int
	matched       bool
}

func (b Builder) findRelated(ctx context.Context, root string, repo *model.Repository, d model.Diagnostic, source string) []relatedFile {
	if d.Location.File == "" {
		return nil
	}
	var candidates []string
	// Resolve literal local imports before broad symbol candidates. This also
	// connects JavaScript/ESM tests with TypeScript application implementations.
	imports := relativeImport.FindAllStringSubmatch(source, 30)
	imported := map[string]bool{}
	for _, match := range imports {
		file := filepath.Clean(filepath.Join(filepath.Dir(d.Location.File), match[1]))
		if filepath.IsAbs(file) {
			if rel, err := filepath.Rel(root, file); err == nil {
				file = rel
			}
		}
		if file == ".." || strings.HasPrefix(file, ".."+string(filepath.Separator)) {
			continue
		}
		base := strings.TrimSuffix(file, filepath.Ext(file))
		for _, candidate := range []string{file, base + ".ts", base + ".tsx", file + ".ts", file + ".js", filepath.Join(file, "index.ts"), filepath.Join(file, "index.js")} {
			candidate = filepath.ToSlash(candidate)
			imported[candidate] = true
			candidates = append(candidates, candidate)
		}
	}
	if filepath.Ext(d.Location.File) == ".py" {
		for _, file := range pythonImports(d.Location.File, source) {
			imported[file] = true
			candidates = append(candidates, file)
		}
	}
	candidates = append(candidates, repo.Changes.All...)
	if b.Files != nil {
		candidates = append(candidates, b.Files...)
	} else {
		result := b.Runner.Run(ctx, root, []string{"git", "ls-files", "-z"}, 15_000_000_000)
		if result.ExitCode == 0 && !result.Truncated {
			candidates = append(candidates, strings.Split(result.Stdout, "\x00")...)
		}
	}
	symbols := regexp.MustCompile(`\b[A-Za-z_][A-Za-z0-9_]{2,}\s*\(`).FindAllString(limitContext(source, d.Location.Line, 25), 100)
	// Include imported names as well, so aliased calls can locate declarations
	// beyond the beginning of a long implementation file.
	for _, declaration := range regexp.MustCompile(`(?s)\bimport\s+(.+?)\s+from\s*["']`).FindAllStringSubmatch(source, 30) {
		symbols = append(symbols, regexp.MustCompile(`\b[A-Za-z_$][A-Za-z0-9_$]{2,}\b`).FindAllString(declaration[1], 30)...)
	}
	for _, declaration := range regexp.MustCompile(`(?m)^\s*from\s+[.\w]+\s+import\s+([^\n]+)`).FindAllStringSubmatch(source, 30) {
		symbols = append(symbols, regexp.MustCompile(`\b[A-Za-z_][A-Za-z0-9_]*\b`).FindAllString(declaration[1], 30)...)
	}
	symbols = append(symbols, regexp.MustCompile(`\b[A-Z][A-Za-z0-9_]{2,}\b`).FindAllString(d.Message, 30)...)
	var out []relatedFile
	seen := map[string]bool{}
	for _, file := range candidates {
		if ctx.Err() != nil {
			break
		}
		if len(seen) > 200 {
			break
		}
		if file == "" || file == d.Location.File || seen[file] || sensitive(file) || !imported[file] && filepath.Ext(file) != filepath.Ext(d.Location.File) {
			continue
		}
		seen[file] = true
		path := filepath.Join(root, filepath.FromSlash(file))
		if !safeRepositoryPath(root, path) {
			continue
		}
		info, err := os.Stat(path)
		if err != nil || info.Size() > 1<<20 || !info.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		src := string(data)
		line := 1
		found := imported[file]
		matched := false
		for _, symbol := range symbols {
			symbol = strings.TrimSpace(strings.TrimSuffix(symbol, "("))
			if symbol == "expect" || symbol == "describe" || symbol == "test" || symbol == "require" || symbol == "function" || symbol == "import" || symbol == "type" {
				continue
			}
			if idx := strings.Index(src, symbol); idx >= 0 {
				line = strings.Count(src[:idx], "\n") + 1
				found = true
				matched = true
				break
			}
		}
		if !found {
			continue
		}
		snippet := limitContext(src, line, 8)
		if len(snippet) > 1000 {
			snippet = snippet[:1000]
		}
		out = append(out, relatedFile{file: file, line: line, snippet: security.Redact(snippet), matched: matched})
	}
	// Keep explicit imports ahead of broad symbol guesses, and implementations
	// with matching symbols ahead of unrelated imported types within that set.
	sort.SliceStable(out, func(i, j int) bool {
		if imported[out[i].file] != imported[out[j].file] {
			return imported[out[i].file]
		}
		return out[i].matched && !out[j].matched
	})
	// When imports resolve, broad substring guesses add unrelated services (for
	// example every file mentioning Ticket). Keep guesses only as a fallback.
	if len(out) > 0 && imported[out[0].file] {
		end := 0
		for end < len(out) && imported[out[end].file] {
			end++
		}
		out = out[:end]
	}
	if len(out) > 5 {
		out = out[:5]
	}
	return out
}
func sensitive(path string) bool { return security.SensitivePath(path) }
func redact(s string, enabled bool) string {
	if enabled {
		return security.Redact(s)
	}
	return s
}
func safeRepositoryPath(root, path string) bool {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(realRoot, realPath)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n] + "\n[context truncated]"
	}
	return s
}
