package core

import (
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/cespare/xxhash/v2"
)

// OpenWithRetry wraps os.Open with retry logic.
func OpenWithRetry(path string, maxRetries int) (*os.File, error) {
	var f *os.File
	var err error
	for i := 0; i < maxRetries; i++ {
		f, err = os.Open(path)
		if err == nil {
			return f, nil
		}
		if errno, ok := err.(*os.PathError); ok {
			switch errno.Err {
			case syscall.EINTR, syscall.EAGAIN, syscall.EIO, syscall.EBUSY:
				time.Sleep(200 * time.Millisecond)
				continue
			}
		}
		break
	}
	return f, err
}

// CreateWithRetry wraps os.Create with retry logic.
func CreateWithRetry(path string, maxRetries int) (*os.File, error) {
	var f *os.File
	var err error
	for i := 0; i < maxRetries; i++ {
		f, err = os.Create(path)
		if err == nil {
			return f, nil
		}
		if errno, ok := err.(*os.PathError); ok {
			switch errno.Err {
			case syscall.EINTR, syscall.EAGAIN, syscall.EIO, syscall.EBUSY:
				time.Sleep(200 * time.Millisecond)
				continue
			}
		}
		break
	}
	return f, err
}

// ParseSize parses a size like "4MB", "256KB", "1GB", "512k" or "1048576"
// (bytes) into bytes. Units are binary (1KB = 1024 bytes) and case-insensitive;
// the number must be a positive whole number.
func ParseSize(s string) (int, error) {
	t := strings.ToUpper(strings.TrimSpace(s))
	mult := 1
	for _, u := range []struct {
		suffix string
		mult   int
	}{{"GB", 1 << 30}, {"G", 1 << 30}, {"MB", 1 << 20}, {"M", 1 << 20}, {"KB", 1 << 10}, {"K", 1 << 10}, {"B", 1}} {
		if strings.HasSuffix(t, u.suffix) {
			t, mult = strings.TrimSpace(strings.TrimSuffix(t, u.suffix)), u.mult
			break
		}
	}
	n, err := strconv.Atoi(t)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid size %q: use a positive whole number with an optional KB, MB or GB unit", s)
	}
	if n > math.MaxInt/mult {
		return 0, fmt.Errorf("size %q is too large", s)
	}
	return n * mult, nil
}

// HumanSize converts a size in bytes to a human-readable string.
func HumanSize(n int) string {
	if n >= 1024*1024 {
		return fmt.Sprintf("%.2f MB", float64(n)/(1024*1024))
	} else if n >= 1024 {
		return fmt.Sprintf("%.2f KB", float64(n)/1024)
	}
	return fmt.Sprintf("%d bytes", n)
}

// RenderProgressBar generates a progress bar string based on current and total values.
func RenderProgressBar(current, total int64, width int) string {
	if total == 0 {
		return "[----------] 0%"
	}
	percent := float64(current) / float64(total)
	filled := int(percent * float64(width))
	bar := strings.Repeat("=", filled)
	if filled < width {
		bar += ">"
		bar += strings.Repeat("-", width-filled-1)
	}
	return fmt.Sprintf("[%s] %3.0f%%", bar, percent*100)
}

// FileHash computes and returns the xxHash of a file.
func FileHash(path string) (uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	h := xxhash.New()
	if _, err := io.Copy(h, f); err != nil {
		return 0, err
	}
	return h.Sum64(), nil
}

// CopyAndHash copies src to dst through buf and returns the number of bytes
// written and the xxHash64 of exactly those bytes, so a copied file never
// needs a second read just to be hashed. Short writes are reported as errors.
func CopyAndHash(dst io.Writer, src io.Reader, buf []byte) (int64, uint64, error) {
	h := xxhash.New()
	var written int64
	for {
		nr, rerr := src.Read(buf)
		if nr > 0 {
			nw, werr := dst.Write(buf[:nr])
			if werr != nil {
				return written, 0, werr
			}
			if nw != nr {
				return written, 0, io.ErrShortWrite
			}
			h.Write(buf[:nr])
			written += int64(nw)
		}
		if rerr == io.EOF {
			return written, h.Sum64(), nil
		}
		if rerr != nil {
			return written, 0, rerr
		}
	}
}
