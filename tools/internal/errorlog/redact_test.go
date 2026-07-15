package errorlog

import (
	"strings"
	"testing"
)

func TestRedactConsistentTokens(t *testing.T) {
	r := NewRedactor()
	lines := []string{
		"Log was backed up. Database: sales.",
		"Login failed for user 'ACME\\svc'. [CLIENT : 10.0.0.5]",
		"Log was backed up. Database: sales.",
	}
	for _, l := range lines {
		r.Scan(l)
	}
	out0 := r.Apply(lines[0])
	out2 := r.Apply(lines[2])
	if out0 != out2 {
		t.Errorf("same DB must map identically: %q vs %q", out0, out2)
	}
	if strings.Contains(out0, "sales") {
		t.Errorf("db name not redacted: %q", out0)
	}
	out1 := r.Apply(lines[1])
	if strings.Contains(out1, "ACME\\svc") || strings.Contains(out1, "10.0.0.5") {
		t.Errorf("login/IP not redacted: %q", out1)
	}
	if len(r.Legend()) == 0 {
		t.Error("legend empty")
	}
}
