package main

import (
	"bytes"
	"cache_copy/core"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// #4: the copy carries the source's modification time and permissions, and a
// read-only destination can be replaced on the next run (Windows refuses to
// rename over a read-only file unless the flag is cleared first).
func TestCopyPreservesModTimeAndPermissions(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src.bin"), filepath.Join(dir, "dst.bin")
	writeRandom(t, src, 10000)
	mtime := time.Date(2020, 5, 17, 10, 30, 0, 0, time.UTC)
	os.Chtimes(src, mtime, mtime)
	mode := os.FileMode(0750)
	if runtime.GOOS == "windows" {
		mode = 0444 // Windows only has the read-only bit
	}
	os.Chmod(src, mode)
	t.Cleanup(func() { os.Chmod(src, 0644); os.Chmod(dst, 0644) })

	copyOnce := func() {
		t.Helper()
		if err := performFileCopy("dst.bin", src, dst, newTestCache(t), make([]byte, 4096), testOpts(), (&testLogger{}).log); err != nil {
			t.Fatalf("copy failed: %v", err)
		}
		di, _ := os.Stat(dst)
		if !di.ModTime().Equal(mtime) {
			t.Fatalf("mtime = %v, want %v", di.ModTime(), mtime)
		}
		if di.Mode().Perm() != mode {
			t.Fatalf("mode = %v, want %v", di.Mode().Perm(), mode)
		}
	}
	copyOnce()

	// Change the source and copy again over the now read-only destination.
	os.Chmod(src, 0644)
	want := writeRandom(t, src, 12000)
	os.Chtimes(src, mtime, mtime)
	os.Chmod(src, mode)
	copyOnce()
	if got, _ := os.ReadFile(dst); !bytes.Equal(got, want) {
		t.Fatal("second copy did not replace the read-only destination")
	}
}

// #5: a link is recreated as a link with the same target, including a link to
// a directory (never followed) and a dangling link.
func TestSymlinkRecreatedAsLink(t *testing.T) {
	srcDir, dstDir := t.TempDir(), t.TempDir()
	os.Mkdir(filepath.Join(srcDir, "realdir"), 0755)
	os.WriteFile(filepath.Join(srcDir, "realdir", "inner.txt"), []byte("x"), 0644)
	links := map[string]string{"to-dir": "realdir", "dangling": "does-not-exist"}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(srcDir, name)); err != nil {
			t.Skipf("cannot create symlinks here: %v", err)
		}
	}
	cache := newTestCache(t)
	run := func() []string {
		return runCopyWorkers([]string{"to-dir", "dangling"}, srcDir, dstDir, cache,
			testOpts(), 100, (&testLogger{}).log, func(int64, int64) {})
	}
	if f := run(); len(f) != 0 {
		t.Fatalf("unexpected failures: %q", f)
	}
	for name, target := range links {
		got, err := os.Readlink(filepath.Join(dstDir, name))
		if err != nil || got != target {
			t.Fatalf("%s: link target %q (err %v), want %q", name, got, err, target)
		}
	}
	if fi, _ := os.Lstat(filepath.Join(dstDir, "to-dir")); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("directory link was followed and copied as a directory")
	}
	if f := run(); len(f) != 0 { // second run: links already correct
		t.Fatalf("unexpected failures on rerun: %q", f)
	}
}

// #5: a named pipe would block a reader forever; it must be reported instead.
func TestSpecialFileIsReported(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("named pipes in the filesystem are a Unix feature")
	}
	srcDir := t.TempDir()
	if err := exec.Command("mkfifo", filepath.Join(srcDir, "pipe")).Run(); err != nil {
		t.Skipf("mkfifo not available: %v", err)
	}
	failures := runCopyWorkers([]string{"pipe"}, srcDir, t.TempDir(), newTestCache(t),
		testOpts(), 0, (&testLogger{}).log, func(int64, int64) {})
	if len(failures) != 1 || !strings.Contains(failures[0], "unsupported file type") {
		t.Fatalf("expected an unsupported-file-type failure, got %q", failures)
	}
}

