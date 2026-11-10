package core

import (
	"path/filepath"
	"syscall"
)

// Filesystem magic numbers from statfs(2) for network filesystems.
var networkFSTypes = map[int64]bool{
	0xFF534D42: true, // CIFS
	0xFE534D42: true, // SMB2/3
	0x517B:     true, // SMB (old smbfs)
	0x6969:     true, // NFS
}

// IsNetworkPath reports whether path is on a network filesystem (CIFS/SMB or
// NFS). A path that does not exist yet, like a new destination, is judged by
// its nearest existing parent.
func IsNetworkPath(path string) bool {
	p, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	for {
		var st syscall.Statfs_t
		if err := syscall.Statfs(p, &st); err == nil {
			return networkFSTypes[int64(st.Type)]
		}
		parent := filepath.Dir(p)
		if parent == p {
			return false
		}
		p = parent
	}
}
