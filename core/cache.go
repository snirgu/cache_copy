package core

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unicode"
)

// GlobalCache manages the file copy cache, storing file metadata to avoid unnecessary copies.
type GlobalCache struct {
	sync.RWMutex
	data     map[string]*CacheEntry
	path     string
	readOnly bool
	dirty    bool // changed since the last save
	loadMsg  string
}

// SetReadOnly makes SaveCache a no-op, so a dry run can use the cache to
// decide what would be copied without changing the file on disk.
func (c *GlobalCache) SetReadOnly() {
	c.Lock()
	c.readOnly = true
	c.Unlock()
}

// CacheEntry holds metadata about a copied file for cache validation.
type CacheEntry struct {
	Size    int64  // File size in bytes
	Hash    uint64 // xxHash64 checksum of file contents
	ModTime int64  // When this entry was last copied or verified (Unix seconds); drives --max-cache-age
	// SrcMtime is the source's modification time (UnixNano) when it was copied
	// or verified, and DstMtime the destination's as stored after the copy
	// (filesystems round times differently, so the stored value is recorded
	// rather than assumed). Both are 0 in caches written by older versions.
	SrcMtime int64 `json:",omitempty"`
	DstMtime int64 `json:",omitempty"`
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

// LoadMessage describes how the cache was loaded, as "LEVEL message", for the
// caller to log; core does not print.
func (c *GlobalCache) LoadMessage() string { return c.loadMsg }

// Path is the cache file's location.
func (c *GlobalCache) Path() string { return c.path }

// load reads the cache file from disk, if it exists.
func (c *GlobalCache) load() {
	f, err := os.Open(c.path)
	if err != nil {
		if os.IsNotExist(err) {
			c.loadMsg = fmt.Sprintf("INFO No cache yet, starting fresh (will be saved to %s)", c.path)
		} else {
			c.loadMsg = fmt.Sprintf("ERROR Error opening cache file %s: %v", c.path, err)
		}
		return
	}
	defer f.Close()
	if err := json.NewDecoder(f).Decode(&c.data); err != nil {
		c.loadMsg = fmt.Sprintf("WARN Cache file %s is unreadable, starting fresh: %v", c.path, err)
		c.data = make(map[string]*CacheEntry)
		return
	}
	c.loadMsg = fmt.Sprintf("INFO Loaded cache file: %s (%d entries)", c.path, len(c.data))
}

// SaveCache writes the cache to disk in minified JSON format, only if it
// changed since the last save. It writes a temp file and renames it over the
// cache, so a crash mid-save leaves the previous cache intact rather than a
// truncated one.
func (c *GlobalCache) SaveCache() error {
	c.Lock()
	defer c.Unlock()
	if c.readOnly || !c.dirty {
		return nil
	}
	data, err := json.Marshal(c.data)
	if err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmp, c.path); err != nil {
		os.Remove(tmp)
		return err
	}
	c.dirty = false
	return nil
}

// Update adds or updates a cache entry for a file. The caller must hold the lock.
func (c *GlobalCache) Update(relPath string, size int64, hash uint64, modTime int64) {
	c.data[relPath] = &CacheEntry{Size: size, Hash: hash, ModTime: modTime}
	c.dirty = true
}

// Put stores a complete cache entry for a file. The caller must hold the lock.
func (c *GlobalCache) Put(relPath string, e CacheEntry) {
	c.data[relPath] = &e
	c.dirty = true
}

// Remove deletes a cache entry for a file or directory. The caller must hold the lock.
func (c *GlobalCache) Remove(relPath string) {
	if _, ok := c.data[relPath]; ok {
		delete(c.data, relPath)
		c.dirty = true
	}
}

// RemoveTree deletes the entry for relPath and every entry below it, for when
// a whole directory is removed. The caller must hold the lock.
func (c *GlobalCache) RemoveTree(relPath string) {
	prefix := relPath + string(filepath.Separator)
	for k := range c.data {
		if k == relPath || strings.HasPrefix(k, prefix) {
			delete(c.data, k)
			c.dirty = true
		}
	}
}

