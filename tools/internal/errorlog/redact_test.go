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

// TestRedactProseNotCaptured guards against the DB-name regex being loosened
// back into a single unquoted/uncolon'd pattern. The old regex matched any
// word following "Database"/"base de données" — including in ordinary prose
// like "The tempdb database has 4 data file(s)" — so common words (has, is,
// mirroring, ...) were minted as DB tokens and then substituted everywhere in
// the digest, corrupting unrelated text. These lines contain the word
// "database" but no real database name, so nothing must be redacted.
func TestRedactProseNotCaptured(t *testing.T) {
	prose := []string{
		"The tempdb database has 4 data file(s).",
		"Database mirroring has been enabled on this instance of SQL Server.",
		"The database has already joined the availability group. This is an informational message.",
	}
	r := NewRedactor()
	for _, l := range prose {
		r.Scan(l)
	}

	// No token may be minted at all — there is no real DB/login/IP here.
	legend := r.Legend()
	if len(legend) != 0 {
		t.Fatalf("prose lines minted %d token(s), want 0: %v", len(legend), legend)
	}

	// Belt-and-suspenders: no ordinary word may appear as a redacted value in
	// the legend. If a loose regex captured e.g. "has" as DB_1, this fails.
	for _, entry := range legend {
		for _, word := range []string{"has", "was", "is", "mirroring", "been", "enabled"} {
			if strings.Contains(entry, "= "+word) {
				t.Errorf("prose word %q was minted as a token: %q", word, entry)
			}
		}
	}

	// The prose lines must survive Apply byte-for-byte. If a loose regex had
	// captured "has"/"is"/... as tokens, Apply would substitute every such
	// substring and mangle the text.
	for _, l := range prose {
		if out := r.Apply(l); out != l {
			t.Errorf("prose changed by Apply:\n in:  %q\n out: %q", l, out)
		}
	}
}

// TestRedactRealNamesCaptured proves both real-DB-name forms are still
// redacted: the quoted form (database 'master') and the colon form
// (Database: sales). If reDBQuoted/reDBColon are dropped, this fails.
func TestRedactRealNamesCaptured(t *testing.T) {
	quoted := "Starting up database 'master'."
	colon := "Log was backed up. Database: sales."

	r := NewRedactor()
	r.Scan(quoted)
	r.Scan(colon)

	outQuoted := r.Apply(quoted)
	if strings.Contains(outQuoted, "master") {
		t.Errorf("quoted db name 'master' not redacted: %q", outQuoted)
	}
	if !strings.Contains(outQuoted, "DB_") {
		t.Errorf("quoted db name did not mint a DB token: %q", outQuoted)
	}

	outColon := r.Apply(colon)
	if strings.Contains(outColon, "sales") {
		t.Errorf("colon db name 'sales' not redacted: %q", outColon)
	}
	if !strings.Contains(outColon, "DB_") {
		t.Errorf("colon db name did not mint a DB token: %q", outColon)
	}
}

// TestRedactShortLoginDoesNotCorruptWords guards against Apply's
// substring-replacement corrupting ordinary text when a short value like
// "sa" is registered as a login. Before the boundary-aware fix, Apply used
// strings.ReplaceAll, so redacting the quoted login 'sa' also rewrote every
// occurrence of the substring "sa" anywhere else in the text (e.g. "message"
// became "mesLOGIN_1ge", and "sales" was mangled too). If this regresses,
// this test fails.
func TestRedactShortLoginDoesNotCorruptWords(t *testing.T) {
	text := "Login failed for user 'sa'. This is an informational message about the sales database."

	r := NewRedactor()
	r.Scan(text)
	out := r.Apply(text)

	if strings.Contains(out, "'sa'") {
		t.Errorf("quoted login 'sa' was not redacted: %q", out)
	}
	if !strings.Contains(out, "LOGIN_") {
		t.Errorf("no LOGIN token minted: %q", out)
	}
	if !strings.Contains(out, "message") {
		t.Errorf("standalone word %q was corrupted by short-login replacement: %q", "message", out)
	}
	if !strings.Contains(out, "sales") {
		t.Errorf("standalone word %q was corrupted by short-login replacement: %q", "sales", out)
	}
}

// TestRedactAddLoginCoversServiceAccount simulates the pipeline path: a bare
// value such as a service-account name (e.g. "CORP\svc-01") never matches
// Scan's quoted "user '...'" pattern, so it must be registered directly via
// AddLogin. This proves AddLogin makes Apply redact that value both when it
// appears standalone (the summary field) and inside a quoted sentence (a
// boot event line), and that the Legend records the mapping.
func TestRedactAddLoginCoversServiceAccount(t *testing.T) {
	const svcAcct = `CORP\svc-01`

	r := NewRedactor()
	r.AddLogin(svcAcct)

	outSentence := r.Apply("The service account is '" + svcAcct + "'.")
	if strings.Contains(outSentence, svcAcct) {
		t.Errorf("service account leaked in event text: %q", outSentence)
	}
	if !strings.Contains(outSentence, "LOGIN_") {
		t.Errorf("no LOGIN token in event text: %q", outSentence)
	}

	outField := r.Apply(svcAcct)
	if strings.Contains(outField, svcAcct) {
		t.Errorf("service account leaked in summary field: %q", outField)
	}
	if !strings.Contains(outField, "LOGIN_") {
		t.Errorf("no LOGIN token in summary field: %q", outField)
	}

	legend := r.Legend()
	found := false
	for _, l := range legend {
		if strings.Contains(l, "= "+svcAcct) {
			found = true
		}
	}
	if !found {
		t.Errorf("legend does not contain service account mapping: %v", legend)
	}
}

// TestRedactIPBoundarySanity proves the boundary-aware Apply still redacts
// an IP flanked by brackets/spaces, and that a registered short IP is not
// applied as a corrupting substring match inside an unrelated, unregistered
// longer IP that shares the same prefix (e.g. 10.0.0.5 vs 10.0.0.50). Only
// 10.0.0.5 is scanned/registered here, mirroring a real case where the two
// IPs come from different text — Apply must not turn "10.0.0.50" into
// something like "IP_10" (the old ReplaceAll behavior).
func TestRedactIPBoundarySanity(t *testing.T) {
	r := NewRedactor()
	r.Scan("[CLIENT : 10.0.0.5]")

	out := r.Apply("[CLIENT : 10.0.0.5] then later [CLIENT : 10.0.0.50]")

	if strings.Contains(out, "10.0.0.5]") {
		t.Errorf("10.0.0.5 not redacted: %q", out)
	}
	if !strings.Contains(out, "IP_") {
		t.Errorf("no IP token minted: %q", out)
	}
	if !strings.Contains(out, "10.0.0.50]") {
		t.Errorf("unregistered 10.0.0.50 was corrupted by the registered 10.0.0.5 match: %q", out)
	}
}
