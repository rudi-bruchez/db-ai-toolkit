package main

import (
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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

// Option values that would otherwise be accepted and then quietly mean
// something else. -maxrows -1 reached NewRowSet, where "<= 0" means unlimited,
// so a typo silently removed the cap the flag exists to impose; -timeout -1
// built an already-expired context, so the query was cancelled before it was
// sent and the error said nothing about why.
func TestOptionsRejectNonsenseValues(t *testing.T) {
	cases := []struct {
		name string
		o    options
		want string
	}{
		{"negative maxrows", options{maxRows: -1, timeoutSec: 30}, "-maxrows"},
		{"negative timeout", options{maxRows: 50, timeoutSec: -1}, "-timeout"},
		{"zero timeout", options{maxRows: 50, timeoutSec: 0}, "-timeout"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.o.validate()
			if err == nil {
				t.Fatalf("%+v should be refused", tc.o)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q should name %s", err, tc.want)
			}
		})
	}
	// 0 means unlimited for maxrows, and that is documented.
	if err := (options{maxRows: 0, timeoutSec: 30}).validate(); err != nil {
		t.Errorf("-maxrows 0 is the documented way to ask for unlimited: %v", err)
	}
}
