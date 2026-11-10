// Command-line parsing, the run configuration, path safety checks and the help text.

package main

import (
	"cache_copy/core"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

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
	Retry        bool
	DryRun       bool
	Fsync        bool
	CacheDir     string
	Network      bool // source or destination is on a network filesystem
	OriginalCmd  string
}

// copyOptions are the per-run settings the copy engine needs.
type copyOptions struct {
	BufSize  int
	Workers  int
	Verbose  int
	NoCache  bool
	Validate bool
	Retry    bool
	DryRun   bool
	Fsync    bool
	Network  bool // more copy attempts on network filesystems
}

func (c *Config) copyOptions() copyOptions {
	return copyOptions{
		BufSize: c.BufferSize, Workers: c.Workers, Verbose: c.Verbose,
		NoCache: c.NoCache, Validate: c.Validate, Retry: c.Retry, DryRun: c.DryRun, Fsync: c.Fsync,
		Network: c.Network,
	}
}

// splitArgs separates the positional arguments (src, dst) from the flags, so
// flags may appear anywhere: before, between or after the paths. It knows which
// flags take a value, so "--workers 4" never mistakes "4" for a path, and it
// accepts "--flag true|false" for on/off flags (Go's flag package only accepts
// "--flag=false"). Everything after "--" is positional, for paths that start
// with "-".
func splitArgs(args []string, fs *flag.FlagSet) (positional, flagArgs []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			positional = append(positional, a)
			continue
		}
		name := strings.TrimLeft(a, "-")
		f := fs.Lookup(name)
		if strings.Contains(name, "=") || f == nil || i+1 >= len(args) {
			// "--flag=value", an unknown flag, or the last argument:
			// flag.Parse accepts it or reports the problem.
			flagArgs = append(flagArgs, a)
			continue
		}
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
			if v := strings.ToLower(args[i+1]); v == "true" || v == "false" {
				flagArgs = append(flagArgs, a+"="+v)
				i++
			} else {
				flagArgs = append(flagArgs, a)
			}
			continue
		}
		flagArgs = append(flagArgs, a, args[i+1])
		i++
	}
	return positional, flagArgs
}

// parseArguments parses command line arguments and returns configuration
func parseArguments() *Config {
	originalCommand := strings.Join(os.Args, " ")

	workers := flag.Int("workers", runtime.GOMAXPROCS(0), "Number of concurrent workers for file copying")
	clearCache := flag.Bool("clear-cache", false, "Delete the cache.json file before starting copy")
	mirror := flag.Bool("mirror", false, "Mirror source to destination: delete extra files in the destination that are not in the source")
	noCache := flag.Bool("no-cache", false, "Disable cache: always copy all files")
	maxCacheAge := flag.Int("max-cache-age", 90, "Maximum age (in days) for cache entries (default 90)")
	verbose := flag.Int("verbose", 0, "Set verbosity level (0=quiet, 1=print large file operations >1GB, 2=print all file operations, 3=cache decisions)")
	logPath := flag.String("log-path", "", "Path to log file (all stdout will also be written here)")
	bufferSizeStr := flag.String("buffer-size", "4MB", "Buffer size for file copy (e.g. 4MB, 256KB, 1GB, 1048576)")
	noTUI := flag.Bool("no-tui", false, "Disable TUI and use classic terminal output (default: TUI enabled)")
	validate := flag.Bool("validate", false, "Validate files by comparing size and hash between source and destination (slower but 100% accurate)")
	autoClean := flag.Bool("auto-clean", true, "Automatically clean stale cache entries on every run (default: true)")
	retry := flag.Bool("retry", true, "Retry a failed file copy from scratch (default: true); false gives every file exactly one attempt")
	fsync := flag.Bool("fsync", true, "Flush every copied file to disk before it is renamed into place (default: true)")
	cacheDir := flag.String("cache-dir", "", "Folder for cache files (default: the user cache folder)")
	dryRun := flag.Bool("dry-run", false, "Show what would be copied and (with --mirror) deleted, without changing anything")

	flag.Usage = getUsageFunc()
	positional, flagArgs := splitArgs(os.Args[1:], flag.CommandLine)
	flag.CommandLine.Parse(flagArgs) // exits with usage on an unknown or malformed flag

	if len(positional) != 2 {
		if len(positional) > 2 {
			fmt.Fprintf(os.Stderr, "[%s] [ERROR] Too many arguments %q: expected only [src] and [dst] (quote paths that contain spaces)\n", timestamp(), positional)
		}
		flag.Usage()
		os.Exit(1)
	}
	src, dst := positional[0], positional[1]

	// Parse buffer size
	bufSize, err := core.ParseSize(*bufferSizeStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[%s] [ERROR] Invalid buffer size: %v\n", timestamp(), err)
		os.Exit(1)
	}

	// Determine copy behavior and destination
	srcClean := filepath.Clean(src)
	// A trailing separator means "copy the contents". Windows accepts both
	// separators, so both count there.
	copyContents := strings.HasSuffix(src, string(filepath.Separator)) ||
		(runtime.GOOS == "windows" && strings.HasSuffix(src, "/"))

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
		Retry:        *retry,
		DryRun:       *dryRun,
		Fsync:        *fsync,
		CacheDir:     resolveCacheDir(*cacheDir),
		OriginalCmd:  originalCommand,
	}
}

