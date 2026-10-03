package detector

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Tools abstracts executable discovery so plans are testable and deterministic.
type Tools interface {
	LookPath(name string) (string, error)
	// Python returns an interpreter command (e.g. ["python3"] or ["py", "-3"])
	// for a project directory, preferring a project virtual environment.
	Python(dir string) []string
	// PythonModule reports whether the interpreter can import a module.
	PythonModule(python []string, module string) bool
}

// SystemTools probes the real machine. Results are cached for the process.
type SystemTools struct{}

var (
	pythonOnce   sync.Once
	pythonCached []string
	moduleCache  sync.Map
)

func (SystemTools) LookPath(name string) (string, error) { return exec.LookPath(name) }

func (SystemTools) Python(dir string) []string {
	if dir != "" {
		for _, venv := range []string{".venv", "venv", "env"} {
			base := filepath.Join(dir, venv)
			if !fileExists(filepath.Join(base, "pyvenv.cfg")) {
				continue
			}
			exe := filepath.Join(base, "bin", "python")
			if runtime.GOOS == "windows" {
				exe = filepath.Join(base, "Scripts", "python.exe")
			}
			if fileExists(exe) {
				return []string{exe}
			}
		}
	}
	pythonOnce.Do(func() { pythonCached = systemPython() })
	return append([]string(nil), pythonCached...)
}

func systemPython() []string {
	if configured := os.Getenv("PUSHGUARD_PYTHON"); configured != "" {
		return []string{configured}
	}
	candidates := [][]string{{"python3"}, {"python"}}
	if runtime.GOOS == "windows" {
		// The WindowsApps python3 alias is often a Store stub; probing rejects it.
		candidates = [][]string{{"python"}, {"py", "-3"}, {"python3"}}
	}
	for _, candidate := range candidates {
		if _, err := exec.LookPath(candidate[0]); err != nil {
			continue
		}
		if probe(append(append([]string(nil), candidate...), "-I", "-S", "-c", "import sys;print(sys.version_info[0])")) == "3" {
			return candidate
		}
	}
	return nil
}

func (SystemTools) PythonModule(python []string, module string) bool {
	if len(python) == 0 {
		return false
	}
	key := strings.Join(python, "\x00") + "\x00" + module
	if cached, ok := moduleCache.Load(key); ok {
		return cached.(bool)
	}
	ok := probe(append(append([]string(nil), python...), "-I", "-c", "import importlib.util,sys;print(importlib.util.find_spec(sys.argv[1]) is not None)", module)) == "True"
	moduleCache.Store(key, ok)
	return ok
}

func probe(args []string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// PythonSyntaxScript compiles (never executes) every file named on stdin and
// prints one JSON object per problem plus a final summary. compile() writes no
// bytecode, unlike py_compile, and continues past the first broken file.
const PythonSyntaxScript = `import sys, json
files = [l.strip() for l in sys.stdin.read().splitlines() if l.strip()] + sys.argv[1:]
bad = 0
for f in files:
    try:
        with open(f, 'rb') as h:
            src = h.read()
        compile(src, f, 'exec', dont_inherit=True)
    except SyntaxError as e:
        bad += 1
        print(json.dumps({'file': f, 'line': e.lineno or 0, 'column': e.offset or 0, 'type': type(e).__name__, 'message': e.msg, 'text': (e.text or '').rstrip()}))
    except (ValueError, OSError) as e:
        bad += 1
        print(json.dumps({'file': f, 'line': 0, 'column': 0, 'type': type(e).__name__, 'message': str(e)}))
print(json.dumps({'checked': len(files), 'errors': bad}))
sys.exit(1 if bad else 0)
`

// NodeSyntaxScript runs node --check (parse only, never executes) per file.
const NodeSyntaxScript = `const fs = require('fs'), cp = require('child_process');
const files = fs.readFileSync(0, 'utf8').split(/\r?\n/).filter(Boolean);
let bad = 0;
for (const f of files) {
  const r = cp.spawnSync(process.execPath, ['--check', f], { encoding: 'utf8' });
  if (r.status !== 0) {
    bad++;
    const err = r.stderr || '';
    const loc = /^(.*):(\d+)\r?$/m.exec(err) || [];
    const msg = /^(\w*Error): (.*)$/m.exec(err) || [];
    console.log(JSON.stringify({ file: f, line: +loc[2] || 0, column: 0, type: msg[1] || 'SyntaxError', message: msg[2] || err.trim().split('\n').pop() || 'syntax check failed' }));
  }
}
console.log(JSON.stringify({ checked: files.length, errors: bad }));
process.exit(bad ? 1 : 0);
`
