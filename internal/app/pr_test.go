package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/pushguard/pushguard/internal/codehost"
	"github.com/pushguard/pushguard/internal/config"
	"github.com/pushguard/pushguard/internal/delivery"
	"github.com/pushguard/pushguard/internal/receipt"
	"github.com/pushguard/pushguard/internal/state"
	"github.com/pushguard/pushguard/internal/testutil"
	"github.com/pushguard/pushguard/internal/ui"
)

func prFixture(t *testing.T, mode string) (string, string, *codehost.MockCodeHostProvider) {
	t.Helper()
	root, remote := fixture(t, mode)
	cfg, _, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	cfg.GitHub.Enabled, cfg.GitHub.PR.Enabled = true, true
	cfg.GitHub.Checks.Required = []string{"ci"}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	testutil.Write(t, root, ".pushguard.json", string(data))
	testutil.Commit(t, root)
	testutil.Git(t, root, "switch", "-c", "feature/verified")
	for _, v := range []struct{ k, v string }{{"GIT_AUTHOR_NAME", "PushGuard Test"}, {"GIT_AUTHOR_EMAIL", "test@example.test"}, {"GIT_COMMITTER_NAME", "PushGuard Test"}, {"GIT_COMMITTER_EMAIL", "test@example.test"}} {
		t.Setenv(v.k, v.v)
	}
	m := &codehost.MockCodeHostProvider{Remote: codehost.RemoteRepository{Host: "github.com", Owner: "acme", Name: "example", DefaultBranch: "main", CanPush: true}, Checks: []codehost.HostedCheck{{Name: "ci", State: codehost.Success}}}
	m.OnCreate = func(in codehost.CreatePullRequestInput) (*codehost.PullRequest, error) {
		v, err := delivery.Extract(in.Body)
		if err != nil {
			return nil, err
		}
		sha := strings.TrimSpace(testutil.Git(t, root, "rev-parse", "HEAD"))
		if v.CommitSHA != sha {
			return nil, fmt.Errorf("receipt does not match actual pushed commit")
		}
		m.Commit = codehost.CommitState{TreeHash: v.TreeHash, ConfigHash: v.ConfigHash}
		return &codehost.PullRequest{Number: 42, Title: in.Title, Body: in.Body, Base: in.Base, Head: in.Head, HeadSHA: sha, HeadRepository: m.Remote.Identity(), State: "open", URL: "https://github.com/acme/example/pull/42"}, nil
	}
	return root, remote, m
}

func TestPRPushAndCreationHaveIndependentApprovals(t *testing.T) {
	for _, tc := range []struct {
		name, input       string
		push, create, non bool
		code              int
	}{
		{"verification denied", "n\n", false, false, false, ExitCancelled},
		{"review denied", "allow\nx\n", false, false, false, ExitCancelled},
		{"branch push denied", "allow\nc\nn\n", false, false, false, ExitCancelled},
		{"PR denied after push", "allow\nc\ny\nn\n", true, false, false, ExitCancelled},
		{"separate approvals", "allow\nc\ny\ny\n", true, true, false, ExitOK},
		{"noninteractive cannot authorize", "allow\nc\ny\ny\n", false, false, true, ExitCancelled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, remote, host := prFixture(t, "pass")
			var out bytes.Buffer
			a := New(ui.New(strings.NewReader(tc.input), &out, &out, tc.non), tc.non)
			a.Workdir, a.CodeHost = root, host
			code := a.RunPR(context.Background(), false, PROptions{Title: "Reviewed change"})
			if code != tc.code {
				t.Fatalf("code %d want %d\n%s", code, tc.code, out.String())
			}
			tip := strings.TrimSpace(testutil.Git(t, root, "ls-remote", "--heads", remote, "refs/heads/feature/verified"))
			if (tip != "") != tc.push {
				t.Fatalf("push boundary: %s", tip)
			}
			if strings.TrimSpace(testutil.Git(t, root, "ls-remote", "--heads", remote, "refs/heads/main")) != "" {
				t.Fatal("base branch was pushed")
			}
			created := false
			for _, c := range host.Calls {
				created = created || c == "create"
			}
			if created != tc.create {
				t.Fatal("PR boundary violated", host.Calls)
			}
			if len(host.Published) != 0 {
				t.Fatal("local CLI bypassed hosted trust publisher")
			}
			if tc.create {
				v, err := receipt.LoadVerification(root, a.Report.DeliveryReceiptID)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.HasPrefix(tip, v.CommitSHA) {
					t.Fatal("wrong commit pushed")
				}
				keys, _ := receipt.PublicTrust()
				validation := receipt.ValidateVerification(v, receipt.Expectation{Repository: host.Remote.Identity(), CommitSHA: host.PR.HeadSHA, Branch: host.PR.Head, TreeHash: host.Commit.TreeHash, ConfigHash: host.Commit.ConfigHash, Keys: keys})
				if validation.Status != receipt.Valid {
					t.Fatal(validation)
				}
			}
		})
	}
}

