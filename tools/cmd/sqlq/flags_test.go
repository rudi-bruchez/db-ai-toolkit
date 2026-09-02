package main

import (
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// A flag nobody wrote down is a flag the operator cannot use, and a flag the
// documentation invents is worse: it is a promise the binary does not keep.
// Both classes have already been found by eye in this tool once; this walks the
// flag set instead, so they cannot come back silently.
func TestEveryFlagIsDocumented(t *testing.T) {
	docs := []string{
		filepath.Join("..", "..", "..", "plugins", "sqlserver-toolkit", "README.md"),
		filepath.Join("..", "..", "..", "plugins", "sqlserver-toolkit", "skills", "live-query", "SKILL.md"),
	}

	fs := flag.NewFlagSet("sqlq", flag.ContinueOnError)
	defineFlags(fs)

	for _, doc := range docs {
		text, err := os.ReadFile(doc)
		if err != nil {
			t.Fatalf("reading %s: %v", doc, err)
		}
		fs.VisitAll(func(f *flag.Flag) {
			// \b so that -profile is not considered documented by a mention
			// of -profiles.
			if !regexp.MustCompile(`-` + regexp.QuoteMeta(f.Name) + `\b`).Match(text) {
				t.Errorf("flag -%s is not documented in %s", f.Name, doc)
			}
		})
	}
}
