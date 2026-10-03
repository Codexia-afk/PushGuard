// Package demo exercises the production workflow on disposable local Git remotes.
// Scripted decisions and the deterministic proposal provider exist only in this demo.
package demo

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pushguard/pushguard/internal/app"
	"github.com/pushguard/pushguard/internal/config"
	"github.com/pushguard/pushguard/internal/model"
	"github.com/pushguard/pushguard/internal/runner"
	"github.com/pushguard/pushguard/internal/ui"
)

type proposals struct{ next, investigations int }

func (p *proposals) Analyze(context.Context, model.ContextBundle) (*model.Analysis, error) {
	p.investigations++
	cause := "Add subtracts instead of adding"
	if p.next > 0 {
		cause = "Multiply adds instead of multiplying"
	}
	return &model.Analysis{Summary: "The reported test location is in arithmetic_test.go. The likely implementation cause is in arithmetic.go; the legitimate test stays unchanged.", RootCause: cause, Confidence: "high", Evidence: []string{"expected arithmetic result differs from actual result"}}, nil
}
func (p *proposals) ProposeFix(context.Context, model.ContextBundle) (*model.RepairProposal, error) {
	old, new := bad, first
	if p.next > 0 {
		old, new = first, good
	}
	p.next++
	var patch strings.Builder
	patch.WriteString("--- a/arithmetic.go\n+++ b/arithmetic.go\n@@ -1,5 +1,5 @@\n")
	before, after := strings.Split(strings.TrimSuffix(old, "\n"), "\n"), strings.Split(strings.TrimSuffix(new, "\n"), "\n")
	for i, line := range before {
		if line == after[i] {
			patch.WriteString(" " + line + "\n")
		} else {
			patch.WriteString("-" + line + "\n+" + after[i] + "\n")
		}
	}
	return &model.RepairProposal{Summary: "Correct the implementation without modifying tests", RootCause: "The arithmetic operator was incorrect", Files: []string{"arithmetic.go"}, Patch: patch.String()}, nil
}

const bad = "package arithmetic\n\nfunc Add(a, b int) int { return a - b }\n\nfunc Multiply(a, b int) int { return a + b }\n"
const first = "package arithmetic\n\nfunc Add(a, b int) int { return a + b }\n\nfunc Multiply(a, b int) int { return a + b }\n"
const good = "package arithmetic\n\nfunc Add(a, b int) int { return a + b }\n\nfunc Multiply(a, b int) int { return a * b }\n"
const tests = `package arithmetic

import "testing"

func TestArithmetic(t *testing.T) {
 if got := Add(2, 3); got != 5 {
  t.Fatalf("Add: expected 5, received %d", got)
 }
 if got := Multiply(2, 3); got != 6 {
  t.Fatalf("Multiply: expected 6, received %d", got)
 }
}
`