func TestPRRepairGatesAndVerifiedDelivery(t *testing.T) {
	for _, tc := range []struct {
		name, input      string
		code             int
		applied, created bool
	}{
		{"investigation denied", "allow\ndeny\n", ExitCancelled, false, false},
		{"patch denied", "allow\nallow\ndeny\n", ExitCancelled, false, false},
		{"complete repaired delivery", "allow\nallow\nallow\nc\nc\nallow\nc\ny\ny\n", ExitOK, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, remote, host := prFixture(t, "first")
			var out bytes.Buffer
			a := New(ui.New(strings.NewReader(tc.input), &out, &out, false), false)
			a.Workdir, a.CodeHost = root, host
			p := &countedProvider{p: proposal("func Value() int { return 0 }", "func Value() int { return 1 }", 3)}
			a.Provider = p
			code := a.RunPR(context.Background(), false, PROptions{})
			if code != tc.code {
				t.Fatalf("%d\n%s", code, out.String())
			}
			if (a.Report.RepairCycles > 0) != tc.applied || (host.PR != nil) != tc.created {
				t.Fatal("incorrect repair/PR result")
			}
			if !tc.created {
				remoteEmpty(t, root, remote)
			} else {
				v, err := receipt.LoadVerification(root, a.Report.DeliveryReceiptID)
				if err != nil {
					t.Fatal(err)
				}
				if len(v.AIRepairs) != 1 || !v.AIRepairs[0].HumanApproved || len(v.AIRepairs[0].VerifiedBy) == 0 || a.Report.CommitResult == nil {
					t.Fatal("repair provenance or approved commit missing")
				}
				if !strings.Contains(out.String(), "FULL VERIFICATION") {
					t.Fatal("full recheck missing")
				}
			}
			if tc.name == "investigation denied" && p.calls != 0 {
				t.Fatal("AI contacted after denial")
			}
		})
	}
}
func TestPRHostedPendingFailureAndNetworkErrors(t *testing.T) {
	for _, hosted := range []codehost.CheckState{codehost.InProgress, codehost.Failure, codehost.Success} {
		t.Run(string(hosted), func(t *testing.T) {
			root, _, host := prFixture(t, "pass")
			host.Checks[0].State = hosted
			var out bytes.Buffer
			a := New(ui.New(strings.NewReader("allow\nc\ny\ny\n"), &out, &out, false), false)
			a.Workdir, a.CodeHost = root, host
			code := a.RunPR(context.Background(), false, PROptions{})
			if hosted == codehost.Failure && code != ExitVerification || hosted != codehost.Failure && code != 0 {
				t.Fatalf("%d %s", code, out.String())
			}
			if hosted == codehost.InProgress && strings.Contains(out.String(), "Ready for human review.") {
				t.Fatal("pending CI became success")
			}
		})
	}
	for _, kind := range []codehost.ErrorKind{codehost.Authentication, codehost.Network, codehost.Permission} {
		root, remote, host := prFixture(t, "pass")
		host.Err = &codehost.Error{Kind: kind, Operation: "detect", Detail: "test failure"}
		var out bytes.Buffer
		a := New(ui.New(strings.NewReader("allow\nc\ny\ny\n"), &out, &out, false), false)
		a.Workdir, a.CodeHost = root, host
		if code := a.RunPR(context.Background(), false, PROptions{}); code == 0 {
			t.Fatal("host error hidden")
		}
		remoteEmpty(t, root, remote)
	}
}

type changingPRReader struct {
	root string
	t    *testing.T
	step int
}

