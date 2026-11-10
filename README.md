# cache_copy

Fast, cached, **faithful** file copying for Windows and Linux.

`cache_copy` copies a folder tree and remembers what it copied, so the next run only copies what changed. A destination file is either an exact copy of its source or is reported as a failure. A damaged or partial file never sits under the real name looking like a good copy.

- [What it guarantees](#what-it-guarantees)
- [Install](#install)
- [Usage](#usage)
- [Options](#options)
- [How it decides what to copy](#how-it-decides-what-to-copy)
- [What is copied, and what is not](#what-is-copied-and-what-is-not)
- [Exit codes](#exit-codes)
- [Interrupting a run (Ctrl+C)](#interrupting-a-run-ctrlc)
- [The cache](#the-cache)
- [Building and releasing](#building-and-releasing)

## What it guarantees

- **Atomic copies.** Each file is written to `<name>.cache_copy.tmp` and renamed into place only when every read, write, flush and close succeeded, the byte count matches, and the source did not change during the copy. An interrupted or failed copy never leaves a partial file under the real name.
- **Problems are never silent.** A failed attempt is discarded and the file is copied again from scratch with a new file handle; a failed handle is never reused, since that can produce a file with the right size but wrong content. A file that needed a retry is read back and compared with the source before it is accepted. Every retry is logged as `[WARN]`.
- **Verification reads the real storage.** Read-backs bypass this machine's file cache (`FILE_FLAG_NO_BUFFERING` on Windows, `O_DIRECT` on Linux), so they check what the disk or network server actually holds. Where a filesystem cannot do that, a warning says so.
- **The first failure stops the run** with exit code 1 and a list of the files that were not copied faithfully. A file that still fails after all retries keeps its previous complete version and is copied again on the next run.
- **Safe paths.** A destination inside the source is refused. With `--mirror`, a source inside the destination and a drive or filesystem root (such as `D:\`) are refused too.

## Install

Download the binary for your system from the [Releases page](https://github.com/snirgu/cache_copy/releases):

| System | File |
|---|---|
| Windows (64-bit) | `cache_copy-<version>-windows-amd64.exe` |
| Linux (64-bit) | `cache_copy-<version>-linux-amd64` (run `chmod +x` on it) |

Rename it to `cache_copy` / `cache_copy.exe` if you like. It is a single file with no dependencies. To build it yourself, see [Building and releasing](#building-and-releasing).

## Usage

```
cache_copy [src] [dst] [options]
```

Options may come before, between or after the paths. Use `--` to end the options, for example for a path that starts with `-`. On/off options accept `--retry=false` or `--retry false`.

**Whether `[src]` ends with a separator matters:**

| Command | Result |
|---|---|
| `cache_copy myfolder dest` | creates `dest\myfolder\` and copies everything into it |
| `cache_copy myfolder\ dest` | copies the **contents** of `myfolder` directly into `dest\` |

On Windows both `\` and `/` count as a trailing separator; on Linux, `/`. With `--mirror`, the same rule decides which destination folder is mirrored.

**Examples:**

```
cache_copy D:\data E:\backup                          copy D:\data into E:\backup\data
cache_copy D:\data\ E:\backup --mirror                make E:\backup an exact mirror of D:\data
cache_copy D:\data\ E:\backup --mirror --dry-run      show what the mirror would delete and copy
cache_copy /src /dst --validate                       copy, then re-read every copied file to verify it
cache_copy /src /dst --log-path copy.log --no-tui     plain output, also saved to copy.log
```

## Options

| Option | Default | Description |
|---|---|---|
| `--workers N` | CPU cores | Number of files copied in parallel. |
| `--buffer-size SIZE` | `4MB` | Copy buffer per worker. Accepts `256KB`, `8MB`, `1GB`, `512k`, or bytes. Units are binary (1KB = 1024 bytes). |
| `--mirror` | off | Delete files and folders in the destination that are not in the source. Use `--dry-run` first. |
| `--dry-run` | off | Show what would be copied and (with `--mirror`) deleted, without changing anything: no files, folders or cache are written. |
| `--validate` | off | Compare source and destination content for every file, and re-read every copied file afterwards. Slow but complete; a mismatching destination file is deleted. |
| `--retry` | on | Retry a failed copy from scratch, then verify it. `--retry=false` gives every file exactly one attempt. |
| `--fsync` | on | Flush every file to disk before it is renamed into place. `--fsync=false` is faster for many small files, but write errors then appear only when the file is closed, and a power cut may lose recently copied files. |
| `--no-cache` | off | Ignore the cache and copy every file. |
| `--clear-cache` | off | Delete this job's cache first, so every file is checked and copied again. |
| `--cache-dir DIR` | user cache folder | Where cache files are kept (see [The cache](#the-cache)). |
| `--max-cache-age DAYS` | `90` | Drop cache entries for files not seen for this many days. |
| `--auto-clean` | on | Drop cache entries for files no longer in the source. |
| `--no-tui` | off | Plain output instead of the full-screen display. The TUI is also skipped automatically when the output is not an interactive terminal (scripts, pipes, redirected output); a `[PROGRESS]` line is then logged every 30 seconds. |
| `--log-path FILE` | none | Also append every log line of the run to this file. |
| `--verbose N` | `0` | `0` progress and errors only; `1` also files over 1 GB; `2` every file copied or skipped; `3` also why each file was copied or skipped. |

## How it decides what to copy

The checks run from cheapest to most expensive, and the first difference means the file is copied:

| # | Check | Cost |
|---|---|---|
| 1 | Is the file in the cache? | memory |
| 2 | Source size, then modification time, against the cache | memory (both come from one metadata read) |
| 3 | Destination size and modification time against what was recorded after the copy | one metadata read of the destination |
| 4 | **Source content hash** (xxHash64) against the cache | reads the whole source file |

A file is skipped only if all four match. A file that changed but kept the same size and date is still caught by its hash, and a destination file that was edited after it was copied is caught by step 3. This means every unchanged file is still read once per run to hash it; that is the price of the guarantee.

A destination file that was edited so it keeps exactly the same size *and* its modification time was set back is detected only by `--validate`, which reads the destination itself.

## What is copied, and what is not

**Copied:**
- file contents, verified as described above;
- each file's modification time;
- permissions: the read-only flag on Windows, and the mode bits (including execute) on Linux;
- symbolic links, recreated as links with the same target. Links are never followed, and links whose target does not exist are kept as they are. Creating links on Windows needs Developer Mode or an administrator prompt;
- empty folders.

**Not copied:**
- **folder modification times**: folders get the time they were created at the destination;
- **file owner and group on Linux** (that would need root), **Windows ACLs** (security permissions), **alternate data streams** and other extended attributes;
- **Windows junctions, named pipes, sockets and devices**: these are reported as errors and stop the run (exit code 1). They are never followed or read, so they can neither hang the copy nor be copied wrongly.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Every file is a faithful copy of the source (with `--dry-run`: the report completed). |
| `1` | A file could not be copied faithfully, or the run could not start (bad path, unreadable source, ...). The log lists what failed. |
| `2` | Unknown or malformed option. |
| `130` | Interrupted by Ctrl+C. Files already copied are complete; run again to finish. |

## Interrupting a run (Ctrl+C)

The first Ctrl+C stops the run safely:
- no new files are started;
- files being copied stop at their next chunk, and their temp files are deleted;
- the cache is saved;
- the run exits with code 130.

Files already copied are complete and verified, so running the same command again finishes the job. A second Ctrl+C quits immediately; any leftover `*.cache_copy.tmp` files are replaced by the next run (and deleted by `--mirror`). In the TUI, quitting before the copy finishes stops it the same way.

## The cache

- Each source/destination pair has its own cache file, named `<source>_to_<destination>_<hash>.json`.
- Cache files are kept in `%LocalAppData%\cache_copy` on Windows and `~/.cache/cache_copy` on Linux, or in `--cache-dir`. The folder you run the command from does not matter.
- Different spellings of the same folders (letter case on Windows, short names like `AERONA~1`, symbolic links) share one cache.
- A cache created by version 1.0.3 or older (in `.cache_cache_copy` under the current folder) is moved to the new location automatically, so upgrading does not cause a full re-copy.
- Each entry records the size, the content hash, and the source and destination modification times.
- While copying, the cache is saved every 5 seconds and at the end, always by writing a new file and renaming it, so a crash cannot corrupt it. If the cache is lost, the next run re-checks files by hash; nothing is copied wrongly.

## Building and releasing

Requires Go 1.24 or newer and GNU Make 4.2 or newer (`winget install ezwinports.make` on Windows). Dependencies are vendored, so a build needs no network access. The `VERSION` file holds the version and is compiled into the binaries.

| Command | What it does |
|---|---|
| `make build` | Build `bin/cache_copy.exe` and `bin/cache_copy` |
| `make check` | `go vet` and all tests |
| `make dist` | Build the versioned release files into `dist/` |
| `make release V=x.y.z` | Check and build, then commit `VERSION` and tag `vx.y.z` (needs a clean working tree) |
| `make publish` | Push the branch and tag, then create the GitHub Release with both binaries attached |
| `make clean` | Remove build output |

`make publish` needs the GitHub CLI, logged in once: `winget install GitHub.cli`, then `gh auth login`. It refuses to run unless the current commit is exactly the release tag, so the uploaded binaries are built from exactly the tagged code. Binaries are not committed to git; they are attached to the GitHub Release.

`build.bat` builds both binaries without `make`, as a fallback.

Run `make help` to see every target.
