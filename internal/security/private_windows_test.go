package security

import (
	"os/user"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

func TestWindowsEvidenceACLAllowsOnlyCurrentUser(t *testing.T) {
	dir := t.TempDir()
	if err := SecureDirectory(dir); err != nil {
		t.Fatal(err)
	}
	var uid string
	if u, err := user.Current(); err == nil {
		uid = u.Uid
	}
	if uid == "" {
		if sid, err := currentUserSID(); err == nil {
			uid = sid
		}
	}
	name, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		t.Fatal(err)
	}
	api := syscall.NewLazyDLL("advapi32.dll")
	free := syscall.NewLazyDLL("kernel32.dll").NewProc("LocalFree")
	var descriptor uintptr
	code, _, _ := api.NewProc("GetNamedSecurityInfoW").Call(uintptr(unsafe.Pointer(name)), 1, 4, 0, 0, 0, 0, uintptr(unsafe.Pointer(&descriptor)))
	if code != 0 {
		t.Fatal(syscall.Errno(code))
	}
	defer free.Call(descriptor)
	var textPointer *uint16
	var length uint32
	ok, _, err := api.NewProc("ConvertSecurityDescriptorToStringSecurityDescriptorW").Call(descriptor, 1, 4, uintptr(unsafe.Pointer(&textPointer)), uintptr(unsafe.Pointer(&length)))
	if ok == 0 {
		t.Fatal(err)
	}
	defer free.Call(uintptr(unsafe.Pointer(textPointer)))
	if length == 0 || length > 65536 {
		t.Fatal("invalid ACL string length")
	}
	sddl := syscall.UTF16ToString(unsafe.Slice(textPointer, int(length)))
	if !strings.HasPrefix(sddl, "D:P") || strings.Count(sddl, "(A;") != 1 {
		t.Fatalf("unexpected evidence ACL: %s", sddl)
	}
	matched := false
	if uid != "" && strings.Contains(sddl, ";;;"+uid+")") {
		matched = true
	}
	for _, alias := range []string{";;;LA)", ";;;BA)", ";;;SY)", ";;;OW)", ";;;CO)"} {
		if strings.Contains(sddl, alias) {
			matched = true
			break
		}
	}
	if !matched {
		t.Fatalf("unexpected evidence ACL principal: %s (expected %s or well-known alias)", sddl, uid)
	}
}