// Keys returns a slice of all cache entry keys (relative paths).
func (c *GlobalCache) Keys() []string {
	keys := make([]string, 0, len(c.data))
	for k := range c.data {
		keys = append(keys, k)
	}
	return keys
}

// Get returns the cache entry for relPath, if there is one. The caller must hold at least the read lock.
func (c *GlobalCache) Get(relPath string) (*CacheEntry, bool) {
	entry, ok := c.data[relPath]
	return entry, ok
}

// Clear removes all cache entries.
func (c *GlobalCache) Clear() {
	c.Lock()
	defer c.Unlock()
	c.data = make(map[string]*CacheEntry)
	c.dirty = true
}

// CanonicalPath returns one stable spelling of a path, so the same folder is
// always recognized as the same: absolute, with symbolic links and Windows
// short (8.3) names like AERONA~1 resolved for the part of the path that
// exists, and lower-cased on Windows, whose paths are case-insensitive.
func CanonicalPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	existing, rest := abs, ""
	for {
		if resolved, err := filepath.EvalSymlinks(existing); err == nil {
			abs = filepath.Join(resolved, rest)
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			break
		}
		rest = filepath.Join(filepath.Base(existing), rest)
		existing = parent
	}
	if runtime.GOOS == "windows" {
		abs = strings.ToLower(abs)
	}
	return abs
}

// cacheKeyHash identifies a copy job by its source and final destination, in
// canonical form, so "d:\opt", "D:\Opt" and a short-name spelling of the same
// folder all share one cache.
func cacheKeyHash(src, rootDst string) string {
	sum := sha256.Sum256([]byte(CanonicalPath(src) + "|" + CanonicalPath(rootDst)))
	return fmt.Sprintf("%x", sum)[:8]
}

// legacyKeyHash is the job hash used up to version 1.0.3: the paths exactly
// as typed (made absolute), so a different spelling meant a different cache.
func legacyKeyHash(src, rootDst string) string {
	absSrc, _ := filepath.Abs(src)
	absDst, _ := filepath.Abs(rootDst)
	sum := sha256.Sum256([]byte(absSrc + "|" + absDst))
	return fmt.Sprintf("%x", sum)[:8]
}

// CacheFileName names the cache file for a copy job: the source folder name,
// the destination folder name as the user typed it, and the job hash, which is
// what makes the name unique. d:\opt -> d:\opt-2 gives "opt_to_opt-2_<hash>.json".
func CacheFileName(src, dst, rootDst string) string {
	return fmt.Sprintf("%s_to_%s_%s.json", readableName(src), readableName(dst), cacheKeyHash(src, rootDst))
}

// LegacyCacheFile is where versions up to 1.0.3 kept a job's cache: in
// .cache_cache_copy under the current directory, named after the final
// destination folder (so d:\opt -> d:\opt-2 was "opt_to_opt_<hash>.json").
func LegacyCacheFile(src, rootDst string) string {
	absSrc, _ := filepath.Abs(src)
	absDst, _ := filepath.Abs(rootDst)
	srcBase := filepath.Base(strings.TrimRight(absSrc, string(filepath.Separator)))
	dstBase := filepath.Base(strings.TrimRight(absDst, string(filepath.Separator)))
	clean := strings.NewReplacer(":", "_", " ", "_")
	return filepath.Join(".cache_cache_copy",
		fmt.Sprintf("%s_to_%s_%s.json", clean.Replace(srcBase), clean.Replace(dstBase), legacyKeyHash(src, rootDst)))
}

// readableName is a path's last element made safe for a file name on any OS:
// letters (any script) and digits are kept, everything else becomes '_'.
// A drive or filesystem root becomes "root". It uses the canonical path, so
// every spelling of a folder gives the same file name (lower case on Windows).
func readableName(path string) string {
	abs := CanonicalPath(path)
	base := filepath.Base(abs)
	if base == string(filepath.Separator) || base == "." || base == filepath.VolumeName(abs) || filepath.Dir(abs) == abs {
		return "root"
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '.' {
			return r
		}
		return '_'
	}, base)
}
