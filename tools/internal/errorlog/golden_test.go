package errorlog

import (
	"os"
	"path/filepath"
	"testing"
)

// TestGoldenDigest is an end-to-end regression test: it decodes a synthetic
// UTF-16LE ERRORLOG fixture, runs it through the full Process pipeline, and
// compares the rendered text digest against a committed golden file. This
// catches regressions in the interaction between decode, parse, rules,
// aggregation, advisories, and rendering that unit tests for individual
// stages cannot see.
func TestGoldenDigest(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("testdata", "ERRORLOG_synthetic.txt"))
	if err != nil {
		t.Fatal(err)
	}
	raw := encodeUTF16LE(string(src)) // exercise the UTF-16 decode path

	got, err := Process([]Source{{Name: "synthetic", Data: raw}}, Options{
		MinSeverity: 16, Format: "text", Aggregate: true, ShowSummary: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	goldenPath := filepath.Join("testdata", "expected_text.txt")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("golden mismatch. Re-run with UPDATE_GOLDEN=1 to refresh if intended.\n--- got ---\n%s", got)
	}
}
