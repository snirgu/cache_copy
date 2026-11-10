package main

import (
	"cache_copy/core"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/mem"
)

// Application version, injected at build time from the VERSION file
// (make build passes -ldflags "-X main.VERSION=..."); "dev" means an unversioned build.
var VERSION = "dev"

// timestamp returns the current time formatted as a string for logging purposes
// Format: "2006-01-02 15:04:05.000" (YYYY-MM-DD HH:MM:SS.mmm)
func timestamp() string {
	return time.Now().Format("2006-01-02 15:04:05.000")
}

// LoggerFunc defines a function type for logging messages with printf-style formatting
type LoggerFunc func(format string, args ...interface{})

// ProgressFunc defines a function type for reporting copy progress
// Parameters: copiedBytes (amount copied so far), totalBytes (total amount to copy)
type ProgressFunc func(copiedBytes, totalBytes int64)

// FatalFunc defines a function type for reporting fatal errors and terminating execution
type FatalFunc func(format string, args ...interface{})

// Config holds all configuration flags and parsed arguments
type Config struct {
	Src          string
	Dst          string
	RootDst      string
	CopyContents bool
	Workers      int
	ClearCache   bool
	Mirror       bool
	NoCache      bool
	MaxCacheAge  int
	Verbose      int
	LogPath      string
	BufferSize   int
	NoTUI        bool
	Validate     bool
	AutoClean    bool
	OriginalCmd  string
}

