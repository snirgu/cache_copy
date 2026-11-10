// Copying one file faithfully: temp file, flush, verify, metadata, atomic rename.

package main

import (
	"cache_copy/core"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// tmpSuffix marks an in-progress copy. Data is written to <dst>+tmpSuffix and
// only renamed over <dst> once it is complete, flushed and closed without
// error, so an interrupted or failed copy can never leave a damaged file under
// the real name. The name is deterministic, so a leftover from a crashed run
// is simply overwritten by the next one.
const tmpSuffix = ".cache_copy.tmp"

// createDst opens the temp destination file for writing. Tests replace it to
// inject faulty handles.
var createDst = core.CreateWithRetry

// removeTemp deletes a temp file. A temp file may carry the source's read-only
// permission, which stops Windows deleting it, so that flag is cleared first.
// Links are never chmod'ed, since that would change their target.
func removeTemp(path string) {
	if runtime.GOOS == "windows" {
		if fi, err := os.Lstat(path); err == nil && fi.Mode().IsRegular() {
			os.Chmod(path, 0666)
		}
	}
	os.Remove(path)
}

// performFileCopy copies srcPath to dstPath so that dstPath ends up byte-for-byte
// equal to the source, or returns an error. Each attempt writes a fresh temp
// file from scratch; any anomaly (I/O error, flush or close error, short copy,
// source changing mid-copy, verification mismatch) throws that temp file away
// and starts over. A retry never reuses a handle that has already failed: after
// a failed flush the OS may have dropped the unwritten data, so a second flush
// can "succeed" on a file that is the right size but has the wrong content.
//
// With retry false there is exactly one attempt and no low-level open/create
// retries either: the first problem fails the file.
func performFileCopy(relPath, srcPath, dstPath string, cache *core.GlobalCache,
	buf []byte, opts copyOptions, logger LoggerFunc) error {

	if err := os.MkdirAll(filepath.Dir(dstPath), os.ModePerm); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", filepath.Dir(dstPath), err)
	}

	tmpPath := dstPath + tmpSuffix
	retryCount, openTries := 3, 2
	if opts.Network {
		retryCount = 5 // network filesystems have more transient errors
	}
	if !opts.Retry {
		retryCount, openTries = 1, 1
	}
	var lastErr error

	for attempt := 1; attempt <= retryCount; attempt++ {
		if interrupted.Load() {
			return errInterrupted
		}
		if attempt > 1 {
			// Always shown: a retry means something abnormal happened on this file.
			logger("[%s] [WARN] Copy attempt %d/%d for %s failed: %v - discarding partial file and recopying from scratch\n",
				timestamp(), attempt-1, retryCount, relPath, lastErr)
			time.Sleep(time.Duration(250<<(attempt-2)) * time.Millisecond)
		}

		// After any failure, read the new copy back and compare it with the
		// source hash before trusting it.
		removeTemp(tmpPath) // a leftover from a crashed run may be read-only
		srcInfo, hash, err := copyAttempt(srcPath, tmpPath, buf, openTries, opts.Fsync, attempt > 1, logger)
		if err == nil {
			err = applyMetadata(tmpPath, dstPath, srcInfo)
		}
		if err == nil {
			if err = os.Rename(tmpPath, dstPath); err != nil {
				err = fmt.Errorf("failed to move completed copy into place: %w", err)
			}
		}
		if err != nil {
			removeTemp(tmpPath)
			if errors.Is(err, errInterrupted) {
				return err // the user asked to stop: no retry
			}
			lastErr = err
			continue
		}

		if attempt > 1 {
			logger("[%s] [WARN] %s copied successfully on attempt %d/%d and verified against the source\n",
				timestamp(), relPath, attempt, retryCount)
		}
		updateCacheAfterCopy(relPath, dstPath, srcInfo, hash, cache, opts.NoCache, opts.Verbose, logger)
		return nil
	}

	// Never trust a destination we failed to refresh: drop its cache entry so the
	// next run copies it again. The previous complete version (if any) is left in
	// place untouched, since no partial data ever reached the real name.
	cache.Lock()
	cache.Remove(relPath)
	cache.Unlock()
	if retryCount == 1 {
		return fmt.Errorf("failed (retries disabled): %w", lastErr)
	}
	return fmt.Errorf("gave up after %d attempts: %w", retryCount, lastErr)
}

