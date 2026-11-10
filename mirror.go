// --mirror: deleting what the destination has that the source does not.

package main

import (
	"cache_copy/core"
	"fmt"
	"os"
	"path/filepath"
)

// handleMirrorMode deletes extra files in destination if mirror mode is enabled
func handleMirrorMode(cache *core.GlobalCache, config *Config) {
	if !config.Mirror {
		return
	}

	deleted, err := deleteExtraFiles(config.Src, config.RootDst, cache, config.DryRun)
	if err != nil {
		fatalf(cache, "Error deleting extra files: %v", err)
	}
	if config.DryRun {
		fmt.Fprintf(logOut, "[%s] [DRY-RUN] Mirror: %d extra item(s) would be deleted\n", timestamp(), deleted)
	}
}

// deleteExtraFiles removes everything in dstDir that has no counterpart in
// srcDir, plus entries whose kind changed (a file where the source now has a
// directory, or the reverse) so the copy that follows can recreate them.
// Symbolic links are removed as links, never followed. With dryRun it only
// reports. Returns how many entries were (or would be) deleted.
func deleteExtraFiles(srcDir, dstDir string, cache *core.GlobalCache, dryRun bool) (int, error) {
	if _, err := os.Lstat(dstDir); os.IsNotExist(err) {
		return 0, nil // nothing copied yet, so nothing extra
	}
	deleted := 0
	err := filepath.Walk(dstDir, func(dstPath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if dstPath == dstDir {
			return nil
		}
		relPath, err := filepath.Rel(dstDir, dstPath)
		if err != nil {
			return err
		}
		srcInfo, err := os.Lstat(filepath.Join(srcDir, relPath))
		switch {
		case os.IsNotExist(err):
			// not in the source: delete
		case err != nil:
			return err
		case srcInfo.IsDir() == info.IsDir():
			return nil // still in the source as the same kind: keep
		}

		what := "file"
		if info.IsDir() {
			what = "directory"
		}
		deleted++
		if dryRun {
			fmt.Fprintf(logOut, "[%s] [DRY-RUN] Would delete extra %s: %s\n", timestamp(), what, dstPath)
		} else {
			fmt.Fprintf(logOut, "[%s] [INFO] Deleting extra %s: %s\n", timestamp(), what, dstPath)
			if err := os.RemoveAll(dstPath); err != nil {
				return fmt.Errorf("failed to delete %s %s: %w", what, dstPath, err)
			}
			cache.Lock()
			cache.RemoveTree(relPath)
			cache.Unlock()
		}
		if info.IsDir() {
			return filepath.SkipDir
		}
		return nil
	})
	return deleted, err
}
