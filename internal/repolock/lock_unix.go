//go:build !windows

package repolock

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func lockFile(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return ErrBusy
	}
	if err != nil {
		return fmt.Errorf("acquire repository lock: %w", err)
	}
	return nil
}

func processAlive(pid int) bool {
	return !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}
