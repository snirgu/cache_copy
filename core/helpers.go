package core

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/cespare/xxhash/v2"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/mem"
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

// ParseSize parses a string like "4MB", "256KB", or "1048576" into bytes.
func ParseSize(s string) (int, error) {
	var size int
	var unit string
	n, err := fmt.Sscanf(s, "%d%s", &size, &unit)
	if n == 1 && err == nil {
		return size, nil // No unit, just bytes
	}
	if n == 2 && err == nil {
		switch strings.ToUpper(unit) {
		case "KB":
			return size * 1024, nil
		case "MB":
			return size * 1024 * 1024, nil
		case "B":
			return size, nil
		default:
			return 0, fmt.Errorf("unknown size unit: %s", unit)
		}
	}
	return 0, fmt.Errorf("invalid size format: %s", s)
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

// Exists checks if a given file or directory path exists on disk.
func Exists(path string) bool {
	_, err := os.Stat(path)
	return !os.IsNotExist(err)
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

// PrintCPUMonitor prints CPU and memory usage to stdout.
func PrintCPUMonitor() {
	for {
		percent, _ := cpu.Percent(0, false)
		numGoroutine := runtime.NumGoroutine()
		vmem, _ := mem.VirtualMemory()
		fmt.Fprintf(os.Stdout, "[MONITOR] CPU: %.2f%% | Mem: %.2f%% (%.2f GB/%.2f GB) | Goroutines: %d\n",
			percent[0],
			vmem.UsedPercent,
			float64(vmem.Used)/(1024*1024*1024),
			float64(vmem.Total)/(1024*1024*1024),
			numGoroutine)
		time.Sleep(1 * time.Second)
	}
}
