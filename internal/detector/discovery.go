package detector

import (
	"context"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pushguard/pushguard/internal/model"
	"github.com/pushguard/pushguard/internal/runner"
)

// Discovery is a bounded, read-only description of the repository's source
// files and project boundaries. It never executes repository code.
type Discovery struct {
	Root       string
	Files      []string // slash-separated, relative to Root
	Languages  map[string]int
	Boundaries []model.ProjectBoundary
	Truncated  bool
	FromGit    bool
}

const maxDiscoveredFiles = 50000

// Directories that never contain first-party source worth verifying.
var ignoredDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true, "node_modules": true, "vendor": true, "dist": true, "build": true,
	"coverage": true, ".venv": true, "venv": true, "__pycache__": true, ".tox": true, ".nox": true,
	".mypy_cache": true, ".pytest_cache": true, ".ruff_cache": true, "target": true, ".next": true,
	".nuxt": true, ".gradle": true, ".idea": true, ".vscode": true, "site-packages": true, ".eggs": true,
	"bower_components": true, ".terraform": true, ".cache": true, ".parcel-cache": true, ".turbo": true,
}

var extensionLanguage = map[string]string{
	".py": "Python", ".pyi": "Python",
	".ts": "TypeScript", ".tsx": "TypeScript", ".mts": "TypeScript", ".cts": "TypeScript",
	".js": "JavaScript", ".jsx": "JavaScript", ".mjs": "JavaScript", ".cjs": "JavaScript",
	".go": "Go", ".rs": "Rust", ".java": "Java", ".kt": "Kotlin", ".kts": "Kotlin",
	".c": "C", ".h": "C", ".cpp": "C++", ".cc": "C++", ".cxx": "C++", ".hpp": "C++", ".hh": "C++", ".hxx": "C++",
	".cs": "C#", ".rb": "Ruby", ".php": "PHP", ".swift": "Swift",
}

// LanguageOf returns the language implied by a file extension, or "".
func LanguageOf(file string) string {
	name := strings.ToLower(path.Base(filepath.ToSlash(file)))
	if strings.HasSuffix(name, ".d.ts") {
		return "TypeScript"
	}
	return extensionLanguage[path.Ext(name)]
}

// Ignored reports whether a relative path lies in a dependency, cache, or build directory.
func Ignored(rel string) bool {
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if ignoredDirs[part] || strings.HasSuffix(part, ".egg-info") {
			return true
		}
	}
	return false
}

// Discover lists candidate files (Git-aware when possible) and derives
// language boundaries from manifests first and source extensions second.
func Discover(ctx context.Context, root string, useGit bool) Discovery {
	d := Discovery{Root: root, Languages: map[string]int{}}
	var files []string
	if useGit {
		res := runner.Runner{}.Run(ctx, root, []string{"git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"}, 30*time.Second)
		if res.ExitCode == 0 && !res.Truncated {
			d.FromGit = true
			for _, f := range strings.Split(res.Stdout, "\x00") {
				if f == "" || Ignored(f) {
					continue
				}
				// Deleted-but-tracked files are not sources to verify.
				if info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(f))); err != nil || !info.Mode().IsRegular() {
					continue
				}
				files = append(files, f)
				if len(files) >= maxDiscoveredFiles {
					d.Truncated = true
					break
				}
			}
		}
	}
	if !d.FromGit {
		files, d.Truncated = walk(ctx, root)
	}
	sort.Strings(files)
	d.Files = files
	for _, f := range files {
		if lang := LanguageOf(f); lang != "" {
			d.Languages[lang]++
		}
	}
	d.Boundaries = boundaries(root, files)
	return d
}

