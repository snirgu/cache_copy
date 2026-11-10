package core

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseSize(t *testing.T) {
	ok := map[string]int{
		"4MB": 4 << 20, "4mb": 4 << 20, "4M": 4 << 20, "256KB": 256 << 10, "512k": 512 << 10,
		"1GB": 1 << 30, "2g": 2 << 30, "1048576": 1048576, "100B": 100, " 8 MB ": 8 << 20,
	}
	for in, want := range ok {
		if got, err := ParseSize(in); err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "abc", "0", "-4MB", "4TB", "1.5MB", "MB", "99999999999999GB"} {
		if got, err := ParseSize(in); err == nil {
			t.Errorf("ParseSize(%q) = %d, want an error", in, got)
		}
	}
}

// #13: the name shows the destination as typed; the old layout is still
// computed exactly as version 1.0.3 did, so its files can be found and moved.
func TestCacheFileName(t *testing.T) {
	base := t.TempDir()
	src, dst := filepath.Join(base, "opt"), filepath.Join(base, "opt-2")
	rootDst := filepath.Join(dst, "opt")

	name := CacheFileName(src, dst, rootDst)
	if !strings.HasPrefix(name, "opt_to_opt-2_") || !strings.HasSuffix(name, ".json") {
		t.Errorf("name %q, want opt_to_opt-2_<hash>.json", name)
	}
	legacy := LegacyCacheFile(src, rootDst)
	if filepath.Dir(legacy) != ".cache_cache_copy" || !strings.HasPrefix(filepath.Base(legacy), "opt_to_opt_") {
		t.Errorf("legacy path %q, want .cache_cache_copy/opt_to_opt_<hash>.json", legacy)
	}

	if got := CacheFileName(filepath.Join(base, "נתונים"), filepath.Join(base, "my backup"), rootDst); !strings.HasPrefix(got, "נתונים_to_my_backup_") {
		t.Errorf("non-ASCII/space name: %q", got)
	}
	root := filepath.VolumeName(base) + string(filepath.Separator)
	if got := CacheFileName(src, root, root); !strings.HasPrefix(got, "opt_to_root_") {
		t.Errorf("root destination: %q", got)
	}
}

// The same folder written differently must map to the same cache, or a run
// with the other spelling would recopy everything.
func TestCacheFileNameIgnoresSpelling(t *testing.T) {
	base := t.TempDir()
	long, err := filepath.EvalSymlinks(base) // expands Windows short names
	if err != nil {
		t.Fatal(err)
	}
	src, rootDst := filepath.Join(base, "opt"), filepath.Join(base, "dst", "opt") // dst not created yet
	want := CacheFileName(src, filepath.Dir(rootDst), rootDst)

	variants := map[string][2]string{
		"long spelling": {filepath.Join(long, "opt"), filepath.Join(long, "dst", "opt")},
		"trailing dots": {filepath.Join(base, ".", "opt"), filepath.Join(base, "dst", "..", "dst", "opt")},
	}
	if runtime.GOOS == "windows" {
		variants["upper case"] = [2]string{strings.ToUpper(src), strings.ToUpper(rootDst)}
	}
	for name, v := range variants {
		if got := CacheFileName(v[0], filepath.Dir(rootDst), v[1]); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
	if base != long {
		t.Logf("checked short name %s against %s", base, long)
	}
}

func TestIsNetworkPath(t *testing.T) {
	if IsNetworkPath(t.TempDir()) {
		t.Error("local temp folder reported as network")
	}
	if IsNetworkPath(filepath.Join(t.TempDir(), "not", "created", "yet")) {
		t.Error("missing path under a local folder reported as network")
	}
	if runtime.GOOS == "windows" {
		share := `\\localhost\` + os.Getenv("SystemDrive")[:1] + `$`
		if !IsNetworkPath(share) {
			t.Errorf("%s not reported as network", share)
		}
	}
}
