package main

import (
	"flag"
	"fmt"
	"cache_copy/core"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/mem"
)

func timestamp() string {
	return time.Now().Format("2006-01-02 15:04:05.000")
}

type LoggerFunc func(format string, args ...interface{})
type ProgressFunc func(copiedBytes, totalBytes int64)
type FatalFunc func(format string, args ...interface{})

func runCopyWorkers(
	fileList []string,
	src, rootDst string,
	cache *core.GlobalCache,
	bufSize int,
	noCache bool,
	validate bool, // Add this parameter
	verbose int,
	workers int,
	totalBytes int64,
	logger LoggerFunc,
	progress ProgressFunc,
	fatal FatalFunc,
) {
	var copiedBytes int64
	var mu sync.Mutex
	var wg sync.WaitGroup
	fileChan := make(chan string, len(fileList))

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer func() {
				cache.SaveCache()
				wg.Done()
			}()
			buf := make([]byte, bufSize)
			lastProgressUpdate := time.Now()
			for relPath := range fileChan {
				srcPath := filepath.Join(src, relPath)
				dstPath := filepath.Join(rootDst, relPath)

				srcInfo, err := os.Stat(srcPath)
				if err != nil {
					logger("[%s] [ERROR] Failed to stat %s: %v\n", timestamp(), srcPath, err)
					continue
				}

				shouldCopy := true
				var hash uint64

				// Check cache to determine if file needs to be copied
				if !noCache && !validate {
					cache.RLock()
					entry, ok := cache.IsUpToDate(relPath)
					cache.RUnlock()
					if ok && entry.Size == srcInfo.Size() {
						hash, err = core.FileHash(srcPath)
						if err == nil && entry.Hash == hash {
							if dstInfo, err := os.Stat(dstPath); err == nil && dstInfo.Mode().IsRegular() {
								shouldCopy = false
							}
							if verbose >= 3 {
								logger("Checking file: %s\n", relPath)
								logger("Cache entry exists: %v\n", ok)
								if ok {
									logger("Cache size=%d, current size=%d\n", entry.Size, srcInfo.Size())
									logger("Cache hash=%d, current hash=%d\n", entry.Hash, hash)
								}
								_, statErr := os.Stat(dstPath)
								logger("Destination exists: %v\n", statErr == nil)
							}
						}
					}
				} else if validate {
					// VALIDATION MODE - Start validation logging
					if verbose >= 2 {
						logger("[%s] [VALIDATE] Starting validation for: %s\n", timestamp(), relPath)
					}

					// Check if destination file exists
					if dstInfo, err := os.Stat(dstPath); err == nil && dstInfo.Mode().IsRegular() {
						// Check size first (fast)
						if srcInfo.Size() == dstInfo.Size() {
							if verbose >= 3 {
								logger("[%s] [VALIDATE] Size match - calculating hashes for: %s\n", timestamp(), relPath)
							}

							// Calculate both hashes (slower)
							srcHash, srcErr := core.FileHash(srcPath)
							dstHash, dstErr := core.FileHash(dstPath)

							if srcErr == nil && dstErr == nil && srcHash == dstHash {
								shouldCopy = false
								if verbose >= 2 {
									logger("[%s] [VALIDATE] SUCCESS - File validated: %s (size: %d, hash: %d)\n", timestamp(), relPath, srcInfo.Size(), srcHash)
								} else if verbose == 1 {
									logger("[%s] [VALIDATE] SUCCESS - %s\n", timestamp(), relPath)
								}

								// Update cache after successful validation
								if !noCache {
									cache.Lock()
									cache.Update(relPath, srcInfo.Size(), srcHash, time.Now().Unix())
									cache.Unlock()
									if verbose >= 3 {
										logger("[%s] [CACHE] Updated after validation: %s\n", timestamp(), relPath)
									}
								}
							} else {
								// Hash mismatch
								if srcErr != nil {
									logger("[%s] [VALIDATE] ERROR - Cannot calculate source hash for %s: %v\n", timestamp(), relPath, srcErr)
								} else if dstErr != nil {
									logger("[%s] [VALIDATE] ERROR - Cannot calculate destination hash for %s: %v\n", timestamp(), relPath, dstErr)
								} else {
									logger("[%s] [VALIDATE] MISMATCH - Hash differs for %s\n", timestamp(), relPath)
									logger("[%s] [VALIDATE]   Source hash: %d\n", timestamp(), srcHash)
									logger("[%s] [VALIDATE]   Destination hash: %d\n", timestamp(), dstHash)
								}
							}
						} else {
							// Size mismatch
							logger("[%s] [VALIDATE] MISMATCH - Size differs for %s\n", timestamp(), relPath)
							logger("[%s] [VALIDATE]   Source size: %d bytes\n", timestamp(), srcInfo.Size())
							logger("[%s] [VALIDATE]   Destination size: %d bytes\n", timestamp(), dstInfo.Size())
						}
					} else {
						// Destination missing or not a regular file
						if verbose >= 2 {
							logger("[%s] [VALIDATE] MISMATCH - Destination file missing: %s\n", timestamp(), relPath)
						}
					}
				}

				// Print verbose output for file operations
				if shouldCopy {
					if verbose >= 2 {
						logger("[%s] [VERBOSE] Copying file: %s (%.2f MB)\n", timestamp(), srcPath, float64(srcInfo.Size())/float64(1<<20))
					} else if verbose == 1 && srcInfo.Size() > 1000*1024*1024 {
						logger("[%s] [VERBOSE] Copying large file: %s (%.2f MB)\n", timestamp(), srcPath, float64(srcInfo.Size())/float64(1<<20))
					}
				} else {
					if verbose >= 2 {
						logger("[%s] [VERBOSE] Skipping file (cached): %s (%.2f MB)\n", timestamp(), srcPath, float64(srcInfo.Size())/float64(1<<20))
					} else if verbose == 1 && srcInfo.Size() > 1000*1024*1024 {
						logger("[%s] [VERBOSE] Skipping large file (cached): %s (%.2f MB)\n", timestamp(), srcPath, float64(srcInfo.Size())/float64(1<<20))
					}
				}

				// Copy or skip the file, update progress
				if shouldCopy {
					if err := os.MkdirAll(filepath.Dir(dstPath), os.ModePerm); err != nil {
						cache.SaveCache()
						fatal("[%s] [ERROR] Failed to create directory %s: %v\n", timestamp(), filepath.Dir(dstPath), err)
						return
					}
					// Delete the destination file if it exists
					if _, err := os.Stat(dstPath); err == nil {
						if rmErr := os.Remove(dstPath); rmErr != nil {
							cache.SaveCache()
							fatal("[%s] [ERROR] Failed to remove old destination file %s: %v\n", timestamp(), dstPath, rmErr)
							return
						}
					}
					in, err := core.OpenWithRetry(srcPath, 5)
					if err != nil {
						cache.SaveCache()
						fatal("[%s] [ERROR] Failed to open source file %s: %v\n", timestamp(), srcPath, err)
						return
					}
					outFile, err := core.CreateWithRetry(dstPath, 5)
					if err != nil {
						in.Close()
						cache.SaveCache()
						fatal("[%s] [ERROR] Failed to create destination file %s: %v\n", timestamp(), dstPath, err)
						return
					}
					_, err = io.CopyBuffer(outFile, in, buf)

					retries := 3
					var closeErr error
					for i := 0; i < retries; i++ {
						closeErr = outFile.Sync()
						if closeErr == nil {
							break
						}
						if i == retries-1 {
							cache.SaveCache()
							fatal("[%s] [ERROR] Error syncing destination file %s: %v\n", timestamp(), dstPath, closeErr)
							return
						}
						time.Sleep(500 * time.Millisecond)
					}
					closeErr = outFile.Close()
					if closeErr != nil {
						cache.SaveCache()
						fatal("[%s] [ERROR] Error closing destination file %s: %v\n", timestamp(), dstPath, closeErr)
						return
					}
					closeErr = in.Close()
					if closeErr != nil {
						cache.SaveCache()
						fatal("[%s] [ERROR] Error closing source file %s: %v\n", timestamp(), srcPath, closeErr)
						return
					}

					if err != nil {
						cache.SaveCache()
						fatal("[%s] [ERROR] Failed to copy %s to %s: %v\n", timestamp(), srcPath, dstPath, err)
						return
					}
					// After successful copy, when updating cache:
					if !noCache {
						hash, _ = core.FileHash(srcPath)
						cache.Lock()
						_, existed := cache.IsUpToDate(relPath)
						cache.Update(relPath, srcInfo.Size(), hash, time.Now().Unix())
						cache.Unlock()

						if verbose >= 3 {
							if existed {
								logger("[%s] [CACHE] Updated cache entry: %s (size=%d, hash=%d)\n", timestamp(), relPath, srcInfo.Size(), hash)
							} else {
								logger("[%s] [CACHE] Added new cache entry: %s (size=%d, hash=%d)\n", timestamp(), relPath, srcInfo.Size(), hash)
							}
						}
						cache.SaveCache()
					}
				}

				mu.Lock()
				copiedBytes += srcInfo.Size()
				now := time.Now()
				if now.Sub(lastProgressUpdate) > 100*time.Millisecond || copiedBytes == totalBytes {
					lastProgressUpdate = now
					progress(copiedBytes, totalBytes)
				}
				mu.Unlock()
			}
		}()
	}

	for _, relPath := range fileList {
		fileChan <- relPath
	}
	close(fileChan)
	wg.Wait()
	cache.SaveCache()
}

