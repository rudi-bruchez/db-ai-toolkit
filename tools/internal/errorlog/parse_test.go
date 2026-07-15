package errorlog

import "testing"

func TestParseGroupsContinuations(t *testing.T) {
	// The "Detail line" starts with a timestamp too, so it is its own entry.
	// Build an explicit continuation case with a TAB-prefixed line instead.
	text := "2026-06-12 15:28:18.32 Server     Registry startup parameters:\n" +
		"\t -d F:\\master.mdf\n" +
		"\t -e F:\\ERRORLOG\n" +
		"2026-06-12 15:28:19.00 Server     Next.\n"
	entries := Parse(text)
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	if len(entries[0].Lines) != 3 {
		t.Fatalf("first entry lines = %d, want 3 (1 + 2 continuations)", len(entries[0].Lines))
	}
	if entries[0].Source != "Server" {
		t.Fatalf("source = %q, want Server", entries[0].Source)
	}
	if entries[0].Time.IsZero() {
		t.Fatal("timestamp not parsed")
	}
}

func TestParseMessageStripsPrefix(t *testing.T) {
	e := Parse("2026-06-12 15:28:18.32 Logon      Login failed for user 'x'.\n")[0]
	if got := e.Message(); got != "Login failed for user 'x'." {
		t.Fatalf("Message = %q", got)
	}
}

func TestParseEmptyInput(t *testing.T) {
	if entries := Parse(""); len(entries) != 0 {
		t.Fatalf("entries = %d, want 0", len(entries))
	}
}

func TestParseTrimsTrailingBlankLines(t *testing.T) {
	// Trailing blank/whitespace-only continuation lines are trimmed from the
	// entry's Lines. The final "\n" plus blank lines must not add a stray entry.
	text := "2026-06-12 15:28:18.32 Server     Only line.\n" +
		"\n" +
		"   \n"
	entries := Parse(text)
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if len(entries[0].Lines) != 1 {
		t.Fatalf("lines = %d, want 1 (trailing blanks trimmed)", len(entries[0].Lines))
	}
	if entries[0].Lines[0] != "2026-06-12 15:28:18.32 Server     Only line." {
		t.Fatalf("line = %q", entries[0].Lines[0])
	}
}

func TestParseBlankOnlyInputDropsEmptyEntry(t *testing.T) {
	// An entry that is nothing but blank lines is dropped entirely.
	if entries := Parse("\n\n   \n"); len(entries) != 0 {
		t.Fatalf("entries = %d, want 0", len(entries))
	}
}

func TestParsePreambleBeforeFirstTimestamp(t *testing.T) {
	// A non-timestamped line before any timestamp (the cur == nil branch) is
	// kept as its own entry, with no parsed Time or Source.
	text := "Microsoft SQL Server startup banner\n" +
		"2026-06-12 15:28:18.32 Server     First real entry.\n"
	entries := Parse(text)
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	if got := entries[0].Text(); got != "Microsoft SQL Server startup banner" {
		t.Fatalf("preamble text = %q", got)
	}
	if !entries[0].Time.IsZero() {
		t.Fatal("preamble entry should have zero Time")
	}
	if entries[0].Source != "" {
		t.Fatalf("preamble source = %q, want empty", entries[0].Source)
	}
	if entries[1].Source != "Server" {
		t.Fatalf("second source = %q, want Server", entries[1].Source)
	}
}
