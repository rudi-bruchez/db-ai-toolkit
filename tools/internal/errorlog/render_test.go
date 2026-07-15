package errorlog

import (
	"strings"
	"testing"
	"time"
)

func sampleReport() Report {
	t0 := time.Date(2026, 6, 12, 15, 28, 0, 0, time.UTC)
	return Report{
		Summary: InstanceSummary{Product: "Microsoft SQL Server 2022 16.0.4255.1",
			Edition: "Standard Edition", OS: "Windows Server 2022", CPU: "2 sockets, 16 logical processors",
			RAMMB: 65535, AuthMode: "MIXED",
			Collation: "French_CI_AS", FirstTime: t0, LastTime: t0.Add(72 * time.Hour), StartTime: t0},
		Advisories:   []Advisory{{ID: "backup", Message: "105575 backups ... TF 3226", URL: "https://x"}},
		Total:        108713,
		Kept:         2,
		Dropped:      108711,
		DroppedByCat: map[string]int{"backup": 105575},
		Events: []Event{
			{First: t0, Last: t0.Add(2 * time.Hour), Count: 128, Text: "Login failed for user 'DB_1'"},
			{First: t0, Last: t0, Count: 1, Text: "2026-06-13 00:00:00 Erreur : 824, gravité : 24\ndetail: incorrect checksum, page (1:2571), file 'D:\\x.mdf'"},
		},
	}
}

func TestRenderText(t *testing.T) {
	out := Render(sampleReport(), RenderOptions{Format: "text", ShowSummary: true})
	for _, want := range []string{"=== INSTANCE ===", "=== ADVISORIES ===", "=== COUNTS ===", "=== EVENTS ===", "[×128]", "TF 3226"} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q\n---\n%s", want, out)
		}
	}
	// A singleton event keeps its full multi-line detail; the continuation
	// line carries the diagnostic value and must survive rendering.
	if !strings.Contains(out, "detail: incorrect checksum") {
		t.Errorf("text output dropped singleton detail line\n---\n%s", out)
	}
	// Continuation lines are indented so they don't read as new events.
	if !strings.Contains(out, "\n  detail: incorrect checksum") {
		t.Errorf("text output did not indent continuation line\n---\n%s", out)
	}
}

func TestRenderMarkdown(t *testing.T) {
	out := Render(sampleReport(), RenderOptions{Format: "md", ShowSummary: true})
	if !strings.Contains(out, "## Instance") || !strings.Contains(out, "| ") {
		t.Errorf("markdown output malformed:\n%s", out)
	}
	// OS and CPU rows must be present in the instance table.
	for _, want := range []string{"| OS |", "| CPU |"} {
		if !strings.Contains(out, want) {
			t.Errorf("markdown output missing %q\n---\n%s", want, out)
		}
	}
	// Events render inside a fenced code block, preserving multi-line detail
	// verbatim and keeping CommonMark from misparsing '#'/'|'/'-' lines.
	if !strings.Contains(out, "```") {
		t.Errorf("markdown events not wrapped in a code fence\n---\n%s", out)
	}
	if !strings.Contains(out, "detail: incorrect checksum") {
		t.Errorf("markdown output dropped singleton detail line\n---\n%s", out)
	}
}
