package repolock

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestRepositoryLockExclusionAndRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pushguard.lock")
	release, err := Acquire(path, "first")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if second, err := Acquire(path, "second"); !errors.Is(err, ErrBusy) {
		if second != nil {
			second()
		}
		t.Fatalf("concurrent acquisition allowed: %v", err)
	}
	release()
	second, err := Acquire(path, "second")
	if err != nil {
		t.Fatal(err)
	}
	defer second()
	release() // A repeated old release must not unlock the new owner.
	if third, err := Acquire(path, "third"); !errors.Is(err, ErrBusy) {
		if third != nil {
			third()
		}
		t.Fatalf("old release interfered with new owner: %v", err)
	}
}

func TestRepositoryLockReleasedAfterKilledProcess(t *testing.T) {
	if path := os.Getenv("PUSHGUARD_LOCK_HELPER"); path != "" {
		if _, err := Acquire(path, "child"); err != nil {
			t.Fatal(err)
		}
		fmt.Println("locked")
		time.Sleep(time.Minute)
		return
	}
	path := filepath.Join(t.TempDir(), "pushguard.lock")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestRepositoryLockReleasedAfterKilledProcess$")
	cmd.Env = append(os.Environ(), "PUSHGUARD_LOCK_HELPER="+path)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "locked\n" {
		t.Fatalf("child did not acquire lock: %q %v", line, err)
	}
	if release, err := Acquire(path, "parent"); !errors.Is(err, ErrBusy) {
		if release != nil {
			release()
		}
		t.Fatalf("live child not protected: %v", err)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	release, err := Acquire(path, "after-crash")
	if err != nil {
		t.Fatalf("crash left stale ownership: %v", err)
	}
	release()
	// The same terminated PID in the old format can be migrated safely.
	if err := os.WriteFile(path, []byte(fmt.Sprintf("pid=%d session=old\n", cmd.Process.Pid)), 0600); err != nil {
		t.Fatal(err)
	}
	release, err = Acquire(path, "migration")
	if err != nil {
		t.Fatalf("stale legacy lock not recovered: %v", err)
	}
	release()
}

func TestRepositoryLockRespectsLiveLegacyProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pushguard.lock")
	if err := os.WriteFile(path, []byte(fmt.Sprintf("pid=%d session=legacy\n", os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	if release, err := Acquire(path, "new"); !errors.Is(err, ErrBusy) {
		if release != nil {
			release()
		}
		t.Fatalf("live legacy owner ignored: %v", err)
	}
}

func TestRepositoryLockIOErrorIsNotReportedAsContention(t *testing.T) {
	if _, err := Acquire(filepath.Join(t.TempDir(), "missing", "lock"), "new"); err == nil || errors.Is(err, ErrBusy) {
		t.Fatalf("wrong error for unavailable lock path: %v", err)
	}
}
