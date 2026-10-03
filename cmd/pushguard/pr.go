package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pushguard/pushguard/internal/app"
	"github.com/pushguard/pushguard/internal/codehost"
	"github.com/pushguard/pushguard/internal/config"
	"github.com/pushguard/pushguard/internal/delivery"
	"github.com/pushguard/pushguard/internal/receipt"
	"github.com/pushguard/pushguard/internal/repository"
	"github.com/pushguard/pushguard/internal/runner"
	"github.com/pushguard/pushguard/internal/security"
	"github.com/pushguard/pushguard/internal/ui"
)

func prCommand(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) int {
	command := "create"
	number := 0
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command = args[0]
		args = args[1:]
		if command != "status" && command != "review" && command != "repair" && command != "key" && command != "receipt" {
			fmt.Fprintln(errOut, "Usage: pushguard pr [status|review|repair|key|receipt] [options]")
			return app.ExitConfig
		}
		if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
			var e error
			number, e = strconv.Atoi(args[0])
			if e != nil || number < 1 {
				fmt.Fprintln(errOut, "A positive PR number is required")
				return app.ExitConfig
			}
			args = args[1:]
		}
	}
	f := flag.NewFlagSet("pr "+command, flag.ContinueOnError)
	f.SetOutput(errOut)
	root := f.String("repo", ".", "Local repository directory")
	plain := f.Bool("plain", false, "Plain terminal output")
	jsonOut := f.Bool("json", false, "JSON output; never repair, commit, push or create/update PRs")
	non := f.Bool("non-interactive", false, "No interactive mutations")
	f.IntVar(&number, "number", number, "Pull request number (otherwise find current branch's open PR)")
	title := f.String("title", "", "PR title")
	bodyFile := f.String("body-file", "", "UTF-8 description file (verification section is appended)")
	base := f.String("base", "", "Base branch (defaults to configuration or repository default)")
	draft := f.Bool("draft", false, "Create a draft PR")
	wait := f.Duration("wait", 0, "Maximum time to wait for hosted checks, for example 5m")
	trustFile := f.String("trusted-keys", "", "Maintainer-controlled public-key JSON file; never loaded from PR content")
	f.Usage = func() {
		fmt.Fprintln(out, "Usage: pushguard pr [status|review|repair NUMBER|key|receipt] [options]")
		f.SetOutput(out)
		f.PrintDefaults()
	}
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return app.ExitConfig
	}
	if f.NArg() != 0 || *wait < 0 || *wait > 30*time.Minute {
		fmt.Fprintln(errOut, "Unexpected arguments or wait outside 0..30m")
		return app.ExitConfig
	}
	abs, err := filepath.Abs(*root)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return app.ExitConfig
	}
	u := ui.New(in, out, errOut, *non || *jsonOut)
	u.Plain = u.Plain || *plain
	problem := func(err error) int { u.Error(err.Error()); return app.ExitConfig }
	if command == "key" {
		keys, err := receipt.PublicTrust()
		if err != nil {
			return problem(err)
		}
		if err = ui.PrintJSON(out, keys); err != nil {
			return problem(err)
		}
		return 0
	}
	r := runner.Runner{}
	repo, err := (repository.Service{Runner: r}).Discover(ctx, abs)
	if err != nil {
		return problem(err)
	}
	abs = repo.Root
	if command == "receipt" {
		report, err := receipt.Latest(abs)
		if err != nil {
			return problem(err)
		}
		v, err := receipt.LoadVerification(abs, report.DeliveryReceiptID)
		if err != nil {
			return problem(err)
		}
		if err = ui.PrintJSON(out, v); err != nil {
			return problem(err)
		}
		return 0
	}
	cfg, _, err := config.Load(abs)
	if err != nil {
		return problem(err)
	}
	if command == "create" || command == "repair" {
		if command == "repair" && number < 1 {
			return problem(fmt.Errorf("pushguard pr repair requires a PR number"))
		}
		description := ""
		if *bodyFile != "" {
			data, e := boundedFile(*bodyFile, 16000)
			if e != nil {
				return problem(e)
			}
			description = string(data)
		}
		options := app.PROptions{Title: *title, Description: description, Base: *base, Wait: *wait}
		if command == "repair" {
			options.Number = number
		}
		f.Visit(func(v *flag.Flag) {
			if v.Name == "draft" {
				options.Draft = draft
			}
		})
		a := app.New(u, *non || *jsonOut)
		a.Workdir = abs
		return a.RunPR(ctx, *jsonOut, options)
	}
	if !cfg.GitHub.Enabled {
		return problem(fmt.Errorf("GitHub integration is disabled in the selected configuration"))
	}
	provider := &codehost.GitHubProvider{Runner: r, Workdir: abs}
	remote, err := provider.DetectRepository(ctx, *repo)
	if err != nil {
		return problem(err)
	}
	if number == 0 {
		pr, e := provider.FindPullRequest(ctx, repo.Branch, *base)
		if e != nil {
			return problem(e)
		}
		if pr == nil {
			return problem(fmt.Errorf("no open PR for branch %s", repo.Branch))
		}
		number = pr.Number
	}
	keys, err := localTrust(*trustFile)
	if err != nil {
		return problem(err)
	}
	svc := delivery.Service{Provider: provider, Repository: *remote, Keys: keys, Required: cfg.GitHub.Checks.Required}
	deadline := time.Now().Add(*wait)
	for {
		snapshot, e := svc.Inspect(ctx, number)
		if e != nil {
			return problem(e)
		}
		if cfg.GitHub.Checks.Publish {
			delivery.RequirePublished(snapshot)
		}
		pending := snapshot.Overall == codehost.InProgress || snapshot.Overall == codehost.Queued || snapshot.Overall == codehost.Unknown
		if !pending || *wait == 0 || time.Now().After(deadline) {
			if command == "review" {
				snapshot.Files, e = provider.GetChangedFiles(ctx, number)
				if e != nil {
					return problem(e)
				}
			}
			if *jsonOut {
				if e = ui.PrintJSON(out, snapshot); e != nil {
					return problem(e)
				}
			} else {
				u.Say(snapshot.Summary)
				u.Say("PushGuard GitHub Check: " + string(snapshot.Published) + "\nURL: " + snapshot.PR.URL)
				if command == "review" {
					u.Say("Changed files:\n" + strings.Join(snapshot.Files, "\n"))
					if snapshot.Receipt != nil {
						for _, repair := range snapshot.Receipt.AIRepairs {
							u.Say("AI-assisted files: " + strings.Join(repair.Files, ", "))
						}
					}
				}
			}
			if snapshot.Overall == codehost.Failure || snapshot.Overall == codehost.Cancelled {
				return app.ExitVerification
			}
			return 0
		}
		if !*jsonOut {
			u.Status("WAITING", "hosted verification", snapshot.PR.URL)
		}
		timer := time.NewTimer(min(10*time.Second, time.Until(deadline)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return app.ExitCancelled
		case <-timer.C:
		}
	}
}

