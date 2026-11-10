package main

import (
	"cache_copy/core"
	"os"
	"path/filepath"
	"testing"
)

// #12: a cache left by version 1.0.3 in .cache_cache_copy under the current
// folder is moved to the cache folder on first use, so nothing is re-hashed,
// and the old folder is removed once empty.
func TestLegacyCacheIsMovedToCacheDir(t *testing.T) {
	work := t.TempDir()
	t.Chdir(work)
	src, dst := filepath.Join(work, "opt"), filepath.Join(work, "opt-2")
	rootDst := filepath.Join(dst, "opt")

	legacyPath := core.LegacyCacheFile(src, rootDst)
	os.MkdirAll(filepath.Dir(legacyPath), 0755)
	old := core.NewGlobalCache(legacyPath)
	old.Lock()
	old.Update("a.txt", 5, 42, 1)
	old.Unlock()
	if err := old.SaveCache(); err != nil {
		t.Fatal(err)
	}

	cacheDir := filepath.Join(t.TempDir(), "cachedir")
	cache := initializeCache(&Config{Src: src, Dst: dst, RootDst: rootDst, CacheDir: cacheDir})

	if e, ok := cache.Get("a.txt"); !ok || e.Hash != 42 {
		t.Fatalf("legacy entries not carried over: %+v %v", e, ok)
	}
	if filepath.Dir(cache.Path()) != cacheDir || filepath.Base(cache.Path()) != core.CacheFileName(src, dst, rootDst) {
		t.Fatalf("cache at %s, want %s in %s", cache.Path(), core.CacheFileName(src, dst, rootDst), cacheDir)
	}
	if _, err := os.Stat(filepath.Join(work, ".cache_cache_copy")); !os.IsNotExist(err) {
		t.Fatal("empty legacy folder left behind")
	}
}

func TestResolveCacheDir(t *testing.T) {
	if got := resolveCacheDir("X"); got != "X" {
		t.Errorf("--cache-dir ignored: %q", got)
	}
	want, err := os.UserCacheDir()
	if err == nil && resolveCacheDir("") != filepath.Join(want, "cache_copy") {
		t.Errorf("default = %q, want %q", resolveCacheDir(""), filepath.Join(want, "cache_copy"))
	}
}
