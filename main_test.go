package main

import (
	"bytes"
	"cache_copy/core"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// testLogger collects log lines so tests can assert the user was warned.
type testLogger struct {
	sync.Mutex
	lines []string
}

func (l *testLogger) log(format string, args ...interface{}) {
	l.Lock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
	l.Unlock()
}

func (l *testLogger) contains(s string) bool {
	l.Lock()
	defer l.Unlock()
	for _, line := range l.lines {
		if strings.Contains(line, s) {
			return true
		}
	}
	return false
}

func writeRandom(t *testing.T, path string, size int) []byte {
	t.Helper()
	data := make([]byte, size)
	rand.Read(data)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	return data
}

func newTestCache(t *testing.T) *core.GlobalCache {
	t.Helper()
	return core.NewGlobalCache(filepath.Join(t.TempDir(), "cache.json"))
}

// withCreateDst swaps the destination opener for the duration of a test.
func withCreateDst(t *testing.T, f func(string, int) (*os.File, error)) {
	t.Helper()
	orig := createDst
	createDst = f
	t.Cleanup(func() { createDst = orig })
}

func TestCopyIsFaithfulAndCached(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src.bin"), filepath.Join(dir, "out", "dst.bin")
	want := writeRandom(t, src, 3*1024*1024+17) // not a multiple of the buffer size
	cache := newTestCache(t)
	log := &testLogger{}

	if err := performFileCopy("dst.bin", src, dst, cache, make([]byte, 64*1024), testOpts(), log.log); err != nil {
		t.Fatalf("copy failed: %v", err)
	}
	got, _ := os.ReadFile(dst)
	if !bytes.Equal(got, want) {
		t.Fatal("destination differs from source")
	}
	if _, err := os.Stat(dst + tmpSuffix); !os.IsNotExist(err) {
		t.Fatal("temp file left behind")
	}
	wantHash, _ := core.FileHash(src)
	if e, ok := cache.Get("dst.bin"); !ok || e.Hash != wantHash || e.Size != int64(len(want)) {
		t.Fatalf("cache entry wrong: %+v", e)
	}
	if log.contains("[WARN]") {
		t.Fatal("clean copy should not warn")
	}
}

// Reproduces the production bug class: the first attempt gets a handle whose
// writes fail (as with "bad file descriptor"). The partial file must be thrown
// away, the copy redone from scratch and verified, and the user warned.
func TestFaultyHandleIsDiscardedAndRecopied(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src.bin"), filepath.Join(dir, "dst.bin")
	want := writeRandom(t, src, 1024*1024)
	os.WriteFile(dst, []byte("old version"), 0644)

	calls := 0
	withCreateDst(t, func(path string, _ int) (*os.File, error) {
		calls++
		f, err := os.Create(path)
		if err != nil || calls > 1 {
			return f, err
		}
		f.Close()
		return os.Open(path) // read-only handle: every write fails
	})

	cache := newTestCache(t)
	log := &testLogger{}
	if err := performFileCopy("dst.bin", src, dst, cache, make([]byte, 64*1024), testOpts(), log.log); err != nil {
		t.Fatalf("copy should recover on retry: %v", err)
	}
	got, _ := os.ReadFile(dst)
	if !bytes.Equal(got, want) {
		t.Fatal("destination differs from source after recovery")
	}
	if calls != 2 {
		t.Fatalf("expected 2 attempts, got %d", calls)
	}
	if !log.contains("discarding partial file") || !log.contains("verified against the source") {
		t.Fatalf("user was not warned about the retry: %q", log.lines)
	}
}

// When every attempt fails, the error must surface, no partial data may reach
// the real name, the previous complete version stays intact, and the cache
// must stop trusting that file.
func TestPersistentFailureReportsErrorAndNeverWritesPartialFile(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src.bin"), filepath.Join(dir, "dst.bin")
	writeRandom(t, src, 256*1024)
	old := []byte("previous complete version")
	os.WriteFile(dst, old, 0644)

	withCreateDst(t, func(path string, _ int) (*os.File, error) {
		os.WriteFile(path, []byte("partial"), 0644)
		return nil, errors.New("simulated bad file descriptor")
	})

	cache := newTestCache(t)
	cache.Update("dst.bin", 1, 1, 1)
	err := performFileCopy("dst.bin", src, dst, cache, make([]byte, 64*1024), testOpts(), (&testLogger{}).log)
	if err == nil || !strings.Contains(err.Error(), "simulated bad file descriptor") {
		t.Fatalf("expected the underlying error, got %v", err)
	}
	if got, _ := os.ReadFile(dst); !bytes.Equal(got, old) {
		t.Fatal("destination was modified by a failed copy")
	}
	if _, err := os.Stat(dst + tmpSuffix); !os.IsNotExist(err) {
		t.Fatal("partial temp file left behind")
	}
	if _, ok := cache.Get("dst.bin"); ok {
		t.Fatal("failed file is still trusted by the cache")
	}
}

// The first failure stops the run: files after it are not started.
// One worker makes the processing order deterministic.
func TestRunCopyWorkersStopsOnFirstFailure(t *testing.T) {
	srcDir, dstDir := t.TempDir(), t.TempDir()
	writeRandom(t, filepath.Join(srcDir, "before.bin"), 1000)
	writeRandom(t, filepath.Join(srcDir, "after.bin"), 1000)
	log := &testLogger{}
	// "missing.bin" is listed but does not exist, like a file deleted mid-run.
	failures := runCopyWorkers([]string{"before.bin", "missing.bin", "after.bin"}, srcDir, dstDir, newTestCache(t),
		testOpts(), 2000, log.log, func(int64, int64) {})
	if len(failures) != 1 || !strings.HasPrefix(failures[0], "missing.bin:") {
		t.Fatalf("expected exactly missing.bin to fail, got %q", failures)
	}
	if _, err := os.Stat(filepath.Join(dstDir, "before.bin")); err != nil {
		t.Fatal("file before the failure was not copied")
	}
	if _, err := os.Stat(filepath.Join(dstDir, "after.bin")); !os.IsNotExist(err) {
		t.Fatal("file after the failure was copied; run should have stopped")
	}
	if !log.contains("1 file(s) were not processed") {
		t.Fatalf("user was not told the run stopped: %q", log.lines)
	}
}

func TestPostCopyValidationDeletesMismatchingFile(t *testing.T) {
	srcDir, dstDir := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(srcDir, "f.bin"), []byte("AAAA"), 0644)
	os.WriteFile(filepath.Join(dstDir, "f.bin"), []byte("AAAB"), 0644) // same size, wrong content
	cache := newTestCache(t)
	cache.Update("f.bin", 4, 1, 1)
	failures := &failureList{}

	runPostCopyValidation([]string{"f.bin"}, srcDir, dstDir, cache, 0, (&testLogger{}).log, failures)

	if len(failures.items) != 1 {
		t.Fatalf("expected one failure, got %q", failures.items)
	}
	if _, err := os.Stat(filepath.Join(dstDir, "f.bin")); !os.IsNotExist(err) {
		t.Fatal("mismatching destination file was not deleted")
	}
	if _, ok := cache.Get("f.bin"); ok {
		t.Fatal("mismatching file is still trusted by the cache")
	}
}