// #6: mirror deletes extras and kind changes, keeps everything else, drops the
// deleted subtree from the cache, and a dry run touches nothing.
func TestDeleteExtraFiles(t *testing.T) {
	srcDir, dstDir := t.TempDir(), t.TempDir()
	write := func(base, rel string) {
		p := filepath.Join(base, rel)
		os.MkdirAll(filepath.Dir(p), 0755)
		os.WriteFile(p, []byte(rel), 0644)
	}
	for _, rel := range []string{"keep.txt", filepath.Join("sub", "keep.txt"), filepath.Join("nowdir", "f.txt")} {
		write(srcDir, rel)
		write(dstDir, rel)
	}
	os.RemoveAll(filepath.Join(dstDir, "nowdir"))
	write(dstDir, "nowdir")                                 // a file where the source has a directory
	write(dstDir, "extra.txt")                              // not in the source
	write(dstDir, filepath.Join("olddir", "a", "deep.txt")) // a whole extra tree
	write(dstDir, "keep.txt"+tmpSuffix)                     // a leftover temp file

	cache := newTestCache(t)
	deep := filepath.Join("olddir", "a", "deep.txt")
	cache.Update(deep, 1, 1, 1)
	cache.Update("keep.txt", 1, 1, 1)

	n, err := deleteExtraFiles(srcDir, dstDir, cache, true)
	if err != nil || n != 4 {
		t.Fatalf("dry run: n=%d err=%v, want 4 nil", n, err)
	}
	if _, err := os.Stat(filepath.Join(dstDir, "extra.txt")); err != nil {
		t.Fatal("dry run deleted a file")
	}

	n, err = deleteExtraFiles(srcDir, dstDir, cache, false)
	if err != nil || n != 4 {
		t.Fatalf("real run: n=%d err=%v, want 4 nil", n, err)
	}
	for _, gone := range []string{"extra.txt", "olddir", "nowdir", "keep.txt" + tmpSuffix} {
		if _, err := os.Lstat(filepath.Join(dstDir, gone)); !os.IsNotExist(err) {
			t.Errorf("%s should have been deleted", gone)
		}
	}
	for _, kept := range []string{"keep.txt", filepath.Join("sub", "keep.txt")} {
		if _, err := os.Stat(filepath.Join(dstDir, kept)); err != nil {
			t.Errorf("%s should have been kept", kept)
		}
	}
	if _, ok := cache.Get(deep); ok {
		t.Error("cache still has an entry under a deleted directory")
	}
	if _, ok := cache.Get("keep.txt"); !ok {
		t.Error("cache lost an entry for a kept file")
	}
}

// #6: a destination inside the source is always refused; with --mirror, a
// source inside the destination and a filesystem root are refused too.
func TestCheckPaths(t *testing.T) {
	base := t.TempDir()
	root := filepath.VolumeName(base) + string(filepath.Separator)
	type tc struct {
		name       string
		src, dst   string
		mirror, ok bool
	}
	cases := []tc{
		{"separate folders", filepath.Join(base, "a"), filepath.Join(base, "b"), true, true},
		{"sibling with shared prefix", filepath.Join(base, "data"), filepath.Join(base, "data-backup"), true, true},
		{"dst inside src", filepath.Join(base, "a"), filepath.Join(base, "a", "copy"), false, false},
		{"src inside dst, plain copy", filepath.Join(base, "a", "b"), filepath.Join(base, "a"), false, true},
		{"src inside dst, mirror", filepath.Join(base, "a", "b"), filepath.Join(base, "a"), true, false},
		{"same folder", filepath.Join(base, "a"), filepath.Join(base, "a"), false, false},
		{"mirror into root", filepath.Join(base, "a"), root, true, false},
		{"copy into root without mirror", filepath.Join(base, "a"), root, false, true},
	}
	if runtime.GOOS == "windows" {
		cases = append(cases, tc{"case-insensitive overlap", filepath.Join(base, "A"), filepath.Join(base, "a", "x"), false, false})
	}
	// The temp folder is often under a short name (C:\Users\ABCDEF~1); an
	// overlap written once short and once long must still be caught.
	if long, err := filepath.EvalSymlinks(base); err == nil && long != base {
		cases = append(cases, tc{"overlap hidden by short name", filepath.Join(base, "a"), filepath.Join(long, "a", "x"), false, false})
	}
	for _, c := range cases {
		err := checkPaths(&Config{Src: c.src, RootDst: c.dst, Mirror: c.mirror})
		if (err == nil) != c.ok {
			t.Errorf("%s: err=%v, want ok=%v", c.name, err, c.ok)
		}
	}
}

