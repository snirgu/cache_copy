// Where the cache lives, moving caches from older versions, and pruning entries.

package main

import (
	"cache_copy/core"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// resolveCacheDir is where cache files live: --cache-dir, else a cache_copy
// folder in the user's cache directory (%LocalAppData%\cache_copy on Windows,
// ~/.cache/cache_copy on Linux), so the cache no longer depends on the folder
// the command is run from.
func resolveCacheDir(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if dir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(dir, "cache_copy")
	}
	return ".cache_cache_copy" // no user cache directory (e.g. HOME unset)
}

// initializeCache creates and initializes the cache
func initializeCache(config *Config) *core.GlobalCache {
	cachePath := filepath.Join(config.CacheDir, core.CacheFileName(config.Src, config.Dst, config.RootDst))
	legacyPath := core.LegacyCacheFile(config.Src, config.RootDst)

	// A dry run reads the cache to decide what would be copied but never writes it.
	if config.DryRun {
		loadPath := cachePath
		if !fileExists(cachePath) && fileExists(legacyPath) {
			loadPath = legacyPath
		}
		cache := core.NewGlobalCache(loadPath)
		logCacheLoad(cache)
		if config.ClearCache {
			fmt.Fprintf(logOut, "[%s] [DRY-RUN] Would delete cache: %s\n", timestamp(), loadPath)
			cache.Clear()
		}
		cache.SetReadOnly()
		return cache
	}

	if err := os.MkdirAll(config.CacheDir, os.ModePerm); err != nil {
		fatalf(nil, "Cannot create cache folder %s: %v (use --cache-dir to choose another)", config.CacheDir, err)
	}
	migrateLegacyCache(legacyPath, cachePath)

	if config.ClearCache {
		if err := os.Remove(cachePath); err == nil {
			fmt.Fprintf(logOut, "[%s] [INFO] Cache deleted: %s\n", timestamp(), cachePath)
		} else if !os.IsNotExist(err) {
			fatalf(nil, "Failed to delete cache: %v", err)
		}
	}

	cache := core.NewGlobalCache(cachePath)
	logCacheLoad(cache)
	return cache
}

// migrateLegacyCache moves a cache left by version 1.0.3 or older (in
// .cache_cache_copy under the current folder, with the old file name) to the
// new location, so upgrading does not force every file to be re-verified.
func migrateLegacyCache(legacyPath, cachePath string) {
	if fileExists(cachePath) {
		return
	}
	data, err := os.ReadFile(legacyPath)
	if err != nil {
		return // no legacy cache
	}
	if err := os.WriteFile(cachePath, data, 0644); err != nil {
		fmt.Fprintf(logOut, "[%s] [WARN] Could not move old cache %s to %s: %v\n", timestamp(), legacyPath, cachePath, err)
		return
	}
	os.Remove(legacyPath)
	os.Remove(filepath.Dir(legacyPath)) // removes .cache_cache_copy only once it is empty
	fmt.Fprintf(logOut, "[%s] [INFO] Moved cache from %s to %s\n", timestamp(), legacyPath, cachePath)
}

// logCacheLoad logs the cache's "LEVEL message" load status.
func logCacheLoad(cache *core.GlobalCache) {
	level, msg, _ := strings.Cut(cache.LoadMessage(), " ")
	fmt.Fprintf(logOut, "[%s] [%s] %s\n", timestamp(), level, msg)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// cleanStaleCache removes cache entries for files no longer in the source. It
// uses the file list from the source scan instead of a stat per cache entry,
// which on a network share was one round trip per cached file before copying.
func cleanStaleCache(cache *core.GlobalCache, config *Config, fileList []string) {
	if !config.AutoClean {
		return
	}

	inSource := make(map[string]struct{}, len(fileList))
	for _, relPath := range fileList {
		inSource[relPath] = struct{}{}
	}

	cache.Lock()
	stale := 0
	for _, relPath := range cache.Keys() {
		if _, ok := inSource[relPath]; !ok {
			if config.Verbose >= 3 {
				fmt.Fprintf(logOut, "[%s] [CACHE] Removing stale entry: %s\n", timestamp(), relPath)
			}
			cache.Remove(relPath)
			stale++
		}
	}
	cache.Unlock()

	if stale > 0 && config.Verbose >= 1 {
		fmt.Fprintf(logOut, "[%s] [INFO] Auto-cleaned %d stale cache entries\n", timestamp(), stale)
	}
}

// cleanOldCacheEntries removes cache entries older than maxCacheAge
func cleanOldCacheEntries(cache *core.GlobalCache, config *Config) {
	now := time.Now().Unix()
	maxAgeSeconds := int64(config.MaxCacheAge) * 24 * 60 * 60

	cache.Lock()
	for _, key := range cache.Keys() {
		entry, ok := cache.Get(key)
		if ok && entry.ModTime > 0 && now-entry.ModTime > maxAgeSeconds {
			cache.Remove(key)
		}
	}
	cache.Unlock()

	// If all cache entries are old, delete the cache file
	allOld := true
	cache.Lock()
	for _, key := range cache.Keys() {
		entry, ok := cache.Get(key)
		if ok && entry.ModTime > 0 && now-entry.ModTime <= maxAgeSeconds {
			allOld = false
			break
		}
	}
	cache.Unlock()

	if allOld && len(cache.Keys()) > 0 && !config.DryRun {
		cachePath := cache.Path()
		fmt.Fprintf(logOut, "[%s] [INFO] All cache entries older than %d days, deleting cache file: %s\n", timestamp(), config.MaxCacheAge, cachePath)
		os.Remove(cachePath)
	}
}
