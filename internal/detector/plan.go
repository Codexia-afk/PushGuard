package detector

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/pushguard/pushguard/internal/config"
	"github.com/pushguard/pushguard/internal/model"
)

// Options tune plan construction. Zero values select the real machine.
type Options struct {
	Git     bool
	Targets []string // explicit files (relative to root) for standalone file checks
	Tools   Tools
	// Discovery may be supplied to avoid rescanning within one session.
	Discovery *Discovery
}

// Plan is the deterministic verification plan plus the evidence behind it.
type Plan struct {
	Checks    []model.Check
	Discovery Discovery
	Notes     []string
}

// Checks keeps the historical entry point: configured checks or a discovered plan.
func Checks(root string, cfg config.Config) ([]model.Check, error) {
	plan, err := Build(context.Background(), root, cfg, Options{Git: fileExists(filepath.Join(root, ".git"))})
	return plan.Checks, err
}

// Build returns configured checks unchanged (the project owns its plan) or
// constructs real tool checks for every discovered project boundary.
func Build(ctx context.Context, root string, cfg config.Config, opts Options) (Plan, error) {
	if opts.Tools == nil {
		opts.Tools = SystemTools{}
	}
	var plan Plan
	if opts.Discovery != nil {
		plan.Discovery = *opts.Discovery
	} else {
		plan.Discovery = Discover(ctx, root, opts.Git)
	}
	if len(cfg.Checks) > 0 {
		plan.Checks = append([]model.Check(nil), cfg.Checks...)
		plan.Notes = append(plan.Notes, "Using checks configured in PushGuard configuration.")
		return plan, nil
	}
	if ctx.Err() != nil {
		return plan, ctx.Err()
	}
	if plan.Discovery.Truncated {
		return plan, fmt.Errorf("source discovery is incomplete; configure explicit checks before verification")
	}
	for _, boundary := range plan.Discovery.Boundaries {
		if boundary.Kind != "Node.js" {
			continue
		}
		file := filepath.Join(root, filepath.FromSlash(boundary.Dir), "package.json")
		data, err := os.ReadFile(file)
		var manifest map[string]json.RawMessage
		if err != nil || json.Unmarshal(data, &manifest) != nil || manifest == nil {
			return plan, fmt.Errorf("%s is unavailable or invalid JSON", file)
		}
	}
	b := builder{root: root, tools: opts.Tools, d: plan.Discovery}
	if len(opts.Targets) > 0 {
		b.targets(opts.Targets)
	} else {
		b.repository()
	}
	if opts.Git && len(opts.Targets) == 0 {
		required := len(b.checks) == 0
		b.add(model.Check{Name: "repository sanity", Category: "generic", Args: []string{"git", "diff", "--check"}, Required: required, Description: "whitespace errors and conflict markers in local changes"})
	}
	plan.Checks = b.ordered()
	plan.Notes = append(plan.Notes, b.notes...)
	if plan.Discovery.Truncated {
		plan.Notes = append(plan.Notes, fmt.Sprintf("Discovery stopped after %d files; configure checks explicitly for very large repositories.", maxDiscoveredFiles))
	}
	return plan, nil
}

type builder struct {
	root   string
	tools  Tools
	d      Discovery
	checks []model.Check
	notes  []string
}

func (b *builder) add(c model.Check) {
	c.Discovered = true
	for _, existing := range b.checks {
		if existing.Name == c.Name {
			return
		}
	}
	b.checks = append(b.checks, c)
}

func (b *builder) note(format string, args ...any) {
	b.notes = append(b.notes, fmt.Sprintf(format, args...))
}

func prefix(dir, name string) string {
	if dir == "." || dir == "" {
		return name
	}
	return dir + ": " + name
}

func workdir(dir string) string {
	if dir == "." {
		return ""
	}
	return dir
}

