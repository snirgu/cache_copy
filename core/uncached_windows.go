package core

import (
	"errors"
	"os"
	"syscall"
)

const (
	fileFlagNoBuffering    = 0x20000000
	fileFlagSequentialScan = 0x08000000
)

// openUncached opens path with FILE_FLAG_NO_BUFFERING. For an SMB share this
// also disables the client-side cache for the handle, so reads go to the server.
func openUncached(path string) (*os.File, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, fileFlagNoBuffering|fileFlagSequentialScan, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}

func isUncachedUnsupported(err error) bool {
	return errors.Is(err, errorInvalidParameter) || errors.Is(err, errorInvalidFunction)
}

const (
	errorInvalidFunction  = syscall.Errno(1)
	errorInvalidParameter = syscall.Errno(87)
)
