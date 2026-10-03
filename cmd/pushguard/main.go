package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/pushguard/pushguard/internal/app"
	"github.com/pushguard/pushguard/internal/config"
	"github.com/pushguard/pushguard/internal/demo"
	"github.com/pushguard/pushguard/internal/detector"
	"github.com/pushguard/pushguard/internal/diagnostics"
	"github.com/pushguard/pushguard/internal/integrity"
	"github.com/pushguard/pushguard/internal/model"
	"github.com/pushguard/pushguard/internal/patch"
	"github.com/pushguard/pushguard/internal/preflight"
	"github.com/pushguard/pushguard/internal/receipt"
	"github.com/pushguard/pushguard/internal/repository"
	"github.com/pushguard/pushguard/internal/runner"
	"github.com/pushguard/pushguard/internal/security"
	"github.com/pushguard/pushguard/internal/ui"
)

func main() {
	// SIGINT and SIGTERM cancel the workflow; deferred cleanup releases the
	// repository lock (the OS also releases it if the process dies).
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	os.Exit(runWith(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
func run() int { return runWith(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr) }
func runWith(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) int {
	if len(args) == 0 {
		usage(out)
		return app.ExitConfig
	}
	command := args[0]
	if command == "pr" {
		return prCommand(ctx, args[1:], in, out, errOut)
	}
	if command == "ai" {
		if len(args) < 2 || args[1] != "status" {
			fmt.Fprintln(errOut, "Usage: pushguard ai status [--test] [--json] [--repo PATH]")
			return app.ExitConfig
		}
		command = "ai-status"
		args = append([]string{command}, args[2:]...)
	}
	if command == "help" || command == "--help" || command == "-h" {
		usage(out)
		return 0
	}
	if command == "--version" || command == "-v" {
		command = "version"
	}
	allowed := map[string]bool{"ai-status": true, "push": true, "check": true, "ready": true, "init": true, "doctor": true, "status": true, "config": true, "review": true, "rollback": true, "explain": true, "version": true, "hook-check": true, "demo": true}
	if !allowed[command] {
		fmt.Fprintf(errOut, "Unknown command %q. Run pushguard help.\n", command)
		return app.ExitConfig
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(errOut)
	root := flags.String("repo", "", "Project directory (defaults to the current directory)")
	plain := flags.Bool("plain", false, "Plain terminal output")
	var jsonOut, non, hook, all, testAI bool
	scenario := "repair"
	switch command {
	case "ai-status":
		flags.BoolVar(&jsonOut, "json", false, "Emit AI readiness as JSON")
		flags.BoolVar(&testAI, "test", false, "Make an explicit context-free structured generation request (no repository files)")
	case "demo":
		flags.StringVar(&scenario, "scenario", "repair", "repair, repair-deny, patch-deny, pass, deny, stale, all, or live (real configured provider)")
	case "push", "check", "ready":
		flags.BoolVar(&jsonOut, "json", false, "Emit a versioned JSON report; never push or repair")
		flags.BoolVar(&non, "non-interactive", false, "Run without prompts; never repair or push")
	case "doctor", "status", "config", "review", "explain":
		flags.BoolVar(&jsonOut, "json", false, "Emit JSON")
	case "init":
		flags.BoolVar(&hook, "hook", false, "Also install a receipt-checking pre-push hook")
	case "rollback":
		flags.BoolVar(&non, "non-interactive", false, "Never modify files without a prompt")
		flags.BoolVar(&all, "all", false, "Restore all snapshots recorded in the latest repair session")
	}
	flags.Usage = func() {
		fmt.Fprintf(out, "Usage: pushguard %s [options]\n", command)
		flags.SetOutput(out)
		flags.PrintDefaults()
	}
	// Flags may follow positional file arguments: pushguard check foo.py --json.
	var positional []string
	rest := args[1:]
	for {
		if err := flags.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return app.ExitConfig
		}
		rest = flags.Args()
		if len(rest) == 0 {
			break
		}
		positional = append(positional, rest[0])
		rest = rest[1:]
	}
	if len(positional) > 0 && command != "check" && command != "ready" {
		fmt.Fprintln(errOut, "Unexpected arguments:", strings.Join(positional, " "))
		return app.ExitConfig
	}
	// pushguard check FILE...: verify only those files; works without Git.
	var targets []string
	for _, p := range positional {
		abs, err := filepath.Abs(p)
		if err == nil {
			abs, err = filepath.EvalSymlinks(abs)
		}
		var info os.FileInfo
		if err == nil {
			info, err = os.Stat(abs)
		}
		if err != nil {
			fmt.Fprintf(errOut, "Cannot check %s: %v\n", p, err)
			return app.ExitConfig
		}
		if info.IsDir() {
			if *root == "" {
				*root = abs
			}
			continue
		}
		targets = append(targets, abs)
		if *root == "" {
			*root = filepath.Dir(abs)
		}
	}
	if *root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(errOut, err)
			return app.ExitConfig
		}
		*root = cwd
	}
	absolute, err := filepath.Abs(*root)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return app.ExitConfig
	}
	absolute, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return app.ExitConfig
	}
	u := ui.New(in, out, errOut, non || jsonOut)
	u.BindContext(ctx)
	if *plain {
		u.Plain = true
	}
	r := runner.Runner{}
	service := repository.Service{Runner: r}
	switch command {
	case "ai-status":
		return aiStatusCommand(ctx, absolute, u, jsonOut, testAI)
	case "demo":
		if err := demo.Run(ctx, out, errOut, scenario); err != nil {
			u.Error(err.Error())
			return app.ExitVerification
		}
		return 0
	case "version":
		fmt.Fprintf(out, "PushGuard v%s\nCommit: %s\nBuilt: %s\nGo: %s\nPlatform: %s/%s\n", model.Version, model.BuildCommit, model.BuildDate, runtime.Version(), runtime.GOOS, runtime.GOARCH)
		return 0
	case "push", "check", "ready":
		if command == "ready" {
			non = true
		}
		a := app.New(u, non || jsonOut)
		a.Workdir = absolute
		a.TargetPaths = targets
		if command == "push" {
			return a.RunPush(ctx, jsonOut)
		}
		return a.RunCheck(ctx, jsonOut)
	case "init":
		if repo, err := service.Discover(ctx, absolute); err == nil {
			absolute = repo.Root
		}
		cfg, _, err := config.Load(absolute)
		if err != nil {
			u.Error(err.Error())
			return app.ExitConfig
		}
		if err := config.Save(absolute, cfg); err != nil {
			u.Error(err.Error())
			return app.ExitConfig
		}
		u.Say("Created " + filepath.Join(absolute, ".pushguard.json"))
		if hook {
			if err := installHook(ctx, absolute, r); err != nil {
				u.Error(err.Error())
				return app.ExitConfig
			}
			u.Say("Installed pre-push receipt gate.")
		}
		return 0
	case "doctor":
		return doctor(ctx, absolute, u, jsonOut)
	case "config":
		if repo, err := service.Discover(ctx, absolute); err == nil {
			absolute = repo.Root
		}
		cfg, path, err := config.Load(absolute)
		if err != nil {
			u.Error(err.Error())
			return app.ExitConfig
		}
		if !jsonOut {
			u.Say("Configuration: " + path)
		}
		cfg.AI.Endpoint = security.Redact(cfg.AI.Endpoint)
		for i := range cfg.Checks {
			cfg.Checks[i].Command = security.Redact(cfg.Checks[i].Command)
			for j := range cfg.Checks[i].Args {
				cfg.Checks[i].Args[j] = security.Redact(cfg.Checks[i].Args[j])
			}
		}
		if err := ui.PrintJSON(out, cfg); err != nil {
			return app.ExitConfig
		}
		return 0
	}
	repo, err := service.Discover(ctx, absolute)
	if err != nil {
		u.Error(err.Error())
		return app.ExitConfig
	}
	absolute = repo.Root
	previous, previousErr := receipt.Latest(absolute)
	fresh := false
	if previousErr == nil && previous.Fingerprint != nil {
		cfg, _, e := config.Load(absolute)
		if e == nil {
			fp, e := integrity.Compute(ctx, absolute, config.Hash(cfg), r)
			fresh = e == nil && previous.ConfigHash == config.Hash(cfg) && fp.Value == previous.Fingerprint.Value
		}
	}
	switch command {
	case "status":
		if jsonOut {
			return printJSON(out, map[string]any{"schemaVersion": "2", "repository": repo, "receiptAvailable": previousErr == nil, "receiptCurrent": fresh})
		}
		u.Say(fmt.Sprintf("%s\nBranch: %s\nTarget: %s/%s\nOutgoing: %d commits, %d files\nLocal edits: %d\nVerification receipt current: %t", repo.Project.Name, repo.Branch, repo.Target.Remote, repo.Target.Branch, repo.Changes.Commits, len(repo.Changes.Files), len(repo.Changes.All), fresh))
		return 0
	case "explain":
		if previousErr != nil {
			u.Say("No session evidence exists yet. Run pushguard check first.")
			return app.ExitCancelled
		}
		if jsonOut {
			return printJSON(out, security.SanitizeReport(previous))
		}
		u.Say(fmt.Sprintf("Latest session: %s (%s)\nState: %s\nEvidence matches current repository: %t", previous.SessionID, previous.Operation, previous.State, fresh))
		if previous.Error != "" {
			u.Say(previous.Error)
		}
		for _, d := range previous.Diagnostics {
			u.Say(diagnostics.Render(absolute, d, 3))
		}
		for _, p := range previous.Repairs {
			u.Say("Repair: " + p.Summary + "\nLikely root cause: " + p.RootCause)
		}
		if len(previous.Diagnostics) == 0 && previous.Error == "" {
			u.Say("All required checks passed for the recorded state.")
		}
		return 0
	case "review":
		if jsonOut {
			return printJSON(out, map[string]any{"schemaVersion": "2", "repository": repo, "session": security.SanitizeReport(previous), "receiptCurrent": fresh})
		}
		u.Say(fmt.Sprintf("%d outgoing commits, %d outgoing files, %d local edits", repo.Changes.Commits, len(repo.Changes.Files), len(repo.Changes.All)))
		for _, entry := range previous.Review {
			u.Say(entry.File + ": " + entry.Reason)
		}
		for _, proposal := range previous.Repairs {
			u.Say(proposal.Patch)
		}
		args := []string{"git", "diff", "--no-color", "--no-ext-diff", "--no-textconv", "HEAD", "--"}
		if repo.HEAD == "" {
			args = []string{"git", "diff", "--cached", "--no-ext-diff", "--no-textconv", "--"}
		}
		res := r.Run(ctx, absolute, args, 30*time.Second)
		if res.ExitCode != 0 || res.Truncated {
			u.Error("Diff evidence incomplete.")
			return app.ExitConfig
		}
		u.Say(res.Stdout)
		if repo.HEAD != "" {
			diff, e := service.OutgoingDiff(ctx, repo)
			if e != nil {
				u.Error(e.Error())
				return app.ExitConfig
			}
			u.Say("Outgoing commit diff:")
			u.Say(diff)
		}
		if len(repo.Changes.Untracked) > 0 {
			u.Say("Untracked files (not part of Git push):\n" + strings.Join(repo.Changes.Untracked, "\n"))
		}
		return 0
	case "rollback":
		store := patch.SnapshotStore{}
		snapshot, err := store.LatestFor(absolute)
		if err != nil {
			u.Say("No repair snapshot is available for this repository.")
			return app.ExitCancelled
		}
		snapshots := []model.Snapshot{snapshot}
		if all {
			if previousErr != nil || len(previous.Snapshots) == 0 {
				u.Error("No complete repair session snapshot history is available.")
				return app.ExitCancelled
			}
			snapshots = previous.Snapshots
		}
		u.Say(fmt.Sprintf("Restore %d snapshot(s) in reverse order. Files: %s", len(snapshots), strings.Join(snapshot.Files, ", ")))
		if !u.Prompt("Restore these approved repair snapshots?", "[Y] Restore", "[N] Stop") {
			return app.ExitCancelled
		}
		for i := len(snapshots) - 1; i >= 0; i-- {
			if err := store.Restore(absolute, snapshots[i]); err != nil {
				u.Error(err.Error())
				return app.ExitPatch
			}
		}
		if err = store.ClearLatestFor(absolute); err != nil {
			u.Error(err.Error())
			return app.ExitPatch
		}
		if _, err = receipt.Save(model.SessionReport{SchemaVersion: "2", Version: model.Version, Root: absolute, Operation: "rollback", Status: model.StatusSkipped, GeneratedAt: time.Now().UTC()}); err != nil {
			u.Error(err.Error())
			return app.ExitConfig
		}
		u.Say("Restored affected files. Previous verification is invalid; run pushguard check.")
		return 0
	case "hook-check":
		if previousErr != nil || !fresh || previous.Fingerprint == nil || previous.Status == model.StatusFail || time.Since(previous.Fingerprint.CreatedAt) > 15*time.Minute {
			u.Error("Recent verification is required. Run pushguard check or pushguard push.")
			return app.ExitVerification
		}
		// Git supplies the refs being pushed on stdin. Every non-deletion source must
		// match the verified HEAD; one receipt cannot authorize unrelated branches.
		data, err := io.ReadAll(io.LimitReader(in, 1<<20))
		if err != nil {
			return app.ExitConfig
		}
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if line == "" {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) != 4 || fields[1] != previous.Fingerprint.HEAD {
				u.Error("Push ref is not the verified HEAD.")
				return app.ExitVerification
			}
		}
		return 0
	}
	return app.ExitConfig
}
func printJSON(out io.Writer, value any) int {
	if err := ui.PrintJSON(out, value); err != nil {
		return app.ExitConfig
	}
	return 0
}
func doctor(ctx context.Context, root string, u *ui.UI, jsonOut bool) int {
	type item struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		Detail string `json:"detail"`
	}
	var items []item
	code := 0
	service := repository.Service{Runner: runner.Runner{}}
	if repo, err := service.Discover(ctx, root); err != nil {
		items = append(items, item{"repository", "WARN", "not detected: " + err.Error()})
	} else {
		root = repo.Root
		detail := repo.Project.Name
		if repo.Branch != "" {
			detail += " | branch " + repo.Branch
		}
		if repo.Target.Remote != "" {
			detail += " | remote " + repo.Target.Remote
		}
		items = append(items, item{"repository", "PASS", detail})
	}
	check := func(name string, args []string, required bool) {
		res := runner.Runner{}.Run(ctx, root, args, 10*time.Second)
		status := "PASS"
		detail := strings.TrimSpace(res.Stdout)
		if res.ExitCode != 0 {
			status = "FAIL"
			detail = strings.TrimSpace(res.Stderr + " " + res.Terminated)
			if required {
				code = app.ExitConfig
			} else {
				status = "WARN"
			}
		}
		items = append(items, item{name, status, detail})
	}
	check("Git", []string{"git", "--version"}, true)
	check("GitHub CLI", []string{"gh", "--version"}, false)
	check("Docker", []string{"docker", "--version"}, false)
	cfg, path, err := config.Load(root)
	if err != nil {
		items = append(items, item{"configuration", "FAIL", err.Error()})
		code = app.ExitConfig
	} else {
		items = append(items, item{"configuration", "PASS", path})
		checks, e := detector.Checks(root, cfg)
		if e != nil {
			items = append(items, item{"checks", "FAIL", e.Error()})
			code = app.ExitConfig
		} else {
			seen := map[string]bool{}
			for _, c := range checks {
				if len(c.Args) == 0 || seen[c.Args[0]] {
					continue
				}
				seen[c.Args[0]] = true
				name := c.Args[0]
				if name == "git" {
					continue
				}
				res := runner.Runner{}.Run(ctx, root, []string{name, "--version"}, 10*time.Second)
				if name == "go" {
					res = runner.Runner{}.Run(ctx, root, []string{name, "version"}, 10*time.Second)
				}
				if name == "gofmt" {
					res = runner.Runner{}.Run(ctx, root, []string{name, "-h"}, 10*time.Second)
					if res.ExitCode == 0 {
						res.Stdout = "available (Go formatter)"
					}
				}
				status := "PASS"
				detail := strings.TrimSpace(res.Stdout)
				if res.ExitCode != 0 {
					status = "FAIL"
					detail = res.Terminated + " " + res.Stderr
					if c.Required {
						code = app.ExitConfig
					}
				}
				items = append(items, item{name, status, detail})
			}
		}
		ai := inspectAI(ctx, cfg, false)
		items = append(items, item{"AI service", string(ai.Service), ai.Provider + " | " + ai.Endpoint}, item{"AI model", string(ai.ModelStatus), ai.Model}, item{"AI generation", string(ai.Generation), ai.NextStep})
		if ai.Problem != "" {
			items = append(items, item{"AI repair", "WARNING", ai.Problem + " | Selected configuration: " + path})
		}
		for _, v := range githubDoctor(ctx, root, cfg) {
			items = append(items, item{v["name"], v["status"], v["detail"]})
		}
	}
	if base, e := receipt.Base(); e != nil {
		code = app.ExitConfig
		items = append(items, item{"evidence storage", "FAIL", e.Error()})
	} else {
		if e = os.MkdirAll(base, 0700); e == nil {
			e = preflight.Writable(base)
		}
		if e != nil {
			code = app.ExitConfig
			items = append(items, item{"evidence storage", "FAIL", e.Error()})
		} else {
			items = append(items, item{"evidence storage", "PASS", base})
		}
	}
	if jsonOut {
		returnCode := printJSON(u.Out, map[string]any{"schemaVersion": "2", "items": items, "ready": code == 0})
		if returnCode != 0 {
			return returnCode
		}
		return code
	}
	u.Title("PushGuard doctor")
	for _, v := range items {
		u.Status(v.Status, v.Name, v.Detail)
	}
	if code == 0 {
		u.Say("\nPushGuard is ready for local verification.")
	}
	return code
}
func installHook(ctx context.Context, root string, r runner.Runner) error {
	path := r.Run(ctx, root, []string{"git", "rev-parse", "--git-path", "hooks/pre-push"}, 10*time.Second)
	if path.ExitCode != 0 {
		return fmt.Errorf("hook requires a Git repository")
	}
	name := strings.TrimSpace(path.Stdout)
	if !filepath.IsAbs(name) {
		name = filepath.Join(root, name)
	}
	if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0755)
	if err != nil {
		return fmt.Errorf("existing hooks are preserved: %w", err)
	}
	_, err = f.WriteString("#!/bin/sh\n# PushGuard receipt gate; installed by pushguard init --hook.\nexec pushguard hook-check\n")
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}
func usage(out io.Writer) {
	fmt.Fprint(out, `PushGuard — verify locally, review clearly, push with human approval.

  pushguard push      Verify, optionally repair, review, and authorize a push
  pushguard pr        Verify, repair, review, push a feature branch, then authorize a PR
  pushguard pr status Observe commit-bound local evidence and hosted verification
  pushguard pr review Inspect PR files, AI provenance, receipt and hosted checks
  pushguard pr repair NUMBER  Re-enter the local repair workflow for an open PR
  pushguard pr key     Export this machine's receipt-signing PUBLIC key
  pushguard pr receipt Export the latest portable signed delivery receipt
  pushguard check     Interactive verification and approved repair; never push
  pushguard check FILE...  Verify only the named files (works without Git)
  pushguard ready     Run every check without repair (automation)
  pushguard init      Create safe project configuration
  pushguard doctor    Check tools, configuration, and evidence storage
  pushguard ai status Show selected provider, service/model availability; --test probes generation
  pushguard status    Show repository and verification status
  pushguard review    Inspect outgoing commits, local changes, and repair history
  pushguard explain   Explain the latest recorded verification
  pushguard rollback  Restore the latest approved repair snapshot
  pushguard config    Show effective configuration
  pushguard version   Show installed version
  pushguard demo      Demonstrate repairs and push gates on local temporary repos

Use --repo PATH to work on another project.
check uses the same repair gates as push, but can never push.
ready and check --json/--non-interactive run checks without repair or push.
push --json/--non-interactive verifies gates and stops without authorization.
Run pushguard COMMAND --help for command options.
`)
}
