package main

import (
	"cache_copy/core"
	"fmt"
	"os"
	"time"
)

// Application version, injected at build time from the VERSION file
// (make build passes -ldflags "-X main.VERSION=..."); "dev" means an unversioned build.
var VERSION = "dev"

// timestamp returns the current time formatted as a string for logging purposes
// Format: "2006-01-02 15:04:05.000" (YYYY-MM-DD HH:MM:SS.mmm)
func timestamp() string {
	return time.Now().Format("2006-01-02 15:04:05.000")
}

// fatalf logs an error, saves the cache (when there is one) so work done so
// far is not lost, and exits with status 1. Every error exit after the log is
// set up goes through here.
func fatalf(cache *core.GlobalCache, format string, args ...interface{}) {
	fmt.Fprintf(logOut, "[%s] [ERROR] %s\n", timestamp(), fmt.Sprintf(format, args...))
	if cache != nil {
		saveCache(cache)
	}
	os.Exit(1)
}

// saveCache writes the cache at the end of a run. A failure does not undo
// the copy, so it is a warning: the next run simply re-verifies files.
// (While copying, runCopyWorkers also saves every few seconds.)
func saveCache(cache *core.GlobalCache) {
	if err := cache.SaveCache(); err != nil {
		fmt.Fprintf(logOut, "[%s] [WARN] Could not save the cache (%v); the copy itself is complete, the next run will re-verify files\n", timestamp(), err)
	}
}

// main runs one copy job: parse arguments, open the log, check the paths,
// load the cache, mirror, scan the source, copy, and exit 1 if any file could
// not be copied faithfully.
func main() {
	// Parse arguments and configuration
	config := parseArguments()
	if err := setupLog(config.LogPath); err != nil {
		fmt.Fprintf(os.Stderr, "[%s] [ERROR] Failed to open log file %s: %v\n", timestamp(), config.LogPath, err)
		os.Exit(1)
	}
	fmt.Fprintf(logOut, "[%s] [INFO] Command: %s\n", timestamp(), config.OriginalCmd)
	if err := checkPaths(config); err != nil {
		fatalf(nil, "%v", err)
	}
	if config.DryRun {
		fmt.Fprintf(logOut, "[%s] [DRY-RUN] Dry run: nothing will be copied, deleted or written\n", timestamp())
	}
	if !config.NoTUI && !isInteractive() {
		config.NoTUI = true
		fmt.Fprintf(logOut, "[%s] [INFO] Not running in an interactive terminal: using plain output instead of the TUI\n", timestamp())
	}
	config.Network = core.IsNetworkPath(config.Src) || core.IsNetworkPath(config.RootDst)
	if config.Network && config.Retry {
		fmt.Fprintf(logOut, "[%s] [INFO] Network filesystem detected: up to 5 copy attempts per file\n", timestamp())
	}

	// Initialize cache
	cache := initializeCache(config)

	// Handle mirror mode
	handleMirrorMode(cache, config)

	// Clean old cache entries
	cleanOldCacheEntries(cache, config)

	// Gather file list and calculate total bytes
	dirs, fileList, totalBytes, err := gatherFileList(config)
	if err != nil {
		fatalf(cache, "Error gathering file list: %v", err)
	}

	// Clean stale cache entries
	cleanStaleCache(cache, config, fileList)

	// Create destination directories
	if config.DryRun {
		dirs = nil
	}
	if err := createDestinationDirectories(dirs, config); err != nil {
		fatalf(cache, "%v", err)
	}

	// Run copy operation in appropriate mode
	var failures []string
	if config.NoTUI {
		failures = runNoTUIMode(cache, config, fileList, totalBytes)
	} else {
		failures = runTUIMode(cache, config, fileList, totalBytes)
	}

	saveCache(cache)
	if len(failures) > 0 {
		os.Exit(1)
	}
}
