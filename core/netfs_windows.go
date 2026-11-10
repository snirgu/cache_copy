package core

import (
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

var procGetDriveTypeW = syscall.NewLazyDLL("kernel32.dll").NewProc("GetDriveTypeW")

const driveRemote = 4 // DRIVE_REMOTE

// IsNetworkPath reports whether path is on a network share: a UNC path
// (\\server\share) or a drive letter Windows reports as a mapped network drive.
func IsNetworkPath(path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	vol := filepath.VolumeName(abs)
	if strings.HasPrefix(vol, `\\`) {
		return true
	}
	if vol == "" {
		return false
	}
	root, err := syscall.UTF16PtrFromString(vol + `\`)
	if err != nil {
		return false
	}
	t, _, _ := procGetDriveTypeW.Call(uintptr(unsafe.Pointer(root)))
	return t == driveRemote
}
