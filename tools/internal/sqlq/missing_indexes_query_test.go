package sqlq

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The missing-indexes query promises bounds and filters that nothing else
// checks before a live run: a later edit that drops one still passes the
// guard and still returns plausible rows. This pins the text that carries
// each promise. Correctness is proven on an instance, not here.
func TestMissingIndexesQueryContract(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(bundledQueriesDir, "missing-indexes.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if r := refusals(string(body)); len(r) > 0 {
		t.Fatalf("sqlq would refuse the query: %s", strings.Join(r, "; "))
	}
	code := strings.ToUpper(Sanitize(string(body)))
	norm := regexp.MustCompile(`\s+`).ReplaceAllString(code, " ")

	// Each pattern is anchored at both ends: a bare substring would let
	// "<= 8" pass as "<= 80" and ">= 500" as ">= 5000".
	mustMatch := []struct{ pattern, why string }{
		{`\bTOP \(70\)`, "the final bound equal to -maxrows 70"},
		{`\bTOP \(3\)`, "three ranked tables"},
		{`\bMISS_SEQ <= 8\b`, "eight suggestions per table"},
		{`\bIDX_SEQ <= 15\b`, "fifteen indexes per table"},
		{`\bD\.DATABASE_ID = DB_ID\(\)`, "suggestions restricted to the current database"},
		{`\bUS\.DATABASE_ID = DB_ID\(\)`, "usage restricted to the current database"},
		{`>= 500\b`, "the collection cap threshold"},
		{`\bOPTION \(RECOMPILE, MAXDOP 1\)`, "the query hints of the source scripts"},
	}
	for _, m := range mustMatch {
		if !regexp.MustCompile(m.pattern).MatchString(norm) {
			t.Errorf("missing %s (%s)", m.pattern, m.why)
		}
	}

	forbidden := []string{`\bDECIMAL\b`, `\bNUMERIC\b`, `\bSTRING_AGG\b`, `\bDECLARE\b`, `\bOBJECT_NAME\s*\(`, `\bROUND\s*\(\s*CAST\s*\(\s*DATEDIFF\b`}
	for _, pattern := range forbidden {
		if regexp.MustCompile(pattern).MatchString(norm) {
			t.Errorf("the query matches %s, which the spec rules out", pattern)
		}
	}
}
