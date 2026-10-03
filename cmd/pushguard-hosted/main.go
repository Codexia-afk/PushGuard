// pushguard-hosted is run from maintainer-trusted code with narrowly scoped
// hosting credentials. It never checks out, executes, repairs, pushes or merges
// pull-request code. Installing its workflow authorizes check publication only.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/pushguard/pushguard/internal/codehost"
	"github.com/pushguard/pushguard/internal/delivery"
	"github.com/pushguard/pushguard/internal/model"
	"github.com/pushguard/pushguard/internal/receipt"
	"github.com/pushguard/pushguard/internal/runner"
	"github.com/pushguard/pushguard/internal/ui"
)

type names []string

func (n *names) String() string { return strings.Join(*n, ",") }
func (n *names) Set(s string) error {
	if strings.TrimSpace(s) == "" || len(s) > 200 {
		return fmt.Errorf("invalid check name")
	}
	*n = append(*n, s)
	return nil
}
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, out, errOut io.Writer) int {
	f := flag.NewFlagSet("pushguard-hosted", flag.ContinueOnError)
	f.SetOutput(errOut)
	repository := f.String("repository", os.Getenv("GITHUB_REPOSITORY"), "GitHub owner/repository")
	pr := f.Int("pr", 0, "Pull request number")
	event := f.String("event-file", os.Getenv("GITHUB_EVENT_PATH"), "Trusted GitHub Actions event file (alternative to --pr)")
	trust := f.String("trusted-keys", "", "Maintainer-controlled public key JSON file (or PUSHGUARD_TRUSTED_KEYS environment)")
	publish := f.Bool("publish", false, "Explicitly authorize Check API writes; no other remote mutation is supported")
	jsonOut := f.Bool("json", false, "Emit structured snapshots")
	var required names
	f.Var(&required, "require", "Expected hosted check name; repeatable, configured by maintainers")
	if err := f.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if f.NArg() != 0 {
		return 2
	}
	u := ui.New(strings.NewReader(""), out, errOut, true)
	problem := func(err error) int { u.Error(err.Error()); return 2 }
	if _, err := codehost.ParseGitHubRemote("https://github.com/" + *repository); err != nil {
		return problem(err)
	}
	data := []byte(os.Getenv("PUSHGUARD_TRUSTED_KEYS"))
	if len(data) == 0 {
		data = []byte("{}")
	}
	if *trust != "" {
		var err error
		data, err = readBounded(*trust, 64<<10)
		if err != nil {
			return problem(err)
		}
	}
	keys, err := receipt.ParseTrust(data)
	if err != nil {
		return problem(err)
	}
	root, err := os.Getwd()
	if err != nil {
		return problem(err)
	}
	provider := &codehost.GitHubProvider{Runner: runner.Runner{}, Workdir: root}
	remote, err := provider.DetectRepository(ctx, model.Repository{Target: model.PushTarget{RemoteURL: "https://github.com/" + *repository}})
	if err != nil {
		return problem(err)
	}
	targets := []delivery.EventTarget{}
	if *pr > 0 {
		targets = append(targets, delivery.EventTarget{Number: *pr})
	} else {
		if *event == "" {
			return problem(fmt.Errorf("--pr or --event-file is required"))
		}
		data, err := readBounded(*event, 4<<20)
		if err != nil {
			return problem(err)
		}
		targets, err = delivery.EventTargets(data, *repository)
		if err != nil {
			return problem(err)
		}
	}
	svc := delivery.Service{Provider: provider, Repository: *remote, Keys: keys, Required: required}
	var reports []*delivery.Snapshot
	code := 0
	for _, target := range targets {
		number := target.Number
		if number == 0 {
			pr, e := provider.FindPullRequest(ctx, target.Branch, "")
			if e != nil {
				return problem(e)
			}
			if pr == nil {
				continue
			}
			number = pr.Number
		}
		snapshot, e := svc.Inspect(ctx, number)
		if e != nil {
			return problem(e)
		}
		if target.HeadSHA != "" && target.HeadSHA != snapshot.PR.HeadSHA {
			if !*jsonOut {
				u.Say("Stale event ignored; PR HEAD has changed.")
			}
			continue
		}
		if *publish {
			if e = svc.Publish(ctx, snapshot); e != nil {
				return problem(e)
			}
		}
		reports = append(reports, snapshot)
		if !*jsonOut {
			u.Say(snapshot.Summary)
			if *publish {
				u.Say("GitHub Check updated: " + snapshot.PR.URL)
			}
		}
		if snapshot.Overall == codehost.Failure || snapshot.Overall == codehost.Cancelled {
			code = 1
		}
	}
	if *jsonOut {
		if err = ui.PrintJSON(out, reports); err != nil {
			return problem(err)
		}
	}
	return code
}
func readBounded(path string, max int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > max {
		return nil, fmt.Errorf("file must be regular and bounded")
	}
	return os.ReadFile(path)
}
