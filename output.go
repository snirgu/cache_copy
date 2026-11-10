// Logging and the two front ends: plain output (with a progress bar in a terminal) and the TUI.

package main

import (
	"cache_copy/core"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/mem"
	"golang.org/x/term"
)

// logOut receives every log line: the console, plus the --log-path file when
// set. main sets it up first thing, so the log file captures the whole run.
// logFile is that file alone (nil without --log-path), for the TUI, which
// draws its own screen instead of writing to the console.
var (
	logOut  io.Writer = os.Stdout
	logFile io.Writer
)

// setupLog opens the --log-path file for appending and routes every later
// log line to both the console and the file.
func setupLog(path string) error {
	if path == "" {
		return nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	logFile = f
	logOut = io.MultiWriter(os.Stdout, f)
	return nil
}

// isInteractive reports whether stdin and stdout are both a terminal, which
// the TUI needs: it reads keys and draws on the screen. Scripts, pipes,
// redirected output and CI logs are not.
func isInteractive() bool {
	return term.IsTerminal(int(os.Stdout.Fd())) && term.IsTerminal(int(os.Stdin.Fd()))
}

// LoggerFunc defines a function type for logging messages with printf-style formatting
type LoggerFunc func(format string, args ...interface{})

// ProgressFunc defines a function type for reporting copy progress
// Parameters: copiedBytes (amount copied so far), totalBytes (total amount to copy)
type ProgressFunc func(copiedBytes, totalBytes int64)

// validateBufferAllocation checks if total buffer allocation is reasonable
func validateBufferAllocation(config *Config) {
	totalBuffer := int64(config.Workers) * int64(config.BufferSize)
	if totalBuffer > 2*1024*1024*1024 {
		fmt.Fprintf(logOut, "[%s] [WARN] Total buffer allocation is %.2f GB (%d workers × %s)\n",
			timestamp(), float64(totalBuffer)/(1024*1024*1024), config.Workers, core.HumanSize(config.BufferSize))
		fmt.Fprintf(logOut, "[%s] [WARN] Consider reducing --workers or --buffer-size to avoid running out of memory.\n", timestamp())
	}
}

// runNoTUIMode runs the copy operation in classic terminal mode and returns
// the files that could not be copied faithfully. In a terminal it draws a
// live progress bar; when output goes to a file, pipe or CI log it writes a
// [PROGRESS] line every 30 seconds instead, so logs stay clean.
func runNoTUIMode(cache *core.GlobalCache, config *Config, fileList []string, totalBytes int64) []string {
	validateBufferAllocation(config)

	fmt.Fprintf(logOut, "[%s] [INFO] Using %d worker(s)\n", timestamp(), config.Workers)
	fmt.Fprintf(logOut, "[%s] [INFO] Using buffer size: %s (%d bytes)\n", timestamp(), core.HumanSize(config.BufferSize), config.BufferSize)

	if config.Validate {
		fmt.Fprintf(logOut, "[%s] [VALIDATE] Validation mode enabled - all files will be verified\n", timestamp())
		if config.Verbose >= 1 {
			fmt.Fprintf(logOut, "[%s] [VALIDATE] This will compare file sizes and content hashes\n", timestamp())
		}
	}

	var copiedBytes int64
	startTime := time.Now()
	progress := func(copied, total int64) {
		atomic.StoreInt64(&copiedBytes, copied)
	}
	progressLine := func() string {
		cur := atomic.LoadInt64(&copiedBytes)
		return fmt.Sprintf("Total: %.2f MB / %.2f MB %s | %s",
			float64(cur)/(1024*1024), float64(totalBytes)/(1024*1024),
			core.RenderProgressBar(cur, totalBytes, 40), time.Since(startTime).Round(time.Second))
	}

	// The live bar is redrawn in place with \r. A log line first blanks the
	// bar, then the next redraw puts it back under the new line. termMu keeps
	// the bar and log lines from interleaving.
	interactive := term.IsTerminal(int(os.Stdout.Fd()))
	clearLine := "\r" + strings.Repeat(" ", 100) + "\r"
	var termMu sync.Mutex
	logger := func(format string, args ...interface{}) {
		termMu.Lock()
		defer termMu.Unlock()
		if interactive {
			fmt.Print(clearLine)
		}
		fmt.Fprintf(logOut, format, args...)
		if len(format) == 0 || format[len(format)-1] != '\n' {
			fmt.Fprintln(logOut)
		}
	}

	done := make(chan struct{})
	var progressDone sync.WaitGroup
	progressDone.Add(1)
	go func() {
		defer progressDone.Done()
		interval := 30 * time.Second
		if interactive {
			interval = 500 * time.Millisecond
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if interactive {
					termMu.Lock()
					fmt.Print("\r" + progressLine())
					termMu.Unlock()
				} else {
					logger("[%s] [PROGRESS] %s\n", timestamp(), progressLine())
				}
			}
		}
	}()

	failures := runCopyWorkers(fileList, config.Src, config.RootDst, cache, config.copyOptions(), totalBytes, logger, progress)
	close(done)
	progressDone.Wait()
	if interactive {
		fmt.Print("\r" + progressLine() + "\n")
	}

	writeSummary(logOut, config, failures)
	return failures
}

