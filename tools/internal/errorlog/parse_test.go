package errorlog

import "testing"

const twoEntries = "2026-06-12 15:28:18.32 spid61     Erreur : 824, Gravité : 24, État : 2.\n" +
	"2026-06-12 15:28:18.32 spid61     Detail line about page (1:2571).\n" +
	"2026-06-12 15:28:19.00 Server     Next entry.\n"

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
