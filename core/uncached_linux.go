package core

import (
	"errors"
	"os"
	"syscall"
)

// openUncached opens path with O_DIRECT so reads skip the page cache. On a
// CIFS mount this reads from the server instead of locally cached pages.
func openUncached(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_DIRECT, 0)
}

func isUncachedUnsupported(err error) bool {
	return errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.EOPNOTSUPP)
}
