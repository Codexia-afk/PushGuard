package context

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pushguard/pushguard/internal/detector"
	"github.com/pushguard/pushguard/internal/model"
	"github.com/pushguard/pushguard/internal/patch"
	"github.com/pushguard/pushguard/internal/security"
)

var symbolName = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]{0,99}$`)

// Expand reads at most four requested source files and searches at most 200
// indexed files (2 MiB total) for exact symbol tokens. Requests cannot execute
// commands, traverse directories, follow symlinks or select sensitive files.
func (b Builder) Expand(ctx context.Context, root string, in model.ContextBundle, request model.ContextRequest) (model.ContextBundle, error) {
	if len(request.Files) > 4 || len(request.Symbols) > 4 || len(request.Files)+len(request.Symbols) == 0 || len(request.Reason) > 1000 {
		return in, fmt.Errorf("context request exceeds file/symbol bounds or is empty")
	}
	for _, symbol := range request.Symbols {
		if !symbolName.MatchString(symbol) {
			return in, fmt.Errorf("context symbol must be an identifier")
		}
	}
	validPath := func(file string) bool {
		return file != "" && !filepath.IsAbs(file) && filepath.ToSlash(filepath.Clean(file)) == file && file != ".." && !strings.HasPrefix(file, "../") && !strings.ContainsAny(file, "\\:*?\x00\n\r") && !detector.Ignored(file) && detector.LanguageOf(file) != ""
	}
	files := append([]string(nil), request.Files...)
	for _, file := range files {
		if !validPath(file) {
			return in, fmt.Errorf("unsafe or unsupported context path: %s", file)
		}
	}
	if len(request.Symbols) > 0 {
		bytesRead := 0
		for i, file := range b.Files {
			if ctx.Err() != nil {
				return in, ctx.Err()
			}
			if i >= 200 || bytesRead >= 2<<20 || len(files) >= 4 {
				break
			}
			if !validPath(file) {
				continue
			}
			data, err := security.RepositoryFile(root, file)
			if err != nil {
				continue
			}
			bytesRead += len(data)
			for _, symbol := range request.Symbols {
				if regexp.MustCompile(`\b` + regexp.QuoteMeta(symbol) + `\b`).Match(data) {
					found := false
					for _, old := range files {
						found = found || old == file
					}
					if !found {
						files = append(files, file)
					}
					break
				}
			}
		}
	}
	if len(files) == 0 {
		return in, fmt.Errorf("requested symbols were not found within the bounded source index")
	}
	out := in
	out.SourceFiles = append([]model.ContextFile(nil), in.SourceFiles...)
	for _, file := range files {
		if ctx.Err() != nil {
			return in, ctx.Err()
		}
		data, err := security.RepositoryFile(root, file)
		if err != nil {
			return in, fmt.Errorf("requested context %s withheld or unavailable: %w", file, err)
		}
		points := []int{}
		for _, symbol := range request.Symbols {
			if loc := regexp.MustCompile(`\b` + regexp.QuoteMeta(symbol) + `\b`).FindIndex(data); loc != nil {
				points = append(points, strings.Count(string(data[:loc[0]]), "\n")+1)
			}
		}
		if len(points) == 0 {
			points = []int{1}
		}
		windows := sourceWindows(file, string(data), points)
		if len(windows) == 0 {
			return in, fmt.Errorf("requested context window exceeds source limits")
		}
		kept := []model.ContextFile{}
		for _, existing := range out.SourceFiles {
			if existing.File != file {
				kept = append(kept, existing)
			}
		}
		for i := range windows {
			windows[i].Content = security.Redact(windows[i].Content)
			windows[i].Editable = patch.EditableFile(root, file)
			windows[i].FocusLines = append([]int(nil), points...)
		}
		out.SourceFiles = append(windows, kept...)
	}
	used := 0
	out.EditableFiles = nil
	seen := map[string]bool{}
	for _, file := range out.SourceFiles {
		used += len(file.Content)
		if file.Editable && !seen[file.File] {
			out.EditableFiles = append(out.EditableFiles, file.File)
			seen[file.File] = true
		}
	}
	if used > sourceBudget || len(out.SourceFiles) > 16 {
		return in, fmt.Errorf("expanded context exceeds the source window budget")
	}
	return out, nil
}

func pythonImports(file, source string) []string {
	var out []string
	re := regexp.MustCompile(`(?m)^\s*(?:from\s+([.A-Za-z_][.A-Za-z0-9_]*)\s+import|import\s+([A-Za-z_][.A-Za-z0-9_]*))`)
	for _, m := range re.FindAllStringSubmatch(source, 30) {
		module := m[1]
		if module == "" {
			module = m[2]
		}
		base := ""
		if strings.HasPrefix(module, ".") {
			base = filepath.Dir(file)
			module = module[1:]
			for strings.HasPrefix(module, ".") {
				base = filepath.Dir(base)
				module = module[1:]
			}
		}
		stem := filepath.Join(base, strings.ReplaceAll(module, ".", string(filepath.Separator)))
		for _, prefix := range []string{"", "src"} {
			out = append(out, filepath.ToSlash(filepath.Join(prefix, stem)+".py"), filepath.ToSlash(filepath.Join(prefix, stem, "__init__.py")))
		}
	}
	return out
}
