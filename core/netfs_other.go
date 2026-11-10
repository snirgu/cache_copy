//go:build !windows && !linux

package core

// IsNetworkPath is not implemented on this platform; paths are treated as local.
func IsNetworkPath(path string) bool { return false }
