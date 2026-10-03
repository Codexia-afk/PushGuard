package verify

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/pushguard/pushguard/internal/model"
)

func TestUnavailableRequiredCheckBlocksAndOptionalSkips(t *testing.T) {
	root := t.TempDir()
	required := Service{}.RunCheck(context.Background(), root, model.Check{Name: "go vet", Required: true, Args: []string{"go"}, Unavailable: "go is not installed"})
	if required.Status != model.StatusBlocked || !required.Blocking() || required.Diagnostics[0].Classification != model.ClassEnvironment {
		t.Fatalf("required unavailable check must block: %+v", required)
	}
	optional := Service{}.RunCheck(context.Background(), root, model.Check{Name: "ruff", Required: false, Args: []string{"ruff"}, Unavailable: "missing"})
	if optional.Status != model.StatusSkipped || optional.Blocking() {
		t.Fatalf("optional unavailable check must be skipped: %+v", optional)
	}
}

func TestMissingExecutableIsEnvironmentNotCode(t *testing.T) {
	r := Service{}.RunCheck(context.Background(), t.TempDir(), model.Check{Name: "lint", Category: "lint", Required: true, Args: []string{"pushguard-definitely-missing-tool"}})
	if r.Status != model.StatusBlocked || r.Diagnostics[0].Classification != model.ClassEnvironment {
		t.Fatalf("missing tool misclassified: %+v", r)
	}
}

func TestWorkingDirectoryIsConfinedAndPathsRebased(t *testing.T) {
	root := t.TempDir()
	if r := (Service{}).RunCheck(context.Background(), root, model.Check{Name: "escape", Required: true, Args: []string{"git", "--version"}, WorkingDir: "../"}); r.Status != model.StatusBlocked {
		t.Fatalf("working directory escape not refused: %+v", r)
	}
	python, err := exec.LookPath("python")
	if err != nil {
		if python, err = exec.LookPath("python3"); err != nil {
			t.Skip("python unavailable")
		}
	}
	sub := filepath.Join(root, "svc")
	os.MkdirAll(sub, 0755)
	os.WriteFile(filepath.Join(sub, "bad.py"), []byte("def f(:\n"), 0600)
	script := "import sys\ntry:\n    compile(open('bad.py').read(), 'bad.py', 'exec')\nexcept SyntaxError as e:\n    print('bad.py:%d:%d: SyntaxError %s' % (e.lineno, e.offset, e.msg)); sys.exit(1)\n"
	r := (Service{}).RunCheck(context.Background(), root, model.Check{Name: "svc syntax", Category: "lint", Required: true, Args: []string{python, "-c", script}, WorkingDir: "svc"})
	if r.Status != model.StatusFail || len(r.Diagnostics) == 0 || r.Diagnostics[0].Location.File != "svc/bad.py" {
		t.Fatalf("diagnostic path not rebased to repository root: %+v", r.Diagnostics)
	}
	if r.Diagnostics[0].Check != "svc syntax" || r.Diagnostics[0].Classification != model.ClassCode {
		t.Fatalf("diagnostic not attributed/classified: %+v", r.Diagnostics[0])
	}
}

func TestEnvHelper(t *testing.T) {
	if os.Getenv("PUSHGUARD_ENV_HELPER") != "1" {
		return
	}
	if os.Getenv("PUSHGUARD_AI_API_KEY") != "" {
		os.Stdout.WriteString("LEAKED")
		os.Exit(1)
	}
	os.Exit(0)
}

func TestAIKeyIsNotForwardedToTools(t *testing.T) {
	t.Setenv("PUSHGUARD_AI_API_KEY", "should-not-leak")
	t.Setenv("PUSHGUARD_ENV_HELPER", "1")
	r := (Service{}).RunCheck(context.Background(), t.TempDir(), model.Check{Name: "env", Required: true, Args: []string{os.Args[0], "-test.run=^TestEnvHelper$"}})
	if r.Status != model.StatusPass {
		t.Fatalf("AI key forwarded to verification tools: %s %s", r.Command.Stdout, r.Command.Stderr)
	}
}
