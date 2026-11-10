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

// flagValues holds the parsed option values.
type flagValues struct {
	workers, maxCacheAge, verbose                           *int
	logPath, bufferSize, cacheDir                           *string
	clearCache, mirror, noCache, noTUI, validate, autoClean *bool
	retry, fsync, dryRun                                    *bool
}

// defineFlags declares every option on fs. It is separate from
// parseArguments so tests can list the options and check that --help and the
// README describe each one.
func defineFlags(fs *flag.FlagSet) *flagValues {
	return &flagValues{
		workers:     fs.Int("workers", runtime.GOMAXPROCS(0), "Number of concurrent workers for file copying"),
		clearCache:  fs.Bool("clear-cache", false, "Delete the cache file before starting copy"),
		mirror:      fs.Bool("mirror", false, "Mirror source to destination: delete extra files in the destination that are not in the source"),
		noCache:     fs.Bool("no-cache", false, "Disable cache: always copy all files"),
		maxCacheAge: fs.Int("max-cache-age", 90, "Maximum age (in days) for cache entries (default 90)"),
		verbose:     fs.Int("verbose", 0, "Set verbosity level (0=quiet, 1=print large file operations >1GB, 2=print all file operations, 3=cache decisions)"),
		logPath:     fs.String("log-path", "", "Path to log file (every log line is also appended here)"),
		bufferSize:  fs.String("buffer-size", "4MB", "Buffer size for file copy (e.g. 4MB, 256KB, 1GB, 1048576)"),
		noTUI:       fs.Bool("no-tui", false, "Disable TUI and use classic terminal output (default: TUI enabled)"),
		validate:    fs.Bool("validate", false, "Validate files by comparing size and hash between source and destination (slower but 100% accurate)"),
		autoClean:   fs.Bool("auto-clean", true, "Automatically clean stale cache entries on every run (default: true)"),
		retry:       fs.Bool("retry", true, "Retry a failed file copy from scratch (default: true); false gives every file exactly one attempt"),
		fsync:       fs.Bool("fsync", true, "Flush every copied file to disk before it is renamed into place (default: true)"),
		cacheDir:    fs.String("cache-dir", "", "Folder for cache files (default: the user cache folder)"),
		dryRun:      fs.Bool("dry-run", false, "Show what would be copied and (with --mirror) deleted, without changing anything"),
	}
}

// parseArguments parses command line arguments and returns configuration
func parseArguments() *Config {
	originalCommand := strings.Join(os.Args, " ")
	f := defineFlags(flag.CommandLine)

	flag.Usage = func() { fmt.Fprint(os.Stderr, usageText()) }
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
	bufSize, err := core.ParseSize(*f.bufferSize)
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
		Workers:      *f.workers,
		ClearCache:   *f.clearCache,
		Mirror:       *f.mirror,
		NoCache:      *f.noCache,
		MaxCacheAge:  *f.maxCacheAge,
		Verbose:      *f.verbose,
		LogPath:      *f.logPath,
		BufferSize:   bufSize,
		NoTUI:        *f.noTUI,
		Validate:     *f.validate,
		AutoClean:    *f.autoClean,
		Retry:        *f.retry,
		DryRun:       *f.dryRun,
		Fsync:        *f.fsync,
		CacheDir:     resolveCacheDir(*f.cacheDir),
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

// usageText is the --help text. The README carries the same information in
// more detail; TestDocsCoverEveryOption checks that both mention every option.
func usageText() string {
	return fmt.Sprintf(`cache_copy v%s - fast, cached, faithful file copying for Windows and Linux

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
  -verbose 3: Shows why each file is copied or skipped (cache decisions)

FLAGS:
  -workers int
        Number of concurrent workers for file copying (default: number of CPU cores)

  -clear-cache
        Delete the cache file before starting copy (every file is then checked and copied again)

  -mirror
        Mirror source to destination: delete extra files in the destination that are not in the source

  -no-cache
        Disable cache: always copy all files (ignores existing cache, slower but always accurate)

  -max-cache-age int
        Drop cache entries for files not seen for this many days (default: 90)

  -verbose int
        Set verbosity level: 0=quiet, 1=large files >1GB, 2=all files, 3=cache decisions (default: 0)

  -log-path string
        Path to log file: every log line of the run is also appended here

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
  - Symbolic links are recreated as links (never followed)
  - The first file that cannot be copied faithfully stops the run
  - The destination may not be inside the source; with --mirror the source may not be
    inside the destination, and a drive/filesystem root is refused
  - Ctrl+C stops safely: no new files start, files being copied are discarded, the
    cache is saved. Press Ctrl+C again to quit immediately

WHAT IS NOT COPIED:
  - Folder modification times (folders get the time they were created at the destination)
  - File owner and group on Linux (would need root), Windows ACLs and alternate data streams
  - Windows junctions, named pipes, sockets and devices: reported as errors (exit code 1)

EXIT CODES:
  0    every file is a faithful copy of the source (or, with --dry-run, nothing to report)
  1    a file could not be copied faithfully, or the run could not start (see the log)
  2    unknown or malformed option
  130  interrupted by Ctrl+C; files already copied are complete, run again to finish

EXAMPLES:
  cache_copy /source/folder /destination/folder
  cache_copy /source/folder/ /destination/folder --mirror
  cache_copy /source /dest --workers 8 --buffer-size 8MB --verbose 2
  cache_copy /source /dest --validate --no-cache --verbose 3
  cache_copy /source /dest --clear-cache --mirror --log-path copy.log
  cache_copy /source /dest --mirror --dry-run

CACHE BEHAVIOR:
  - Cache files are stored in the user cache folder (%%LocalAppData%%\cache_copy on Windows,
    ~/.cache/cache_copy on Linux), or in --cache-dir; a cache from version 1.0.3 or older
    (.cache_cache_copy in the current folder) is moved there automatically
  - Each source/destination pair gets its own cache file; different spellings of the same
    folders (letter case, Windows short names, symbolic links) share one
  - A file is skipped only if, in this order (cheapest first), its size and modification
    time match the cache, the destination is unchanged, and its content hash matches;
    the first difference makes it copy. Unchanged files are still read once to hash them
  - Use --clear-cache to start fresh and delete the entire cache file
  - Use --validate to bypass cache and verify actual file content
  - Stale cache entries are automatically cleaned by default (disable with --auto-clean=false)

PERFORMANCE TIPS:
  - Increase --workers for many small files (default is usually good)
  - Increase --buffer-size for large files (4MB-8MB recommended)
  - --fsync=false speeds up many small files, at the cost described above
  - Use --validate only when you need 100%% verification (slower)

Documentation and releases: https://github.com/snirgu/cache_copy

`, VERSION)
}
