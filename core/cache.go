package core

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// GlobalCache manages the file copy cache, storing file metadata to avoid unnecessary copies.
type GlobalCache struct {
	sync.RWMutex
	data map[string]*CacheEntry
	path string
}

// CacheEntry holds metadata about a copied file for cache validation.
type CacheEntry struct {
	Size    int64  // File size in bytes
	Hash    uint64 // xxHash64 checksum of file contents
	ModTime int64  // Last modification time (Unix timestamp)
}

// NewGlobalCache loads or creates a cache for the given path.
func NewGlobalCache(path string) *GlobalCache {
	c := &GlobalCache{
		data: make(map[string]*CacheEntry),
		path: path,
	}
	c.load()
	return c
}

func timestamp() string {
	return time.Now().Format("2006-01-02 15:04:05.000")
}

// load reads the cache file from disk, if it exists.
func (c *GlobalCache) load() {
	f, err := os.Open(c.path)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "[%s] [INFO] No cache file found: %s\n", timestamp(), c.path)
		} else {
			fmt.Fprintf(os.Stderr, "[%s] [ERROR] Error opening cache file %s: %v\n", timestamp(), c.path, err)
		}
		return
	}
	defer f.Close()
	json.NewDecoder(f).Decode(&c.data)
}

// SaveCache writes the current cache data to disk in minified JSON format.
func (c *GlobalCache) SaveCache() error {
	c.Lock()
	defer c.Unlock()
	data, err := json.Marshal(c.data)
	if err != nil {
		return err
	}
	f, err := os.Create(c.path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(data)
	return err
}

// Update adds or updates a cache entry for a file.
func (c *GlobalCache) Update(relPath string, size int64, hash uint64, modTime int64) {
	c.data[relPath] = &CacheEntry{Size: size, Hash: hash, ModTime: modTime}
}

// Remove deletes a cache entry for a file or directory.
func (c *GlobalCache) Remove(relPath string) {
	delete(c.data, relPath)
}

// Keys returns a slice of all cache entry keys (relative paths).
func (c *GlobalCache) Keys() []string {
	keys := make([]string, 0, len(c.data))
	for k := range c.data {
		keys = append(keys, k)
	}
	return keys
}

// IsUpToDate checks if a cache entry exists for the given path and returns it.
func (c *GlobalCache) IsUpToDate(relPath string) (*CacheEntry, bool) {
	entry, ok := c.data[relPath]
	return entry, ok
}

// CleanUpMissingFiles removes cache entries for files that no longer exist in the source directory.
func (c *GlobalCache) CleanUpMissingFiles(srcDir string) {
	c.Lock()
	defer c.Unlock()
	for path := range c.data {
		absPath := filepath.Join(srcDir, path)
		if _, err := os.Stat(absPath); os.IsNotExist(err) {
			delete(c.data, path)
		}
	}
}

// Clear removes all cache entries.
func (c *GlobalCache) Clear() {
	c.Lock()
	defer c.Unlock()
	c.data = make(map[string]*CacheEntry)
}

func LocalCacheFile(src, dst string) string {
	absSrc, _ := filepath.Abs(src)
	absDst, _ := filepath.Abs(dst)
	sum := sha256.Sum256([]byte(absSrc + "|" + absDst))
	hash := fmt.Sprintf("%x", sum)[:8] // Shorter hash since we have names now

	// Get base names and clean them for filename safety
	srcBase := filepath.Base(strings.TrimRight(absSrc, string(filepath.Separator)))
	dstBase := filepath.Base(strings.TrimRight(absDst, string(filepath.Separator)))

	// Clean names for filename safety (remove invalid characters)
	srcBase = strings.ReplaceAll(srcBase, ":", "_")
	srcBase = strings.ReplaceAll(srcBase, " ", "_")
	dstBase = strings.ReplaceAll(dstBase, ":", "_")
	dstBase = strings.ReplaceAll(dstBase, " ", "_")

	return fmt.Sprintf(".cache_cache_copy/%s_to_%s_%s.json", srcBase, dstBase, hash)
}
