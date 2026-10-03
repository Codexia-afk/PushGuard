package repolock

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

var lockFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("LockFileEx")

func lockFile(f *os.File) error {
	var overlapped syscall.Overlapped
	// LOCKFILE_EXCLUSIVE_LOCK | LOCKFILE_FAIL_IMMEDIATELY, one byte at offset 0.
	ok, _, err := lockFileEx.Call(f.Fd(), 3, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if ok != 0 {
		return nil
	}
	if errors.Is(err, syscall.Errno(33)) { // ERROR_LOCK_VIOLATION
		return ErrBusy
	}
	return fmt.Errorf("acquire repository lock: %w", err)
}

func processAlive(pid int) bool {
	handle, err := syscall.OpenProcess(0x00100000, false, uint32(pid)) // SYNCHRONIZE
	if err != nil {
		return !errors.Is(err, syscall.Errno(87)) // ERROR_INVALID_PARAMETER: absent PID
	}
	defer syscall.CloseHandle(handle)
	status, err := syscall.WaitForSingleObject(handle, 0)
	return err != nil || status != syscall.WAIT_OBJECT_0
}
