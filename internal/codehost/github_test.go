package codehost

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pushguard/pushguard/internal/model"
)

type call struct {
	method, path, input string
	args                []string
}
type ghFixture struct {
	t       *testing.T
	calls   []call
	respond func(call) model.CommandResult
}

func (f *ghFixture) RunInput(_ context.Context, _ string, args []string, _ time.Duration, input string) model.CommandResult {
	f.t.Helper()
	c := call{input: input, args: args}
	for i, s := range args {
		if s == "--method" {
			c.method = args[i+1]
		}
		if strings.HasPrefix(s, "repos/") {
			c.path = s
		}
	}
	if len(args) < 2 || args[0] != "gh" || args[1] != "api" {
		f.t.Fatal("not a gh API invocation")
	}
	f.calls = append(f.calls, c)
	return f.respond(c)
}
func TestGitHubRemoteParsing(t *testing.T) {
	for _, input := range []string{"https://github.com/acme/example.git", "git@github.com:acme/example.git", "ssh://git@github.com/acme/example"} {
		r, e := ParseGitHubRemote(input)
		if e != nil || r.Identity() != "github.com/acme/example" {
			t.Fatalf("%s: %v", input, e)
		}
	}
	for _, input := range []string{"https://github.com.evil/acme/example", "https://token@github.com/acme/example", "https://github.com/acme/example?token=secret", "https://github.com/acme/../example", "https://github.com/acme/%2fexample", "file:///tmp/repo", "git@github.com:acme/example\n--force", "https://github.com/acme/example/", "ssh://attacker@github.com/acme/example"} {
		if _, err := ParseGitHubRemote(input); err == nil {
			t.Fatalf("unsafe remote accepted: %s", input)
		}
	}
}
func TestGitHubStateMappingNeverConvertsPendingToPass(t *testing.T) {
	for _, tc := range []struct {
		status, conclusion string
		want               CheckState
	}{{"queued", "success", Queued}, {"in_progress", "success", InProgress}, {"completed", "success", Success}, {"completed", "failure", Failure}, {"completed", "timed_out", Failure}, {"completed", "cancelled", Cancelled}, {"completed", "skipped", Neutral}, {"completed", "neutral", Neutral}, {"completed", "", Unknown}, {"unexpected", "success", Unknown}} {
		if got := MapCheck(tc.status, tc.conclusion); got != tc.want {
			t.Fatalf("%+v: %s", tc, got)
		}
	}
}
func TestAPIErrorKindsAndCredentialRedaction(t *testing.T) {
	for _, tc := range []struct {
		code int
		kind ErrorKind
	}{{401, Authentication}, {403, Permission}, {404, NotFound}, {429, RateLimit}, {422, Invalid}, {503, Network}} {
		f := &ghFixture{t: t, respond: func(call) model.CommandResult {
			return model.CommandResult{ExitCode: 1, Stderr: fmt.Sprintf("HTTP %d gho_%s", tc.code, strings.Repeat("x", 40))}
		}}
		g := GitHubProvider{Runner: f, Repository: RemoteRepository{Host: "github.com", Owner: "acme", Name: "example"}}
		err := g.api(context.Background(), "GET", g.prefix(), nil, new(any))
		var typed *Error
		if !errors.As(err, &typed) || typed.Kind != tc.kind || strings.Contains(err.Error(), strings.Repeat("x", 40)) {
			t.Fatalf("unexpected error: %v", err)
		}
	}
}
func TestPRMetadataIsJSONDataNotShellCode(t *testing.T) {
	sha := strings.Repeat("a", 40)
	f := &ghFixture{t: t, respond: func(c call) model.CommandResult {
		if c.method != "POST" || c.path != "repos/acme/example/pulls" {
			t.Fatal(c)
		}
		var body map[string]any
		if json.Unmarshal([]byte(c.input), &body) != nil || body["title"] != "$(touch /tmp/not-executed)" {
			t.Fatal("metadata was not transported as JSON")
		}
		return model.CommandResult{Stdout: `{"number":42,"state":"open","head":{"ref":"feature/fix","sha":"` + sha + `","repo":{"full_name":"acme/example"}},"base":{"ref":"main"}}`}
	}}
	g := GitHubProvider{Runner: f, Repository: RemoteRepository{Host: "github.com", Owner: "acme", Name: "example"}}
	pr, err := g.CreatePullRequest(context.Background(), CreatePullRequestInput{Title: "$(touch /tmp/not-executed)", Body: "Human description", Base: "main", Head: "feature/fix", Draft: true})
	if err != nil || pr.URL != "https://github.com/acme/example/pull/42" {
		t.Fatalf("%v %+v", err, pr)
	}
}
func TestPublishAddsAnnotationsWhenPendingCheckCompletes(t *testing.T) {
	sha := strings.Repeat("a", 40)
	f := &ghFixture{t: t}
	f.respond = func(c call) model.CommandResult {
		if c.method == "GET" {
			return model.CommandResult{Stdout: `{"total_count":1,"check_runs":[{"id":42,"name":"PushGuard Verification","external_id":"pushguard:old","status":"in_progress","output":{"annotations_count":0}}]}`}
		}
		if c.path != "repos/acme/example/check-runs/42" || c.method != "PATCH" {
			t.Fatal(c)
		}
		var body struct {
			Conclusion string
			Output     struct{ Annotations []Annotation }
		}
		if json.Unmarshal([]byte(c.input), &body) != nil || body.Conclusion != "failure" || len(body.Output.Annotations) != 50 {
			t.Fatalf("invalid payload: %s", c.input)
		}
		return model.CommandResult{Stdout: `{}`}
	}
	g := GitHubProvider{Runner: f, Repository: RemoteRepository{Host: "github.com", Owner: "acme", Name: "example"}}
	annotations := make([]Annotation, 60)
	for i := range annotations {
		annotations[i] = Annotation{Path: "src/a.ts", StartLine: 1, EndLine: 1, Level: "failure", Message: "verified diagnostic"}
	}
	if err := g.PublishVerification(context.Background(), HostedVerification{HeadSHA: sha, ReceiptID: "current", State: Failure, Summary: "Hosted failure", Annotations: annotations}); err != nil {
		t.Fatal(err)
	}
}
func TestChecksIncludeQueuedWorkflowsAndCommitStatuses(t *testing.T) {
	sha := strings.Repeat("a", 40)
	f := &ghFixture{t: t}
	f.respond = func(c call) model.CommandResult {
		switch {
		case strings.Contains(c.path, "check-runs"):
			return model.CommandResult{Stdout: `{"total_count":1,"check_runs":[{"id":1,"name":"lint","status":"completed","conclusion":"success"}]}`}
		case strings.Contains(c.path, "/statuses"):
			return model.CommandResult{Stdout: `[{"context":"external-tests","state":"pending"}]`}
		case strings.Contains(c.path, "actions/runs"):
			return model.CommandResult{Stdout: `{"total_count":1,"workflow_runs":[{"id":2,"workflow_id":3,"name":"Integration","status":"queued","head_sha":"` + sha + `"}]}`}
		default:
			t.Fatal(c)
			return model.CommandResult{ExitCode: 1}
		}
	}
	g := GitHubProvider{Runner: f, Repository: RemoteRepository{Host: "github.com", Owner: "acme", Name: "example"}}
	checks, err := g.GetChecks(context.Background(), sha)
	if err != nil || len(checks) != 3 || checks[1].State != Queued || checks[2].State != Queued {
		t.Fatalf("%v %+v", err, checks)
	}
}

