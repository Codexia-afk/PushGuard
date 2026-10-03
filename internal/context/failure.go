package context

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pushguard/pushguard/internal/model"
	"github.com/pushguard/pushguard/internal/patch"
	"github.com/pushguard/pushguard/internal/security"
)

const sourceBudget = 48 << 10

// BuildFailure assembles the selected bounded diagnostic group. Small
// affected files are included whole; large ones have merged windows around each
// reported location, rather than losing later diagnostics in the same file.
func (b Builder) BuildFailure(ctx context.Context, root string, repo *model.Repository, primary model.Diagnostic, group []model.Diagnostic) model.ContextBundle {
	bundle := b.build(ctx, root, repo, primary, false)
	if len(group) > 100 {
		group = group[:100]
	}
	bundle.Diagnostics = append([]model.Diagnostic(nil), group...)
	for i := range bundle.Diagnostics {
		d := &bundle.Diagnostics[i]
		d.Message, d.RawOutput, d.Command = security.Redact(d.Message), security.Redact(d.RawOutput), security.Redact(d.Command)
		// The primary diagnostic retains a bounded raw evidence excerpt. Do not
		// repeat that same log blob once per diagnostic in the failure group.
		d.RawOutput = ""
	}
	locations := map[string][]int{}
	var files []string
	add := func(file string, line int) {
		if file == "" {
			return
		}
		if filepath.IsAbs(file) {
			rel, err := filepath.Rel(root, file)
			if err != nil {
				return
			}
			file = rel
		}
		file = filepath.ToSlash(filepath.Clean(file))
		if file == ".." || strings.HasPrefix(file, "../") {
			return
		}
		if _, exists := locations[file]; !exists {
			files = append(files, file)
		}
		locations[file] = append(locations[file], line)
	}
	add(primary.Location.File, primary.Location.Line)
	for _, d := range group {
		add(d.Location.File, d.Location.Line)
	}
	affected := len(files)
	// Resolve from complete, unformatted files, including the primary. The
	// assertion window in a long test usually omits its top-level imports.
	if primary.Category != "lint" {
		for _, file := range files[:min(len(files), 4)] {
			if data, err := security.RepositoryFile(root, file); err == nil {
				d := model.Diagnostic{Location: model.SourceLocation{File: file, Line: locations[file][0]}, Message: primary.Message}
				for _, related := range b.findRelated(ctx, root, repo, d, string(data)) {
					add(related.file, related.line)
				}
			}
		}
	}
	// The authoritative sourceFiles contain exact source bytes without display
	// prefixes. Avoid resending duplicate snippets in the legacy display fields.
	bundle.Source, bundle.Related = "", nil
	remaining := sourceBudget
	for index, file := range files {
		if ctx.Err() != nil || index >= 8 || remaining <= 0 {
			bundle.Metadata["omittedSourceFiles"] = fmt.Sprint(len(files) - index)
			break
		}
		data, err := security.RepositoryFile(root, file)
		if err != nil {
			bundle.Related = append(bundle.Related, file+": source withheld or unavailable")
			continue
		}
		editable := patch.EditableFile(root, file)
		windows := sourceWindows(file, string(data), locations[file])
		included := false
		for _, window := range windows {
			window.Content = security.Redact(window.Content)
			if len(window.Content) > remaining {
				bundle.Metadata["sourceBudget"] = "48 KiB reached; some windows omitted"
				continue
			}
			remaining -= len(window.Content)
			window.Editable = editable
			for _, line := range locations[file] {
				if line >= window.StartLine && line <= window.EndLine {
					window.FocusLines = append(window.FocusLines, line)
				}
			}
			bundle.SourceFiles = append(bundle.SourceFiles, window)
			included = true
		}
		if included && editable {
			bundle.EditableFiles = append(bundle.EditableFiles, file)
		}
	}
	bundle.Metadata["failureGroup"] = fmt.Sprintf("%d verified diagnostics across %d reported files", len(group), affected)
	if affected > 0 {
		args := append([]string{"git", "diff", "--no-ext-diff", "--no-textconv", "HEAD", "--"}, files[:affected]...)
		r := b.Runner.Run(ctx, root, args, 15*time.Second)
		if r.ExitCode == 0 {
			bundle.Diff = security.Redact(clip(r.Stdout, 6000))
		}
	}
	bundle.SourceFiles = append(bundle.SourceFiles, projectRules(root, files[:affected])...)
	return bundle
}

