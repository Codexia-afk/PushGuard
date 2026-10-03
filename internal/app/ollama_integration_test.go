package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pushguard/pushguard/internal/config"
	"github.com/pushguard/pushguard/internal/llm"
	"github.com/pushguard/pushguard/internal/model"
	"github.com/pushguard/pushguard/internal/runner"
	"github.com/pushguard/pushguard/internal/testutil"
	"github.com/pushguard/pushguard/internal/ui"
	"github.com/pushguard/pushguard/internal/verify"
)

// Explicit opt-in: installs pinned Node tool versions into a disposable Git
// repository, but NEVER pulls a model. Scripted decisions exercise the same
// independent approval gates as the CLI; no unattended mode exists in the product.
// A completed acceptance test may report REPAIR_LIMIT rather than repaired code.
func TestOllamaLargeAcceptance(t *testing.T) {
	if os.Getenv("PUSHGUARD_OLLAMA_ACCEPTANCE") != "1" {
		t.Skip("opt-in real local Ollama/Node acceptance")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	name := os.Getenv("PUSHGUARD_OLLAMA_MODEL")
	if name == "" {
		name = "qwen2.5-coder:7b"
	}
	provider := llm.Ollama{Endpoint: "http://127.0.0.1:11434", Model: name}
	if err := provider.Available(ctx); err != nil {
		t.Fatalf("install/start the selected local model yourself before opting in: %v", err)
	}
	root, remote := testutil.Repository(t)
	fixture := filepath.Join("..", "..", "testdata", "ollama-large")
	if err := filepath.WalkDir(fixture, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(fixture, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		testutil.Write(t, root, rel, string(data))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	source, _ := os.ReadFile(filepath.Join(root, "src/calculations.js"))
	lines := bytes.Count(source, []byte("\n"))
	if lines < 180 || lines > 230 {
		t.Fatalf("fixture is not roughly 200 lines: %d", lines)
	}
	cfg := config.Default()
	cfg.AI = config.AIConfig{Provider: "ollama", Model: name, Endpoint: provider.Endpoint, LocalFirst: true, MaxOutputTokens: 2048, RequestTimeoutSeconds: 300, Context: config.ContextBudget{MaxInputTokens: 6000, ReservedOutputTokens: 2048, SafetyMarginTokens: 512}}
	cfg.Repair.MaxAttempts, cfg.Repair.MaxDiagnosticsPerGroup = 6, 10
	for _, name := range []string{"lint", "typecheck", "test", "build"} {
		cfg.Checks = append(cfg.Checks, model.Check{Name: name, Category: name, Args: []string{"npm", "run", name}, Required: true, TimeoutSecs: 60})
	}
	if err := config.Save(root, cfg); err != nil {
		t.Fatal(err)
	}
	install := runner.Runner{}.Run(ctx, root, []string{"npm", "install", "--ignore-scripts", "--no-audit", "--no-fund", "--package-lock=false"}, 2*time.Minute)
	if install.ExitCode != 0 {
		t.Fatalf("fixture tool installation failed: %s", install.Stderr)
	}
	testutil.Commit(t, root)
	head := testutil.Git(t, root, "rev-parse", "HEAD")
	v := verify.Service{Runner: runner.Runner{}}
	baseline := v.RunAll(ctx, root, cfg.Checks, false)
	if baseline[0].Status != model.StatusFail || len(baseline[0].Diagnostics) != 50 || baseline[1].Status != model.StatusPass || baseline[2].Status != model.StatusFail || baseline[3].Status != model.StatusPass {
		t.Fatalf("unexpected genuine fixture baseline: %+v", baseline)
	}
	var transcript bytes.Buffer
	a := New(ui.New(strings.NewReader(strings.Repeat("allow\n", 20)+"keep\n"), &transcript, &transcript, false), false)
	a.Workdir = root
	code := a.RunCheck(ctx, false)
	final := v.RunAll(ctx, root, cfg.Checks, false)
	if head != testutil.Git(t, root, "rev-parse", "HEAD") || a.Report.PushResult != nil {
		t.Fatal("check committed or pushed")
	}
	remoteEmpty(t, root, remote)
	changed := strings.Fields(testutil.Git(t, root, "diff", "--name-only", "HEAD"))
	for _, path := range changed {
		if path != "src/calculations.js" {
			t.Fatalf("protected evidence changed: %s", path)
		}
	}
	if a.Report.RepairMetrics.AIRequests > 6 || a.Report.RepairMetrics.Attempts > 6 {
		t.Fatal("repair/request cap exceeded")
	}
	for _, audit := range a.Report.RepairAudit {
		if audit.Inference != nil && audit.Inference.EstimatedInputTokens > 6000 {
			t.Fatal("oversized inference sent")
		}
		if audit.Applied && (audit.RepairDecision != "ALLOW" || audit.PatchDecision != "ALLOW") {
			t.Fatal("independent approvals bypassed")
		}
	}
	if code == ExitOK && (!a.Report.ReviewComplete || len(verify.Blocking(final)) != 0 || !strings.Contains(transcript.String(), "FULL VERIFICATION")) {
		t.Fatal("false PASS")
	}
	if code != ExitOK && code != ExitRepairLimit && code != ExitPatch && code != ExitAI {
		t.Fatalf("acceptance interrupted unexpectedly (exit %d):\n%s", code, transcript.String())
	}
	artifacts := t.TempDir()
	if base := os.Getenv("PUSHGUARD_OLLAMA_ARTIFACTS"); base != "" {
		var err error
		artifacts, err = os.MkdirTemp(base, "ollama-acceptance-")
		if err != nil {
			t.Fatal(err)
		}
	}
	writeJSON := func(name string, value any) {
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		testutil.Write(t, artifacts, name, string(data)+"\n")
	}
	writeJSON("baseline.json", baseline)
	writeJSON("final.json", final)
	writeJSON("session.json", a.Report)
	writeJSON("outcome.json", map[string]any{"model": name, "endpoint": provider.Endpoint, "sourceLines": lines, "pushguardExit": code, "repairSucceeded": code == 0, "metrics": a.Report.RepairMetrics})
	testutil.Write(t, artifacts, "transcript.log", transcript.String())
	after, _ := os.ReadFile(filepath.Join(root, "src/calculations.js"))
	testutil.Write(t, artifacts, "source-final.js", string(after))
	t.Logf("Real local model=%s, source lines=%d, exit=%d, attempts=%d, requests=%d, applied=%d, rejected=%d; artifacts=%s", name, lines, code, a.Report.RepairMetrics.Attempts, a.Report.RepairMetrics.AIRequests, a.Report.RepairMetrics.Applied, a.Report.RepairMetrics.Rejected, artifacts)
	for i, r := range final {
		t.Logf("%s: %s (%d initial -> %d final captured diagnostics)", r.Check.Name, r.Status, len(baseline[i].Diagnostics), len(r.Diagnostics))
	}
	if code != 0 {
		t.Log("REPAIR DID NOT SUCCEED. The acceptance harness passed its bounded, honest-failure assertions; that is not a repository PASS.")
	}
}
