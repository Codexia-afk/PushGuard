package verify

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pushguard/pushguard/internal/diagnostics"
	"github.com/pushguard/pushguard/internal/model"
	"github.com/pushguard/pushguard/internal/runner"
)

type Service struct {
	Runner         runner.Runner
	DefaultTimeout time.Duration
	// CacheDir receives tool caches (bytecode, mypy, ruff) so verification
	// never writes generated files into the repository being verified.
	CacheDir string
}

// Variables that must never be forwarded to project tooling.
var withheldEnvironment = []string{"PUSHGUARD_AI_API_KEY"}

func (s Service) cacheDir() string {
	if s.CacheDir != "" {
		return s.CacheDir
	}
	return filepath.Join(os.TempDir(), "pushguard-tool-cache")
}

func (s Service) RunCheck(ctx context.Context, root string, c model.Check) model.CheckResult {
	started := time.Now()
	if c.Unavailable != "" {
		return unavailable(c, started)
	}
	timeout := s.DefaultTimeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	if c.TimeoutSecs > 0 {
		timeout = time.Duration(c.TimeoutSecs) * time.Second
	}
	dir, err := workingDir(root, c.WorkingDir)
	if err != nil {
		res := model.CommandResult{Args: c.Args, Command: strings.Join(c.Args, " "), WorkingDir: c.WorkingDir, ExitCode: 2, Stderr: err.Error(), Started: started, Finished: time.Now()}
		d := model.Diagnostic{Tool: c.Name, Check: c.Name, Category: c.Category, Classification: model.ClassConfiguration, Severity: "error", Message: err.Error(), Command: res.Command, ExitCode: 2}
		return model.CheckResult{Check: c, Command: res, Status: model.StatusBlocked, Diagnostics: []model.Diagnostic{d}, Started: started, Finished: time.Now()}
	}
	cache := s.cacheDir()
	_ = os.MkdirAll(cache, 0700)
	r := s.Runner
	env := map[string]string{
		"CI": "true", "NO_COLOR": "1", "FORCE_COLOR": "0",
		"PYTHONDONTWRITEBYTECODE": "1", "PYTHONPYCACHEPREFIX": filepath.Join(cache, "pycache"),
		"MYPY_CACHE_DIR": filepath.Join(cache, "mypy"), "RUFF_CACHE_DIR": filepath.Join(cache, "ruff"),
		"PYTHONUTF8": "1",
	}
	for key, value := range r.Environment {
		env[key] = value
	}
	r.Environment = env
	r.Remove = append(append([]string(nil), r.Remove...), withheldEnvironment...)
	res := r.RunInput(ctx, dir, c.Args, timeout, c.Input)
	status := model.StatusPass
	if res.ExitCode != 0 || res.Truncated {
		status = model.StatusFail
	}
	ds := diagnostics.Parse(c, res)
	if res.Truncated {
		ds = append(ds, model.Diagnostic{Tool: c.Name, Category: c.Category, Classification: model.ClassResource, Severity: "error", Message: "command output exceeded the 2 MiB per-stream limit; evidence is incomplete", Command: res.Command, ExitCode: res.ExitCode})
	}
	if c.Category == "format" && res.ExitCode == 0 && strings.TrimSpace(res.Stdout) != "" {
		status = model.StatusFail
		for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
			if len(ds) >= diagnostics.MaxDiagnostics {
				break
			}
			if strings.TrimSpace(line) != "" {
				file := strings.TrimSpace(line)
				if len(file) > 4096 {
					file = ""
				}
				ds = append(ds, model.Diagnostic{Tool: c.Name, Category: "format", Severity: "error", Location: model.SourceLocation{File: file, Exact: false}, Code: "unformatted", Message: "file is not formatted", Command: res.Command, ExitCode: 1})
			}
		}
	}
	if status == model.StatusPass && len(ds) > 0 {
		for _, d := range ds {
			if d.Severity == "error" {
				status = model.StatusFail
				break
			}
		}
	}
	rebase(root, c.WorkingDir, ds)
	for i := range ds {
		ds[i].Check = c.Name
	}
	ds = diagnostics.Refine(c, res, ds)
	if status == model.StatusFail && environmentOnly(ds) {
		// The tool could not run; this is not evidence about the source code.
		status = model.StatusBlocked
	}
	if status == model.StatusFail && res.ExitCode == 5 && strings.Contains(strings.Join(c.Args, " "), "pytest") && len(ds) <= 1 && strings.Contains(res.Stdout+res.Stderr, "no tests ran") {
		status = model.StatusWarning
		ds = []model.Diagnostic{{Tool: "pytest", Check: c.Name, Category: c.Category, Classification: model.ClassConfiguration, Severity: "warning", Message: "pytest collected no tests; nothing was verified by this check", Command: res.Command, ExitCode: 5}}
	}
	ds = diagnostics.Normalize(c, ds)
	return model.CheckResult{Check: c, Command: res, Status: status, Diagnostics: ds, DiagnosticsLimited: len(ds) >= diagnostics.MaxDiagnostics, Started: started, Finished: time.Now()}
}

