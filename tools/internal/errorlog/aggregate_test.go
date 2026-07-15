package errorlog

import "testing"

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
