// --validate: the second pass that re-reads every copied file and compares it with the source.

package main

import (
	"cache_copy/core"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// runPostCopyValidation performs validation after all files have been copied
// A file that fails validation is deleted from the destination and dropped from
// the cache: it is known to differ from the source, possibly with the same size
// and date, so leaving it in place would make it look like a good copy.
func runPostCopyValidation(copiedFiles []string, src, rootDst string, cache *core.GlobalCache,
	verbose int, logger LoggerFunc, failures *failureList) {
	logger("[%s] [INFO] Copy phase completed. Starting validation phase for %d files...\n", timestamp(), len(copiedFiles))

	var validationErrors int
	for i, relPath := range copiedFiles {
		if interrupted.Load() {
			break
		}
		if i%10 == 0 || i == len(copiedFiles)-1 {
			logger("[%s] [VALIDATE] Progress: %d/%d files validated\n", timestamp(), i+1, len(copiedFiles))
		}

		srcPath := filepath.Join(src, relPath)
		dstPath := filepath.Join(rootDst, relPath)

		if !validateSingleFile(srcPath, dstPath, relPath, verbose, logger) {
			validationErrors++
			cache.Lock()
			cache.Remove(relPath)
			cache.Unlock()
			if err := os.Remove(dstPath); err != nil && !os.IsNotExist(err) {
				failures.add(relPath, fmt.Errorf("post-copy validation failed and the bad copy could not be deleted: %w", err))
			} else {
				logger("[%s] [VALIDATE] Deleted mismatching destination file: %s\n", timestamp(), dstPath)
				failures.add(relPath, errors.New("post-copy validation failed; destination file deleted"))
			}
			break
		}
	}

	if validationErrors > 0 {
		logger("[%s] [VALIDATE] Validation failed! %d files had validation errors\n", timestamp(), validationErrors)
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

	dstHash, err := hashDestination(dstPath, logger)
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
