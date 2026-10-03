package codehost

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pushguard/pushguard/internal/config"
	"github.com/pushguard/pushguard/internal/model"
	"github.com/pushguard/pushguard/internal/security"
)

type Executor interface {
	RunInput(context.Context, string, []string, time.Duration, string) model.CommandResult
}
type GitHubProvider struct {
	Runner     Executor
	Workdir    string
	Repository RemoteRepository
}

var namePart = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)
var objectID = regexp.MustCompile(`^[a-fA-F0-9]{40}(?:[a-fA-F0-9]{24})?$`)

func ParseGitHubRemote(remote string) (RemoteRepository, error) {
	var path, host string
	if strings.HasPrefix(remote, "git@github.com:") {
		host = "github.com"
		path = strings.TrimPrefix(remote, "git@github.com:")
	} else {
		u, err := url.Parse(remote)
		if err != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
			return RemoteRepository{}, fmt.Errorf("invalid GitHub remote")
		}
		if u.Scheme != "https" && u.Scheme != "ssh" {
			return RemoteRepository{}, fmt.Errorf("GitHub integration requires an HTTPS or SSH github.com remote")
		}
		if u.Host != "github.com" || u.User != nil && (u.Scheme != "ssh" || u.User.String() != "git") {
			return RemoteRepository{}, fmt.Errorf("GitHub remote host or embedded credentials are unsupported")
		}
		host = u.Host
		path = strings.TrimPrefix(u.Path, "/")
	}
	path = strings.TrimSuffix(path, ".git")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || !namePart.MatchString(parts[0]) || !namePart.MatchString(parts[1]) {
		return RemoteRepository{}, fmt.Errorf("GitHub remote must identify exactly owner/repository")
	}
	return RemoteRepository{Host: host, Owner: parts[0], Name: parts[1]}, nil
}

func (g *GitHubProvider) api(ctx context.Context, method, path string, body, out any) error {
	if g.Runner == nil || g.Repository.Host != "github.com" {
		return &Error{Invalid, method, "GitHub provider is not configured"}
	}
	args := []string{"gh", "api", "--hostname", g.Repository.Host, "--method", method, "-H", "Accept: application/vnd.github+json", "-H", "X-GitHub-Api-Version: 2022-11-28", path}
	input := ""
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		input = string(data)
		args = append(args, "--input", "-")
	}
	result := g.Runner.RunInput(ctx, g.Workdir, args, 45*time.Second, input)
	if result.ExitCode != 0 || result.Truncated {
		detail := security.Terminal(security.Redact(strings.TrimSpace(result.Stderr + " " + result.Stdout + " " + result.Terminated)))
		if len(detail) > 700 {
			detail = detail[:700]
		}
		kind := Network
		switch {
		case result.Truncated:
			kind = Incomplete
		case strings.Contains(detail, "HTTP 401") || strings.Contains(detail, "gh auth login"):
			kind = Authentication
		case strings.Contains(detail, "HTTP 429") || strings.Contains(strings.ToLower(detail), "rate limit"):
			kind = RateLimit
		case strings.Contains(detail, "HTTP 403"):
			kind = Permission
		case strings.Contains(detail, "HTTP 404"):
			kind = NotFound
		case strings.Contains(detail, "HTTP 422") || strings.Contains(detail, "HTTP 400"):
			kind = Invalid
		}
		return &Error{kind, method + " " + path, detail}
	}
	if out != nil && json.Unmarshal([]byte(result.Stdout), out) != nil {
		return &Error{Incomplete, method + " " + path, "invalid JSON returned by GitHub"}
	}
	return nil
}
func (g *GitHubProvider) prefix() string {
	return "repos/" + g.Repository.Owner + "/" + g.Repository.Name
}
func (g *GitHubProvider) DetectRepository(ctx context.Context, repo model.Repository) (*RemoteRepository, error) {
	remote, err := ParseGitHubRemote(repo.Target.RemoteURL)
	if err != nil {
		return nil, err
	}
	g.Repository = remote
	var data struct {
		DefaultBranch string `json:"default_branch"`
		Permissions   struct {
			Push bool `json:"push"`
		} `json:"permissions"`
	}
	if err = g.api(ctx, "GET", g.prefix(), nil, &data); err != nil {
		return nil, err
	}
	if data.DefaultBranch == "" {
		return nil, &Error{Incomplete, "repository discovery", "default branch is unavailable"}
	}
	g.Repository.DefaultBranch = data.DefaultBranch
	g.Repository.CanPush = data.Permissions.Push
	result := g.Repository
	return &result, nil
}

