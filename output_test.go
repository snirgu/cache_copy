package main

import (
	"cache_copy/core"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

// withLogSetup runs setupLog and restores the logging globals afterwards.
func withLogSetup(t *testing.T) {
	t.Helper()
	origOut, origFile := logOut, logFile
	t.Cleanup(func() { logOut, logFile = origOut, origFile; startupLog.Reset() })
	if err := setupLog(""); err != nil {
		t.Fatal(err)
	}
}

// Lines logged before the copy are recorded once, then recording stops.
func TestStartupLogIsRecordedUntilCopyStarts(t *testing.T) {
	withLogSetup(t)
	io.WriteString(logOut, "before copy\n")
	if got := takeStartupLog(); got != "before copy\n" {
		t.Fatalf("recorded %q", got)
	}
	io.WriteString(logOut, "during copy\n")
	if got := takeStartupLog(); got != "" {
		t.Fatalf("still recording after the copy started: %q", got)
	}
}

// screenText returns what a simulated screen shows, one line per row.
func screenText(s tcell.SimulationScreen) string {
	cells, w, h := s.GetContents()
	var b strings.Builder
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if r := cells[y*w+x].Runes; len(r) > 0 {
				b.WriteRune(r[0])
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// unwrapped joins the screen's rows without spaces, undoing the TUI's word
// wrapping (which turns a space into a line break) so a long line, like one
// with a full path, can be searched as a whole. Compare with noSpaces(want).
func unwrapped(text string) string {
	return noSpaces(strings.ReplaceAll(text, "\n", ""))
}

func noSpaces(s string) string { return strings.ReplaceAll(s, " ", "") }

// The TUI shows the cache file line (and the rest of the startup log), as
// --no-tui does, instead of hiding it when it takes over the screen.
func TestTUIShowsCacheFileLine(t *testing.T) {
	withLogSetup(t)
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	tuiScreen = screen
	t.Cleanup(func() { tuiScreen = nil })

	srcDir, dstDir := t.TempDir(), t.TempDir()
	writeRandom(t, filepath.Join(srcDir, "a.bin"), 1000)
	cachePath := filepath.Join(t.TempDir(), "job_cache.json")
	cache := core.NewGlobalCache(cachePath)
	logCacheLoad(cache) // logged before the TUI starts, as in main

	config := &Config{Src: srcDir, RootDst: dstDir, Workers: 1, BufferSize: 4096, Retry: true}
	done := make(chan []string)
	go func() { done <- runTUIMode(cache, config, []string{"a.bin"}, 1000) }()

	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(screenText(screen), "Press any key to exit") {
		if time.Now().After(deadline) {
			t.Fatalf("TUI never finished; screen:\n%s", screenText(screen))
		}
		time.Sleep(20 * time.Millisecond)
	}
	text := screenText(screen)
	screen.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	<-done

	flat := unwrapped(text)
	if !strings.Contains(flat, noSpaces("[INFO] No cache yet, starting fresh (will be saved to "+cachePath+")")) {
		t.Fatalf("cache file line (with its [INFO] level) not shown in the TUI; screen:\n%s", text)
	}
	if strings.Count(flat, noSpaces("No cache yet")) != 1 {
		t.Fatalf("cache line shown more than once; screen:\n%s", text)
	}
	if !strings.Contains(flat, noSpaces("[INFO] Copy process completed.")) || !strings.Contains(flat, noSpaces("[MONITOR] CPU:")) {
		t.Fatalf("log levels missing from TUI lines; screen:\n%s", text)
	}
	if _, err := os.Stat(filepath.Join(dstDir, "a.bin")); err != nil {
		t.Fatal("file was not copied")
	}
}
