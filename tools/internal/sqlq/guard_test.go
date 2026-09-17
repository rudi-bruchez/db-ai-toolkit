package sqlq

import "testing"

func TestSanitizeRemovesCommentsAndLiterals(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string // token that must NOT survive
		keep string // token that MUST survive
	}{
		{"line comment", "SELECT 1 -- DROP TABLE t\n, 2", "DROP", "SELECT"},
		{"block comment", "/* DELETE FROM t */ SELECT 1", "DELETE", "SELECT"},
		{"nested block comment", "/* a /* DROP */ b */ SELECT 1", "DROP", "SELECT"},
		{"string literal", "SELECT 'DROP TABLE t' AS x", "DROP", "SELECT"},
		{"escaped quote in literal", "SELECT 'it''s a DELETE' AS x", "DELETE", "SELECT"},
		{"bracketed identifier", "SELECT [update] FROM t", "update", "SELECT"},
		{"quoted identifier", `SELECT "delete" FROM t`, "delete", "SELECT"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Sanitize(tc.in)
			if containsToken(got, tc.want) {
				t.Errorf("Sanitize(%q) = %q; token %q should have been stripped", tc.in, got, tc.want)
			}
			if !containsToken(got, tc.keep) {
				t.Errorf("Sanitize(%q) = %q; token %q should have survived", tc.in, got, tc.keep)
			}
		})
	}
}

func TestSanitizeLeavesUnterminatedConstructsStripped(t *testing.T) {
	// An unterminated literal or comment must swallow the rest of the input
	// rather than leaving executable-looking text behind.
	if containsToken(Sanitize("SELECT 'abc"), "abc") {
		t.Error("unterminated literal should be stripped to end of input")
	}
	if containsToken(Sanitize("SELECT 1 /* DROP TABLE t"), "DROP") {
		t.Error("unterminated block comment should be stripped to end of input")
	}
}

func TestFindWritesRejectsWritingStatements(t *testing.T) {
	rejected := []struct {
		name, sql, keyword string
	}{
		{"insert", "INSERT INTO t VALUES (1)", "INSERT"},
		{"update", "UPDATE t SET c = 1", "UPDATE"},
		{"delete", "DELETE FROM t", "DELETE"},
		{"merge", "MERGE t AS x USING s ON 1=1", "MERGE"},
		{"truncate", "TRUNCATE TABLE t", "TRUNCATE"},
		{"drop", "DROP TABLE t", "DROP"},
		{"alter", "ALTER TABLE t ADD c INT", "ALTER"},
		{"create", "CREATE TABLE t (c INT)", "CREATE"},
		{"grant", "GRANT SELECT ON t TO u", "GRANT"},
		{"exec", "EXEC sp_helptext 'dbo.p'", "EXEC"},
		{"execute", "EXECUTE dbo.p", "EXECUTE"},
		{"dbcc", "DBCC FREEPROCCACHE", "DBCC"},
		{"backup", "BACKUP DATABASE d TO DISK = 'x'", "BACKUP"},
		{"kill", "KILL 53", "KILL"},
		{"select into", "SELECT * INTO dbo.copy FROM dbo.src", "INTO"},
		{"cte then delete", "WITH x AS (SELECT 1 AS n) DELETE FROM t", "DELETE"},
		{"write hidden after read", "SELECT 1; DELETE FROM t", "DELETE"},
		{"write after GO batch", "SELECT 1\nGO\nUPDATE t SET c = 1", "UPDATE"},
		{"lowercase", "delete from t", "DELETE"},
		// Sanitize strips string literals before the scan, which is what makes
		// SELECT 'DROP TABLE x' safe. The pass-through query of OPENQUERY is a
		// string literal, so its contents are never examined - the whole
		// construct has to be refused instead.
		{"openquery pass-through", "SELECT * FROM OPENQUERY(LINKED, 'DELETE FROM dbo.T')", "OPENQUERY"},
		{"openrowset pass-through", "SELECT * FROM OPENROWSET('SQLNCLI', 'Server=X;', 'DROP TABLE t')", "OPENROWSET"},
		{"opendatasource", "SELECT * FROM OPENDATASOURCE('SQLNCLI', 'Server=X;').db.dbo.t", "OPENDATASOURCE"},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			v := FindWrites(tc.sql)
			if len(v) == 0 {
				t.Fatalf("FindWrites(%q) = none; expected a violation on %q", tc.sql, tc.keyword)
			}
			if v[0].Keyword != tc.keyword {
				t.Errorf("FindWrites(%q) keyword = %q; want %q", tc.sql, v[0].Keyword, tc.keyword)
			}
		})
	}
}