type githubPR struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	Draft  bool   `json:"draft"`
	State  string `json:"state"`
	Base   struct {
		Ref string `json:"ref"`
	} `json:"base"`
	Head struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
}

func (g *GitHubProvider) pull(p githubPR) (*PullRequest, error) {
	if p.Number < 1 || !objectID.MatchString(p.Head.SHA) {
		return nil, &Error{Incomplete, "pull request", "PR number or commit is missing"}
	}
	return &PullRequest{Number: p.Number, URL: fmt.Sprintf("https://%s/%s/%s/pull/%d", g.Repository.Host, g.Repository.Owner, g.Repository.Name, p.Number), Title: p.Title, Body: p.Body, Base: p.Base.Ref, Head: p.Head.Ref, HeadSHA: p.Head.SHA, HeadRepository: strings.ToLower(g.Repository.Host + "/" + p.Head.Repo.FullName), Draft: p.Draft, State: p.State}, nil
}
func (g *GitHubProvider) GetPullRequest(ctx context.Context, number int) (*PullRequest, error) {
	if number < 1 {
		return nil, &Error{Invalid, "pull request", "positive PR number required"}
	}
	var p githubPR
	if err := g.api(ctx, "GET", fmt.Sprintf("%s/pulls/%d", g.prefix(), number), nil, &p); err != nil {
		return nil, err
	}
	return g.pull(p)
}
func (g *GitHubProvider) FindPullRequest(ctx context.Context, head, base string) (*PullRequest, error) {
	q := url.Values{"state": {"open"}, "head": {g.Repository.Owner + ":" + head}, "per_page": {"100"}}
	if base != "" {
		q.Set("base", base)
	}
	var rows []githubPR
	if err := g.api(ctx, "GET", g.prefix()+"/pulls?"+q.Encode(), nil, &rows); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	if len(rows) > 1 {
		return nil, &Error{Invalid, "find pull request", "multiple matching PRs; specify a PR number"}
	}
	return g.pull(rows[0])
}
func validateMetadata(title, body, base, head string) error {
	if strings.TrimSpace(title) == "" || len(title) > 256 || len(body) > 60000 || strings.ContainsAny(title, "\x00\r\n") || strings.ContainsRune(body, 0) || base == "" || head == "" || base == head || strings.ContainsAny(base+head, "\x00\r\n") {
		return &Error{Invalid, "PR metadata", "nonempty title and distinct base/head branches are required; metadata size/control-character limits apply"}
	}
	return nil
}
func (g *GitHubProvider) CreatePullRequest(ctx context.Context, input CreatePullRequestInput) (*PullRequest, error) {
	if err := validateMetadata(input.Title, input.Body, input.Base, input.Head); err != nil {
		return nil, err
	}
	var p githubPR
	err := g.api(ctx, "POST", g.prefix()+"/pulls", map[string]any{"title": input.Title, "body": input.Body, "base": input.Base, "head": input.Head, "draft": input.Draft}, &p)
	if err != nil {
		return nil, err
	}
	return g.pull(p)
}
func (g *GitHubProvider) UpdatePullRequest(ctx context.Context, input UpdatePullRequestInput) error {
	if input.Number < 1 || strings.TrimSpace(input.Title) == "" || len(input.Title) > 256 || len(input.Body) > 60000 || strings.ContainsAny(input.Title, "\x00\r\n") {
		return &Error{Invalid, "update PR", "invalid PR metadata"}
	}
	return g.api(ctx, "PATCH", fmt.Sprintf("%s/pulls/%d", g.prefix(), input.Number), map[string]any{"title": input.Title, "body": input.Body, "base": input.Base}, nil)
}