func main() {
	// CAPTURE ORIGINAL COMMAND FIRST
	originalCommand := strings.Join(os.Args, " ")

	os.MkdirAll(".cache_cache_copy", os.ModePerm)

	// Step 1: Find src and dst in os.Args
	args := os.Args[1:]
	var src, dst string
	var flagArgs []string
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			if src == "" {
				src = arg
			} else if dst == "" {
				dst = arg
			} else {
				// Extra positional args, treat as flags
				flagArgs = append(flagArgs, arg)
			}
		} else {
			flagArgs = append(flagArgs, arg)
		}
	}

	// Step 2: Rebuild os.Args so flags are before src/dst for flag.Parse
	os.Args = append([]string{os.Args[0]}, flagArgs...)

	// Step 3: Define flags as usual
	workers := flag.Int("workers", runtime.GOMAXPROCS(0), "Number of concurrent workers for file copying")
	clearCache := flag.Bool("clear-cache", false, "Delete the cache.json file before starting copy")
	mirror := flag.Bool("mirror", false, "Mirror source to destination: delete extra files in the destination that are not in the source")
	noCache := flag.Bool("no-cache", false, "Disable cache: always copy all files")
	maxCacheAge := flag.Int("max-cache-age", 90, "Maximum age (in days) for cache entries (default 90)")
	verbose := flag.Int("verbose", 0, "Set verbosity level (0=quiet, 1=print large file operations >500MB, 2=print all file operations)")
	logPath := flag.String("log-path", "", "Path to log file (all stdout will also be written here)")
	bufferSizeStr := flag.String("buffer-size", "4MB", "Buffer size for file copy (e.g. 4MB, 256KB, 1048576)")
	noTUI := flag.Bool("no-tui", false, "Disable TUI and use classic terminal output (default: TUI enabled)")
	validate := flag.Bool("validate", false, "Validate files by comparing size and hash between source and destination (slower but 100% accurate)")
	autoClean := flag.Bool("auto-clean", true, "Automatically clean stale cache entries on every run (default: true)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `Usage: cache_copy [src] [dst] [options]

[src] and [dst] are required.

SRC PATH BEHAVIOR:
  - If [src] ends with a path separator (\ on Windows, / on Linux), the CONTENTS of the source directory are copied into [dst].
	Example: cache_copy myfolder\ dest\      (copies all files/folders inside myfolder\ into dest\)
  - If [src] does NOT end with a separator, the source directory itself is copied as a subdirectory of [dst].
	Example: cache_copy myfolder dest\       (creates dest\myfolder\ and copies everything inside myfolder into it)

This behavior applies the same way when using --mirror:
  - With --mirror, extra files/directories in the destination (as determined by the src path logic above) will be deleted to match the source.

VERBOSITY LEVELS:
  -verbose 0: Quiet mode (default) - only shows progress and errors
  -verbose 1: Shows large file operations (>1GB)
  -verbose 2: Shows all file operations (copying/skipping)
  -verbose 3: Shows detailed cache debugging information (sizes, hashes, destination checks)

FLAGS:
  -workers int
		Number of concurrent workers for file copying (default: number of CPU cores)
  
  -clear-cache
		Delete the cache file before starting copy (forces fresh copy of all files)
  
  -mirror
		Mirror source to destination: delete extra files in the destination that are not in the source
  
  -no-cache
		Disable cache: always copy all files (ignores existing cache, slower but always accurate)
  
  -max-cache-age int
		Maximum age (in days) for cache entries. Entries older than this are removed (default: 90)
  
  -verbose int
		Set verbosity level: 0=quiet, 1=large files >1GB, 2=all files, 3=cache debug (default: 0)
  
  -log-path string
		Path to log file (all stdout will also be written here, in addition to console output)
  
  -buffer-size string
		Buffer size for file copy operations (default: "4MB")
		Examples: 4MB, 256KB, 1048576, 8MB
  
  -no-tui
		Disable TUI and use classic terminal output (disables fancy progress display)
  
  -validate
		Validate files by comparing size and hash between source and destination
		(slower but 100%% accurate - ignores cache and checks actual file content)
  
  -auto-clean
		Automatically clean stale cache entries on every run (default: true)
		Use --auto-clean=false to disable automatic cache cleaning

EXAMPLES:
  cache_copy /source/folder /destination/folder
  cache_copy /source/folder/ /destination/folder --mirror
  cache_copy /source /dest --workers 8 --buffer-size 8MB --verbose 2
  cache_copy /source /dest --validate --no-cache --verbose 3
  cache_copy /source /dest --clear-cache --mirror --log-path copy.log
  cache_copy /source /dest --auto-clean=false --verbose 1

CACHE BEHAVIOR:
  - Cache files are stored in .cache_cache_copy/ directory
  - Each source/destination pair gets its own unique cache file
  - Cache entries track file size, hash, and modification time
  - Use --clear-cache to start fresh and delete the entire cache file
  - Use --validate to bypass cache and verify actual file content
  - Stale cache entries are automatically cleaned by default (disable with --auto-clean=false)

PERFORMANCE TIPS:
  - Increase --workers for many small files (default is usually good)
  - Increase --buffer-size for large files (4MB-8MB recommended)
  - Use --no-tui for scripting or when TUI causes issues
  - Use --validate only when you need 100%% verification (slower)

`)
	}
	flag.Parse()

	if src == "" || dst == "" {
		flag.Usage()
		return
	}

	// PRINT THE ORIGINAL COMMAND AS ENTERED
	fmt.Fprintf(os.Stderr, "[%s] [INFO] Command: %s\n", timestamp(), originalCommand)

	// After parsing src and dst:
	srcClean := filepath.Clean(src)
	copyContents := false

	// Detect OS-specific separator
	if (runtime.GOOS == "windows" && strings.HasSuffix(src, `\`)) ||
		(runtime.GOOS != "windows" && strings.HasSuffix(src, `/`)) {
		copyContents = true
	}

	// Determine rootDst and file list
	var rootDst string
	if copyContents {
		rootDst = dst
	} else {
		srcBase := filepath.Base(srcClean)
		rootDst = filepath.Join(dst, srcBase)
	}

	// Use rootDst as your destination root in the rest of your logic
	// When gathering fileList, use srcClean as the source root

	// Now use src and dst variables instead of flag.Arg(0), flag.Arg(1)
	cachePath := core.LocalCacheFile(src, rootDst)
	fmt.Fprintf(os.Stderr, "[%s] [INFO] Using cache file: %s\n", timestamp(), cachePath)

	// Optionally clear the cache file before starting
	if *clearCache {
		if err := os.Remove(cachePath); err == nil {
			fmt.Fprintf(os.Stderr, "[%s] [INFO] Cache deleted: %s\n", timestamp(), cachePath)
		} else if !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "[%s] [ERROR] Failed to delete cache: %v\n", timestamp(), err)
			return
		}
	}

	cache := core.NewGlobalCache(cachePath)

	// Conditionally clean stale cache entries based on --auto-clean flag
	if *autoClean {
		cache.Lock()
		staleCacheKeys := []string{}
		for _, relPath := range cache.Keys() {
			srcPath := filepath.Join(src, relPath)
			if _, err := os.Stat(srcPath); os.IsNotExist(err) {
				staleCacheKeys = append(staleCacheKeys, relPath)
			}
		}
		for _, staleKey := range staleCacheKeys {
			if *verbose >= 3 {
				fmt.Fprintf(os.Stderr, "[CACHE] Removing stale entry: %s\n", staleKey)
			}
			cache.Remove(staleKey)
		}
		cache.Unlock()

		if len(staleCacheKeys) > 0 {
			if *verbose >= 1 {
				fmt.Fprintf(os.Stderr, "[%s] [INFO] Auto-cleaned %d stale cache entries\n", timestamp(), len(staleCacheKeys))
			}
			cache.SaveCache()
		}
	}

	// Optionally mirror (delete extra files in destination)
	if *mirror {
		if err := os.MkdirAll(rootDst, os.ModePerm); err != nil {
			cache.SaveCache()
			fmt.Fprintf(os.Stderr, "[%s] [ERROR] Failed to create root destination directory %s: %v\n", timestamp(), rootDst, err)
			return
		}
		err := deleteExtraFiles(src, rootDst, cache)
		if err != nil {
			cache.SaveCache()
			fmt.Fprintf(os.Stderr, "[%s] [ERROR] Error deleting extra files: %v\n", timestamp(), err)
			return
		}
		cache.SaveCache()
	}

	// Remove old cache entries
	now := time.Now().Unix()
	maxAgeSeconds := int64(*maxCacheAge) * 24 * 60 * 60
	cache.Lock()
	for _, key := range cache.Keys() {
		entry, ok := cache.IsUpToDate(key)
		if ok && entry.ModTime > 0 && now-entry.ModTime > maxAgeSeconds {
			cache.Remove(key)
		}
	}
	cache.Unlock()
	cache.SaveCache()

	// If all cache entries are old, delete the cache file
	allOld := true
	cache.Lock()
	for _, key := range cache.Keys() {
		entry, ok := cache.IsUpToDate(key)
		if ok && entry.ModTime > 0 && now-entry.ModTime <= maxAgeSeconds {
			allOld = false
			break
		}
	}
	cache.Unlock()
	if allOld && len(cache.Keys()) > 0 {
		fmt.Fprintf(os.Stderr, "[%s] [INFO] All cache entries older than %d days, deleting cache file: %s\n", timestamp(), *maxCacheAge, cachePath)
		os.Remove(cachePath)
	}

	// Gather all directories and files (relative paths) from the source directory
	dirs := []string{}
	fileList := []string{}
	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relPath, _ := filepath.Rel(src, path)
		if info.IsDir() {
			dirs = append(dirs, relPath)
		} else {
			fileList = append(fileList, relPath)
		}
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "[%s] [ERROR] Error gathering file list: %v\n", timestamp(), err)
		cache.SaveCache()
		return
	}

	// Calculate total bytes to copy for progress bar
	var totalBytes int64
	for _, relPath := range fileList {
		srcPath := filepath.Join(src, relPath)
		info, err := os.Stat(srcPath)
		if err == nil {
			totalBytes += info.Size()
		}
	}

	// Ensure all destination directories exist
	for _, relDir := range dirs {
		dstDir := filepath.Join(rootDst, relDir)
		if err := os.MkdirAll(dstDir, os.ModePerm); err != nil {
			fmt.Fprintf(os.Stderr, "[%s] [ERROR] Failed to create directory %s: %v\n", timestamp(), dstDir, err)
		}
	}

	// --- Classic terminal mode (no TUI) ---
	if *noTUI {
		var out io.Writer = os.Stdout
		if *logPath != "" {
			logFile, err := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
			if err != nil {
				fmt.Fprintf(out, "[%s] [ERROR] Failed to open log file %s: %v\n", timestamp(), *logPath, err)
			} else {
				out = io.MultiWriter(out, logFile)
			}
		}

		bufSize, err := core.ParseSize(*bufferSizeStr)
		if err != nil {
			fmt.Fprintf(out, "[%s] [ERROR] Invalid buffer size: %v\n", timestamp(), err)
			cache.SaveCache()
			return
		}

		totalBuffer := int64(*workers) * int64(bufSize)
		if totalBuffer > 2*1024*1024*1024 {
			fmt.Fprintf(out, "[%s] [WARN] Total buffer allocation is %.2f GB (%d workers × %s)\n",
				timestamp(), float64(totalBuffer)/(1024*1024*1024), *workers, core.HumanSize(bufSize))
			fmt.Fprintf(out, "[%s] [WARN] Consider reducing --workers or --buffer-size to avoid running out of memory.\n", timestamp())
		}

		fmt.Fprintf(out, "[%s] [INFO] Using %d worker(s)\n", timestamp(), *workers)
		fmt.Fprintf(out, "[%s] [INFO] Using buffer size: %s (%d bytes)\n", timestamp(), core.HumanSize(bufSize), bufSize)

		if *validate {
			fmt.Fprintf(out, "[%s] [VALIDATE] Validation mode enabled - all files will be verified\n", timestamp())
			if *verbose >= 1 {
				fmt.Fprintf(out, "[%s] [VALIDATE] This will compare file sizes and content hashes\n", timestamp())
			}
		}

		var copiedBytes int64
		startTime := time.Now()
		done := make(chan struct{})

		progress := func(copied, total int64) {
			copiedBytes = copied
		}

		logger := func(format string, args ...interface{}) {
			fmt.Print("\n")
			fmt.Fprintf(out, format, args...)
			if len(format) == 0 || format[len(format)-1] != '\n' {
				fmt.Println()
			}
		}

		fatal := func(format string, args ...interface{}) {
			cache.SaveCache()
			fmt.Fprintf(out, format, args...)
			os.Exit(1)
		}

		go func() {
			for {
				select {
				case <-done:
					return
				default:
					elapsed := time.Since(startTime).Round(time.Second)
					fmt.Printf("\rTotal: %.2f MB / %.2f MB %s | %s",
						float64(copiedBytes)/(1024*1024), float64(totalBytes)/(1024*1024),
						core.RenderProgressBar(copiedBytes, totalBytes, 40),
						elapsed)
					time.Sleep(500 * time.Millisecond)
				}
			}
		}()

		runCopyWorkers(fileList, src, rootDst, cache, bufSize, *noCache, *validate, *verbose, *workers, totalBytes, logger, progress, fatal)
		close(done)
		fmt.Println() // Move to a new line after the last progress bar

		// ADD THIS VALIDATION COMPLETION MESSAGE FOR NO-TUI MODE:
		if *validate {
			fmt.Fprintf(out, "[%s] [VALIDATE] Validation completed successfully for all files\n", timestamp())
		}

		fmt.Fprintf(out, "[%s] [INFO] Copy process completed.\n", timestamp())
		cache.SaveCache()
		return
	}

	// --- TUI Mode ---
	app := tview.NewApplication()
	logView := tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true).
		SetWrap(true).
		SetChangedFunc(func() {
			app.Draw()
		})

	progressView := tview.NewTextView().SetDynamicColors(true)
	monitorView := tview.NewTextView().SetDynamicColors(true)

	flex := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(monitorView, 1, 0, false).
		AddItem(logView, 0, 1, false).
		AddItem(progressView, 1, 0, false)

	// Always start monitor goroutine in TUI mode
	go func() {
		for {
			percent, _ := cpu.Percent(0, false)
			vmem, _ := mem.VirtualMemory()
			app.QueueUpdateDraw(func() {
				monitorView.Clear()
				fmt.Fprintf(monitorView, "[MONITOR] CPU: %.2f%% | Mem: %.2f%% (%.2f GB/%.2f GB)\n",
					percent[0],
					vmem.UsedPercent,
					float64(vmem.Used)/(1024*1024*1024),
					float64(vmem.Total)/(1024*1024*1024))
			})
			time.Sleep(1 * time.Second)
		}
	}()

	bufSize, err := core.ParseSize(*bufferSizeStr)
	if err != nil {
		fmt.Fprintf(logView, "[%s] [ERROR] Invalid buffer size: %v\n", timestamp(), err)
		cache.SaveCache()
		app.Stop()
		return
	}

	totalBuffer := int64(*workers) * int64(bufSize)
	if totalBuffer > 2*1024*1024*1024 {
		fmt.Fprintf(logView, "[%s] [WARN] Total buffer allocation is %.2f GB (%d workers × %s)\n",
			timestamp(), float64(totalBuffer)/(1024*1024*1024), *workers, core.HumanSize(bufSize))
		fmt.Fprintf(logView, "[%s] [WARN] Consider reducing --workers or --buffer-size to avoid running out of memory.\n", timestamp())
	}

	var out io.Writer = logView
	if *logPath != "" {
		logFile, err := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			fmt.Fprintf(logView, "[%s] [ERROR] Failed to open log file %s: %v\n", timestamp(), *logPath, err)
		} else {
			out = io.MultiWriter(logView, logFile)
		}
	}

	fmt.Fprintf(out, "[%s] [INFO] Using %d worker(s)\n", timestamp(), *workers)
	fmt.Fprintf(out, "[%s] [INFO] Using buffer size: %s (%d bytes)\n", timestamp(), core.HumanSize(bufSize), bufSize)
	fmt.Fprintf(out, "[%s] [INFO] Command: %s\n", timestamp(), originalCommand)

	if *validate {
		fmt.Fprintf(out, "[%s] [VALIDATE] Validation mode enabled - all files will be verified\n", timestamp())
		if *verbose >= 1 {
			fmt.Fprintf(out, "[%s] [VALIDATE] This will compare file sizes and content hashes\n", timestamp())
		}
	}

	// --- NEW: Use a shared variable for copied bytes and a goroutine for progress bar ---
	var copiedBytes int64
	startTime := time.Now()
	done := make(chan struct{})

	// Progress callback: only update copiedBytes
	progress := func(copied, total int64) {
		copiedBytes = copied
	}

	// Progress bar updater goroutine: updates UI every 0.5s
	go func() {
		for {
			select {
			case <-done:
				return
			default:
				elapsed := time.Since(startTime).Round(time.Second)
				app.QueueUpdateDraw(func() {
					progressView.Clear()
					fmt.Fprintf(progressView, "Total: %.2f MB / %.2f MB %s | %s",
						float64(copiedBytes)/(1024*1024), float64(totalBytes)/(1024*1024),
						core.RenderProgressBar(copiedBytes, totalBytes, 40),
						elapsed)
					if copiedBytes == totalBytes {
						fmt.Fprintln(progressView)
					}
				})
				time.Sleep(1 * time.Second)
			}
		}
	}()

	logger := func(format string, args ...interface{}) {
		app.QueueUpdateDraw(func() {
			fmt.Fprintf(out, format, args...)
			logView.ScrollToEnd() // Always scroll to bottom
		})
	}

	fatal := func(format string, args ...interface{}) {
		cache.SaveCache()
		app.QueueUpdateDraw(func() {
			fmt.Fprintf(logView, format, args...)
			logView.ScrollToEnd() // Always scroll to bottom
			app.Stop()
		})
	}

	go func() {
		runCopyWorkers(fileList, src, rootDst, cache, bufSize, *noCache, *validate, *verbose, *workers, totalBytes, logger, progress, fatal)
		close(done)
		cache.SaveCache()
		app.QueueUpdateDraw(func() {
			if *validate {
				fmt.Fprintf(out, "[%s] [VALIDATE] Validation completed successfully for all files\n", timestamp())
			}
			fmt.Fprintf(logView, "[%s] [INFO] Copy process completed. Press any key to exit.\n", timestamp())
			logView.ScrollToEnd() // Scroll to bottom for final messages
			app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
				app.Stop()
				return nil
			})
		})
	}()

	if err := app.SetRoot(flex, true).EnableMouse(true).Run(); err != nil {
		cache.SaveCache()
		panic(err)
	}
}

// deleteExtraFiles removes files and directories from the destination that do not exist in the source.
// It also removes corresponding entries from the cache.
func deleteExtraFiles(srcDir, dstDir string, cache *core.GlobalCache) error {
	var deletedDirs = make(map[string]bool)
	err := filepath.Walk(dstDir, func(dstPath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !core.Exists(dstPath) {
			return nil
		}
		relPath, _ := filepath.Rel(dstDir, dstPath)
		srcPath := filepath.Join(srcDir, relPath)
		_, err = os.Stat(srcPath)
		if os.IsNotExist(err) {
			if info.IsDir() {
				fmt.Printf("[%s] [INFO] Marking directory for deletion: %s\n", timestamp(), dstPath)
				deletedDirs[dstPath] = true
			} else {
				fmt.Printf("[%s] [INFO] Deleting extra file: %s\n", timestamp(), dstPath)
				err := os.Remove(dstPath)
				if err != nil {
					return fmt.Errorf("[%s] [ERROR] failed to delete file %s: %v", timestamp(), dstPath, err)
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for dir := range deletedDirs {
		err := os.RemoveAll(dir)
		if err != nil {
			return fmt.Errorf("[%s] [ERROR] failed to delete directory %s: %v", timestamp(), dir, err)
		}
		cache.Lock()
		cache.Remove(dir)
		cache.Unlock()
		cache.SaveCache()
	}
	return nil
}
