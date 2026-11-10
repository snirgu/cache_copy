// The copy engine: workers, per-file decisions (skip, copy, link) and the source scan.

package main

import (
	"cache_copy/core"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// failureList collects files that could not be copied faithfully. The first
// failure stops the run: no new files are started, files already being copied
// by other workers finish (each is atomic, so they are either complete or
// absent), and the run exits non-zero. Concurrent workers can fail at the same
// moment, so the list may hold more than one entry.
type failureList struct {
	sync.Mutex
	items     []string
	stop      atomic.Bool
	wouldCopy atomic.Int64 // dry run: files that would be copied
}

func (f *failureList) add(relPath string, err error) {
	f.Lock()
	f.items = append(f.items, fmt.Sprintf("%s: %v", relPath, err))
	f.Unlock()
	f.stop.Store(true)
}

func (f *failureList) stopped() bool { return f.stop.Load() }

// gatherFileList collects all files and directories from source
func gatherFileList(config *Config) ([]string, []string, int64, error) {
	dirs := []string{}
	fileList := []string{}
	var totalBytes int64

	err := filepath.Walk(config.Src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relPath, _ := filepath.Rel(config.Src, path)
		if info.IsDir() {
			dirs = append(dirs, relPath)
		} else {
			fileList = append(fileList, relPath)
			totalBytes += info.Size()
		}
		return nil
	})

	return dirs, fileList, totalBytes, err
}

// createDestinationDirectories ensures all destination directories exist
func createDestinationDirectories(dirs []string, config *Config) error {
	for _, relDir := range dirs {
		dstDir := filepath.Join(config.RootDst, relDir)
		if err := os.MkdirAll(dstDir, os.ModePerm); err != nil {
			return fmt.Errorf("failed to create directory %s: %v", dstDir, err)
		}
	}
	return nil
}

// copyRun is the state shared by all workers of one copy run.
type copyRun struct {
	src, rootDst string
	cache        *core.GlobalCache
	opts         copyOptions
	logger       LoggerFunc
	progress     ProgressFunc
	failures     *failureList
	totalBytes   int64

	mu           sync.Mutex // guards the fields below
	doneBytes    int64
	lastProgress time.Time
	copiedFiles  []string // copied this run, for the --validate second pass
}

// runCopyWorkers copies fileList from src to rootDst with opts.Workers
// concurrent workers and returns the files that could not be copied
// faithfully. The first failure stops the run (see failureList).
func runCopyWorkers(
	fileList []string,
	src, rootDst string,
	cache *core.GlobalCache,
	opts copyOptions,
	totalBytes int64,
	logger LoggerFunc,
	progress ProgressFunc,
) []string {
	r := &copyRun{
		src: src, rootDst: rootDst, cache: cache, opts: opts,
		logger: logger, progress: progress, failures: &failureList{}, totalBytes: totalBytes,
	}

	// Save the cache every few seconds instead of after every file: rewriting
	// the whole cache per file made large runs quadratic. A crash loses at most
	// the last few seconds of entries, which only means those files are
	// re-verified next run. The final save is the caller's (see saveCache).
	stopSaver := make(chan struct{})
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				cache.SaveCache()
			case <-stopSaver:
				return
			}
		}
	}()
	defer close(stopSaver)

	var notStarted atomic.Int64
	var wg sync.WaitGroup
	fileChan := make(chan string, len(fileList))
	for i := 0; i < opts.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, opts.BufSize)
			for relPath := range fileChan {
				if r.failures.stopped() || interrupted.Load() {
					notStarted.Add(1)
					continue
				}
				r.processFile(relPath, buf)
			}
		}()
	}
	for _, relPath := range fileList {
		fileChan <- relPath
	}
	close(fileChan)
	wg.Wait()

	if interrupted.Load() {
		logger("[%s] [ERROR] Interrupted: %d file(s) were not processed\n", timestamp(), notStarted.Load())
		return r.failures.items
	}
	if r.failures.stopped() {
		logger("[%s] [ERROR] Stopped after first failure: %d file(s) were not processed\n", timestamp(), notStarted.Load())
		return r.failures.items
	}
	if opts.DryRun {
		logger("[%s] [DRY-RUN] %d of %d file(s) would be copied\n", timestamp(), r.failures.wouldCopy.Load(), len(fileList))
		return r.failures.items
	}
	if opts.Validate && len(r.copiedFiles) > 0 {
		runPostCopyValidation(r.copiedFiles, src, rootDst, cache, opts.Verbose, logger, r.failures)
	}
	return r.failures.items
}

