package delivery

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

type EventTarget struct {
	Number  int
	Branch  string
	HeadSHA string
}

// EventTargets reads routing data only. Check conclusions and receipt claims are
// always fetched again from the code host, never accepted from an event body.
func EventTargets(data []byte, repository string) ([]EventTarget, error) {
	if len(data) > 4<<20 {
		return nil, fmt.Errorf("event exceeds 4 MiB")
	}
	var event struct {
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		Number      int `json:"number"`
		PullRequest *struct {
			Number int                       `json:"number"`
			Head   struct{ SHA, Ref string } `json:"head"`
		} `json:"pull_request"`
		WorkflowRun *struct {
			HeadBranch   string                 `json:"head_branch"`
			HeadSHA      string                 `json:"head_sha"`
			PullRequests []struct{ Number int } `json:"pull_requests"`
		} `json:"workflow_run"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return nil, fmt.Errorf("invalid GitHub event JSON")
	}
	if !strings.EqualFold(event.Repository.FullName, repository) {
		return nil, fmt.Errorf("event repository does not match the configured repository")
	}
	if event.PullRequest != nil {
		n := event.PullRequest.Number
		if n == 0 {
			n = event.Number
		}
		if n < 1 {
			return nil, fmt.Errorf("invalid event PR number")
		}
		return []EventTarget{{Number: n, Branch: event.PullRequest.Head.Ref, HeadSHA: event.PullRequest.Head.SHA}}, nil
	}
	if event.WorkflowRun != nil {
		run := event.WorkflowRun
		var targets []EventTarget
		if len(run.PullRequests) > 20 {
			return nil, fmt.Errorf("too many event PR targets")
		}
		for _, pr := range run.PullRequests {
			if pr.Number < 1 {
				return nil, fmt.Errorf("invalid event PR number")
			}
			targets = append(targets, EventTarget{Number: pr.Number, HeadSHA: run.HeadSHA})
		}
		if len(targets) == 0 && run.HeadBranch != "" {
			targets = append(targets, EventTarget{Branch: run.HeadBranch, HeadSHA: run.HeadSHA})
		}
		return targets, nil
	}
	return nil, fmt.Errorf("supported events are pull_request and workflow_run")
}

// VerifyWebhook is the boundary for a future App/server adapter. Call this
// before EventTargets when accepting an HTTP webhook; no server lives in CLI.
func VerifyWebhook(body []byte, signature string, secret []byte) error {
	if len(secret) < 16 || !strings.HasPrefix(signature, "sha256=") {
		return fmt.Errorf("webhook authentication missing")
	}
	want, err := hex.DecodeString(strings.TrimPrefix(signature, "sha256="))
	if err != nil {
		return fmt.Errorf("invalid webhook signature")
	}
	m := hmac.New(sha256.New, secret)
	_, _ = m.Write(body)
	if !hmac.Equal(m.Sum(nil), want) {
		return fmt.Errorf("webhook signature mismatch")
	}
	return nil
}
