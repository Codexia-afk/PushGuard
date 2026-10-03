// Package repolock holds an OS-owned repository lock. The lock file stays in
// place; unlinking it would allow two processes to lock different file objects.
package repolock

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/pushguard/pushguard/internal/model"
)

var ErrBusy = errors.New("another PushGuard session owns the repository lock")

func Acquire(path, session string) (func(), error) {
	return AcquireFor(path, session, "unspecified")
}

func AcquireFor(path, session, command string) (func(), error) {
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("repository lock must be a regular file")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open repository lock: %w", err)
	}
	if err = lockFile(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			_ = f.Close()
		}
	}()
	data := make([]byte, 4096)
	n, err := f.ReadAt(data, 0)
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("read repository lock: %w", err)
	}
	previous := string(data[:n])
	// Migrate an interrupted pre-advisory lock, but respect a live older CLI.
	if strings.TrimSpace(previous) != "" && !strings.Contains(previous, "kind=advisory") {
		var pid int
		if _, err := fmt.Sscanf(previous, "pid=%d", &pid); err != nil || pid <= 0 {
			return nil, fmt.Errorf("unrecognized legacy repository lock; inspect %s", path)
		}
		if processAlive(pid) {
			return nil, fmt.Errorf("%w (legacy PID %d)", ErrBusy, pid)
		}
	}
	if err = f.Truncate(0); err == nil {
		_, err = fmt.Fprintf(f, "pid=%d session=%s kind=advisory started=%s command=%q version=%s\n", os.Getpid(), session, time.Now().UTC().Format(time.RFC3339Nano), command, model.Version)
	}
	if err == nil {
		err = f.Sync()
	}
	if err != nil {
		return nil, fmt.Errorf("persist repository lock: %w", err)
	}
	success = true
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = f.Truncate(0)
			_ = f.Close() // The OS also releases ownership on process termination.
		})
	}, nil
}
