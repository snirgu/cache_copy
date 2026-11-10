package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain lets tests run the real program: started with CACHE_COPY_RUN_MAIN=1,
// the test binary behaves as cache_copy itself, so exit codes, output and
// on-disk results are checked exactly as a script would see them.
func TestMain(m *testing.M) {
	if os.Getenv("CACHE_COPY_RUN_MAIN") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runProgram runs cache_copy with args in dir and returns its exit code and
// combined output. Every run gets its own cache folder.
func runProgram(t *testing.T, dir string, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], append(args, "--cache-dir", filepath.Join(dir, "cache"))...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CACHE_COPY_RUN_MAIN=1")
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		return exitErr.ExitCode(), string(out)
	case err != nil:
		t.Fatalf("could not run the program: %v", err)
	}
	return 0, string(out)
}

// newTree creates src/ with a few files in a fresh folder and returns the folder.
func newTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, rel := range []string{"a.bin", filepath.Join("sub", "b.bin"), filepath.Join("sub", "deep", "c.bin")} {
		p := filepath.Join(dir, "src", rel)
		os.MkdirAll(filepath.Dir(p), 0755)
		writeRandom(t, p, 20000)
	}
	return dir
}

// sameTree reports whether every file under a exists under b with identical content.
func sameTree(t *testing.T, a, b string) bool {
	t.Helper()
	same := true
	filepath.Walk(a, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(a, p)
		want, _ := os.ReadFile(p)
		got, err := os.ReadFile(filepath.Join(b, rel))
		if err != nil || !bytes.Equal(got, want) {
			t.Logf("differs: %s", rel)
			same = false
		}
		return nil
	})
	return same
}

func TestExitCodes(t *testing.T) {
	t.Run("successful copy exits 0", func(t *testing.T) {
		dir := newTree(t)
		code, out := runProgram(t, dir, "src", "dst")
		if code != 0 || !sameTree(t, filepath.Join(dir, "src"), filepath.Join(dir, "dst", "src")) {
			t.Fatalf("code %d, output:\n%s", code, out)
		}
		// Not a terminal, so the TUI must have been skipped on its own.
		if !strings.Contains(out, "Not running in an interactive terminal") {
			t.Errorf("TUI not auto-disabled:\n%s", out)
		}
		code, out = runProgram(t, dir, "src", "dst")
		if code != 0 || !strings.Contains(out, "Loaded cache file") {
			t.Fatalf("second run: code %d, output:\n%s", code, out)
		}
	})

	t.Run("--validate success exits 0", func(t *testing.T) {
		dir := newTree(t)
		if code, out := runProgram(t, dir, "src", "dst", "--validate"); code != 0 {
			t.Fatalf("code %d, output:\n%s", code, out)
		}
	})

	t.Run("dry run exits 0 and writes nothing", func(t *testing.T) {
		dir := newTree(t)
		code, out := runProgram(t, dir, "--dry-run", "src", "dst", "--mirror")
		if code != 0 {
			t.Fatalf("code %d, output:\n%s", code, out)
		}
		if _, err := os.Stat(filepath.Join(dir, "dst")); !os.IsNotExist(err) {
			t.Fatal("dry run created the destination")
		}
		if _, err := os.Stat(filepath.Join(dir, "cache")); !os.IsNotExist(err) {
			t.Fatal("dry run created the cache folder")
		}
	})

	// A file that cannot be put in place: the destination already has a
	// non-empty directory with the file's name. The run must stop, list the
	// file, and exit 1.
	t.Run("a file that cannot be copied exits 1", func(t *testing.T) {
		dir := newTree(t)
		blocker := filepath.Join(dir, "dst", "src", "a.bin", "inside")
		os.MkdirAll(filepath.Dir(blocker), 0755)
		os.WriteFile(blocker, []byte("x"), 0644)
		code, out := runProgram(t, dir, "src", "dst", "--retry=false", "--workers", "1")
		if code != 1 || !strings.Contains(out, "NOT copied faithfully") || !strings.Contains(out, "a.bin") {
			t.Fatalf("code %d, output:\n%s", code, out)
		}
	})

	for _, c := range []struct {
		name string
		args []string
		code int
		says string
	}{
		{"missing source exits 1", []string{"nosuchdir", "dst"}, 1, "Error gathering file list"},
		{"destination inside source exits 1", []string{"src", filepath.Join("src", "inner")}, 1, "inside the source"},
		{"too many arguments exits 1", []string{"src", "dst", "extra"}, 1, "Too many arguments"},
		{"missing destination exits 1", []string{"src"}, 1, "Usage:"},
		{"unknown flag exits 2", []string{"src", "dst", "--nope"}, 2, "flag provided but not defined"},
		{"bad buffer size exits 1", []string{"src", "dst", "--buffer-size", "1.5MB"}, 1, "Invalid buffer size"},
		{"--help exits 0", []string{"--help"}, 0, "Usage:"},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, out := runProgram(t, newTree(t), c.args...)
			if code != c.code || !strings.Contains(out, c.says) {
				t.Fatalf("code %d (want %d), output should contain %q:\n%s", code, c.code, c.says, out)
			}
		})
	}
}