func (g *GitHubProvider) GetCommitState(ctx context.Context, ref string) (CommitState, error) {
	if !objectID.MatchString(ref) {
		return CommitState{}, &Error{Invalid, "commit state", "an exact commit SHA is required"}
	}
	var commit struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := g.api(ctx, "GET", g.prefix()+"/git/commits/"+ref, nil, &commit); err != nil {
		return CommitState{}, err
	}
	if !objectID.MatchString(commit.Tree.SHA) {
		return CommitState{}, &Error{Incomplete, "commit state", "missing tree SHA"}
	}
	var tree struct {
		Truncated bool                                     `json:"truncated"`
		Tree      []struct{ Path, Mode, SHA, Type string } `json:"tree"`
	}
	if err := g.api(ctx, "GET", g.prefix()+"/git/trees/"+commit.Tree.SHA, nil, &tree); err != nil {
		return CommitState{}, err
	}
	if tree.Truncated {
		return CommitState{}, &Error{Incomplete, "commit tree", "root tree was truncated"}
	}
	state := CommitState{TreeHash: commit.Tree.SHA}
	for _, entry := range tree.Tree {
		if entry.Path != ".pushguard.json" && entry.Path != ".pushguard.yaml" && entry.Path != ".pushguard.yml" {
			continue
		}
		if state.ConfigHash != "" || entry.Type != "blob" || entry.Mode == "120000" || !objectID.MatchString(entry.SHA) {
			return state, &Error{Invalid, "committed configuration", "ambiguous or symlinked configuration"}
		}
		var blob struct {
			Content, Encoding string
			Size              int
		}
		if err := g.api(ctx, "GET", g.prefix()+"/git/blobs/"+entry.SHA, nil, &blob); err != nil {
			return state, err
		}
		if blob.Encoding != "base64" || blob.Size > 1<<20 {
			return state, &Error{Incomplete, "configuration blob", "unsupported or oversized configuration"}
		}
		data, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(blob.Content, "\n", ""))
		if err != nil {
			return state, err
		}
		cfg, err := config.Decode(data, entry.Path)
		if err != nil {
			return state, err
		}
		state.ConfigHash = config.Hash(cfg)
	}
	return state, nil
}

func MapCheck(status, conclusion string) CheckState {
	if status == "queued" || status == "requested" || status == "waiting" || status == "pending" {
		return Queued
	}
	if status == "in_progress" {
		return InProgress
	}
	if status != "completed" {
		return Unknown
	}
	switch conclusion {
	case "success":
		return Success
	case "failure", "timed_out", "action_required", "startup_failure", "stale":
		return Failure
	case "cancelled":
		return Cancelled
	case "neutral", "skipped":
		return Neutral
	default:
		return Unknown
	}
}

type githubCheck struct {
	ID                       int64 `json:"id"`
	Name, Status, Conclusion string
	ExternalID               string `json:"external_id"`
	HTMLURL                  string `json:"html_url"`
	Output                   struct {
		AnnotationsCount int `json:"annotations_count"`
	} `json:"output"`
}

