package main

import (
	"testing"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/sqlq"
)

// The refactoring must not change a single exit code of the existing command.
func TestGuardKeepsExitCodes(t *testing.T) {
	ro := sqlq.Profile{Name: "ro"}
	rw := sqlq.Profile{Name: "rw", Mode: sqlq.ModeReadWrite}
	cases := []struct {
		name  string
		sql   string
		p     sqlq.Profile
		allow bool
		want  int
	}{
		{"read", "SELECT 1;", ro, false, exitOK},
		{"write on readonly", "DELETE FROM dbo.T;", ro, true, exitRefused},
		{"write without -allow-write", "DELETE FROM dbo.T;", rw, false, exitRefused},
		{"write allowed", "DELETE FROM dbo.T;", rw, true, exitOK},
		{"use", "USE tempdb;", ro, false, exitRefused},
		{"go", "SELECT 1;\nGO\n", ro, false, exitUsage},
		{"write allowed then go", "DELETE FROM dbo.T;\nGO\n", rw, true, exitUsage},
	}
	for _, c := range cases {
		code, err := guard(c.sql, c.p, c.allow)
		if code != c.want || (code == exitOK) != (err == nil) {
			t.Errorf("%s: code %d err %v, want %d", c.name, code, err, c.want)
		}
	}
}
