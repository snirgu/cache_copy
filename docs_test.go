package main

import (
	"flag"
	"io"
	"os"
	"strings"
	"testing"
)

// Every option the program accepts must be described in --help and in the
// README, so the docs cannot silently fall behind the code.
func TestDocsCoverEveryOption(t *testing.T) {
	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	help := usageText()

	fs := flag.NewFlagSet("cache_copy", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	defineFlags(fs)
	count := 0
	fs.VisitAll(func(f *flag.Flag) {
		count++
		if !strings.Contains(help, "-"+f.Name+"\n") && !strings.Contains(help, "-"+f.Name+" ") {
			t.Errorf("--help does not describe -%s", f.Name)
		}
		if !strings.Contains(string(readme), "--"+f.Name) {
			t.Errorf("README does not describe --%s", f.Name)
		}
	})
	if count < 15 {
		t.Fatalf("only %d options found; defineFlags changed?", count)
	}
}

// The help and the README both list the exit codes scripts depend on.
func TestDocsListExitCodes(t *testing.T) {
	readme, _ := os.ReadFile("README.md")
	for _, doc := range []struct{ name, text string }{{"--help", usageText()}, {"README", string(readme)}} {
		if !strings.Contains(doc.text, "130") {
			t.Errorf("%s does not document exit code 130", doc.name)
		}
		if !strings.Contains(strings.ToLower(doc.text), "not copied") {
			t.Errorf("%s does not say what is not copied", doc.name)
		}
	}
}
