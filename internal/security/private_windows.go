package security

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"syscall"
	"unsafe"
)

func currentUserSID() (string, error) {
	api := syscall.NewLazyDLL("advapi32.dll")
	kernel := syscall.NewLazyDLL("kernel32.dll")
	procOpenProcessToken := api.NewProc("OpenProcessToken")
	procGetTokenInformation := api.NewProc("GetTokenInformation")
	procConvertSidToStringSidW := api.NewProc("ConvertSidToStringSidW")
	procGetCurrentProcess := kernel.NewProc("GetCurrentProcess")
	procLocalFree := kernel.NewProc("LocalFree")
	procCloseHandle := kernel.NewProc("CloseHandle")

	procHandle, _, _ := procGetCurrentProcess.Call()
	var token syscall.Handle
	r1, _, _ := procOpenProcessToken.Call(procHandle, 0x0008 /* TOKEN_QUERY */, uintptr(unsafe.Pointer(&token)))
	if r1 != 0 {
		defer procCloseHandle.Call(uintptr(token))
		var reqLen uint32
		procGetTokenInformation.Call(uintptr(token), 1 /* TokenUser */, 0, 0, uintptr(unsafe.Pointer(&reqLen)))
		if reqLen > 0 {
			buf := make([]byte, reqLen)
			r1, _, _ = procGetTokenInformation.Call(uintptr(token), 1, uintptr(unsafe.Pointer(&buf[0])), uintptr(reqLen), uintptr(unsafe.Pointer(&reqLen)))
			if r1 != 0 {
				sidPtr := *(*uintptr)(unsafe.Pointer(&buf[0]))
				var strPtr *uint16
				r1, _, _ = procConvertSidToStringSidW.Call(sidPtr, uintptr(unsafe.Pointer(&strPtr)))
				if r1 != 0 && strPtr != nil {
					defer procLocalFree.Call(uintptr(unsafe.Pointer(strPtr)))
					sid := syscall.UTF16ToString(unsafe.Slice(strPtr, 256))
					if sid != "" {
						return sid, nil
					}
				}
			}
		}
	}

	if u, err := user.Current(); err == nil && u.Uid != "" {
		return u.Uid, nil
	}
	return "", fmt.Errorf("unable to determine current user SID")
}

// Windows ignores Unix permission bits. Install a protected DACL granting only
// the current user full access, inherited by newly created evidence files.
// https://learn.microsoft.com/en-us/windows/win32/api/aclapi/nf-aclapi-setnamedsecurityinfow
func SecureDirectory(path string) error {
	sid, err := currentUserSID()
	if err != nil {
		return err
	}
	sddl, err := syscall.UTF16PtrFromString("D:P(A;OICI;FA;;;" + sid + ")")
	if err != nil {
		return err
	}
	name, err := syscall.UTF16PtrFromString(filepath.Clean(path))
	if err != nil {
		return err
	}
	api := syscall.NewLazyDLL("advapi32.dll")
	var descriptor, acl uintptr
	ok, _, callErr := api.NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW").Call(uintptr(unsafe.Pointer(sddl)), 1, uintptr(unsafe.Pointer(&descriptor)), 0)
	if ok == 0 {
		return fmt.Errorf("create private Windows ACL: %w", callErr)
	}
	defer syscall.NewLazyDLL("kernel32.dll").NewProc("LocalFree").Call(descriptor)
	var present, defaulted uint32
	ok, _, callErr = api.NewProc("GetSecurityDescriptorDacl").Call(descriptor, uintptr(unsafe.Pointer(&present)), uintptr(unsafe.Pointer(&acl)), uintptr(unsafe.Pointer(&defaulted)))
	if ok == 0 || present == 0 || acl == 0 {
		return fmt.Errorf("read private Windows ACL: %v", callErr)
	}
	code, _, _ := api.NewProc("SetNamedSecurityInfoW").Call(uintptr(unsafe.Pointer(name)), 1, 0x80000004, 0, 0, acl, 0)
	if code != 0 {
		return fmt.Errorf("protect evidence directory: %w", syscall.Errno(code))
	}
	return nil
}

// A file's privacy comes from its containing directory's protected DACL.
func PrivateMode(mode os.FileMode) bool { return true }
