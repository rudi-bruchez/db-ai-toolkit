package errorlog

import "testing"

const bootSample = "2026-06-12 15:28:18.32 Server     Microsoft SQL Server 2022 (RTM-CU25) (KB5081477) - 16.0.4255.1 (X64)\n" +
	"2026-06-12 15:28:18.32 Server     Standard Edition (64-bit) on Windows Server 2025 Datacenter\n" +
	"2026-06-12 15:28:18.33 Server     UTC adjustment: 2:00\n" +
	"2026-06-12 15:28:18.34 Server     Authentication mode is MIXED.\n" +
	"2026-06-12 15:28:18.37 Server     SQL Server detected 1 sockets with 8 cores per socket and 8 logical processors per socket, 8 total logical processors.\n" +
	"2026-06-12 15:28:18.38 Server     Detected 65535 MB of RAM, 60393 MB of available memory.\n" +
	"2026-06-12 15:28:18.66 Server     Default collation: French_CI_AS (Français 1036)\n" +
	"2026-06-12 15:28:20.53 spid35s    Server is listening on [ 'any' <ipv4> 1433] accept sockets 1.\n" +
	"2026-06-12 15:28:19.86 Server     Database Instant File Initialization: activé.\n" +
	"2026-06-12 15:28:21.10 Server     The SQL Server Network Interface library successfully registered the Service Principal Name (SPN) [ MSSQLSvc/host.corp.local:1433 ] for the SQL Server service.\n" +
	"2026-06-16 09:12:00.00 Backup     Log was backed up. Database: sales.\n"

func TestSummarizeExtractsCoreFacts(t *testing.T) {
	s := Summarize(Parse(bootSample))
	if s.RAMMB != 65535 {
		t.Errorf("RAMMB = %d, want 65535", s.RAMMB)
	}
	if s.AuthMode != "MIXED" {
		t.Errorf("AuthMode = %q, want MIXED", s.AuthMode)
	}
	if s.Collation != "French_CI_AS" {
		t.Errorf("Collation = %q", s.Collation)
	}
	if s.StartTime.IsZero() || s.FirstTime.IsZero() || s.LastTime.IsZero() {
		t.Error("timestamps not set")
	}
	if s.LastTime.Before(s.FirstTime) {
		t.Error("LastTime before FirstTime")
	}
	if len(s.TCPPorts) == 0 || s.TCPPorts[0] != "1433" {
		t.Errorf("TCPPorts = %v, want [1433 ...]", s.TCPPorts)
	}
	if s.IFI == "" {
		t.Error("IFI not detected")
	}
	if s.OS == "" {
		t.Errorf("OS = %q, want non-empty (e.g. Windows Server 2025 Datacenter)", s.OS)
	}
	if s.CPU == "" {
		t.Errorf("CPU = %q, want non-empty (e.g. 1 sockets ... 8 total logical processors)", s.CPU)
	}
	if s.SPN == "" {
		t.Errorf("SPN = %q, want non-empty Service Principal Name line", s.SPN)
	}
}
