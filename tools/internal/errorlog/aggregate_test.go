package errorlog

import (
	"strings"
	"testing"
)

func TestAggregateCollapsesRepeats(t *testing.T) {
	text := ""
	for i := 0; i < 3; i++ {
		text += "2026-06-16 00:00:0" + string(rune('0'+i)) + ".00 Logon      Login failed for user 'PECHEUR\\SQL1$'. [CLIENT : 172.16.70.81]\n"
	}
	text += "2026-06-13 00:00:00.50 spid61     Erreur : 824, Gravité : 24.\n"
	events := Aggregate(Parse(text))
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	if events[0].Count != 3 {
		t.Errorf("first event count = %d, want 3", events[0].Count)
	}
	if events[1].Count != 1 {
		t.Errorf("second event count = %d, want 1", events[1].Count)
	}
	if events[0].First.After(events[0].Last) {
		t.Error("First must be <= Last")
	}
}

// TestAggregateNormalizesHexAddresses proves the hex normalization is
// load-bearing: two entries differing ONLY in a hex address (in a hex *letter*,
// so the digit pass alone cannot collapse them) must aggregate into one event.
// Removing reHex makes this fail.
func TestAggregateNormalizesHexAddresses(t *testing.T) {
	text := "2026-06-16 00:00:00.00 spid61     Non-yielding scheduler at 0x00000000ABCDEFAB detected.\n" +
		"2026-06-16 00:00:01.00 spid61     Non-yielding scheduler at 0x00000000ABCDEFAC detected.\n"
	events := Aggregate(Parse(text))
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1 (hex addresses should normalize together)", len(events))
	}
	if events[0].Count != 2 {
		t.Errorf("count = %d, want 2", events[0].Count)
	}
}

// TestAggregateNormalizesIPAddresses proves IP normalization collapses entries
// that differ ONLY in a client IP address. Because the trailing digit pass
// (reDigit) would also reduce a dotted quad to "#.#.#.#", the Count assertion
// alone is not load-bearing for reIP; the signature-token assertions below make
// it so — they fail if reIP is removed (the IP would then read "#.#.#.#" rather
// than the dedicated "ipaddr" placeholder).
func TestAggregateNormalizesIPAddresses(t *testing.T) {
	text := "2026-06-16 00:00:00.00 Logon      Login failed for user 'sa'. [CLIENT : 172.16.70.81]\n" +
		"2026-06-16 00:00:01.00 Logon      Login failed for user 'sa'. [CLIENT : 10.0.0.254]\n"
	entries := Parse(text)
	events := Aggregate(entries)
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1 (IP addresses should normalize together)", len(events))
	}
	if events[0].Count != 2 {
		t.Errorf("count = %d, want 2", events[0].Count)
	}
	sig := signature(entries[0])
	if !strings.Contains(sig, "ipaddr") {
		t.Errorf("signature %q missing the reIP placeholder %q", sig, "ipaddr")
	}
	if strings.Contains(sig, "#.#.#.#") {
		t.Errorf("signature %q normalized the IP via the digit pass, not reIP", sig)
	}
}

// TestAggregateNormalizesDecimalNumbers proves the digit normalization is
// load-bearing: two entries differing ONLY in a decimal number must aggregate
// into one event. Removing reDigit makes this fail.
func TestAggregateNormalizesDecimalNumbers(t *testing.T) {
	text := "2026-06-16 00:00:00.00 spid61     Erreur : 824, Gravité : 24.\n" +
		"2026-06-16 00:00:01.00 spid61     Erreur : 825, Gravité : 24.\n"
	events := Aggregate(Parse(text))
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1 (decimal numbers should normalize together)", len(events))
	}
	if events[0].Count != 2 {
		t.Errorf("count = %d, want 2", events[0].Count)
	}
}
