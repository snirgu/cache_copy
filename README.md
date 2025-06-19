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



## build instructions:
run build.bat script, binaries results are inside 'bin' folder