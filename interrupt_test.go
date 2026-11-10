package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// withInterrupt resets the interrupt flag after a test that sets it.
func withInterrupt(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { interrupted.Store(false) })
}

// An interrupt during a copy stops it at the next chunk: no retry, the temp
// file is deleted, and the previous destination file is left untouched.
func TestInterruptStopsCopyInProgress(t *testing.T) {
	withInterrupt(t)
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src.bin"), filepath.Join(dir, "dst.bin")
	writeRandom(t, src, 1<<20)
	old := []byte("previous complete version")
	os.WriteFile(dst, old, 0644)

	attempts := 0
	withCreateDst(t, func(path string, tries int) (*os.File, error) {
		attempts++
		interrupted.Store(true) // Ctrl+C arrives right after the copy started
		return os.Create(path)
	})

	err := performFileCopy("dst.bin", src, dst, newTestCache(t), make([]byte, 4096), testOpts(), (&testLogger{}).log)
	if !errors.Is(err, errInterrupted) {
		t.Fatalf("got %v, want errInterrupted", err)
	}
	if attempts != 1 {
		t.Fatalf("%d attempts: an interrupted copy must not be retried", attempts)
	}
	if got, _ := os.ReadFile(dst); !bytes.Equal(got, old) {
		t.Fatal("destination changed by an interrupted copy")
	}
	if _, err := os.Stat(dst + tmpSuffix); !os.IsNotExist(err) {
		t.Fatal("temp file left after interrupt")
	}
}

// After an interrupt, workers start no new files and say how many were left.
func TestInterruptStartsNoNewFiles(t *testing.T) {
	withInterrupt(t)
	srcDir, dstDir := t.TempDir(), t.TempDir()
	for _, n := range []string{"a", "b", "c"} {
		writeRandom(t, filepath.Join(srcDir, n), 100)
	}
	interrupted.Store(true)
	log := &testLogger{}
	runCopyWorkers([]string{"a", "b", "c"}, srcDir, dstDir, newTestCache(t), testOpts(), 300, log.log, func(int64, int64) {})
	if entries, _ := os.ReadDir(dstDir); len(entries) != 0 {
		t.Fatalf("files copied after the interrupt: %v", entries)
	}
	if !log.contains("Interrupted: 3 file(s) were not processed") {
		t.Fatalf("missing interrupt summary: %q", log.lines)
	}
}
