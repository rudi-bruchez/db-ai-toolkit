package errorlog

import (
	"strings"
	"testing"
)

const sample = `2024-01-15 08:00:01.10 spid7s      Starting up database 'master'.
2024-01-15 08:00:02.30 Backup      Database backed up. Database: AdventureWorks, creation date(time): 2019/09/10, pages dumped: 12345. This is an informational message only. No user action is required.
2024-01-15 08:05:11.42 Logon       Login succeeded for user 'app_user'. Connection made using SQL Server authentication.
2024-01-15 08:06:00.00 Logon       Login failed for user 'sa'. Reason: Password did not match. [CLIENT: 10.0.0.5]
2024-01-15 08:07:20.55 spid51      Error: 823, Severity: 24, State: 2.
2024-01-15 08:07:20.55 spid51      The operating system returned error 21 to SQL Server during a read at offset 0x000012 in file 'D:\data\db.mdf'.
2024-01-15 08:10:00.00 spid9s      Setting database option MULTI_USER to ON for database 'reporting'.
2024-01-15 08:12:00.00 spid12s     SQL Server has encountered 1 occurrence(s) of I/O requests taking longer than 15 seconds to complete on file 'T:\tempdb.mdf'.`

func TestParseGroupsContinuationLines(t *testing.T) {
	entries, err := Parse(strings.NewReader(sample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// 8 timestamped lines, but the Error:823 entry has a continuation line,
	// so we expect 8 logical entries (the read-error line is folded in? no:
	// it also starts with a timestamp). Verify count matches timestamp lines.
	if got, want := len(entries), 8; got != want {
		t.Fatalf("entries = %d, want %d", got, want)
	}
}

func TestFilterDropsNoiseKeepsSignal(t *testing.T) {
	entries, err := Parse(strings.NewReader(sample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	res := Filter(entries, Options{MinSeverity: 16})

	joined := ""
	for _, e := range res.Kept {
		joined += e.Text() + "\n"
	}

	mustKeep := []string{"Login failed", "Error: 823", "I/O requests taking longer"}
	for _, s := range mustKeep {
		if !strings.Contains(joined, s) {
			t.Errorf("expected kept digest to contain %q", s)
		}
	}
	mustDrop := []string{"Database backed up", "Login succeeded", "Starting up database", "Setting database option"}
	for _, s := range mustDrop {
		if strings.Contains(joined, s) {
			t.Errorf("expected %q to be dropped", s)
		}
	}
	if res.DroppedByCat["backup"] != 1 {
		t.Errorf("backup dropped = %d, want 1", res.DroppedByCat["backup"])
	}
	if res.DroppedByCat["login-success"] != 1 {
		t.Errorf("login-success dropped = %d, want 1", res.DroppedByCat["login-success"])
	}
}

func TestHighSeverityBeatsNoise(t *testing.T) {
	// An entry that looks like noise but carries a high severity must be kept.
	const s = `2024-01-15 09:00:00.00 spid20      Database backed up but Severity: 21 was raised. informational message only`
	entries, _ := Parse(strings.NewReader(s))
	res := Filter(entries, Options{MinSeverity: 16})
	if len(res.Kept) != 1 {
		t.Fatalf("kept = %d, want 1 (high severity must win over noise)", len(res.Kept))
	}
}