func (b *builder) repository() {
	pythonDone, jsDone := false, false
	for _, boundary := range b.d.Boundaries {
		switch boundary.Kind {
		case "Python":
			b.python(boundary)
		case "Node.js":
			b.node(boundary)
		case "Go":
			b.golang(boundary)
		case "Rust":
			b.rust(boundary)
		case "Java/Maven":
			b.add(model.Check{Name: prefix(boundary.Dir, "Maven verify"), Category: "build", Required: true, Args: []string{mavenExecutable(b.abs(boundary.Dir)), "--batch-mode", "verify"}, WorkingDir: workdir(boundary.Dir), Language: "Java", Project: boundary.Dir})
		case "Java/Gradle":
			b.add(model.Check{Name: prefix(boundary.Dir, "Gradle check and build"), Category: "build", Required: true, Args: []string{gradleExecutable(b.abs(boundary.Dir)), "check", "build"}, WorkingDir: workdir(boundary.Dir), Language: "Java", Project: boundary.Dir})
		case ".NET":
			b.add(b.unavailableIfMissing(model.Check{Name: prefix(boundary.Dir, "dotnet build"), Category: "build", Required: true, Args: []string{"dotnet", "build"}, WorkingDir: workdir(boundary.Dir), Language: "C#", Project: boundary.Dir}))
			b.add(b.unavailableIfMissing(model.Check{Name: prefix(boundary.Dir, "dotnet test"), Category: "test", Required: true, Args: []string{"dotnet", "test"}, WorkingDir: workdir(boundary.Dir), Language: "C#", Project: boundary.Dir}))
		case "C/C++":
			b.note("C/C++ build system detected in %s; configure its build/test commands in PushGuard configuration.", boundary.Dir)
		case "source":
			switch boundary.Language {
			case "Python":
				b.python(boundary)
			case "JavaScript":
				jsDone = true
				b.nodeSyntax(b.d.FilesFor(".", "JavaScript"), "")
			case "TypeScript":
				b.note("TypeScript sources have no package.json; no type checker is configured.")
			case "Go":
				b.note("Go sources have no go.mod; run go mod init or configure checks.")
			case "C", "C++":
				b.cSyntax(boundary.Language, b.d.FilesFor(".", boundary.Language))
			default:
				b.note("%s sources detected; no safe automatic checker is available. Configure checks explicitly.", boundary.Language)
			}
		}
		if boundary.Language == "Python" {
			pythonDone = true
		}
	}
	// Python syntax is repository-wide: every .py file is verified exactly once.
	if pythonDone {
		b.pythonSyntax(b.d.FilesFor(".", "Python"))
	}
	_ = jsDone
	if len(b.checks) == 0 {
		for _, target := range makeTargets(b.root) {
			category := target
			if target == "check" {
				category = "build"
			}
			b.add(model.Check{Name: "make " + target, Category: category, Args: []string{"make", target}, Required: true})
		}
	}
}

// targets plans file-level checks for explicitly named files (standalone mode).
func (b *builder) targets(files []string) {
	byLang := map[string][]string{}
	for _, f := range files {
		byLang[LanguageOf(f)] = append(byLang[LanguageOf(f)], f)
	}
	if list := byLang["Python"]; len(list) > 0 {
		b.pythonSyntax(list)
		python := b.tools.Python(b.root)
		if ruff := b.ruffArgs(python); ruff != nil {
			configured := ruffConfigured(b.root)
			args := append(ruff, "check", "--output-format", "json", "--no-cache")
			b.add(model.Check{Name: "ruff", Category: "lint", Required: configured, Args: append(args, list...), Language: "Python"})
		}
	}
	if list := byLang["JavaScript"]; len(list) > 0 {
		b.nodeSyntax(list, "")
	}
	if list := byLang["Go"]; len(list) > 0 {
		b.add(b.unavailableIfMissing(model.Check{Name: "gofmt", Category: "format", Required: true, Args: append([]string{"gofmt", "-e", "-l"}, list...), Language: "Go"}))
	}
	for _, lang := range []string{"C", "C++"} {
		if list := byLang[lang]; len(list) > 0 {
			b.cSyntax(lang, list)
		}
	}
	for lang, list := range byLang {
		switch lang {
		case "Python", "JavaScript", "Go", "C", "C++":
		case "":
			b.note("No language could be identified for: %s", strings.Join(list, ", "))
		default:
			b.note("%s file checks are not available in file mode; run pushguard check without file arguments for project checks.", lang)
		}
	}
}

func (b *builder) abs(dir string) string { return filepath.Join(b.root, filepath.FromSlash(dir)) }

