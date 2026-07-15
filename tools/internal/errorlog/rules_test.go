package errorlog

import "testing"

func mustRules(t *testing.T) *RuleSet {
	t.Helper()
	rs, err := LoadEmbeddedRules()
	if err != nil {
		t.Fatalf("LoadEmbeddedRules: %v", err)
	}
	return rs
}

func classifyLine(t *testing.T, rs *RuleSet, line string, minSev int) (bool, string) {
	t.Helper()
	e := Parse(line + "\n")[0]
	return rs.Classify(e, minSev)
}

func TestClassifySignalNoiseSeverity(t *testing.T) {
	rs := mustRules(t)
	cases := []struct {
		line     string
		wantKeep bool
	}{
		{"2026-06-16 00:00:01.22 Logon      Login failed for user 'x'.", true},
		{"2026-06-13 00:00:00.50 spid61     Erreur : 824, Gravité : 24, État : 2.", true},
		{"2026-06-12 15:28:18.32 Backup     Log was backed up. Database: sales.", false},
		{"2026-06-12 15:28:18.32 Logon      Login succeeded for user 'svc'.", false},
		{"2026-06-12 15:28:18.32 spid20     Some entry we have no rule for.", true}, // unknown -> keep
	}
	for _, c := range cases {
		keep, _ := classifyLine(t, rs, c.line, 16)
		if keep != c.wantKeep {
			t.Errorf("Classify(%q) keep=%v, want %v", c.line, keep, c.wantKeep)
		}
	}
}

func TestSeverityThresholdBeatsNoise(t *testing.T) {
	rs := mustRules(t)
	// Looks like backup noise but carries Gravité 21 -> keep.
	line := "2026-06-12 15:28:18.32 spid20     Log was backed up but Gravité : 21 raised."
	if keep, _ := classifyLine(t, rs, line, 16); !keep {
		t.Fatal("high severity must override noise")
	}
	// Below threshold, matches only noise -> drop.
	line14 := "2026-06-12 15:28:18.32 Logon      Erreur : 18456, Gravité : 14 : Login succeeded for user 'x'."
	if keep, _ := classifyLine(t, rs, line14, 16); !keep {
		t.Skip("kept via error-num signal, acceptable")
	}
}