// writeSummary prints the end-of-run result. Any failure is listed by file so
// the user knows exactly which destination files are not faithful copies.
func writeSummary(out io.Writer, config *Config, failures []string) {
	if len(failures) > 0 {
		fmt.Fprintf(out, "[%s] [ERROR] Copy finished with %d file(s) NOT copied faithfully:\n", timestamp(), len(failures))
		for _, f := range failures {
			fmt.Fprintf(out, "[%s] [ERROR]   %s\n", timestamp(), f)
		}
		return
	}
	if config.DryRun {
		fmt.Fprintf(out, "[%s] [DRY-RUN] Dry run completed - nothing was changed.\n", timestamp())
		return
	}
	if config.Validate {
		fmt.Fprintf(out, "[%s] [VALIDATE] Validation completed successfully for all files\n", timestamp())
	}
	fmt.Fprintf(out, "[%s] [INFO] Copy process completed.\n", timestamp())
}

// runTUIMode runs the copy operation with TUI interface and returns the files
// that could not be copied faithfully. If the user quits before the copy
// finishes, that is reported as a failure too.
func runTUIMode(cache *core.GlobalCache, config *Config, fileList []string, totalBytes int64) []string {
	app := tview.NewApplication()
	logView := tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true).
		SetWrap(true).
		SetChangedFunc(func() {
			app.Draw()
		})

	progressView := tview.NewTextView().SetDynamicColors(true)
	monitorView := tview.NewTextView().SetDynamicColors(true)

	flex := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(monitorView, 1, 0, false).
		AddItem(logView, 0, 1, false).
		AddItem(progressView, 1, 0, false)

	// System monitor goroutine
	go func() {
		for {
			percent, err := cpu.Percent(0, false)
			vmem, verr := mem.VirtualMemory()
			if err != nil || len(percent) == 0 || verr != nil {
				time.Sleep(1 * time.Second)
				continue // no reading this second; never index an empty result
			}
			app.QueueUpdateDraw(func() {
				monitorView.Clear()
				fmt.Fprintf(monitorView, "[MONITOR] CPU: %.2f%% | Mem: %.2f%% (%.2f GB/%.2f GB)\n",
					percent[0],
					vmem.UsedPercent,
					float64(vmem.Used)/(1024*1024*1024),
					float64(vmem.Total)/(1024*1024*1024))
			})
			time.Sleep(1 * time.Second)
		}
	}()

	validateBufferAllocation(config)

	// The TUI owns the screen, so its log lines go to the log view and the
	// --log-path file, not to the console.
	var out io.Writer = logView
	if logFile != nil {
		out = io.MultiWriter(logView, logFile)
	}

	fmt.Fprintf(out, "[%s] [INFO] Using %d worker(s)\n", timestamp(), config.Workers)
	fmt.Fprintf(out, "[%s] [INFO] Using buffer size: %s (%d bytes)\n", timestamp(), core.HumanSize(config.BufferSize), config.BufferSize)
	fmt.Fprintf(out, "[%s] [INFO] Command: %s\n", timestamp(), config.OriginalCmd)

	if config.Validate {
		fmt.Fprintf(out, "[%s] [VALIDATE] Validation mode enabled - all files will be verified\n", timestamp())
		if config.Verbose >= 1 {
			fmt.Fprintf(out, "[%s] [VALIDATE] This will compare file sizes and content hashes\n", timestamp())
		}
	}

	var copiedBytes int64
	startTime := time.Now()
	done := make(chan struct{})

	progress := func(copied, total int64) {
		atomic.StoreInt64(&copiedBytes, copied)
	}

	// Progress bar updater goroutine
	go func() {
		for {
			select {
			case <-done:
				return
			default:
				elapsed := time.Since(startTime).Round(time.Second)
				app.QueueUpdateDraw(func() {
					progressView.Clear()
					currentCopied := atomic.LoadInt64(&copiedBytes)
					fmt.Fprintf(progressView, "Total: %.2f MB / %.2f MB %s | %s",
						float64(currentCopied)/(1024*1024), float64(totalBytes)/(1024*1024),
						core.RenderProgressBar(currentCopied, totalBytes, 40),
						elapsed)
					if currentCopied == totalBytes {
						fmt.Fprintln(progressView)
					}
				})
				time.Sleep(1 * time.Second)
			}
		}
	}()

	logger := func(format string, args ...interface{}) {
		app.QueueUpdateDraw(func() {
			fmt.Fprintf(out, format, args...)
			logView.ScrollToEnd()
		})
	}

	var failures []string
	var finished atomic.Bool

	go func() {
		result := runCopyWorkers(fileList, config.Src, config.RootDst, cache, config.copyOptions(), totalBytes, logger, progress)
		close(done)
		app.QueueUpdateDraw(func() {
			failures = result
			finished.Store(true)
			writeSummary(out, config, failures)
			fmt.Fprintf(logView, "Press any key to exit.\n")
			logView.ScrollToEnd()
			app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
				app.Stop()
				return nil
			})
		})
	}()

	if err := app.SetRoot(flex, true).EnableMouse(true).Run(); err != nil {
		fatalf(cache, "The TUI failed: %v (use --no-tui)", err)
	}

	// Run returns before the copy finished only if the user quit (e.g. Ctrl+C).
	// Files still in flight are left as temp files, never under their real name.
	if !finished.Load() {
		fmt.Fprintf(logOut, "[%s] [ERROR] Copy interrupted before it finished - destination is incomplete\n", timestamp())
		return []string{"copy interrupted by user"}
	}
	// The summary is already in the log file; repeat it on the console, which
	// the TUI just cleared.
	writeSummary(os.Stdout, config, failures)
	return failures
}