func unavailable(c model.Check, started time.Time) model.CheckResult {
	res := model.CommandResult{Args: c.Args, Command: strings.Join(c.Args, " "), WorkingDir: c.WorkingDir, ExitCode: 127, Terminated: "unavailable", Stderr: c.Unavailable, Started: started, Finished: time.Now()}
	status := model.StatusSkipped
	severity := "warning"
	if c.Required {
		status = model.StatusBlocked
		severity = "error"
	}
	d := model.Diagnostic{Tool: c.Name, Check: c.Name, Category: c.Category, Classification: model.ClassEnvironment, Severity: severity, Message: "check could not run: " + c.Unavailable, Command: res.Command, ExitCode: 127}
	return model.CheckResult{Check: c, Command: res, Status: status, Diagnostics: diagnostics.Normalize(c, []model.Diagnostic{d}), Started: started, Finished: time.Now()}
}

func environmentOnly(ds []model.Diagnostic) bool {
	if len(ds) == 0 {
		return false
	}
	for _, d := range ds {
		if d.Classification != model.ClassEnvironment {
			return false
		}
	}
	return true
}

// workingDir resolves a check directory and refuses anything outside the root.
func workingDir(root, rel string) (string, error) {
	if rel == "" || rel == "." {
		return root, nil
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("check working directory %q leaves the repository", rel)
	}
	full := filepath.Join(root, clean)
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(full)
	if err != nil {
		return "", fmt.Errorf("check working directory %q: %w", rel, err)
	}
	back, err := filepath.Rel(realRoot, real)
	if err != nil || back == ".." || strings.HasPrefix(back, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("check working directory %q resolves outside the repository", rel)
	}
	return real, nil
}

// rebase makes every reported location relative to the repository root, so
// grouping, context retrieval and patch validation share one path space.
func rebase(root, wd string, ds []model.Diagnostic) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		realRoot = root
	}
	fix := func(file string) string {
		if file == "" {
			return file
		}
		native := filepath.FromSlash(strings.TrimPrefix(file, "./"))
		if filepath.IsAbs(native) {
			for _, base := range []string{realRoot, root} {
				if rel, e := filepath.Rel(base, native); e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					return filepath.ToSlash(rel)
				}
			}
			return file
		}
		if wd != "" {
			native = filepath.Join(filepath.FromSlash(wd), native)
		}
		return filepath.ToSlash(filepath.Clean(native))
	}
	for i := range ds {
		ds[i].Location.File = fix(ds[i].Location.File)
		for j := range ds[i].StackTrace {
			ds[i].StackTrace[j].File = fix(ds[i].StackTrace[j].File)
		}
	}
}

func (s Service) RunAll(ctx context.Context, root string, checks []model.Check, stopOnFailure bool) []model.CheckResult {
	out := make([]model.CheckResult, 0, len(checks))
	captured := 0
	for _, c := range checks {
		if ctx.Err() != nil {
			break
		}
		r := s.RunCheck(ctx, root, c)
		remaining := (16 << 20) - captured
		size := len(r.Command.Stdout) + len(r.Command.Stderr)
		if size > remaining {
			r.Status = model.StatusFail
			r.Command.Truncated = true
			if len(r.Command.Stdout) > remaining {
				r.Command.Stdout = r.Command.Stdout[:remaining]
				r.Command.Stderr = ""
			} else {
				r.Command.Stderr = r.Command.Stderr[:remaining-len(r.Command.Stdout)]
			}
			r.Diagnostics = []model.Diagnostic{{Tool: c.Name, Check: c.Name, Category: "resource", Classification: model.ClassResource, Severity: "error", Message: "session log budget of 16 MiB exceeded; evidence incomplete"}}
			size = remaining
		}
		captured += size
		out = append(out, r)
		if stopOnFailure && r.Blocking() {
			break
		}
	}
	return out
}

func Blocking(results []model.CheckResult) []model.CheckResult {
	var out []model.CheckResult
	for _, r := range results {
		if r.Blocking() {
			out = append(out, r)
		}
	}
	return out
}
