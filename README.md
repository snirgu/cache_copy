## cache_copy:
Cli tool for fast copy and caching for win and linux.


## help:
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
		Folder for cache files (default: %LocalAppData%\cache_copy on Windows,
		~/.cache/cache_copy on Linux)

  -fsync
		Flush every copied file to disk before it is renamed into place (default: true).
		Use --fsync=false for speed with many small files; write errors then show up
		only when the file is closed, and a power cut may lose recently copied files

  -dry-run
		Show what would be copied and (with --mirror) deleted, without changing
		anything: no files, directories or cache are written

Options may come before, between or after [src] and [dst]. Use -- to end the
options, e.g. for a path that starts with "-".

COPY BEHAVIOR:
  - Files are written to <name>.cache_copy.tmp and renamed into place only when
	complete, so an interrupted copy never leaves a partial file under the real name
  - Modification time and permissions are copied from the source
  - Symbolic links are recreated as links (never followed); devices, sockets, pipes
	and Windows junctions are reported as errors
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
  - Cache files are stored in the user cache folder (%LocalAppData%\cache_copy on Windows,
	~/.cache/cache_copy on Linux), or in --cache-dir; a cache from version 1.0.3 or older
	(.cache_cache_copy in the current folder) is moved there automatically
  - Each source/destination pair gets its own cache file, named <source>_to_<destination>_<hash>.json.
	Different spellings of the same folders (letter case, Windows short names like
	AERONA~1, symbolic links) share one cache
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



## build instructions:
run build.bat script, binaries results are inside 'bin' folder