// parseArguments parses command line arguments and returns configuration
func parseArguments() *Config {
	originalCommand := strings.Join(os.Args, " ")

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
				flagArgs = append(flagArgs, arg)
			}
		} else {
			flagArgs = append(flagArgs, arg)
		}
	}

	// Step 2: Rebuild os.Args so flags are before src/dst for flag.Parse
	os.Args = append([]string{os.Args[0]}, flagArgs...)

	// Step 3: Define flags
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

	flag.Usage = getUsageFunc()
	flag.Parse()

	if src == "" || dst == "" {
		flag.Usage()
		os.Exit(1)
	}

	// Parse buffer size
	bufSize, err := core.ParseSize(*bufferSizeStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[%s] [ERROR] Invalid buffer size: %v\n", timestamp(), err)
		os.Exit(1)
	}

	// Determine copy behavior and destination
	srcClean := filepath.Clean(src)
	copyContents := false
	if (runtime.GOOS == "windows" && strings.HasSuffix(src, `\`)) ||
		(runtime.GOOS != "windows" && strings.HasSuffix(src, `/`)) {
		copyContents = true
	}

	var rootDst string
	if copyContents {
		rootDst = dst
	} else {
		srcBase := filepath.Base(srcClean)
		rootDst = filepath.Join(dst, srcBase)
	}

	return &Config{
		Src:          src,
		Dst:          dst,
		RootDst:      rootDst,
		CopyContents: copyContents,
		Workers:      *workers,
		ClearCache:   *clearCache,
		Mirror:       *mirror,
		NoCache:      *noCache,
		MaxCacheAge:  *maxCacheAge,
		Verbose:      *verbose,
		LogPath:      *logPath,
		BufferSize:   bufSize,
		NoTUI:        *noTUI,
		Validate:     *validate,
		AutoClean:    *autoClean,
		OriginalCmd:  originalCommand,
	}
}

// initializeCache creates and initializes the cache
func initializeCache(config *Config) *core.GlobalCache {
	os.MkdirAll(".cache_cache_copy", os.ModePerm)

	cachePath := core.LocalCacheFile(config.Src, config.RootDst)
	fmt.Printf("[%s] [INFO] Using cache file: %s\n", timestamp(), cachePath)

	if config.ClearCache {
		if err := os.Remove(cachePath); err == nil {
			fmt.Printf("[%s] [INFO] Cache deleted: %s\n", timestamp(), cachePath)
		} else if !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "[%s] [ERROR] Failed to delete cache: %v\n", timestamp(), err)
			os.Exit(1)
		}
	}

	return core.NewGlobalCache(cachePath)
}

// cleanStaleCache removes stale cache entries if auto-clean is enabled
func cleanStaleCache(cache *core.GlobalCache, config *Config) {
	if !config.AutoClean {
		return
	}

	cache.Lock()
	staleCacheKeys := []string{}
	for _, relPath := range cache.Keys() {
		srcPath := filepath.Join(config.Src, relPath)
		if _, err := os.Stat(srcPath); os.IsNotExist(err) {
			staleCacheKeys = append(staleCacheKeys, relPath)
		}
	}
	for _, staleKey := range staleCacheKeys {
		if config.Verbose >= 3 {
			fmt.Fprintf(os.Stderr, "[CACHE] Removing stale entry: %s\n", staleKey)
		}
		cache.Remove(staleKey)
	}
	cache.Unlock()

	if len(staleCacheKeys) > 0 {
		if config.Verbose >= 1 {
			fmt.Printf("[%s] [INFO] Auto-cleaned %d stale cache entries\n", timestamp(), len(staleCacheKeys))
		}
		cache.SaveCache()
	}
}

// handleMirrorMode deletes extra files in destination if mirror mode is enabled
func handleMirrorMode(cache *core.GlobalCache, config *Config) {
	if !config.Mirror {
		return
	}

	if err := os.MkdirAll(config.RootDst, os.ModePerm); err != nil {
		cache.SaveCache()
		fmt.Printf("[%s] [ERROR] Failed to create root destination directory %s: %v\n", timestamp(), config.RootDst, err)
		os.Exit(1)
	}

	err := deleteExtraFiles(config.Src, config.RootDst, cache)
	if err != nil {
		cache.SaveCache()
		fmt.Printf("[%s] [ERROR] Error deleting extra files: %v\n", timestamp(), err)
		os.Exit(1)
	}
	cache.SaveCache()
}

// cleanOldCacheEntries removes cache entries older than maxCacheAge
func cleanOldCacheEntries(cache *core.GlobalCache, config *Config) {
	now := time.Now().Unix()
	maxAgeSeconds := int64(config.MaxCacheAge) * 24 * 60 * 60

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
		cachePath := core.LocalCacheFile(config.Src, config.RootDst)
		fmt.Printf("[%s] [INFO] All cache entries older than %d days, deleting cache file: %s\n", timestamp(), config.MaxCacheAge, cachePath)
		os.Remove(cachePath)
	}
}

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

// validateBufferAllocation checks if total buffer allocation is reasonable
func validateBufferAllocation(config *Config) {
	totalBuffer := int64(config.Workers) * int64(config.BufferSize)
	if totalBuffer > 2*1024*1024*1024 {
		fmt.Printf("[%s] [WARN] Total buffer allocation is %.2f GB (%d workers × %s)\n",
			timestamp(), float64(totalBuffer)/(1024*1024*1024), config.Workers, core.HumanSize(config.BufferSize))
		fmt.Printf("[%s] [WARN] Consider reducing --workers or --buffer-size to avoid running out of memory.\n", timestamp())
	}
}

// runNoTUIMode runs the copy operation in classic terminal mode
func runNoTUIMode(cache *core.GlobalCache, config *Config, fileList []string, totalBytes int64) {
	var out io.Writer = os.Stdout
	if config.LogPath != "" {
		logFile, err := os.OpenFile(config.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			fmt.Fprintf(out, "[%s] [ERROR] Failed to open log file %s: %v\n", timestamp(), config.LogPath, err)
			cache.SaveCache()
			os.Exit(1)
		} else {
			out = io.MultiWriter(out, logFile)
		}
	}

	validateBufferAllocation(config)

	fmt.Fprintf(out, "[%s] [INFO] Using %d worker(s)\n", timestamp(), config.Workers)
	fmt.Fprintf(out, "[%s] [INFO] Using buffer size: %s (%d bytes)\n", timestamp(), core.HumanSize(config.BufferSize), config.BufferSize)

	if config.Validate {
		fmt.Fprintf(out, "[%s] [VALIDATE] Validation mode enabled - all files will be verified\n", timestamp())
		if config.Verbose >= 1 {
			fmt.Fprintf(out, "[%s] [VALIDATE] This will compare file sizes and content hashes\n", timestamp())
		}
	}

	var copiedBytes int64
	startTime := time.Now()
	done := make(chan struct{})

	progress := func(copied, total int64) {
		atomic.StoreInt64(&copiedBytes, copied)
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
				currentCopied := atomic.LoadInt64(&copiedBytes)
				fmt.Printf("\rTotal: %.2f MB / %.2f MB %s | %s",
					float64(currentCopied)/(1024*1024), float64(totalBytes)/(1024*1024),
					core.RenderProgressBar(currentCopied, totalBytes, 40),
					elapsed)
				time.Sleep(500 * time.Millisecond)
			}
		}
	}()

	runCopyWorkers(fileList, config.Src, config.RootDst, cache, config.BufferSize, config.NoCache, config.Validate, config.Verbose, config.Workers, totalBytes, logger, progress, fatal)
	close(done)
	fmt.Println()

	if config.Validate {
		fmt.Fprintf(out, "[%s] [VALIDATE] Validation completed successfully for all files\n", timestamp())
	}

	fmt.Fprintf(out, "[%s] [INFO] Copy process completed.\n", timestamp())
}

// runTUIMode runs the copy operation with TUI interface
func runTUIMode(cache *core.GlobalCache, config *Config, fileList []string, totalBytes int64) {
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

	// System monitor goroutine
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

	validateBufferAllocation(config)

	var out io.Writer = logView
	if config.LogPath != "" {
		logFile, err := os.OpenFile(config.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			fmt.Fprintf(logView, "[%s] [ERROR] Failed to open log file %s: %v\n", timestamp(), config.LogPath, err)
			cache.SaveCache()
			app.Stop()
			os.Exit(1)
		} else {
			out = io.MultiWriter(logView, logFile)
		}
	}

	fmt.Fprintf(out, "[%s] [INFO] Using %d worker(s)\n", timestamp(), config.Workers)
	fmt.Fprintf(out, "[%s] [INFO] Using buffer size: %s (%d bytes)\n", timestamp(), core.HumanSize(config.BufferSize), config.BufferSize)
	fmt.Fprintf(out, "[%s] [INFO] Command: %s\n", timestamp(), config.OriginalCmd)

	if config.Validate {
		fmt.Fprintf(out, "[%s] [VALIDATE] Validation mode enabled - all files will be verified\n", timestamp())
		if config.Verbose >= 1 {
			fmt.Fprintf(out, "[%s] [VALIDATE] This will compare file sizes and content hashes\n", timestamp())
		}
	}

	var copiedBytes int64
	startTime := time.Now()
	done := make(chan struct{})

	progress := func(copied, total int64) {
		atomic.StoreInt64(&copiedBytes, copied)
	}

	// Progress bar updater goroutine
	go func() {
		for {
			select {
			case <-done:
				return
			default:
				elapsed := time.Since(startTime).Round(time.Second)
				app.QueueUpdateDraw(func() {
					progressView.Clear()
					currentCopied := atomic.LoadInt64(&copiedBytes)
					fmt.Fprintf(progressView, "Total: %.2f MB / %.2f MB %s | %s",
						float64(currentCopied)/(1024*1024), float64(totalBytes)/(1024*1024),
						core.RenderProgressBar(currentCopied, totalBytes, 40),
						elapsed)
					if currentCopied == totalBytes {
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
			logView.ScrollToEnd()
		})
	}

	fatal := func(format string, args ...interface{}) {
		cache.SaveCache()
		app.QueueUpdateDraw(func() {
			fmt.Fprintf(logView, format, args...)
			logView.ScrollToEnd()
			app.Stop()
		})
	}

	go func() {
		runCopyWorkers(fileList, config.Src, config.RootDst, cache, config.BufferSize, config.NoCache, config.Validate, config.Verbose, config.Workers, totalBytes, logger, progress, fatal)
		close(done)
		cache.SaveCache()
		app.QueueUpdateDraw(func() {
			if config.Validate {
				fmt.Fprintf(out, "[%s] [VALIDATE] Validation completed successfully for all files\n", timestamp())
			}
			fmt.Fprintf(logView, "[%s] [INFO] Copy process completed. Press any key to exit.\n", timestamp())
			logView.ScrollToEnd()
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

// main function - now much cleaner and more readable
func main() {
	// Parse arguments and configuration
	config := parseArguments()
	fmt.Printf("[%s] [INFO] Command: %s\n", timestamp(), config.OriginalCmd)

	// Initialize cache
	cache := initializeCache(config)

	// Clean stale cache entries
	cleanStaleCache(cache, config)

	// Handle mirror mode
	handleMirrorMode(cache, config)

	// Clean old cache entries
	cleanOldCacheEntries(cache, config)

	// Gather file list and calculate total bytes
	dirs, fileList, totalBytes, err := gatherFileList(config)
	if err != nil {
		fmt.Printf("[%s] [ERROR] Error gathering file list: %v\n", timestamp(), err)
		cache.SaveCache()
		os.Exit(1)
	}

	// Create destination directories
	if err := createDestinationDirectories(dirs, config); err != nil {
		fmt.Printf("[%s] [ERROR] %v\n", timestamp(), err)
		cache.SaveCache()
		os.Exit(1)
	}

	// Run copy operation in appropriate mode
	if config.NoTUI {
		runNoTUIMode(cache, config, fileList, totalBytes)
	} else {
		runTUIMode(cache, config, fileList, totalBytes)
	}

	cache.SaveCache()
}

// runCopyWorkers orchestrates the file copying process using multiple concurrent workers
func runCopyWorkers(
	fileList []string,
	src, rootDst string,
	cache *core.GlobalCache,
	bufSize int,
	noCache bool,
	validate bool,
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
	var copiedFiles []string
	var copiedFilesMutex sync.Mutex
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
				processSingleFile(relPath, src, rootDst, cache, buf, noCache, validate, verbose,
					&copiedBytes, &mu, &copiedFiles, &copiedFilesMutex, &lastProgressUpdate,
					totalBytes, logger, progress, fatal)
			}
		}()
	}

	for _, relPath := range fileList {
		fileChan <- relPath
	}
	close(fileChan)
	wg.Wait()
	cache.SaveCache()

	// Post-copy validation if validate flag is enabled
	if validate && len(copiedFiles) > 0 {
		runPostCopyValidation(copiedFiles, src, rootDst, verbose, logger, fatal)
	}
}

// processSingleFile handles the copying logic for a single file
func processSingleFile(relPath, src, rootDst string, cache *core.GlobalCache, buf []byte,
	noCache, validate bool, verbose int, copiedBytes *int64, mu *sync.Mutex,
	copiedFiles *[]string, copiedFilesMutex *sync.Mutex, lastProgressUpdate *time.Time,
	totalBytes int64, logger LoggerFunc, progress ProgressFunc, fatal FatalFunc) {

	srcPath := filepath.Join(src, relPath)
	dstPath := filepath.Join(rootDst, relPath)

	srcInfo, err := os.Stat(srcPath)
	if err != nil {
		logger("[%s] [ERROR] Failed to stat %s: %v\n", timestamp(), srcPath, err)
		return
	}

	shouldCopy := determineShouldCopy(relPath, srcPath, dstPath, srcInfo, cache, noCache, validate, verbose, logger)

	logFileOperation(shouldCopy, srcPath, srcInfo, verbose, logger)

	if shouldCopy {
		performFileCopy(relPath, srcPath, dstPath, srcInfo, cache, buf, noCache, validate, verbose, logger, fatal)

		if validate {
			copiedFilesMutex.Lock()
			*copiedFiles = append(*copiedFiles, relPath)
			copiedFilesMutex.Unlock()
		}
	}

	updateProgress(srcInfo, copiedBytes, mu, lastProgressUpdate, totalBytes, progress)
}

// determineShouldCopy decides whether a file should be copied based on cache and validation settings
func determineShouldCopy(relPath, srcPath, dstPath string, srcInfo os.FileInfo, cache *core.GlobalCache,
	noCache, validate bool, verbose int, logger LoggerFunc) bool {

	if noCache {
		if verbose >= 3 {
			logger("[%s] [NO-CACHE] Forcing copy (cache disabled): %s\n", timestamp(), relPath)
		}
		return true
	}

	if !validate {
		return checkCacheForCopy(relPath, srcPath, dstPath, srcInfo, cache, verbose, logger)
	}

	return checkValidationForCopy(relPath, srcPath, dstPath, srcInfo, cache, noCache, verbose, logger)
}

// checkCacheForCopy implements normal cache checking logic
func checkCacheForCopy(relPath, srcPath, dstPath string, srcInfo os.FileInfo, cache *core.GlobalCache, verbose int, logger LoggerFunc) bool {
	cache.RLock()
	entry, ok := cache.IsUpToDate(relPath)
	cache.RUnlock()

	if ok && entry.Size == srcInfo.Size() {
		hash, err := core.FileHash(srcPath)
		if err == nil && entry.Hash == hash {
			if dstInfo, err := os.Stat(dstPath); err == nil && dstInfo.Mode().IsRegular() {
				if verbose >= 3 {
					logger("Checking file: %s\n", relPath)
					logger("Cache entry exists: %v\n", ok)
					logger("Cache size=%d, current size=%d\n", entry.Size, srcInfo.Size())
					logger("Cache hash=%d, current hash=%d\n", entry.Hash, hash)
					logger("Destination exists: %v\n", true)
				}
				return false
			}
		}
	}
	return true
}

// checkValidationForCopy implements validation mode logic
func checkValidationForCopy(relPath, srcPath, dstPath string, srcInfo os.FileInfo, cache *core.GlobalCache, noCache bool, verbose int, logger LoggerFunc) bool {
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
	dstHash, dstErr := core.FileHash(dstPath)

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

	if !noCache {
		cache.Lock()
		cache.Update(relPath, srcInfo.Size(), srcHash, time.Now().Unix())
		cache.Unlock()
		if verbose >= 3 {
			logger("[%s] [CACHE] Updated after validation: %s\n", timestamp(), relPath)
		}
	}

	return false
}

// logFileOperation logs the file operation based on verbosity settings
func logFileOperation(shouldCopy bool, srcPath string, srcInfo os.FileInfo, verbose int, logger LoggerFunc) {
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
}

// performFileCopy handles the actual file copying with retry logic
func performFileCopy(relPath, srcPath, dstPath string, srcInfo os.FileInfo, cache *core.GlobalCache,
	buf []byte, noCache bool, validate bool, verbose int, logger LoggerFunc, fatal FatalFunc) {

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

	// Enhanced copy operation with retry logic
	retryCount := core.GetRetryCount(srcPath, dstPath)
	var copyErr error

	for attempt := 0; attempt < retryCount; attempt++ {
		if attempt > 0 {
			if attempt%2 == 0 && verbose >= 2 {
				logger("[%s] [RETRY] Copy attempt %d/%d for %s\n", timestamp(), attempt+1, retryCount, relPath)
			}
			time.Sleep(time.Duration(100+attempt*50) * time.Millisecond)
		}

		copyErr = performSingleCopyAttempt(srcPath, dstPath, srcInfo, buf, verbose, logger, relPath, attempt)
		if copyErr == nil {
			break
		}

		// Clean up partial file on error
		if _, statErr := os.Stat(dstPath); statErr == nil {
			os.Remove(dstPath)
		}
	}

	if copyErr != nil {
		cache.SaveCache()
		fatal("[%s] [ERROR] Failed to copy %s to %s after %d attempts: %v\n", timestamp(), srcPath, dstPath, retryCount, copyErr)
		return
	}

	// Update cache after successful copy
	updateCacheAfterCopy(relPath, srcPath, srcInfo, cache, noCache, verbose, logger)
}

// performSingleCopyAttempt performs a single copy attempt with proper error handling
func performSingleCopyAttempt(srcPath, dstPath string, srcInfo os.FileInfo, buf []byte,
	verbose int, logger LoggerFunc, relPath string, attempt int) error {

	in, err := core.OpenWithRetry(srcPath, 2)
	if err != nil {
		return err
	}
	defer in.Close()

	outFile, err := core.CreateWithRetry(dstPath, 2)
	if err != nil {
		return err
	}
	defer outFile.Close()

	n, err := io.CopyBuffer(outFile, in, buf)
	if err != nil {
		return err
	}

	if n != srcInfo.Size() {
		return fmt.Errorf("incomplete copy: copied %d bytes, expected %d bytes", n, srcInfo.Size())
	}

	// Enhanced sync with retry for bad file descriptor
	return performSyncWithRetry(outFile, verbose, logger, relPath, attempt)
}

// performSyncWithRetry handles file sync with retry logic for bad file descriptors
func performSyncWithRetry(outFile *os.File, verbose int, logger LoggerFunc, relPath string, attempt int) error {
	syncRetries := 3
	for syncAttempt := 0; syncAttempt < syncRetries; syncAttempt++ {
		err := outFile.Sync()
		if err == nil {
			return nil
		}

		if strings.Contains(err.Error(), "bad file descriptor") ||
			strings.Contains(err.Error(), "invalid handle") {
			if syncAttempt < syncRetries-1 {
				if attempt == 0 && verbose >= 2 {
					logger("[%s] [RETRY] Sync bad file descriptor, retrying: %s\n", timestamp(), relPath)
				}
				time.Sleep(time.Duration(200+syncAttempt*100) * time.Millisecond)
				continue
			}
		}
		return err
	}
	return nil
}

// updateCacheAfterCopy updates the cache after a successful copy operation
func updateCacheAfterCopy(relPath, srcPath string, srcInfo os.FileInfo, cache *core.GlobalCache,
	noCache bool, verbose int, logger LoggerFunc) {

	if noCache {
		return
	}

	hash, _ := core.FileHash(srcPath)
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

// updateProgress updates the progress tracking
func updateProgress(srcInfo os.FileInfo, copiedBytes *int64, mu *sync.Mutex,
	lastProgressUpdate *time.Time, totalBytes int64, progress ProgressFunc) {

	mu.Lock()
	*copiedBytes += srcInfo.Size()
	now := time.Now()
	if now.Sub(*lastProgressUpdate) > 100*time.Millisecond || *copiedBytes == totalBytes {
		*lastProgressUpdate = now
		progress(*copiedBytes, totalBytes)
	}
	mu.Unlock()
}

// runPostCopyValidation performs validation after all files have been copied
func runPostCopyValidation(copiedFiles []string, src, rootDst string, verbose int, logger LoggerFunc, fatal FatalFunc) {
	logger("[%s] [INFO] Copy phase completed. Starting validation phase for %d files...\n", timestamp(), len(copiedFiles))

	var validationErrors int
	for i, relPath := range copiedFiles {
		if i%10 == 0 || i == len(copiedFiles)-1 {
			logger("[%s] [VALIDATE] Progress: %d/%d files validated\n", timestamp(), i+1, len(copiedFiles))
		}

		srcPath := filepath.Join(src, relPath)
		dstPath := filepath.Join(rootDst, relPath)

		if !validateSingleFile(srcPath, dstPath, relPath, verbose, logger) {
			validationErrors++
		}
	}

	if validationErrors > 0 {
		fatal("[%s] [VALIDATE] Validation failed! %d files had validation errors\n", timestamp(), validationErrors)
	} else {
		logger("[%s] [VALIDATE] All %d files validated successfully\n", timestamp(), len(copiedFiles))
	}
}

// validateSingleFile validates a single file during post-copy validation
func validateSingleFile(srcPath, dstPath, relPath string, verbose int, logger LoggerFunc) bool {
	srcInfo, err := os.Stat(srcPath)
	if err != nil {
		logger("[%s] [VALIDATE] ERROR - Cannot stat source file %s: %v\n", timestamp(), relPath, err)
		return false
	}

	dstInfo, err := os.Stat(dstPath)
	if err != nil {
		logger("[%s] [VALIDATE] ERROR - Cannot stat destination file %s: %v\n", timestamp(), relPath, err)
		return false
	}

	if srcInfo.Size() != dstInfo.Size() {
		logger("[%s] [VALIDATE] MISMATCH - Size differs for %s\n", timestamp(), relPath)
		logger("[%s] [VALIDATE]   Source size: %d bytes\n", timestamp(), srcInfo.Size())
		logger("[%s] [VALIDATE]   Destination size: %d bytes\n", timestamp(), dstInfo.Size())
		return false
	}

	srcHash, err := core.FileHash(srcPath)
	if err != nil {
		logger("[%s] [VALIDATE] ERROR - Cannot calculate source hash for %s: %v\n", timestamp(), relPath, err)
		return false
	}

	dstHash, err := core.FileHash(dstPath)
	if err != nil {
		logger("[%s] [VALIDATE] ERROR - Cannot calculate destination hash for %s: %v\n", timestamp(), relPath, err)
		return false
	}

	if srcHash != dstHash {
		logger("[%s] [VALIDATE] MISMATCH - Hash differs for %s\n", timestamp(), relPath)
		logger("[%s] [VALIDATE]   Source hash: %d\n", timestamp(), srcHash)
		logger("[%s] [VALIDATE]   Destination hash: %d\n", timestamp(), dstHash)
		return false
	}

	if verbose >= 2 {
		logger("[%s] [VALIDATE] SUCCESS - File validated: %s (size: %d, hash: %d)\n", timestamp(), relPath, srcInfo.Size(), srcHash)
	}
	return true
}

// deleteExtraFiles removes files and directories from the destination that do not exist in the source
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

// getUsageFunc returns the usage function for the flag package
func getUsageFunc() func() {
	return func() {
		fmt.Fprintf(os.Stderr, `cache_copy v%s - High-performance file copying with intelligent caching

Usage: cache_copy [src] [dst] [options]

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

For more information and updates, visit: https://github.com/your-repo/cache_copy

`, VERSION)
	}
}