func (r *changingPRReader) Read(p []byte) (int, error) {
	r.step++
	switch r.step {
	case 1:
		return copy(p, "allow\n"), nil
	case 2:
		return copy(p, "c\n"), nil
	case 3:
		testutil.Write(r.t, r.root, "unreviewed.txt", "changed after review")
		return copy(p, "y\n"), nil
	default:
		return 0, io.EOF
	}
}
func TestPRStateChangeAfterReviewInvalidatesPush(t *testing.T) {
	root, remote, host := prFixture(t, "pass")
	var out bytes.Buffer
	a := New(ui.New(&changingPRReader{root: root, t: t}, &out, &out, false), false)
	a.Workdir, a.CodeHost = root, host
	if code := a.RunPR(context.Background(), false, PROptions{}); code == 0 {
		t.Fatal("changed state accepted")
	}
	remoteEmpty(t, root, remote)
	if host.PR != nil {
		t.Fatal("PR created without a valid push")
	}
}
func TestPRRejectsBaseBranchAndStateMachineHasNoMerge(t *testing.T) {
	root, remote, host := prFixture(t, "pass")
	testutil.Git(t, root, "switch", "main")
	var out bytes.Buffer
	a := New(ui.New(strings.NewReader("allow\nc\ny\ny\n"), &out, &out, false), false)
	a.Workdir, a.CodeHost = root, host
	if code := a.RunPR(context.Background(), false, PROptions{}); code != ExitPreflight {
		t.Fatal(code)
	}
	remoteEmpty(t, root, remote)
	m := state.New()
	m.Current = state.BranchPushed
	if err := m.Move(state.CreatingPR); err == nil {
		t.Fatal("branch push authorized PR creation")
	}
	m.Current = state.WaitingForPR
	if err := m.Move(state.CreatingPR); err == nil {
		t.Fatal("PR created without approval")
	}
	m.Current = state.HostedFailed
	if err := m.Move(state.State("MERGING")); err == nil {
		t.Fatal("merge state exists")
	}
}

func TestPRReadOnlyHeadSynchronizationAfterPush(t *testing.T) {
	root, _, host := prFixture(t, "pass")
	sha := strings.TrimSpace(testutil.Git(t, root, "rev-parse", "HEAD"))
	host.PR = &codehost.PullRequest{Number: 42, Head: "feature/verified", HeadSHA: strings.Repeat("f", 40), HeadRepository: host.Remote.Identity(), Base: "main", State: "open"}
	gets := 0
	host.OnGet = func(n int) (*codehost.PullRequest, error) { gets++; p := *host.PR; p.HeadSHA = sha; return &p, nil }
	var out bytes.Buffer
	a := New(ui.New(strings.NewReader("allow\nc\ny\nn\n"), &out, &out, false), false)
	a.Workdir, a.CodeHost = root, host
	if code := a.RunPR(context.Background(), false, PROptions{}); code != ExitCancelled || gets != 1 {
		t.Fatalf("code=%d gets=%d\n%s", code, gets, out.String())
	}
	for _, call := range host.Calls {
		if call == "create" || call == "update" || call == "publish" {
			t.Fatal("synchronization wait authorized mutation")
		}
	}
}

func TestPRFailedRepairAndCreateFailureNeverBecomeDeliverySuccess(t *testing.T) {
	t.Run("failed repair", func(t *testing.T) {
		root, remote, host := prFixture(t, "fail")
		var out bytes.Buffer
		a := New(ui.New(strings.NewReader("allow\nallow\nallow\ndeny\n"), &out, &out, false), false)
		a.Workdir, a.CodeHost = root, host
		a.Provider = fake(proposal("func Value() int { return 0 }", "func Value() int { return 1 }", 3))
		if code := a.RunPR(context.Background(), false, PROptions{}); code != ExitCancelled {
			t.Fatalf("%d %s", code, out.String())
		}
		remoteEmpty(t, root, remote)
		if host.PR != nil || a.Report.DeliveryReceiptID != "" {
			t.Fatal("failed local verification obtained delivery evidence")
		}
	})
	t.Run("create network failure", func(t *testing.T) {
		root, remote, host := prFixture(t, "pass")
		var out bytes.Buffer
		host.OnCreate = func(codehost.CreatePullRequestInput) (*codehost.PullRequest, error) {
			return nil, &codehost.Error{Kind: codehost.Network, Operation: "create PR", Detail: "connection interrupted"}
		}
		a := New(ui.New(strings.NewReader("allow\nc\ny\ny\n"), &out, &out, false), false)
		a.Workdir, a.CodeHost = root, host
		if code := a.RunPR(context.Background(), false, PROptions{}); code == 0 {
			t.Fatal("API error hidden")
		}
		if a.Report.PushResult == nil || a.Report.PullRequestNumber != 0 || len(host.Published) > 0 {
			t.Fatal("creation failure misrepresented")
		}
		if strings.TrimSpace(testutil.Git(t, root, "ls-remote", "--heads", remote)) == "" {
			t.Fatal("approved push was not retained")
		}
	})
}
