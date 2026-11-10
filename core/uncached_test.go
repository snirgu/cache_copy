package core

import (
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"unsafe"
)

func TestFileHashUncachedMatchesCachedHash(t *testing.T) {
	dir := t.TempDir()
	// Sizes around the sector and chunk boundaries, where uncached reads differ.
	for _, size := range []int{0, 1, 4095, 4096, 4097, uncachedChunk, uncachedChunk + 1, 3*uncachedChunk + 7} {
		path := filepath.Join(dir, "f.bin")
		data := make([]byte, size)
		rand.Read(data)
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatal(err)
		}
		want, _ := FileHash(path)
		got, err := FileHashUncached(path)
		if errors.Is(err, ErrUncachedUnsupported) {
			t.Skip("temp filesystem does not support uncached reads")
		}
		if err != nil || got != want {
			t.Fatalf("size %d: uncached hash %d err %v, want %d", size, got, err, want)
		}
	}
}

// SMB behaves differently from local disks for unbuffered reads (it rejects a
// read at an unaligned EOF offset), so exercise it through the Windows
// loopback admin share when that is reachable.
func TestFileHashUncachedOverSMB(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("SMB loopback test is Windows-only")
	}
	dir := t.TempDir()
	share := `\\localhost\` + dir[:1] + `$`
	if _, err := os.Stat(share); err != nil {
		t.Skipf("loopback admin share %s not reachable: %v", share, err)
	}
	local := filepath.Join(dir, "f.bin")
	data := make([]byte, 3*uncachedChunk+7) // unaligned size: last read leaves an unaligned offset
	rand.Read(data)
	os.WriteFile(local, data, 0644)

	want, _ := FileHash(local)
	got, err := FileHashUncached(share + local[2:])
	if err != nil || got != want {
		t.Fatalf("uncached hash over SMB = %d, err %v; want %d", got, err, want)
	}
}

// Uncached handles reject misaligned buffers; a normal cached handle never
// does. A failing misaligned read therefore proves the OS cache is bypassed.
func TestOpenUncachedReallyBypassesCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.bin")
	os.WriteFile(path, make([]byte, 64*1024), 0644)
	f, err := openUncached(path)
	if err != nil {
		if isUncachedUnsupported(err) {
			t.Skip("temp filesystem does not support uncached reads")
		}
		t.Fatal(err)
	}
	defer f.Close()

	buf := alignedBuffer(8192+1, uncachedAlign)[1:] // address off by one byte
	if _, err := f.Read(buf); err == nil {
		t.Fatal("misaligned read succeeded: handle is going through the OS cache")
	}
}

func TestAlignedBuffer(t *testing.T) {
	for i := 0; i < 20; i++ {
		b := alignedBuffer(uncachedChunk, uncachedAlign)
		if len(b) != uncachedChunk || uintptr(unsafe.Pointer(&b[0]))%uncachedAlign != 0 {
			t.Fatalf("buffer not aligned or wrong size")
		}
	}
}
