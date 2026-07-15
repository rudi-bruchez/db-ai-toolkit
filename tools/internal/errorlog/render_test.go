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
			Edition: "Standard Edition", RAMMB: 65535, AuthMode: "MIXED",
			Collation: "French_CI_AS", FirstTime: t0, LastTime: t0.Add(72 * time.Hour), StartTime: t0},
		Advisories:   []Advisory{{ID: "backup", Message: "105575 backups ... TF 3226", URL: "https://x"}},
		Total:        108713,
		Kept:         2,
		Dropped:      108711,
		DroppedByCat: map[string]int{"backup": 105575},
		Events: []Event{
			{First: t0, Last: t0.Add(2 * time.Hour), Count: 128, Text: "Login failed for user 'DB_1'"},
			{First: t0, Last: t0, Count: 1, Text: "2026-06-13 00:00:00 Erreur : 824"},
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
}

func TestRenderMarkdown(t *testing.T) {
	out := Render(sampleReport(), RenderOptions{Format: "md", ShowSummary: true})
	if !strings.Contains(out, "## Instance") || !strings.Contains(out, "| ") {
		t.Errorf("markdown output malformed:\n%s", out)
	}
}