// processFile handles one source entry: a regular file is checked against the
// cache and copied if needed, a symbolic link is recreated, anything else is
// reported as a failure.
func (r *copyRun) processFile(relPath string, buf []byte) {
	srcPath := filepath.Join(r.src, relPath)
	dstPath := filepath.Join(r.rootDst, relPath)

	srcInfo, err := os.Lstat(srcPath)
	if err != nil {
		r.logger("[%s] [ERROR] Failed to stat %s: %v\n", timestamp(), srcPath, err)
		r.failures.add(relPath, err)
		return
	}

	if srcInfo.Mode()&os.ModeSymlink != 0 {
		r.processSymlink(relPath, srcPath, dstPath)
		r.addProgress(srcInfo.Size())
		return
	}
	if !srcInfo.Mode().IsRegular() {
		err := fmt.Errorf("unsupported file type (%s): only regular files and symbolic links can be copied faithfully", fileTypeName(srcInfo.Mode()))
		r.logger("[%s] [ERROR] %s: %v\n", timestamp(), srcPath, err)
		r.failures.add(relPath, err)
		return
	}

	shouldCopy := r.shouldCopy(relPath, srcPath, dstPath, srcInfo)
	logFileOperation(shouldCopy, srcPath, srcInfo, r.opts.Verbose, r.logger)

	switch {
	case shouldCopy && r.opts.DryRun:
		r.logger("[%s] [DRY-RUN] Would copy: %s (%.2f MB)\n", timestamp(), srcPath, float64(srcInfo.Size())/float64(1<<20))
		r.failures.wouldCopy.Add(1)
	case shouldCopy:
		if err := performFileCopy(relPath, srcPath, dstPath, r.cache, buf, r.opts, r.logger); err != nil {
			r.logger("[%s] [ERROR] Failed to copy %s to %s: %v\n", timestamp(), srcPath, dstPath, err)
			r.failures.add(relPath, err)
		} else if r.opts.Validate {
			r.mu.Lock()
			r.copiedFiles = append(r.copiedFiles, relPath)
			r.mu.Unlock()
		}
	}
	r.addProgress(srcInfo.Size())
}

// addProgress counts n more bytes as processed and reports progress at most
// every 100ms (and always at the end), so workers do not flood the display.
func (r *copyRun) addProgress(n int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.doneBytes += n
	if now := time.Now(); now.Sub(r.lastProgress) > 100*time.Millisecond || r.doneBytes == r.totalBytes {
		r.lastProgress = now
		r.progress(r.doneBytes, r.totalBytes)
	}
}

// fileTypeName names a non-regular, non-link file type for error messages.
func fileTypeName(m os.FileMode) string {
	switch {
	case m&os.ModeNamedPipe != 0:
		return "named pipe"
	case m&os.ModeSocket != 0:
		return "socket"
	case m&os.ModeDevice != 0:
		return "device"
	case m&os.ModeIrregular != 0 && runtime.GOOS == "windows":
		return "junction or other reparse point"
	default:
		return "irregular file " + m.Type().String()
	}
}

// processSymlink recreates a source symbolic link in the destination.
func (r *copyRun) processSymlink(relPath, srcPath, dstPath string) {
	if r.opts.DryRun {
		if !symlinkUpToDate(srcPath, dstPath) {
			r.logger("[%s] [DRY-RUN] Would link: %s\n", timestamp(), srcPath)
			r.failures.wouldCopy.Add(1)
		}
		return
	}
	changed, err := copySymlink(srcPath, dstPath)
	if err != nil {
		r.logger("[%s] [ERROR] Failed to copy link %s: %v\n", timestamp(), srcPath, err)
		r.failures.add(relPath, err)
	} else if changed && r.opts.Verbose >= 2 {
		r.logger("[%s] [VERBOSE] Linked: %s\n", timestamp(), srcPath)
	}
}

// symlinkUpToDate reports whether dstPath is already a link with srcPath's target.
func symlinkUpToDate(srcPath, dstPath string) bool {
	target, err := os.Readlink(srcPath)
	if err != nil {
		return false
	}
	cur, err := os.Readlink(dstPath)
	return err == nil && cur == target
}

