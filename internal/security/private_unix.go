//go:build !windows

package security

import "os"

func SecureDirectory(path string) error { return os.Chmod(path, 0700) }
func PrivateMode(mode os.FileMode) bool { return mode.Perm()&0077 == 0 }
