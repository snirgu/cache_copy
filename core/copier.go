package core

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type Copier struct {
	verify     bool
	include    string
	exclude    string
	logger     *Logger
	cache      *GlobalCache
	clearCache bool
}

func NewCopier(verify bool, include, exclude string, logger *Logger, cache *GlobalCache, clearCache bool) *Copier {
	if clearCache {
		cache.Clear()
	}
	return &Copier{verify, include, exclude, logger, cache, clearCache}
}

func (c *Copier) match(path string) bool {
	if c.exclude != "" && strings.HasSuffix(path, c.exclude) {
		return false
	}
	if c.include != "" && !strings.HasSuffix(path, c.include) {
		return false
	}
	return true
}

func (c *Copier) CopyDirWithProgress(
	src, dst string,
	fileList []string,
	totalFiles int,
	progressCb func(copiedBytes, totalBytes int64),
) error {
	existing := make(map[string]bool)

	// Print setup/info messages first
	fmt.Printf("[%s] [INFO] Using %d worker(s)\n", timestamp(), 1)
	fmt.Printf("[%s] [INFO] Using buffer size: 4.00 MB (4194304 bytes)\n", timestamp())

	// Buffer for skipped messages
	var skipped []string

	var copiedBytes int64
	var totalBytes int64

	// Calculate total bytes
	for _, relPath := range fileList {
		srcPath := filepath.Join(src, relPath)
		srcInfo, err := os.Stat(srcPath)
		if err != nil {
			return err
		}
		totalBytes += srcInfo.Size()
	}

	for _, relPath := range fileList {
		srcPath := filepath.Join(src, relPath)
		dstPath := filepath.Join(dst, relPath)
		existing[relPath] = true

		err := os.MkdirAll(filepath.Dir(dstPath), os.ModePerm)
		if err != nil {
			return err
		}

		srcInfo, err := os.Stat(srcPath)
		if err != nil {
			return err
		}

		hash, err := FileHash(srcPath)
		if err != nil {
			return err
		}

		entry, ok := c.cache.IsUpToDate(relPath)
		if ok && entry.Size == srcInfo.Size() && entry.Hash == hash {
			dstInfo, dstErr := os.Stat(dstPath)
			if dstErr == nil && dstInfo.Mode().IsRegular() {
				// Buffer skipped messages instead of printing now
				skipped = append(skipped, fmt.Sprintf("Skipped (cached): %s", srcPath))
				copiedBytes += srcInfo.Size()
				if progressCb != nil {
					progressCb(copiedBytes, totalBytes)
				}
				continue
			}
		}

		err = c.CopyFile(srcPath, dstPath, relPath)
		if err != nil {
			return err
		}

		c.cache.Update(relPath, srcInfo.Size(), hash, srcInfo.ModTime().Unix())
		copiedBytes += srcInfo.Size()
		if progressCb != nil {
			progressCb(copiedBytes, totalBytes)
		}
	}

	// After the loop, print a newline to finish the progress bar line
	fmt.Println()

	// Now print skipped messages (after the progress bar)
	for _, msg := range skipped {
		fmt.Println(msg)
	}

	c.cache.CleanUpMissingFiles(src)
	return nil
}

func (c *Copier) CopyFile(src, dst, relPath string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("not a regular file: %s", src)
	}

	buf := make([]byte, 4*1024*1024) // 4MB buffer
	_, err = CopyFile(src, dst, buf)
	if err != nil {
		return err
	}

	hash, err := FileHash(src)
	if err != nil {
		return err
	}

	// c.logger.Log(fmt.Sprintf("Copied %s -> %s (%d bytes)", src, dst, info.Size()))
	c.cache.Update(relPath, info.Size(), hash, info.ModTime().Unix())
	return nil
}

// CopyFile copies a file from src to dst using a buffer.
// Returns the number of bytes copied and any error encountered.
func CopyFile(src, dst string, buf []byte) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()

	// Ensure the destination directory exists
	if err := os.MkdirAll(filepath.Dir(dst), os.ModePerm); err != nil {
		return 0, err
	}

	out, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	defer out.Close()

	n, err := io.CopyBuffer(out, in, buf)
	return n, err
}

func printProgressBar(progress float64) {
	barLength := 50
	progressBar := int(progress / 2)
	bar := ""
	for i := 0; i < barLength; i++ {
		if i < progressBar {
			bar += "#"
		} else {
			bar += " "
		}
	}
	progressStr := fmt.Sprintf("\r[%-*s] %.2f%%", barLength, bar, progress)
	fmt.Print(progressStr)
}
