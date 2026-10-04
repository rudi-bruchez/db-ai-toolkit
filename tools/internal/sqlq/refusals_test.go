package sqlq

import "testing"

func TestRefusalsGiveKeywordAndLine(t *testing.T) {
	cases := []struct {
		sql, reason string
	}{
		{"SELECT 1;\nDELETE FROM dbo.T;", "write keyword DELETE at line 2"},
		{"\n\nUSE master;\nSELECT 1;", "USE at line 3"},
		{"SELECT 1;\nGO\nSELECT 2;", "batch separator GO at line 2"},
		{"SELECT 'DROP TABLE x' AS s; -- EXEC", ""},
	}
	for _, c := range cases {
		r := Refusals(c.sql)
		got := ""
		if len(r) > 0 {
			got = r[0].Reason()
		}
		if got != c.reason {
			t.Errorf("Refusals(%q)[0].Reason() = %q, want %q", c.sql, got, c.reason)
		}
	}
}

// Refusals must refuse exactly what the three historical guards refuse, or the
// catalogue and the command line would disagree about the same file.
func TestRefusalsAgreeWithTheGuards(t *testing.T) {
	corpus := []string{
		"SELECT name FROM sys.objects;",
		"SELECT create_date, TAG_CREATE FROM t;",
		"SELECT * INTO #t FROM sys.objects;",
		"EXEC sp_who;",
		"USE tempdb;",
		"SELECT 1\nGO 2\n",
		"SELECT * FROM OPENQUERY(L, 'DELETE FROM t');",
		"SELECT 'GO' AS g;\n/* GO */ SELECT 2;",
	}
	for _, sql := range corpus {
		kinds := map[RefusalKind]bool{}
		for _, r := range Refusals(sql) {
			kinds[r.Kind] = true
		}
		if kinds[RefusalWrite] != (len(FindWrites(sql)) > 0) {
			t.Errorf("%q: write refusal %v, FindWrites %v", sql, kinds[RefusalWrite], FindWrites(sql))
		}
		if kinds[RefusalContext] != (len(FindContextChanges(sql)) > 0) {
			t.Errorf("%q: context refusal disagrees with FindContextChanges", sql)
		}
		if kinds[RefusalSeparator] != (len(FindBatchSeparators(sql)) > 0) {
			t.Errorf("%q: separator refusal disagrees with FindBatchSeparators", sql)
		}
	}
}