func (g *GitHubProvider) checkRuns(ctx context.Context, ref string) ([]githubCheck, error) {
	var all []githubCheck
	for page := 1; page <= 20; page++ {
		var batch struct {
			Total int           `json:"total_count"`
			Runs  []githubCheck `json:"check_runs"`
		}
		if err := g.api(ctx, "GET", fmt.Sprintf("%s/commits/%s/check-runs?filter=latest&per_page=100&page=%d", g.prefix(), ref, page), nil, &batch); err != nil {
			return nil, err
		}
		all = append(all, batch.Runs...)
		if len(all) >= batch.Total {
			return all, nil
		}
		if len(batch.Runs) == 0 {
			break
		}
	}
	return nil, &Error{Incomplete, "check runs", "pagination exceeded evidence bounds"}
}
func (g *GitHubProvider) GetChecks(ctx context.Context, ref string) ([]HostedCheck, error) {
	if !objectID.MatchString(ref) {
		return nil, &Error{Invalid, "checks", "exact commit SHA required"}
	}
	runs, err := g.checkRuns(ctx, ref)
	if err != nil {
		return nil, err
	}
	var checks []HostedCheck
	for _, c := range runs {
		if c.Name == CheckName && strings.HasPrefix(c.ExternalID, "pushguard:") {
			checks = append(checks, HostedCheck{ID: c.ID, Name: c.Name, State: MapCheck(c.Status, c.Conclusion), URL: c.HTMLURL, Source: "pushguard", ReceiptID: strings.TrimPrefix(c.ExternalID, "pushguard:")})
			continue
		}
		check := HostedCheck{ID: c.ID, Name: c.Name, State: MapCheck(c.Status, c.Conclusion), URL: c.HTMLURL, Source: "check-run"}
		if c.Output.AnnotationsCount > 0 && check.State == Failure {
			var rows []Annotation
			if err := g.api(ctx, "GET", fmt.Sprintf("%s/check-runs/%d/annotations?per_page=100", g.prefix(), c.ID), nil, &rows); err != nil {
				return nil, err
			}
			for _, a := range rows {
				check.Diagnostics = append(check.Diagnostics, model.Diagnostic{Tool: c.Name, Severity: a.Level, Location: model.SourceLocation{File: a.Path, Line: a.StartLine, Column: a.StartColumn, Exact: a.StartLine > 0}, Message: a.Message, ReportedExact: a.StartLine > 0})
			}
		}
		checks = append(checks, check)
	}
	seen := map[string]bool{}
	for page := 1; page <= 20; page++ {
		var statuses []struct {
			Context, State string
			TargetURL      string `json:"target_url"`
		}
		if err := g.api(ctx, "GET", fmt.Sprintf("%s/commits/%s/statuses?per_page=100&page=%d", g.prefix(), ref, page), nil, &statuses); err != nil {
			return nil, err
		}
		for _, s := range statuses {
			if seen[s.Context] {
				continue
			}
			seen[s.Context] = true
			state := Unknown
			switch s.State {
			case "success":
				state = Success
			case "pending":
				state = Queued
			case "error", "failure":
				state = Failure
			}
			checks = append(checks, HostedCheck{Name: s.Context, State: state, URL: s.TargetURL, Source: "commit-status"})
		}
		if len(statuses) < 100 {
			break
		}
		if page == 20 {
			return nil, &Error{Incomplete, "statuses", "pagination exceeded evidence bounds"}
		}
	}
	// Check runs alone may not yet exist for a queued workflow. Include workflow
	// runs so completed jobs cannot mask a still-running GitHub Actions workflow.
	workflowIDs := map[string]bool{}
	publisherRuns := map[string]bool{}
	for page := 1; page <= 20; page++ {
		var batch struct {
			Total int `json:"total_count"`
			Runs  []struct {
				ID                              int64
				Name, Status, Conclusion, Event string
				WorkflowID                      int64  `json:"workflow_id"`
				HeadBranch                      string `json:"head_branch"`
				HeadSHA                         string `json:"head_sha"`
				HTMLURL                         string `json:"html_url"`
			} `json:"workflow_runs"`
		}
		if err := g.api(ctx, "GET", fmt.Sprintf("%s/actions/runs?head_sha=%s&per_page=100&page=%d", g.prefix(), ref, page), nil, &batch); err != nil {
			return nil, err
		}
		for _, run := range batch.Runs {
			if run.Name == PublisherWorkflow {
				publisherRuns[strconv.FormatInt(run.ID, 10)] = true
				continue
			}
			key := fmt.Sprintf("%d/%s/%s", run.WorkflowID, run.Event, run.HeadBranch)
			if run.HeadSHA != ref || run.Name == PublisherWorkflow || workflowIDs[key] {
				continue
			}
			workflowIDs[key] = true
			checks = append(checks, HostedCheck{ID: run.ID, Name: "workflow: " + run.Name, State: MapCheck(run.Status, run.Conclusion), URL: run.HTMLURL, Source: "workflow"})
		}
		if page*100 >= batch.Total {
			break
		}
		if page == 20 {
			return nil, &Error{Incomplete, "workflow runs", "pagination exceeded evidence bounds"}
		}
	}
	filtered := checks[:0]
	for _, check := range checks {
		own := false
		if check.Source == "check-run" {
			for run := range publisherRuns {
				if strings.Contains(check.URL, "/actions/runs/"+run+"/") {
					own = true
					break
				}
			}
		}
		if !own {
			filtered = append(filtered, check)
		}
	}
	return filtered, nil
}