func walk(ctx context.Context, root string) ([]string, bool) {
	var files []string
	truncated := false
	_ = filepath.WalkDir(root, func(p string, entry fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			truncated = true
			return fs.SkipAll
		}
		if err != nil {
			truncated = true
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil || rel == "." {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			name := entry.Name()
			if ignoredDirs[name] || strings.HasSuffix(name, ".egg-info") || fileExists(filepath.Join(p, "pyvenv.cfg")) {
				return fs.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		files = append(files, filepath.ToSlash(rel))
		if len(files) >= maxDiscoveredFiles {
			truncated = true
			return fs.SkipAll
		}
		return nil
	})
	return files, truncated
}

type manifestKind struct {
	kind, language string
}

func manifestFor(base string) (manifestKind, bool) {
	lower := strings.ToLower(base)
	switch lower {
	case "package.json":
		return manifestKind{"Node.js", "JavaScript"}, true
	case "go.mod":
		return manifestKind{"Go", "Go"}, true
	case "pyproject.toml", "setup.py", "setup.cfg", "requirements.txt", "pipfile", "tox.ini", "pytest.ini":
		return manifestKind{"Python", "Python"}, true
	case "cargo.toml":
		return manifestKind{"Rust", "Rust"}, true
	case "pom.xml":
		return manifestKind{"Java/Maven", "Java"}, true
	case "build.gradle", "build.gradle.kts":
		return manifestKind{"Java/Gradle", "Java"}, true
	case "cmakelists.txt", "compile_commands.json":
		return manifestKind{"C/C++", "C/C++"}, true
	}
	if strings.HasSuffix(lower, ".csproj") || strings.HasSuffix(lower, ".sln") {
		return manifestKind{".NET", "C#"}, true
	}
	return manifestKind{}, false
}

// boundaries finds one boundary per manifest directory, collapsing members of
// workspaces that their root manifest already verifies, then adds root-level
// source boundaries for languages that no manifest covers.
func boundaries(root string, files []string) []model.ProjectBoundary {
	byDir := map[string]*model.ProjectBoundary{}
	var order []string
	add := func(dir string, mk manifestKind, indicator string) {
		key := dir + "\x00" + mk.kind
		if b, ok := byDir[key]; ok {
			if !contains(b.Indicators, indicator) {
				b.Indicators = append(b.Indicators, indicator)
			}
			return
		}
		byDir[key] = &model.ProjectBoundary{Dir: dir, Kind: mk.kind, Language: mk.language, Indicators: []string{indicator}}
		order = append(order, key)
	}
	for _, f := range files {
		dir, base := path.Split(f)
		dir = strings.TrimSuffix(dir, "/")
		if dir == "" {
			dir = "."
		}
		if mk, ok := manifestFor(base); ok {
			add(dir, mk, base)
		}
	}
	// Root-level manifests can also be untracked in a fresh directory; include them.
	for _, name := range []string{"package.json", "go.mod", "pyproject.toml", "setup.py", "requirements.txt", "Cargo.toml", "pom.xml", "build.gradle", "build.gradle.kts", "CMakeLists.txt"} {
		if fileExists(filepath.Join(root, name)) {
			mk, _ := manifestFor(name)
			add(".", mk, name)
		}
	}
	var out []model.ProjectBoundary
	for _, key := range order {
		b := *byDir[key]
		if coveredByAncestor(root, b) {
			continue
		}
		if b.Kind == "Node.js" {
			b.Language = nodeLanguage(filepath.Join(root, filepath.FromSlash(b.Dir)))
		}
		out = append(out, b)
	}
	// Count sources per boundary (the deepest boundary of a compatible language owns a file).
	for _, f := range files {
		lang := LanguageOf(f)
		if lang == "" {
			continue
		}
		best := -1
		for i, b := range out {
			if !compatible(b.Language, lang) || !within(f, b.Dir) {
				continue
			}
			if best < 0 || len(b.Dir) > len(out[best].Dir) {
				best = i
			}
		}
		if best >= 0 {
			out[best].Sources++
		}
	}
	// Extension fallback: a language with sources but no manifest still gets a boundary.
	covered := map[string]bool{}
	for _, b := range out {
		covered[b.Language] = true
		if b.Language == "TypeScript" {
			covered["JavaScript"] = true
		}
		if b.Language == "JavaScript" {
			covered["TypeScript"] = true
		}
		if b.Language == "C/C++" {
			covered["C"], covered["C++"] = true, true
		}
	}
	counts := map[string]int{}
	for _, f := range files {
		if lang := LanguageOf(f); lang != "" {
			counts[lang]++
		}
	}
	var fallback []string
	for lang := range counts {
		if !covered[lang] {
			fallback = append(fallback, lang)
		}
	}
	sort.Strings(fallback)
	for _, lang := range fallback {
		out = append(out, model.ProjectBoundary{Dir: ".", Kind: "source", Language: lang, Indicators: []string{"source extensions"}, Sources: counts[lang]})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir == "." || out[j].Dir != "." && out[i].Dir < out[j].Dir
		}
		return false
	})
	return out
}