// #7: flags anywhere, value flags never swallowed as paths, "--flag false"
// for on/off flags, and "--" for paths starting with "-".
func TestSplitArgs(t *testing.T) {
	newFS := func() *flag.FlagSet {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		fs.SetOutput(&bytes.Buffer{})
		fs.Int("workers", 1, "")
		fs.String("log-path", "", "")
		fs.Bool("retry", true, "")
		fs.Bool("no-tui", false, "")
		return fs
	}
	cases := []struct{ in, pos, flags string }{
		{"src dst --workers 4", "src dst", "--workers 4"},
		{"--workers 4 src dst", "src dst", "--workers 4"},
		{"src --workers 4 dst", "src dst", "--workers 4"},
		{"src dst --retry false", "src dst", "--retry=false"},
		{"--retry FALSE src dst", "src dst", "--retry=false"},
		{"--no-tui src dst", "src dst", "--no-tui"},
		{"src dst -retry=false --log-path x.log", "src dst", "-retry=false --log-path x.log"},
		{"--workers 4 -- -weird dst", "-weird dst", "--workers 4"},
		{"src dst extra", "src dst extra", ""},
		{"src dst --unknown", "src dst", "--unknown"},
	}
	for _, c := range cases {
		pos, flags := splitArgs(strings.Fields(c.in), newFS())
		if strings.Join(pos, " ") != c.pos || strings.Join(flags, " ") != c.flags {
			t.Errorf("%q: positional %q flags %q; want %q %q", c.in, pos, flags, c.pos, c.flags)
		}
		if err := newFS().Parse(flags); (err != nil) != strings.Contains(c.in, "unknown") {
			t.Errorf("%q: parse error %v", c.in, err)
		}
	}
}

// #6: a dry run reports what it would copy but writes nothing.
func TestDryRunChangesNothing(t *testing.T) {
	srcDir, dstDir := t.TempDir(), t.TempDir()
	writeRandom(t, filepath.Join(srcDir, "a.bin"), 1000)
	writeRandom(t, filepath.Join(srcDir, "b.bin"), 1000)
	cachePath := filepath.Join(t.TempDir(), "cache.json")
	cache := core.NewGlobalCache(cachePath)
	cache.SetReadOnly()
	log := &testLogger{}

	failures := runCopyWorkers([]string{"a.bin", "b.bin"}, srcDir, dstDir, cache,
		copyOptions{BufSize: 4096, Workers: 2, Retry: true, DryRun: true}, 2000, log.log, func(int64, int64) {})
	if len(failures) != 0 {
		t.Fatalf("unexpected failures: %q", failures)
	}
	if entries, _ := os.ReadDir(dstDir); len(entries) != 0 {
		t.Fatalf("dry run wrote to the destination: %v", entries)
	}
	if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
		t.Fatal("dry run wrote the cache file")
	}
	if !log.contains("2 of 2 file(s) would be copied") {
		t.Fatalf("missing dry-run summary: %q", log.lines)
	}
}