func (g *GitHubProvider) GetChangedFiles(ctx context.Context, number int) ([]string, error) {
	if number < 1 {
		return nil, &Error{Invalid, "PR files", "positive PR number required"}
	}
	var files []string
	for page := 1; page <= 30; page++ {
		var rows []struct {
			Filename string `json:"filename"`
		}
		if err := g.api(ctx, "GET", fmt.Sprintf("%s/pulls/%d/files?per_page=100&page=%d", g.prefix(), number, page), nil, &rows); err != nil {
			return nil, err
		}
		for _, row := range rows {
			files = append(files, row.Filename)
		}
		if len(rows) < 100 {
			return files, nil
		}
	}
	return nil, &Error{Incomplete, "PR files", "GitHub's 3000-file evidence limit reached"}
}

func (g *GitHubProvider) PublishVerification(ctx context.Context, result HostedVerification) error {
	if !objectID.MatchString(result.HeadSHA) || len(result.Summary) > 60000 {
		return &Error{Invalid, "publish check", "invalid commit or oversized summary"}
	}
	status, conclusion := "completed", "neutral"
	switch result.State {
	case Queued:
		status = "queued"
	case InProgress:
		status = "in_progress"
	case Success:
		conclusion = "success"
	case Failure:
		conclusion = "failure"
	case Cancelled:
		conclusion = "cancelled"
	case Neutral, Unknown:
	default:
		return &Error{Invalid, "publish check", "invalid check state"}
	}
	runs, err := g.checkRuns(ctx, result.HeadSHA)
	if err != nil {
		return err
	}
	id := int64(0)
	annotationCount := 0
	for _, run := range runs {
		if run.Name == CheckName && strings.HasPrefix(run.ExternalID, "pushguard:") {
			id = run.ID
			annotationCount = run.Output.AnnotationsCount
			break
		}
	}
	body := map[string]any{"name": CheckName, "status": status, "external_id": "pushguard:" + result.ReceiptID, "output": map[string]any{"title": CheckName, "summary": result.Summary}}
	if status == "completed" {
		body["conclusion"] = conclusion
		body["completed_at"] = time.Now().UTC().Format(time.RFC3339)
	}
	// GitHub accepts at most 50 annotations per call. Publishing a bounded batch
	// with the final summary avoids repeated polling duplicating annotations.
	annotations := result.Annotations
	if len(annotations) > 50 {
		annotations = annotations[:50]
	}
	if annotationCount == 0 && len(annotations) > 0 {
		body["output"].(map[string]any)["annotations"] = annotations
	}
	if id > 0 {
		return g.api(ctx, "PATCH", g.prefix()+"/check-runs/"+strconv.FormatInt(id, 10), body, nil)
	}
	body["head_sha"] = result.HeadSHA
	return g.api(ctx, "POST", g.prefix()+"/check-runs", body, nil)
}