func compatible(boundary, lang string) bool {
	switch boundary {
	case "JavaScript", "TypeScript":
		return lang == "JavaScript" || lang == "TypeScript"
	case "C/C++":
		return lang == "C" || lang == "C++"
	case "Java":
		return lang == "Java" || lang == "Kotlin"
	}
	return boundary == lang
}

func within(file, dir string) bool {
	return dir == "." || strings.HasPrefix(file, dir+"/")
}

// coveredByAncestor collapses workspace members whose root manifest owns verification.
func coveredByAncestor(root string, b model.ProjectBoundary) bool {
	if b.Dir == "." {
		return false
	}
	for dir := path.Dir(b.Dir); ; dir = path.Dir(dir) {
		abs := filepath.Join(root, filepath.FromSlash(dir))
		switch b.Kind {
		case "Node.js":
			// A workspace declaration does not prove that root scripts run any
			// particular member check. Retain each member's verification plan.
		case "Rust":
			if hasText(filepath.Join(abs, "Cargo.toml"), "[workspace]") {
				return true
			}
		case "Java/Maven":
			if fileExists(filepath.Join(abs, "pom.xml")) {
				return true
			}
		case "Java/Gradle":
			if fileExists(filepath.Join(abs, "settings.gradle")) || fileExists(filepath.Join(abs, "settings.gradle.kts")) {
				return true
			}
		case "Python":
			for _, name := range []string{"pyproject.toml", "setup.py", "setup.cfg"} {
				if fileExists(filepath.Join(abs, name)) {
					return true
				}
			}
		case ".NET":
			if matches, _ := filepath.Glob(filepath.Join(abs, "*.sln")); len(matches) > 0 {
				return true
			}
		}
		if dir == "." || dir == "/" || dir == "" {
			return false
		}
	}
}

func nodeLanguage(dir string) string {
	if fileExists(filepath.Join(dir, "tsconfig.json")) || hasText(filepath.Join(dir, "package.json"), `"typescript"`) {
		return "TypeScript"
	}
	return "JavaScript"
}

// FilesFor returns discovered source files of a language within a boundary directory.
func (d Discovery) FilesFor(dir string, languages ...string) []string {
	var out []string
	for _, f := range d.Files {
		if !within(f, dir) {
			continue
		}
		lang := LanguageOf(f)
		for _, want := range languages {
			if lang == want {
				out = append(out, f)
				break
			}
		}
	}
	return out
}

// Summary lists languages with source counts, most common first.
func (d Discovery) Summary() []string {
	type pair struct {
		lang  string
		count int
	}
	var pairs []pair
	for lang, count := range d.Languages {
		pairs = append(pairs, pair{lang, count})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].count != pairs[j].count {
			return pairs[i].count > pairs[j].count
		}
		return pairs[i].lang < pairs[j].lang
	})
	out := make([]string, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, p.lang)
	}
	return out
}

func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