// copySymlink recreates a symbolic link as a link with the same target, not a
// copy of what it points to (like cp -a or rsync -a), so links to directories
// are never followed and dangling links are preserved. Returns whether the
// destination changed.
func copySymlink(srcPath, dstPath string) (bool, error) {
	if symlinkUpToDate(srcPath, dstPath) {
		return false, nil
	}
	target, err := os.Readlink(srcPath)
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(dstPath), os.ModePerm); err != nil {
		return false, err
	}
	tmpPath := dstPath + tmpSuffix
	removeTemp(tmpPath)
	if err := os.Symlink(target, tmpPath); err != nil {
		if runtime.GOOS == "windows" {
			return false, fmt.Errorf("creating symbolic link failed (Windows needs Developer Mode or an administrator prompt for this): %w", err)
		}
		return false, fmt.Errorf("creating symbolic link failed: %w", err)
	}
	if err := os.Rename(tmpPath, dstPath); err != nil {
		removeTemp(tmpPath)
		return false, fmt.Errorf("failed to move link into place: %w", err)
	}
	return true, nil
}

// shouldCopy decides whether a file must be copied: always with --no-cache,
// by comparing source and destination content with --validate, otherwise by
// the cache checks in checkCacheForCopy.
func (r *copyRun) shouldCopy(relPath, srcPath, dstPath string, srcInfo os.FileInfo) bool {
	if r.opts.NoCache {
		if r.opts.Verbose >= 3 {
			r.logger("[%s] [NO-CACHE] Forcing copy (cache disabled): %s\n", timestamp(), relPath)
		}
		return true
	}

	if !r.opts.Validate {
		return checkCacheForCopy(relPath, srcPath, dstPath, srcInfo, r.cache, r.opts.Verbose, r.logger)
	}
	return checkValidationForCopy(relPath, srcPath, dstPath, srcInfo, r.cache, r.opts.Verbose, r.logger)
}

// checkCacheForCopy decides whether a file must be copied. It runs the checks
// from cheapest to most expensive and stops at the first difference:
//
//  1. is the file in the cache                        (memory)
//  2. source size, then modification time, vs cache   (memory: both come from
//     the one stat already done, so neither is cheaper than the other)
//  3. destination size and time vs the cache          (one stat of the destination)
//  4. source content hash vs the cache                (reads the whole source)
//
// A file is skipped only if all four match, so a changed file that kept the
// same size and date is still caught by its hash. Step 3 also catches a
// destination that was modified after it was copied.
func checkCacheForCopy(relPath, srcPath, dstPath string, srcInfo os.FileInfo, cache *core.GlobalCache, verbose int, logger LoggerFunc) bool {
	cache.RLock()
	entry, ok := cache.Get(relPath)
	var e core.CacheEntry
	if ok {
		e = *entry
	}
	cache.RUnlock()

	// Entries written by older versions have no recorded times; they fall
	// through to the hash check and are upgraded below instead of recopied.
	legacy := e.SrcMtime == 0
	srcMtime := srcInfo.ModTime().UnixNano()

	reason := ""
	switch {
	case !ok:
		reason = "not in cache"
	case e.Size != srcInfo.Size():
		reason = "size changed"
	case !legacy && e.SrcMtime != srcMtime:
		reason = "modification time changed"
	}

	var dstInfo os.FileInfo
	if reason == "" {
		var err error
		dstInfo, err = os.Stat(dstPath)
		switch {
		case err != nil || !dstInfo.Mode().IsRegular():
			reason = "destination missing"
		case dstInfo.Size() != e.Size:
			reason = "destination size differs"
		case !legacy && dstInfo.ModTime().UnixNano() != e.DstMtime:
			reason = "destination was modified"
		}
	}

	if reason == "" {
		hash, err := core.FileHash(srcPath)
		switch {
		case err != nil:
			reason = fmt.Sprintf("hashing source failed: %v", err)
		case hash != e.Hash:
			reason = "content changed (same size and modification time)"
		}
	}

	if reason != "" {
		if verbose >= 3 {
			logger("[%s] [CACHE] Copy needed for %s: %s\n", timestamp(), relPath, reason)
		}
		return true
	}

	// Verified unchanged. Upgrade a legacy entry: give the destination the
	// source's time (older versions left the copy time) and record both times.
	if legacy {
		if os.Chtimes(dstPath, time.Time{}, srcInfo.ModTime()) == nil {
			if fi, err := os.Stat(dstPath); err == nil {
				dstInfo = fi
			}
		}
	}
	e.SrcMtime = srcMtime
	e.DstMtime = dstInfo.ModTime().UnixNano()
	e.ModTime = time.Now().Unix() // refreshes the --max-cache-age clock
	cache.Lock()
	cache.Put(relPath, e)
	cache.Unlock()
	if verbose >= 3 {
		logger("[%s] [CACHE] Unchanged (size, time and hash match): %s\n", timestamp(), relPath)
	}
	return false
}