// checkPaths refuses source/destination combinations that would loop or
// destroy data:
//   - a destination inside the source (or the same folder): each run would copy
//     the previous copy again, growing without end;
//   - with --mirror, a source inside the destination: mirroring would delete
//     the source itself, since it is not part of itself;
//   - with --mirror, a filesystem root as destination: a mistyped path would
//     delete everything on the drive that is not in the source.
func checkPaths(config *Config) error {
	// Canonical spellings, so a short name (AERONA~1), different letter case
	// or a symbolic link cannot hide an overlap.
	absSrc, absDst := core.CanonicalPath(config.Src), core.CanonicalPath(config.RootDst)
	if pathWithin(absDst, absSrc) {
		return fmt.Errorf("destination %s is inside the source %s", absDst, absSrc)
	}
	if config.Mirror && pathWithin(absSrc, absDst) {
		return fmt.Errorf("refusing --mirror: the source %s is inside the destination %s and would be deleted", absSrc, absDst)
	}
	if config.Mirror && filepath.Dir(absDst) == absDst {
		return fmt.Errorf("refusing --mirror into filesystem root %s: it would delete everything there that is not in the source", absDst)
	}
	return nil
}

// pathWithin reports whether p is base itself or inside it. filepath.Rel
// compares case-insensitively on Windows, matching the filesystem.
func pathWithin(p, base string) bool {
	rel, err := filepath.Rel(base, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// getUsageFunc returns the usage function for the flag package
func getUsageFunc() func() {
	return func() {
		fmt.Fprintf(os.Stderr, `cache_copy v%s - High-performance file copying with intelligent caching

Usage: cache_copy [src] [dst] [options]

[src] and [dst] are required. Options may come before, between or after them.
Use -- to end the options, e.g. for a path that starts with "-".

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
        Examples: 4MB, 256KB, 1GB, 1048576 (units are binary: 1KB = 1024 bytes)
  
  -no-tui
        Disable TUI and use classic terminal output (disables fancy progress display).
        The TUI is also skipped automatically when not run in an interactive terminal
        (scripts, pipes, redirected output); a [PROGRESS] line is then logged every 30s
  
  -validate
        Validate files by comparing size and hash between source and destination
        (slower but 100%% accurate - ignores cache and checks actual file content)
  
  -auto-clean
        Automatically clean stale cache entries on every run (default: true)
        Use --auto-clean=false to disable automatic cache cleaning

  -retry
        Retry a failed file copy from scratch, then verify the result (default: true)
        Use --retry=false (or --retry false) to give every file exactly one attempt;
        the first error then fails the file and stops the run

  -cache-dir string
        Folder for cache files (default: %%LocalAppData%%\cache_copy on Windows,
        ~/.cache/cache_copy on Linux)

  -fsync
        Flush every copied file to disk before it is renamed into place (default: true).
        Use --fsync=false for speed with many small files; write errors then show up
        only when the file is closed, and a power cut may lose recently copied files

  -dry-run
        Show what would be copied and (with --mirror) deleted, without changing
        anything: no files, directories or cache are written

COPY BEHAVIOR:
  - Files are written to <name>.cache_copy.tmp and renamed into place only when
    complete, so an interrupted copy never leaves a partial file under the real name
  - Modification time and permissions are copied from the source
  - Symbolic links are recreated as links (never followed); devices, sockets and
    pipes are reported as errors
  - The destination may not be inside the source; with --mirror the source may not be
    inside the destination, and a drive/filesystem root is refused

EXAMPLES:
  cache_copy /source/folder /destination/folder
  cache_copy /source/folder/ /destination/folder --mirror
  cache_copy /source /dest --workers 8 --buffer-size 8MB --verbose 2
  cache_copy /source /dest --validate --no-cache --verbose 3
  cache_copy /source /dest --clear-cache --mirror --log-path copy.log
  cache_copy /source /dest --auto-clean=false --verbose 1

CACHE BEHAVIOR:
  - Cache files are stored in the user cache folder (%%LocalAppData%%\cache_copy on Windows,
    ~/.cache/cache_copy on Linux), or in --cache-dir; a cache from version 1.0.3 or older
    (.cache_cache_copy in the current folder) is moved there automatically
  - Each source/destination pair gets its own unique cache file
  - Cache entries track file size, hash, and source/destination modification times
  - A file is skipped only if, in this order (cheapest first), its size and modification
    time match the cache, the destination is unchanged, and its content hash matches;
    the first difference makes it copy. Unchanged files are still read once to hash them
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