func (b *builder) unavailableIfMissing(c model.Check) model.Check {
	if len(c.Args) > 0 && !strings.ContainsAny(c.Args[0], `/\`) {
		if _, err := b.tools.LookPath(c.Args[0]); err != nil {
			c.Unavailable = fmt.Sprintf("%s is not installed or not on PATH", c.Args[0])
		}
	}
	return c
}

func (b *builder) pythonSyntax(files []string) {
	if len(files) == 0 {
		return
	}
	python := b.tools.Python(b.root)
	c := model.Check{Name: "python syntax", Category: "syntax", Required: true, Language: "Python", Description: fmt.Sprintf("compile (without executing) %d Python file(s)", len(files)), Input: strings.Join(files, "\n") + "\n"}
	if len(python) == 0 {
		c.Args = []string{"python3", "-I", "-S", "-c", PythonSyntaxScript}
		c.Unavailable = "no Python 3 interpreter found (set PUSHGUARD_PYTHON or install Python 3)"
	} else {
		c.Args = append(append([]string(nil), python...), "-I", "-S", "-c", PythonSyntaxScript)
	}
	b.add(c)
}

func (b *builder) nodeSyntax(files []string, dir string) {
	if len(files) == 0 {
		return
	}
	c := model.Check{Name: prefix(dir, "javascript syntax"), Category: "syntax", Required: true, Language: "JavaScript", Args: []string{"node", "-e", NodeSyntaxScript}, Description: fmt.Sprintf("node --check (parse only) %d JavaScript file(s)", len(files)), Input: strings.Join(files, "\n") + "\n", WorkingDir: workdir(dir)}
	b.add(b.unavailableIfMissing(c))
}

func (b *builder) cSyntax(lang string, files []string) {
	if len(files) == 0 {
		return
	}
	var sources []string
	for _, f := range files {
		ext := strings.ToLower(path.Ext(f))
		if ext != ".h" && ext != ".hpp" && ext != ".hh" && ext != ".hxx" {
			sources = append(sources, f)
		}
	}
	if len(sources) == 0 {
		return
	}
	if len(sources) > 100 {
		sources = sources[:100]
		b.note("%s syntax check limited to the first 100 files.", lang)
	}
	compilers := []string{"gcc", "clang", "cc"}
	if lang == "C++" {
		compilers = []string{"g++", "clang++", "c++"}
	}
	for _, compiler := range compilers {
		if _, err := b.tools.LookPath(compiler); err == nil {
			// Optional: include paths and defines are unknown without a build system.
			b.add(model.Check{Name: strings.ToLower(lang) + " syntax", Category: "syntax", Required: false, Language: lang, Args: append([]string{compiler, "-fsyntax-only"}, sources...)})
			return
		}
	}
	b.note("%s sources detected but no compiler is on PATH; no %s check runs.", lang, lang)
}

func ruffConfigured(dir string) bool {
	return hasText(filepath.Join(dir, "pyproject.toml"), "[tool.ruff") || fileExists(filepath.Join(dir, "ruff.toml")) || fileExists(filepath.Join(dir, ".ruff.toml"))
}

func (b *builder) ruffArgs(python []string) []string {
	if _, err := b.tools.LookPath("ruff"); err == nil {
		return []string{"ruff"}
	}
	if b.tools.PythonModule(python, "ruff") {
		return append(append([]string(nil), python...), "-m", "ruff")
	}
	return nil
}

func (b *builder) python(boundary model.ProjectBoundary) {
	dir := b.abs(boundary.Dir)
	python := b.tools.Python(dir)
	wd := workdir(boundary.Dir)
	if ruff := b.ruffArgs(python); ruff != nil {
		b.add(model.Check{Name: prefix(boundary.Dir, "ruff"), Category: "lint", Required: ruffConfigured(dir), Args: append(ruff, "check", "--output-format", "json", "--no-cache", "."), WorkingDir: wd, Language: "Python", Project: boundary.Dir})
	} else if ruffConfigured(dir) {
		b.add(model.Check{Name: prefix(boundary.Dir, "ruff"), Category: "lint", Required: true, Args: []string{"ruff", "check", "--output-format", "json", "--no-cache", "."}, WorkingDir: wd, Language: "Python", Project: boundary.Dir, Unavailable: "ruff is configured but not installed"})
	}
	if hasText(filepath.Join(dir, "pyproject.toml"), "[tool.mypy") || hasText(filepath.Join(dir, "setup.cfg"), "[mypy") || fileExists(filepath.Join(dir, "mypy.ini")) || fileExists(filepath.Join(dir, ".mypy.ini")) {
		c := model.Check{Name: prefix(boundary.Dir, "mypy"), Category: "typecheck", Required: true, WorkingDir: wd, Language: "Python", Project: boundary.Dir}
		switch {
		case b.tools.PythonModule(python, "mypy"):
			c.Args = append(append([]string(nil), python...), "-m", "mypy", "--show-column-numbers", "--no-error-summary", ".")
		default:
			c.Args = []string{"mypy", "--show-column-numbers", "--no-error-summary", "."}
			c = b.unavailableIfMissing(c)
		}
		b.add(c)
	}
	if hasText(filepath.Join(dir, "pyproject.toml"), "[tool.pyright") || fileExists(filepath.Join(dir, "pyrightconfig.json")) {
		b.add(b.unavailableIfMissing(model.Check{Name: prefix(boundary.Dir, "pyright"), Category: "typecheck", Required: true, Args: []string{"pyright", "--outputjson"}, WorkingDir: wd, Language: "Python", Project: boundary.Dir}))
	}
	if b.pytestWanted(boundary) {
		c := model.Check{Name: prefix(boundary.Dir, "pytest"), Category: "test", Required: true, WorkingDir: wd, Language: "Python", Project: boundary.Dir}
		flags := []string{"-q", "--tb=short", "-rfE", "-p", "no:cacheprovider"}
		switch {
		case b.tools.PythonModule(python, "pytest"):
			c.Args = append(append(append([]string(nil), python...), "-m", "pytest"), flags...)
		default:
			c.Args = append([]string{"pytest"}, flags...)
			if _, err := b.tools.LookPath("pytest"); err != nil {
				c.Unavailable = "tests were found but pytest is not installed for this interpreter (pip install pytest), or configure test checks explicitly"
			}
		}
		b.add(c)
	}
}

func (b *builder) pytestWanted(boundary model.ProjectBoundary) bool {
	dir := b.abs(boundary.Dir)
	if fileExists(filepath.Join(dir, "pytest.ini")) || fileExists(filepath.Join(dir, "conftest.py")) || hasText(filepath.Join(dir, "pyproject.toml"), "[tool.pytest") || hasText(filepath.Join(dir, "setup.cfg"), "[tool:pytest]") || hasText(filepath.Join(dir, "tox.ini"), "[pytest]") {
		return true
	}
	for _, f := range b.d.FilesFor(boundary.Dir, "Python") {
		base := path.Base(f)
		if strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py") || strings.HasSuffix(base, "_test.py") {
			return true
		}
	}
	return false
}

func (b *builder) node(boundary model.ProjectBoundary) {
	dir := b.abs(boundary.Dir)
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return
	}
	var manifest struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal(data, &manifest) != nil {
		b.note("%s/package.json is invalid JSON; Node checks skipped.", boundary.Dir)
		return
	}
	manager := nodeManager(dir)
	wd := workdir(boundary.Dir)
	// Standard check scripts plus safe CI variants (lint:ci, test:unit, ...);
	// watch/fix/dev/deploy-style scripts are never run.
	names := []string{"lint", "format:check", "typecheck", "type-check", "check:types", "test", "build", "security", "audit", "check"}
	var extra []string
	for name := range manifest.Scripts {
		if safeCheckScript(name) {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	names = append(names, extra...)
	hasTypecheck := false
	seen := map[string]bool{}
	for _, name := range names {
		script, ok := manifest.Scripts[name]
		if !ok || seen[name] {
			continue
		}
		seen[name] = true
		if name == "test" && strings.Contains(script, "no test specified") {
			b.note("%s: npm's placeholder test script was not run.", prefix(boundary.Dir, "test"))
			continue
		}
		category := name
		if colon := strings.IndexByte(category, ':'); colon >= 0 {
			category = category[:colon]
		}
		switch name {
		case "type-check", "check:types":
			category = "typecheck"
		case "format:check":
			category = "lint"
		case "audit":
			category = "security"
		case "check":
			category = "build"
		}
		if category == "typecheck" || strings.Contains(script, "tsc") {
			hasTypecheck = true
		}
		args := []string{manager, "run", name}
		if manager == "yarn" || manager == "pnpm" || manager == "bun" {
			args = []string{manager, name}
		}
		b.add(b.unavailableIfMissing(model.Check{Name: prefix(boundary.Dir, name), Category: category, Args: args, Required: true, WorkingDir: wd, Language: boundary.Language, Project: boundary.Dir}))
	}
	if !hasTypecheck && fileExists(filepath.Join(dir, "tsconfig.json")) {
		tsc := filepath.Join(dir, "node_modules", ".bin", "tsc")
		if runtime.GOOS == "windows" {
			tsc += ".cmd"
		}
		if fileExists(tsc) {
			b.add(model.Check{Name: prefix(boundary.Dir, "typecheck (tsc)"), Category: "typecheck", Required: true, Args: []string{tsc, "--noEmit", "--pretty", "false"}, WorkingDir: wd, Language: "TypeScript", Project: boundary.Dir})
		} else {
			b.add(model.Check{Name: prefix(boundary.Dir, "typecheck (tsc)"), Category: "typecheck", Required: true, Args: []string{tsc, "--noEmit", "--pretty", "false"}, WorkingDir: wd, Language: "TypeScript", Project: boundary.Dir, Unavailable: "tsconfig.json requires TypeScript; install the project's dependencies"})
		}
	}
	if len(seen) == 0 && boundary.Language == "JavaScript" {
		b.nodeSyntax(b.relativeTo(boundary.Dir, b.d.FilesFor(boundary.Dir, "JavaScript")), boundary.Dir)
	}
}

func (b *builder) relativeTo(dir string, files []string) []string {
	if dir == "." {
		return files
	}
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, strings.TrimPrefix(f, dir+"/"))
	}
	return out
}

func (b *builder) golang(boundary model.ProjectBoundary) {
	wd := workdir(boundary.Dir)
	for _, c := range []model.Check{
		{Name: prefix(boundary.Dir, "gofmt"), Category: "format", Args: []string{"gofmt", "-l", "."}, Required: true},
		{Name: prefix(boundary.Dir, "go vet"), Category: "vet", Args: []string{"go", "vet", "./..."}, Required: true},
		{Name: prefix(boundary.Dir, "go test"), Category: "test", Args: []string{"go", "test", "-json", "-count=1", "./..."}, Required: true},
		// A single main package would otherwise write its binary into the
		// repository and change the state being verified.
		{Name: prefix(boundary.Dir, "go build"), Category: "build", Args: []string{"go", "build", "-o", filepath.Join(os.TempDir(), "pushguard-tool-cache", "gobuild") + string(filepath.Separator), "./..."}, Required: true},
	} {
		c.WorkingDir, c.Language, c.Project = wd, "Go", boundary.Dir
		b.add(b.unavailableIfMissing(c))
	}
}

func (b *builder) rust(boundary model.ProjectBoundary) {
	wd := workdir(boundary.Dir)
	for _, c := range []model.Check{
		{Name: prefix(boundary.Dir, "cargo check"), Category: "check", Args: []string{"cargo", "check", "--message-format=json"}, Required: true},
		{Name: prefix(boundary.Dir, "cargo clippy"), Category: "lint", Args: []string{"cargo", "clippy", "--message-format=json", "--", "-D", "warnings"}, Required: true},
		{Name: prefix(boundary.Dir, "cargo test"), Category: "test", Args: []string{"cargo", "test"}, Required: true},
		{Name: prefix(boundary.Dir, "cargo build"), Category: "build", Args: []string{"cargo", "build"}, Required: true},
	} {
		c.WorkingDir, c.Language, c.Project = wd, "Rust", boundary.Dir
		b.add(b.unavailableIfMissing(c))
	}
}

var categoryOrder = map[string]int{"syntax": 0, "format": 1, "check": 2, "lint": 3, "vet": 3, "typecheck": 4, "test": 5, "build": 6, "security": 7, "generic": 9}

// ordered runs cheap syntax checks first and repository sanity last, keeping
// discovery order within a category.
func (b *builder) ordered() []model.Check {
	out := append([]model.Check(nil), b.checks...)
	sort.SliceStable(out, func(i, j int) bool {
		oi, ok := categoryOrder[out[i].Category]
		if !ok {
			oi = 8
		}
		oj, ok := categoryOrder[out[j].Category]
		if !ok {
			oj = 8
		}
		return oi < oj
	})
	return out
}

// Display renders a check for humans without dumping embedded scripts.
func Display(c model.Check) string {
	args := c.Args
	if len(args) >= 3 && (args[len(args)-2] == "-c" || args[len(args)-2] == "-e") && strings.Contains(args[len(args)-1], "\n") {
		args = append(append([]string(nil), args[:len(args)-1]...), "<built-in checker>")
	} else if len(args) > 3 && (args[1] == "-c" || args[1] == "-e") {
		args = append([]string{args[0], args[1], "<built-in checker>"}, args[3:]...)
	}
	text := strings.Join(args, " ")
	if len(text) > 240 {
		text = text[:240] + " …"
	}
	if c.WorkingDir != "" {
		text = "(in " + c.WorkingDir + ") " + text
	}
	return text
}
