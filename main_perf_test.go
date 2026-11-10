package main

import (
	"cache_copy/core"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// setupCopied copies one file into a fresh destination and returns its paths
// and the cache that recorded it.
func setupCopied(t *testing.T) (src, dst string, cache *core.GlobalCache) {
	t.Helper()
	dir := t.TempDir()
	src, dst = filepath.Join(dir, "src.bin"), filepath.Join(dir, "dst.bin")
	writeRandom(t, src, 50000)
	mtime := time.Date(2021, 1, 2, 3, 4, 5, 0, time.UTC)
	os.Chtimes(src, mtime, mtime)
	cache = newTestCache(t)
	if err := performFileCopy("f", src, dst, cache, make([]byte, 4096), testOpts(), (&testLogger{}).log); err != nil {
		t.Fatal(err)
	}
	return src, dst, cache
}

// needsCopy runs the skip check and returns its decision and the logged reason.
func needsCopy(t *testing.T, src, dst string, cache *core.GlobalCache) (bool, *testLogger) {
	t.Helper()
	info, err := os.Lstat(src)
	if err != nil {
		t.Fatal(err)
	}
	log := &testLogger{}
	return checkCacheForCopy("f", src, dst, info, cache, 3, log.log), log
}

// #8: each check in the ladder, cheapest first, with the reason that decided.
func TestSkipCheckLadder(t *testing.T) {
	t.Run("unchanged file is skipped", func(t *testing.T) {
		src, dst, cache := setupCopied(t)
		if copy, log := needsCopy(t, src, dst, cache); copy {
			t.Fatalf("unchanged file would be copied: %q", log.lines)
		}
	})

	t.Run("size changed: decided before hashing", func(t *testing.T) {
		src, dst, cache := setupCopied(t)
		os.WriteFile(src, []byte("shorter"), 0644)
		if copy, log := needsCopy(t, src, dst, cache); !copy || !log.contains("size changed") {
			t.Fatalf("copy=%v log=%q", copy, log.lines)
		}
	})

	t.Run("modification time changed: decided before hashing", func(t *testing.T) {
		src, dst, cache := setupCopied(t)
		later := time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC)
		os.Chtimes(src, later, later)
		if copy, log := needsCopy(t, src, dst, cache); !copy || !log.contains("modification time changed") {
			t.Fatalf("copy=%v log=%q", copy, log.lines)
		}
	})

	// The case that makes the hash step necessary: same size, same date,
	// different bytes.
	t.Run("same size and time but different content: caught by hash", func(t *testing.T) {
		src, dst, cache := setupCopied(t)
		info, _ := os.Stat(src)
		writeRandom(t, src, int(info.Size()))
		os.Chtimes(src, info.ModTime(), info.ModTime())
		if copy, log := needsCopy(t, src, dst, cache); !copy || !log.contains("content changed") {
			t.Fatalf("copy=%v log=%q", copy, log.lines)
		}
	})

	t.Run("destination modified", func(t *testing.T) {
		src, dst, cache := setupCopied(t)
		info, _ := os.Stat(dst)
		writeRandom(t, dst, int(info.Size())) // same size, new time
		if copy, log := needsCopy(t, src, dst, cache); !copy || !log.contains("destination was modified") {
			t.Fatalf("copy=%v log=%q", copy, log.lines)
		}
	})

	t.Run("destination missing", func(t *testing.T) {
		src, dst, cache := setupCopied(t)
		os.Remove(dst)
		if copy, log := needsCopy(t, src, dst, cache); !copy || !log.contains("destination missing") {
			t.Fatalf("copy=%v log=%q", copy, log.lines)
		}
	})
}

// A cache written by an older version has no recorded times. Such a file must
// be verified by hash and upgraded, not recopied, and the destination gets the
// source's time (older versions left the copy time on it).
func TestLegacyCacheEntryIsUpgradedNotRecopied(t *testing.T) {
	src, dst, cache := setupCopied(t)
	info, _ := os.Stat(src)
	hash, _ := core.FileHash(src)
	copyTime := time.Now().Add(-time.Hour)
	os.Chtimes(dst, copyTime, copyTime)
	cache.Lock()
	cache.Update("f", info.Size(), hash, copyTime.Unix()) // old-format entry
	cache.Unlock()

	if copy, log := needsCopy(t, src, dst, cache); copy {
		t.Fatalf("legacy entry caused a recopy: %q", log.lines)
	}
	dinfo, _ := os.Stat(dst)
	if !dinfo.ModTime().Equal(info.ModTime()) {
		t.Fatalf("destination time not upgraded: %v, want %v", dinfo.ModTime(), info.ModTime())
	}
	e, _ := cache.Get("f")
	if e.SrcMtime != info.ModTime().UnixNano() || e.DstMtime != dinfo.ModTime().UnixNano() {
		t.Fatalf("entry not upgraded: %+v", e)
	}
	// And from now on the fast checks apply.
	if copy, log := needsCopy(t, src, dst, cache); copy {
		t.Fatalf("upgraded entry caused a recopy: %q", log.lines)
	}
}

// #10: the cache is written only when it changed, atomically (no temp file
// left behind), and a dry-run cache is never written.
func TestCacheSavesOnlyWhenChangedAndAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	cache := core.NewGlobalCache(path)

	if err := cache.SaveCache(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("unchanged cache was written")
	}

	cache.Lock()
	cache.Put("a", core.CacheEntry{Size: 1, Hash: 2, ModTime: 3, SrcMtime: 4, DstMtime: 5})
	cache.Unlock()
	if err := cache.SaveCache(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("temp file left behind")
	}

	os.Remove(path)
	cache.SaveCache() // nothing changed since the last save
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("cache rewritten without changes")
	}

	cache.Lock()
	cache.Put("b", core.CacheEntry{Size: 1})
	cache.Unlock()
	cache.SaveCache()
	reloaded := core.NewGlobalCache(path)
	if e, ok := reloaded.Get("a"); !ok || e.SrcMtime != 4 || e.DstMtime != 5 {
		t.Fatalf("round trip lost data: %+v %v", e, ok)
	}
}

// Stale entries are found from the scanned file list, without touching disk.
func TestCleanStaleCacheUsesFileList(t *testing.T) {
	cache := newTestCache(t)
	cache.Lock()
	for _, k := range []string{"keep", "gone", filepath.Join("sub", "keep")} {
		cache.Update(k, 1, 1, 1)
	}
	cache.Unlock()
	cleanStaleCache(cache, &Config{AutoClean: true, Src: "/nonexistent"}, []string{"keep", filepath.Join("sub", "keep")})
	if _, ok := cache.Get("gone"); ok {
		t.Error("stale entry kept")
	}
	for _, k := range []string{"keep", filepath.Join("sub", "keep")} {
		if _, ok := cache.Get(k); !ok {
			t.Errorf("entry %s removed", k)
		}
	}
}

// #11: with fsync off the copy is still complete and correct.
func TestCopyWithoutFsyncIsFaithful(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "s"), filepath.Join(dir, "d")
	want := writeRandom(t, src, 300000)
	opts := testOpts()
	opts.Fsync = false
	if err := performFileCopy("s", src, dst, newTestCache(t), make([]byte, 4096), opts, (&testLogger{}).log); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dst)
	if string(got) != string(want) {
		t.Fatal("copy differs")
	}
}