func Run(ctx context.Context, out, errOut io.Writer, scenario string) error {
	if scenario == "all" {
		for _, name := range []string{"repair-deny", "patch-deny", "repair", "pass", "deny", "stale"} {
			if err := Run(ctx, out, errOut, name); err != nil {
				return err
			}
		}
		return nil
	}
	if scenario != "live" && scenario != "repair" && scenario != "repair-deny" && scenario != "patch-deny" && scenario != "pass" && scenario != "deny" && scenario != "stale" {
		return fmt.Errorf("demo scenario must be repair, repair-deny, patch-deny, pass, deny, stale, live, or all")
	}
	temp, err := os.MkdirTemp("", "pushguard-demo-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	previous, exists := os.LookupEnv("PUSHGUARD_CACHE_DIR")
	if err = os.Setenv("PUSHGUARD_CACHE_DIR", filepath.Join(temp, "evidence")); err != nil {
		return err
	}
	defer func() {
		if exists {
			os.Setenv("PUSHGUARD_CACHE_DIR", previous)
		} else {
			os.Unsetenv("PUSHGUARD_CACHE_DIR")
		}
	}()
	work, remote := filepath.Join(temp, "work"), filepath.Join(temp, "remote.git")
	if err = os.Mkdir(work, 0755); err != nil {
		return err
	}
	r := runner.Runner{}
	command := func(root string, args ...string) error {
		result := r.Run(ctx, root, args, 30*time.Second)
		if result.ExitCode != 0 {
			return fmt.Errorf("demo %s: %s %s", args[0], result.Stderr, result.Terminated)
		}
		return nil
	}
	for _, args := range [][]string{{"git", "init", "-q", "--bare", remote}, {"git", "init", "-q", "-b", "main", work}, {"git", "config", "user.name", "PushGuard Demo"}, {"git", "config", "user.email", "demo@example.test"}, {"git", "config", "commit.gpgsign", "false"}, {"git", "config", "core.autocrlf", "false"}, {"git", "remote", "add", "origin", remote}} {
		if err = command(work, args...); err != nil {
			return err
		}
	}
	source := good
	if scenario == "live" || scenario == "repair" || scenario == "repair-deny" || scenario == "patch-deny" {
		source = bad
	}
	for file, contents := range map[string]string{"go.mod": "module demo.local/arithmetic\n\ngo 1.22\n", "arithmetic.go": source, "arithmetic_test.go": tests} {
		if err = os.WriteFile(filepath.Join(work, file), []byte(contents), 0600); err != nil {
			return err
		}
	}
	if err = command(work, "gofmt", "-w", "arithmetic.go", "arithmetic_test.go"); err != nil {
		return err
	}
	cfg := config.Default()
	if scenario == "live" {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		configured, _, err := config.Load(cwd)
		if err != nil {
			return err
		}
		if configured.AI.Provider == "none" {
			return fmt.Errorf("live demo requires a configured real provider")
		}
		cfg.AI, cfg.Privacy = configured.AI, configured.Privacy
	}
	if err = config.Save(work, cfg); err != nil {
		return err
	}
	commit := func() error {
		if err := command(work, "git", "add", "."); err != nil {
			return err
		}
		return command(work, "git", "commit", "-q", "-m", "demo fixture")
	}
	if err = commit(); err != nil {
		return err
	}
	fmt.Fprintf(out, "\nDEMO MODE: %s\nDisposable repository and local remote. Decisions are scripted demo inputs.\n", scenario)
	if scenario == "live" {
		fmt.Fprintf(out, "REAL PROVIDER: %s / %s. Real checks, reviewed commit and real local Git push.\n", cfg.AI.Provider, cfg.AI.Model)
	} else {
		fmt.Fprintln(out, "Real Git, real Go checks, mock proposal-only provider; no AI service required.")
	}
	input := "y\np\ny\n"
	expected := app.ExitOK
	provider := &proposals{}
	if scenario == "repair" {
		input = "allow\nallow\nallow\nallow\nallow\ncontinue\nc\nallow\ncontinue\nallow\n"
	}
	if scenario == "repair-deny" {
		input, expected = "allow\ndeny\n", app.ExitCancelled
	}
	if scenario == "patch-deny" {
		input, expected = "allow\nallow\ndeny\n", app.ExitCancelled
	}
	if scenario == "deny" {
		input = "y\np\nn\n"
		expected = app.ExitCancelled
	}
	writer := out
	if scenario == "stale" {
		writer = &changeWriter{out: out, root: work}
		expected = app.ExitPreflight
	}
	a := app.New(ui.New(strings.NewReader(input), writer, errOut, false), false)
	a.Workdir = work
	a.Provider = provider
	if scenario == "live" {
		script := &liveDecisions{out: out}
		a.UI = ui.New(script, script, errOut, false)
		a.Provider = nil
	}
	code := a.RunPush(ctx, false)
	if code != expected {
		return fmt.Errorf("demo outcome %d, expected %d", code, expected)
	}
	if scenario == "repair" && (a.Report.RepairCycles != 2 || a.Report.CommitResult == nil || !a.Report.ReviewComplete) {
		return fmt.Errorf("single-session repair evidence incomplete")
	}
	if scenario == "repair-deny" || scenario == "patch-deny" {
		data, err := os.ReadFile(filepath.Join(work, "arithmetic.go"))
		if err != nil || string(data) != bad {
			return fmt.Errorf("denial changed source")
		}
		if scenario == "repair-deny" && (provider.investigations != 0 || provider.next != 0) {
			return fmt.Errorf("provider invoked after repair denial")
		}
		if scenario == "patch-deny" && (provider.investigations != 1 || provider.next != 1) {
			return fmt.Errorf("proposal boundary not exercised")
		}
	}
	result := r.Run(ctx, work, []string{"git", "ls-remote", "--heads", remote, "main"}, 10*time.Second)
	if result.ExitCode != 0 {
		return fmt.Errorf("cannot inspect demo remote")
	}
	if expected == 0 && strings.TrimSpace(result.Stdout) == "" || expected != 0 && strings.TrimSpace(result.Stdout) != "" {
		return fmt.Errorf("demo remote result does not match the authorization")
	}
	fmt.Fprintf(out, "\nPASS Demo %s: expected safety outcome confirmed.\n", scenario)
	return nil
}

// liveDecisions is a visibly scripted demonstration driver, never used by push.
// It exercises the real provider and all production gates in a disposable repo.
type liveDecisions struct {
	out    io.Writer
	prompt string
}

func (s *liveDecisions) Write(p []byte) (int, error) {
	if strings.HasSuffix(string(p), "> ") {
		s.prompt = string(p)
	}
	return s.out.Write(p)
}
func (s *liveDecisions) Read(p []byte) (int, error) {
	answer := "deny\n"
	switch {
	case strings.Contains(s.prompt, "Review before push authorization"):
		answer = "continue\n"
	case strings.Contains(s.prompt, "Commit the reviewed repairs here"):
		answer = "c\n"
	case strings.Contains(s.prompt, "Start verification"), strings.Contains(s.prompt, "configured repair agent"), strings.Contains(s.prompt, "Apply this exact diff"), strings.Contains(s.prompt, "Commit this displayed diff"), strings.Contains(s.prompt, "Authorize this exact Git push"):
		answer = "allow\n"
	}
	fmt.Fprint(s.out, "[demo developer] "+answer)
	return copy(p, answer), nil
}

type changeWriter struct {
	out   io.Writer
	root  string
	fired bool
}

func (w *changeWriter) Write(data []byte) (int, error) {
	if !w.fired && strings.Contains(string(data), "Final push authorization") {
		w.fired = true
		if err := os.WriteFile(filepath.Join(w.root, "changed-after-review.txt"), []byte("simulated developer edit\n"), 0600); err != nil {
			return 0, err
		}
	}
	return w.out.Write(data)
}