func TestFindWritesAllowsReadOnlyDiagnostics(t *testing.T) {
	allowed := []struct{ name, sql string }{
		{"plain select", "SELECT TOP (10) name FROM sys.tables ORDER BY name"},
		{"cte select", "WITH x AS (SELECT 1 AS n) SELECT n FROM x"},
		{"set options", "SET NOCOUNT ON; SET STATISTICS XML ON; SELECT 1"},
		{"declare then select", "DECLARE @p INT = 1; SELECT @p"},
		{"column named like keyword", "SELECT create_date, modify_date FROM sys.objects"},
		{"literal mentioning drop", "SELECT name FROM sys.objects WHERE name = 'DROP TABLE'"},
		{"comment mentioning delete", "-- we never DELETE here\nSELECT 1"},
		{"function with keyword prefix", "SELECT dbo.fn_create_key(1)"},
		{"partition stats", "SELECT SUM(row_count) FROM sys.dm_db_partition_stats WHERE index_id IN (0,1)"},
		{"object definition instead of sp_helptext", "SELECT OBJECT_DEFINITION(OBJECT_ID('dbo.p'))"},
	}
	for _, tc := range allowed {
		t.Run(tc.name, func(t *testing.T) {
			if v := FindWrites(tc.sql); len(v) != 0 {
				t.Errorf("FindWrites(%q) = %+v; want no violation", tc.sql, v)
			}
		})
	}
}

func TestStatementsSplitsOnSemicolonAndGo(t *testing.T) {
	got := Statements("SELECT 1; SELECT 2\nGO\nSELECT 3")
	if len(got) != 3 {
		t.Fatalf("Statements() = %d statements (%q); want 3", len(got), got)
	}
}

func TestStatementsIgnoresGoInsideIdentifier(t *testing.T) {
	// "GO" is a batch separator only when alone on its line.
	got := Statements("SELECT going FROM t")
	if len(got) != 1 {
		t.Fatalf("Statements() = %d statements (%q); want 1", len(got), got)
	}
}

// USE writes nothing, so the write guard passes it - and that is the point of
// having a second check rather than adding it to writeKeywords, where the
// refusal would come back as "this batch would write", which is false.
func TestUseIsNotAWriteButIsRefusedAnyway(t *testing.T) {
	const sql = "USE OtherDatabase;\nSELECT TOP (10) name FROM sys.tables"

	if v := FindWrites(sql); len(v) > 0 {
		t.Errorf("USE is not a write; the write guard should not be what catches it (got %s)", v[0].Keyword)
	}
	changes := FindContextChanges(sql)
	if len(changes) == 0 {
		t.Fatal("USE must be refused: it silently makes the reported database wrong and overrides -database")
	}
	if changes[0].Keyword != "USE" {
		t.Errorf("keyword = %q; want USE", changes[0].Keyword)
	}
}

func TestFindContextChangesLeavesOrdinaryReadsAlone(t *testing.T) {
	allowed := []string{
		// Multi-part names are legitimate cross-database reads and stay allowed.
		"SELECT * FROM Other.dbo.Customers",
		"SELECT * FROM [LINKED].[db].[dbo].[T]",
		// A literal, and a column whose name merely contains the keyword.
		"SELECT 'USE this' AS hint",
		"SELECT house_use, warehouse FROM dbo.T",
	}
	for _, sql := range allowed {
		if v := FindContextChanges(sql); len(v) > 0 {
			t.Errorf("%q was refused on %s, but it changes no context", sql, v[0].Keyword)
		}
	}
}