func boundedFile(path string, max int64) ([]byte, error) {
	if security.SensitivePath(path) {
		return nil, fmt.Errorf("sensitive file cannot be used as public metadata")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > max {
		return nil, fmt.Errorf("file must be regular and no larger than %d bytes", max)
	}
	return os.ReadFile(path)
}
func localTrust(file string) (map[string]ed25519.PublicKey, error) {
	if file != "" {
		data, err := boundedFile(file, 64<<10)
		if err != nil {
			return nil, err
		}
		return receipt.ParseTrust(data)
	}
	if raw := os.Getenv("PUSHGUARD_TRUSTED_KEYS"); raw != "" {
		return receipt.ParseTrust([]byte(raw))
	}
	return receipt.PublicTrust()
}

// Used by doctor; capability hints never claim token-specific write permission.
func githubDoctor(ctx context.Context, root string, cfg config.Config) []map[string]string {
	if !cfg.GitHub.Enabled {
		return []map[string]string{{"name": "GitHub integration", "status": "SKIPPED", "detail": "disabled; local verification remains available"}}
	}
	r := runner.Runner{}
	auth := r.Run(ctx, root, []string{"gh", "auth", "status", "--active", "--hostname", "github.com"}, 10*time.Second)
	if auth.ExitCode != 0 {
		return []map[string]string{{"name": "GitHub authentication", "status": "FAIL", "detail": "Run gh auth login, or provide an authorized GH_TOKEN outside repository configuration"}}
	}
	items := []map[string]string{{"name": "GitHub authentication", "status": "PASS", "detail": "gh authentication is available"}}
	repo, err := (repository.Service{Runner: r}).Discover(ctx, root)
	if err == nil {
		g := &codehost.GitHubProvider{Runner: r, Workdir: root}
		var remote *codehost.RemoteRepository
		remote, err = g.DetectRepository(ctx, *repo)
		if err == nil {
			items = append(items, map[string]string{"name": "GitHub repository", "status": "PASS", "detail": remote.Identity()})
			detail := "repository write access unavailable"
			if remote.CanPush {
				detail = "repository write access observed; token-specific Pull Request permission is confirmed only by an authorized API operation"
			}
			items = append(items, map[string]string{"name": "Pull Request permissions", "status": "UNKNOWN", "detail": detail})
		}
	}
	if err != nil {
		items = append(items, map[string]string{"name": "GitHub repository", "status": "FAIL", "detail": err.Error()})
	}
	return items
}