// With --retry=false the same first-attempt fault that is normally recovered
// must fail the file after exactly one attempt, leaving the old file intact.
func TestRetryDisabledFailsOnFirstFault(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src.bin"), filepath.Join(dir, "dst.bin")
	writeRandom(t, src, 64*1024)
	old := []byte("old version")
	os.WriteFile(dst, old, 0644)

	calls := 0
	withCreateDst(t, func(path string, tries int) (*os.File, error) {
		calls++
		if tries != 1 {
			t.Errorf("low-level create retries should be off, got tries=%d", tries)
		}
		f, err := os.Create(path)
		if err != nil {
			return f, err
		}
		f.Close()
		return os.Open(path) // read-only handle: every write fails
	})

	err := performFileCopy("dst.bin", src, dst, newTestCache(t), make([]byte, 64*1024), testOptsNoRetry(), (&testLogger{}).log)
	if err == nil || !strings.Contains(err.Error(), "retries disabled") {
		t.Fatalf("expected a no-retry failure, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 attempt, got %d", calls)
	}
	if got, _ := os.ReadFile(dst); !bytes.Equal(got, old) {
		t.Fatal("destination was modified by a failed copy")
	}
	if _, err := os.Stat(dst + tmpSuffix); !os.IsNotExist(err) {
		t.Fatal("partial temp file left behind")
	}
}

// testOpts are the defaults the tests copy with: one worker, retries and fsync on.
func testOpts() copyOptions {
	return copyOptions{BufSize: 64 * 1024, Workers: 1, Retry: true, Fsync: true}
}

func testOptsNoRetry() copyOptions {
	o := testOpts()
	o.Retry = false
	return o
}
