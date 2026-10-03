package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pushguard/pushguard/internal/config"
	"github.com/pushguard/pushguard/internal/model"
	"github.com/pushguard/pushguard/internal/testutil"
	"github.com/pushguard/pushguard/internal/ui"
)

type transientProvider struct {
	countedProvider
	failures int
}

func (p *transientProvider) Analyze(ctx context.Context, in model.ContextBundle) (*model.Analysis, error) {
	if p.failures > 0 {
		p.failures--
		p.calls++
		return nil, fmt.Errorf("temporary connection failure")
	}
	return p.countedProvider.Analyze(ctx, in)
}

func TestProviderRetryRecoversOnSameStateAndIsBounded(t *testing.T) {
	for _, failures := range []int{1, 20} {
		t.Run(fmt.Sprint(failures), func(t *testing.T) {
			root, remote := fixture(t, "first")
			p := &transientProvider{countedProvider: countedProvider{p: proposal("func Value() int { return 0 }", "func Value() int { return 1 }", 3)}, failures: failures}
			input := "allow\nallow\nr\nallow\nallow\nallow\n"
			want := ExitOK
			if failures > 1 {
				input = "allow\n" + strings.Repeat("allow\nr\n", 10)
				want = ExitRepairLimit
			}
			var out bytes.Buffer
			a := New(ui.New(strings.NewReader(input), &out, &out, false), false)
			a.Workdir, a.Provider = root, p
			if code := a.RunCheck(context.Background(), false); code != want {
				t.Fatalf("exit=%d want=%d calls=%d\n%s", code, want, p.calls, out.String())
			}
			if p.calls > 3 || a.Report.RepairMetrics.Attempts > 3 {
				t.Fatal("retry escaped attempt cap")
			}
			remoteEmpty(t, root, remote)
		})
	}
}

func TestStandaloneCheckWithoutGitExecutable(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PUSHGUARD_CACHE_DIR", t.TempDir())
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Checks = []model.Check{{Name: "local", Required: true, Args: []string{exe, "-test.run=^TestToolHelper$", "--", root, "pass"}}}
	if err := config.Save(root, cfg); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	var out bytes.Buffer
	a := New(ui.New(strings.NewReader(""), &out, &out, true), true)
	a.Workdir = root
	if code := a.RunCheck(context.Background(), false); code != ExitOK || a.Report.Mode != "standalone" {
		t.Fatalf("standalone still requires Git: %d\n%s", code, out.String())
	}
}

func TestCancellationWhileWaitingForApprovalReleasesLock(t *testing.T) {
	root, _ := fixture(t, "pass")
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	w := &triggerWriter{match: "Start verification?", action: func() { close(ready) }}
	a := New(ui.New(reader, w, w, false), false)
	a.Workdir = root
	done := make(chan int, 1)
	go func() { done <- a.RunCheck(ctx, false) }()
	// The cancellation must also work if discovery has not yet reached a prompt.
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-ready:
	case <-timer.C:
	}
	cancel()
	select {
	case code := <-done:
		if code != ExitCancelled {
			t.Fatalf("exit=%d", code)
		}
	case <-time.After(3 * time.Second):
		writer.Close()
		<-done
		t.Fatal("cancelled workflow remained blocked on terminal input")
	}
	data, _ := os.ReadFile(filepath.Join(root, ".git", "pushguard.lock"))
	if len(data) != 0 {
		t.Fatalf("owner metadata survived cancellation: %s", data)
	}
}

func TestStandaloneStateDetectsNewFiles(t *testing.T) {
	root := t.TempDir()
	testutil.Write(t, root, "foo.py", "x = 1\n")
	a := New(ui.New(strings.NewReader(""), io.Discard, io.Discard, true), true)
	a.Workdir = root
	if err := a.prepare(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	before, err := a.repairState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	testutil.Write(t, root, "another.py", "invalid python !\n")
	after, err := a.repairState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if before.Value == after.Value {
		t.Fatal("new source file did not invalidate standalone state")
	}
}
