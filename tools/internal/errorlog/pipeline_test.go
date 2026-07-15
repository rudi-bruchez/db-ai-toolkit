package errorlog

import (
	"strings"
	"testing"
)

func TestProcessEndToEnd(t *testing.T) {
	raw := encodeUTF16LE(
		"2026-06-12 15:28:18.32 Server     Microsoft SQL Server 2022 (RTM-CU25) - 16.0.4255.1 (X64)\r\n" +
			"2026-06-12 15:28:18.34 Server     Authentication mode is MIXED.\r\n" +
			"2026-06-12 15:28:18.38 Server     Detected 65535 MB of RAM.\r\n" +
			"2026-06-12 15:28:18.66 Server     Default collation: French_CI_AS\r\n" +
			"2026-06-12 15:28:19.86 Server     Database Instant File Initialization: désactivé.\r\n" +
			backups(600) +
			"2026-06-16 00:00:01.22 Logon      Login failed for user 'ACME\\SQL1$'. [CLIENT : 10.0.0.5]\r\n" +
			"2026-06-13 00:00:00.50 spid61     Erreur : 824, Gravité : 24, État : 2.\r\n")

	out, err := Process([]Source{{Name: "t", Data: raw}}, Options{
		MinSeverity: 16, Format: "text", Aggregate: true, ShowSummary: true,
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	for _, want := range []string{"French_CI_AS", "trace flag 3226", "Instant File Initialization", "Login failed", "Erreur", "backup="} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q\n---\n%s", want, out)
		}
	}
	if strings.Contains(out, "Log was backed up") {
		t.Error("backup noise leaked into events")
	}
}

func backups(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString("2026-06-15 02:30:54.84 Backup     Log was backed up. Database: sales.\r\n")
	}
	return b.String()
}