func sourceWindows(file, source string, reported []int) []model.ContextFile {
	lines := strings.SplitAfter(source, "\n")
	if len(source) <= 6000 && len(lines) <= 70 {
		return []model.ContextFile{{File: file, Content: source, StartLine: 1, EndLine: len(lines)}}
	}
	points := append([]int(nil), reported...)
	sort.Ints(points)
	type span struct{ start, end int }
	var spans []span
	for _, point := range points {
		if point <= 0 {
			point = 1
		}
		if point > len(lines) {
			continue
		}
		s := span{max(0, point-26), min(len(lines), point+25)}
		// Include the containing top-level declaration when it remains bounded.
		for i := point - 1; i >= max(0, point-51); i-- {
			line := lines[i]
			if strings.HasPrefix(line, "export ") || strings.HasPrefix(line, "function ") || strings.HasPrefix(line, "class ") || strings.HasPrefix(line, "func ") || strings.HasPrefix(line, "type ") || strings.HasPrefix(strings.TrimSpace(line), "def ") || strings.HasPrefix(strings.TrimSpace(line), "async def ") {
				s.start = min(s.start, i)
				break
			}
		}
		if len(spans) > 0 && s.start <= spans[len(spans)-1].end {
			spans[len(spans)-1].end = max(spans[len(spans)-1].end, s.end)
		} else {
			spans = append(spans, s)
		}
	}
	var out []model.ContextFile
	for _, span := range spans {
		text := strings.Join(lines[span.start:span.end], "")
		if len(text) > 12<<10 {
			continue
		} // never split an anchor in mid-line
		out = append(out, model.ContextFile{File: file, Content: text, StartLine: span.start + 1, EndLine: span.end, Truncated: true})
	}
	return out
}

// Configuration is read-only evidence. Never execute imported ESLint/Prettier
// JavaScript to obtain rules, and never offer these files as patch targets.
func projectRules(root string, affected []string) []model.ContextFile {
	dirs := []string{"."}
	seenDirs := map[string]bool{".": true}
	for _, file := range affected {
		for dir := filepath.Dir(file); dir != "." && dir != ""; dir = filepath.Dir(dir) {
			if !seenDirs[dir] {
				dirs = append(dirs, dir)
				seenDirs[dir] = true
			}
		}
	}
	var out []model.ContextFile
	budget := 16 << 10
	for _, dir := range dirs {
		for _, name := range []string{"package.json", "eslint.config.js", "eslint.config.mjs", "eslint.config.cjs", ".eslintrc.json", ".eslintrc.js", ".eslintrc.cjs", ".eslintrc.yaml", ".eslintrc.yml", ".prettierrc", ".prettierrc.json", "prettier.config.js", "prettier.config.cjs", "tsconfig.json", "pyproject.toml", "ruff.toml", ".ruff.toml", "mypy.ini", "pytest.ini", "go.mod", "Cargo.toml"} {
			if len(out) >= 12 {
				return out
			}
			file := filepath.ToSlash(filepath.Join(dir, name))
			data, err := security.RepositoryFile(root, file)
			if err != nil {
				continue
			}
			if name == "package.json" {
				var manifest map[string]json.RawMessage
				if json.Unmarshal(data, &manifest) != nil {
					continue
				}
				selected := map[string]json.RawMessage{}
				for _, key := range []string{"name", "type", "scripts", "packageManager", "engines", "dependencies", "devDependencies", "eslintConfig", "prettier"} {
					if value, exists := manifest[key]; exists {
						selected[key] = value
					}
				}
				data, _ = json.MarshalIndent(selected, "", "  ")
			}
			if len(data) > 8<<10 || len(data) > budget {
				continue
			}
			content := security.Redact(string(data))
			budget -= len(data)
			out = append(out, model.ContextFile{File: file, Content: content, Editable: false})
		}
	}
	return out
}