// applyMetadata gives the completed temp file the source's modification time
// and permission bits before it is renamed into place, so the final file
// appears with the right metadata in one step. Times go first because Windows
// will not change the times of a read-only file.
func applyMetadata(tmpPath, dstPath string, srcInfo os.FileInfo) error {
	if err := os.Chtimes(tmpPath, time.Time{}, srcInfo.ModTime()); err != nil {
		return fmt.Errorf("setting modification time failed: %w", err)
	}
	mode := srcInfo.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
	if err := os.Chmod(tmpPath, mode); err != nil {
		return fmt.Errorf("setting permissions failed: %w", err)
	}
	// Windows will not rename over a read-only file. The old copy is about to
	// be replaced, so clear its read-only flag.
	if runtime.GOOS == "windows" {
		if di, err := os.Lstat(dstPath); err == nil && di.Mode().IsRegular() && di.Mode().Perm()&0200 == 0 {
			os.Chmod(dstPath, 0666)
		}
	}
	return nil
}

var uncachedFallbackWarning sync.Once

// hashDestination hashes a file we wrote, bypassing this machine's OS cache so
// the result reflects what the storage (local disk or network server) actually
// holds, not the copy still sitting in memory. Where the filesystem refuses
// uncached reads it falls back to a normal read and warns once, so the user
// knows verification on that target is weaker.
func hashDestination(path string, logger LoggerFunc) (uint64, error) {
	h, err := core.FileHashUncached(path)
	if errors.Is(err, core.ErrUncachedUnsupported) {
		uncachedFallbackWarning.Do(func() {
			logger("[%s] [WARN] Destination filesystem does not support uncached reads; verification reads may be served from this machine's cache\n", timestamp())
		})
		return core.FileHash(path)
	}
	return h, err
}

// copyAttempt writes a complete copy of srcPath to tmpPath and returns the
// source's FileInfo (as copied) and the hash of the bytes written. It succeeds only if every
// read, write, flush and close succeeded, the byte count matches, and the
// source did not change while it was being read (the flush is skipped when
// fsync is false). With verify set, it also
// reads tmpPath back and checks its hash.
func copyAttempt(srcPath, tmpPath string, buf []byte, openTries int, fsync, verify bool, logger LoggerFunc) (os.FileInfo, uint64, error) {
	before, err := os.Stat(srcPath)
	if err != nil {
		return nil, 0, err
	}

	in, err := core.OpenWithRetry(srcPath, openTries)
	if err != nil {
		return nil, 0, err
	}
	defer in.Close()

	out, err := createDst(tmpPath, openTries)
	if err != nil {
		return nil, 0, err
	}

	n, hash, err := core.CopyAndHash(out, interruptibleReader{in}, buf)
	if err != nil {
		out.Close()
		return nil, 0, err
	}
	if n != before.Size() {
		out.Close()
		return nil, 0, fmt.Errorf("incomplete copy: copied %d bytes, expected %d bytes", n, before.Size())
	}
	// Flushing forces write errors to surface now, before the file is trusted.
	// --fsync=false skips it for speed with many small files; errors then
	// surface only on close.
	if fsync {
		if err := out.Sync(); err != nil {
			out.Close()
			return nil, 0, fmt.Errorf("flush to disk failed: %w", err)
		}
	}
	// Network filesystems often report deferred write errors only on close.
	if err := out.Close(); err != nil {
		return nil, 0, fmt.Errorf("closing destination failed: %w", err)
	}

	after, err := os.Stat(srcPath)
	if err != nil {
		return nil, 0, err
	}
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return nil, 0, fmt.Errorf("source file changed while it was being copied")
	}

	if verify {
		got, err := hashDestination(tmpPath, logger)
		if err != nil {
			return nil, 0, fmt.Errorf("reading back copy for verification failed: %w", err)
		}
		if got != hash {
			return nil, 0, fmt.Errorf("verification failed: destination hash %d does not match source hash %d", got, hash)
		}
	}
	return before, hash, nil
}

// updateCacheAfterCopy records a completed copy, using the hash computed while
// copying so the source is not read a second time. The destination's time is
// read back rather than assumed, since filesystems round it differently.
// The cache is saved periodically by runCopyWorkers, not here.
func updateCacheAfterCopy(relPath, dstPath string, srcInfo os.FileInfo, hash uint64, cache *core.GlobalCache,
	noCache bool, verbose int, logger LoggerFunc) {

	if noCache {
		return
	}
	var dstMtime int64
	if fi, err := os.Stat(dstPath); err == nil {
		dstMtime = fi.ModTime().UnixNano()
	}

	cache.Lock()
	_, existed := cache.Get(relPath)
	cache.Put(relPath, core.CacheEntry{
		Size: srcInfo.Size(), Hash: hash, ModTime: time.Now().Unix(),
		SrcMtime: srcInfo.ModTime().UnixNano(), DstMtime: dstMtime,
	})
	cache.Unlock()

	if verbose >= 3 {
		if existed {
			logger("[%s] [CACHE] Updated cache entry: %s (size=%d, hash=%d)\n", timestamp(), relPath, srcInfo.Size(), hash)
		} else {
			logger("[%s] [CACHE] Added new cache entry: %s (size=%d, hash=%d)\n", timestamp(), relPath, srcInfo.Size(), hash)
		}
	}
}