func TestPublisherJobsDoNotCreateSelfDependency(t *testing.T) {
	sha := strings.Repeat("a", 40)
	f := &ghFixture{t: t}
	f.respond = func(c call) model.CommandResult {
		switch {
		case strings.Contains(c.path, "check-runs"):
			return model.CommandResult{Stdout: `{"total_count":2,"check_runs":[{"id":1,"name":"ci","status":"completed","conclusion":"success"},{"id":2,"name":"publish","status":"in_progress","html_url":"https://github.com/acme/example/actions/runs/99/job/2"}]}`}
		case strings.Contains(c.path, "/statuses"):
			return model.CommandResult{Stdout: `[]`}
		default:
			return model.CommandResult{Stdout: `{"total_count":1,"workflow_runs":[{"id":99,"name":"PushGuard receipt integration","status":"in_progress","head_sha":"` + sha + `"}]}`}
		}
	}
	g := GitHubProvider{Runner: f, Repository: RemoteRepository{Host: "github.com", Owner: "acme", Name: "example"}}
	checks, err := g.GetChecks(context.Background(), sha)
	if err != nil || len(checks) != 1 || checks[0].Name != "ci" {
		t.Fatalf("self-dependent evidence: %v %+v", err, checks)
	}
}

func TestRemoteCommitConfigurationIsFetchedAndValidated(t *testing.T) {
	sha, tree, blob := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	for _, mode := range []string{"100644", "120000"} {
		f := &ghFixture{t: t}
		f.respond = func(c call) model.CommandResult {
			switch {
			case strings.Contains(c.path, "git/commits/"):
				return model.CommandResult{Stdout: `{"tree":{"sha":"` + tree + `"}}`}
			case strings.Contains(c.path, "git/trees/"):
				return model.CommandResult{Stdout: `{"tree":[{"path":".pushguard.json","mode":"` + mode + `","type":"blob","sha":"` + blob + `"}]}`}
			case strings.Contains(c.path, "git/blobs/"):
				return model.CommandResult{Stdout: `{"encoding":"base64","size":13,"content":"` + base64.StdEncoding.EncodeToString([]byte(`{"version":1}`)) + `"}`}
			default:
				t.Fatal(c)
				return model.CommandResult{ExitCode: 1}
			}
		}
		g := GitHubProvider{Runner: f, Repository: RemoteRepository{Host: "github.com", Owner: "acme", Name: "example"}}
		state, err := g.GetCommitState(context.Background(), sha)
		if mode == "100644" && (err != nil || state.ConfigHash == "" || state.TreeHash != tree) {
			t.Fatalf("%v %+v", err, state)
		}
		if mode == "120000" && err == nil {
			t.Fatal("symlink config accepted")
		}
	}
}

func TestRepositoryDiscoveryReadsCapabilitiesWithoutClaimingWriteScopes(t *testing.T) {
	f := &ghFixture{t: t, respond: func(c call) model.CommandResult {
		return model.CommandResult{Stdout: `{"default_branch":"trunk","permissions":{"push":true}}`}
	}}
	g := GitHubProvider{Runner: f}
	r, err := g.DetectRepository(context.Background(), model.Repository{Target: model.PushTarget{RemoteURL: "git@github.com:Acme/Example.git"}})
	if err != nil || r.DefaultBranch != "trunk" || !r.CanPush || r.Identity() != "github.com/acme/example" {
		t.Fatalf("%v %+v", err, r)
	}
}
