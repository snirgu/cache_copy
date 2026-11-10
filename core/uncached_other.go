//go:build !windows && !linux

package core

import "os"

// openUncached is not implemented on this platform; callers fall back to a
// normal (cached) read.
func openUncached(path string) (*os.File, error) {
	return nil, ErrUncachedUnsupported
}

func isUncachedUnsupported(err error) bool { return err == ErrUncachedUnsupported }
