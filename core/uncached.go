package core

import (
	"errors"
	"io"
	"unsafe"

	"github.com/cespare/xxhash/v2"
)

// ErrUncachedUnsupported means the filesystem refused an uncached read (for
// example Linux tmpfs, or a CIFS mount that does not allow O_DIRECT).
var ErrUncachedUnsupported = errors.New("uncached read not supported on this filesystem")

const (
	// uncachedAlign covers 512-byte and 4K-sector disks. Uncached I/O needs the
	// buffer address and each read size to be multiples of the sector size.
	uncachedAlign = 4096
	uncachedChunk = 1024 * 1024
)

// FileHashUncached computes the xxHash of a file while bypassing this
// machine's OS file cache, so the bytes come from the storage itself (the
// local disk, or the server for a network share) rather than from memory that
// still holds what was just written. Use it to verify a destination file.
// Returns ErrUncachedUnsupported if the filesystem cannot do uncached reads.
func FileHashUncached(path string) (uint64, error) {
	f, err := openUncached(path)
	if err != nil {
		if isUncachedUnsupported(err) {
			return 0, ErrUncachedUnsupported
		}
		return 0, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return 0, err
	}

	// Read exactly the file size, never "until EOF": after a partial last chunk
	// the offset is no longer sector-aligned, and SMB rejects an unbuffered read
	// there with ERROR_INVALID_PARAMETER instead of returning EOF.
	buf := alignedBuffer(uncachedChunk, uncachedAlign)
	h := xxhash.New()
	for remaining := info.Size(); remaining > 0; {
		n, err := f.Read(buf)
		if int64(n) > remaining {
			n = int(remaining)
		}
		h.Write(buf[:n])
		remaining -= int64(n)
		if remaining == 0 {
			break
		}
		if err == io.EOF || (err == nil && n == 0) {
			return 0, io.ErrUnexpectedEOF
		}
		if err != nil {
			if isUncachedUnsupported(err) {
				return 0, ErrUncachedUnsupported
			}
			return 0, err
		}
	}
	return h.Sum64(), nil
}

// alignedBuffer returns a size-byte slice whose start address is a multiple of align.
func alignedBuffer(size, align int) []byte {
	raw := make([]byte, size+align)
	off := int(uintptr(unsafe.Pointer(&raw[0])) & uintptr(align-1))
	if off != 0 {
		off = align - off
	}
	return raw[off : off+size]
}
