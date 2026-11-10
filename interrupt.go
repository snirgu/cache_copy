// Ctrl+C handling: the first interrupt stops the run cleanly, a second one
// quits at once.

package main

import (
	"cache_copy/core"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
)

// exitInterrupted is the exit code of an interrupted run (128 + SIGINT, the
// usual shell convention), so scripts can tell it from a copy failure (1).
const exitInterrupted = 130

// interrupted is set by the first Ctrl+C (or termination signal). Workers
// start no new files, copies in progress stop at their next chunk, and the
// run ends through exitIfInterrupted.
var interrupted atomic.Bool

var errInterrupted = errors.New("interrupted by user")

// handleInterrupts installs the Ctrl+C handler. In the TUI, Ctrl+C is a key
// press the TUI handles itself; this covers plain output and kill/SIGTERM.
func handleInterrupts() {
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		interrupted.Store(true)
		fmt.Fprintf(logOut, "\n[%s] [WARN] Interrupt received: stopping safely (files being copied are discarded, the cache is saved). Press Ctrl+C again to quit immediately.\n", timestamp())
		<-sig
		fmt.Fprintf(logOut, "[%s] [ERROR] Second interrupt: quitting immediately. Unfinished *%s files may remain; the next run replaces them.\n", timestamp(), tmpSuffix)
		os.Exit(exitInterrupted)
	}()
}

// interruptibleReader fails with errInterrupted once the run is interrupted,
// so a copy in progress stops at its next chunk instead of finishing a large
// file first.
type interruptibleReader struct{ r io.Reader }

func (ir interruptibleReader) Read(p []byte) (int, error) {
	if interrupted.Load() {
		return 0, errInterrupted
	}
	return ir.r.Read(p)
}

// exitIfInterrupted ends an interrupted run: it saves the cache (every entry
// in it is a complete, verified copy) and exits with exitInterrupted.
func exitIfInterrupted(cache *core.GlobalCache) {
	if !interrupted.Load() {
		return
	}
	fmt.Fprintf(logOut, "[%s] [ERROR] Interrupted: the destination is incomplete. Files already copied are complete, and no partial file was left under a real name. Run again to finish.\n", timestamp())
	if cache != nil {
		saveCache(cache)
	}
	os.Exit(exitInterrupted)
}