// checkValidationForCopy implements validation mode logic
func checkValidationForCopy(relPath, srcPath, dstPath string, srcInfo os.FileInfo, cache *core.GlobalCache, verbose int, logger LoggerFunc) bool {
	if verbose >= 2 {
		logger("[%s] [VALIDATE] Starting validation for: %s\n", timestamp(), relPath)
	}

	dstInfo, err := os.Stat(dstPath)
	if err != nil || !dstInfo.Mode().IsRegular() {
		if verbose >= 2 {
			logger("[%s] [VALIDATE] MISMATCH - Destination file missing: %s\n", timestamp(), relPath)
		}
		return true
	}

	if srcInfo.Size() != dstInfo.Size() {
		logger("[%s] [VALIDATE] MISMATCH - Size differs for %s\n", timestamp(), relPath)
		logger("[%s] [VALIDATE]   Source size: %d bytes\n", timestamp(), srcInfo.Size())
		logger("[%s] [VALIDATE]   Destination size: %d bytes\n", timestamp(), dstInfo.Size())
		return true
	}

	if verbose >= 3 {
		logger("[%s] [VALIDATE] Size match - calculating hashes for: %s\n", timestamp(), relPath)
	}

	srcHash, srcErr := core.FileHash(srcPath)
	dstHash, dstErr := hashDestination(dstPath, logger)

	if srcErr != nil || dstErr != nil || srcHash != dstHash {
		if srcErr != nil {
			logger("[%s] [VALIDATE] ERROR - Cannot calculate source hash for %s: %v\n", timestamp(), relPath, srcErr)
		} else if dstErr != nil {
			logger("[%s] [VALIDATE] ERROR - Cannot calculate destination hash for %s: %v\n", timestamp(), relPath, dstErr)
		} else {
			logger("[%s] [VALIDATE] MISMATCH - Hash differs for %s\n", timestamp(), relPath)
			logger("[%s] [VALIDATE]   Source hash: %d\n", timestamp(), srcHash)
			logger("[%s] [VALIDATE]   Destination hash: %d\n", timestamp(), dstHash)
		}
		return true
	}

	// Files match - update cache and skip copy
	if verbose >= 2 {
		logger("[%s] [VALIDATE] SUCCESS - File validated: %s (size: %d, hash: %d)\n", timestamp(), relPath, srcInfo.Size(), srcHash)
	} else if verbose == 1 {
		logger("[%s] [VALIDATE] SUCCESS - %s\n", timestamp(), relPath)
	}

	cache.Lock()
	cache.Put(relPath, core.CacheEntry{
		Size: srcInfo.Size(), Hash: srcHash, ModTime: time.Now().Unix(),
		SrcMtime: srcInfo.ModTime().UnixNano(), DstMtime: dstInfo.ModTime().UnixNano(),
	})
	cache.Unlock()
	if verbose >= 3 {
		logger("[%s] [CACHE] Updated after validation: %s\n", timestamp(), relPath)
	}
	return false
}

// largeFileSize is the --verbose 1 threshold for logging a file.
const largeFileSize = 1 << 30 // 1 GB

// logFileOperation logs the file operation based on verbosity settings
func logFileOperation(shouldCopy bool, srcPath string, srcInfo os.FileInfo, verbose int, logger LoggerFunc) {
	if shouldCopy {
		if verbose >= 2 {
			logger("[%s] [VERBOSE] Copying file: %s (%.2f MB)\n", timestamp(), srcPath, float64(srcInfo.Size())/float64(1<<20))
		} else if verbose == 1 && srcInfo.Size() > largeFileSize {
			logger("[%s] [VERBOSE] Copying large file: %s (%.2f MB)\n", timestamp(), srcPath, float64(srcInfo.Size())/float64(1<<20))
		}
	} else {
		if verbose >= 2 {
			logger("[%s] [VERBOSE] Skipping file (cached): %s (%.2f MB)\n", timestamp(), srcPath, float64(srcInfo.Size())/float64(1<<20))
		} else if verbose == 1 && srcInfo.Size() > largeFileSize {
			logger("[%s] [VERBOSE] Skipping large file (cached): %s (%.2f MB)\n", timestamp(), srcPath, float64(srcInfo.Size())/float64(1<<20))
		}
	}
}
