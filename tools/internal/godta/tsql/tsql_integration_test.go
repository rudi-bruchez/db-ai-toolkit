package tsql_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/ittest"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/tsql"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/workspace"
)

// TestTemplatesRunOnLab sends once every template that touches neither a file
// nor another database, on a table whose name needs quoting. The golden file
// proves the text; this proves the engine accepts it and acts on the named
// object.
func TestTemplatesRunOnLab(t *testing.T) {
	p, r := ittest.Profile(t, "GODTA_TEST_PROFILE")
	server := ittest.OpenDB(t, p, r, "")
	dbName := ittest.NewDatabase(t, server, "")
	db := ittest.OpenDB(t, p, r, dbName)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	c, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	table := tsql.Obj{Schema: "dbo", Name: "Ord]er s"}
	exec := func(stmt string) {
		t.Helper()
		if _, err := c.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	// Test setup, not a template: the table and a few thousand rows.
	exec("CREATE TABLE " + table.Quoted() + " ([a] int NOT NULL, [b] int NOT NULL, [x y] varchar(20) NOT NULL)")
	exec("INSERT " + table.Quoted() + " ([a], [b], [x y]) SELECT TOP (5000) n % 100, n % 7, CONVERT(varchar(20), n) " +
		"FROM (SELECT ROW_NUMBER() OVER (ORDER BY (SELECT NULL)) n FROM sys.all_columns x CROSS JOIN sys.all_columns y) s")

	var dbID int
	if err := c.QueryRowContext(ctx, "SELECT DB_ID()").Scan(&dbID); err != nil {
		t.Fatal(err)
	}
	exec(tsql.SetLockTimeout(5000))
	exec(tsql.AutopilotReset(dbID))

	run := workspace.NewRunID()
	ix := tsql.IndexDef{
		Table:   table,
		Name:    "godta_" + run + "_1",
		Keys:    []tsql.KeyCol{{Name: "a"}, {Name: "b", Desc: true}},
		Include: []string{"x y"},
	}
	exec(tsql.CreateHypothetical(ix))
	if !exists(ctx, t, c, "SELECT COUNT(*) FROM sys.indexes WHERE object_id = OBJECT_ID(@p1) AND name = @p2 AND is_hypothetical = 1",
		table.Quoted(), ix.Name) {
		t.Fatalf("hypothetical index %s not created", ix.Name)
	}
	// STATISTICS_ONLY = -1 samples the table: the index has statistics with
	// rows, where without the value it would have none (spec section 8.5).
	if !exists(ctx, t, c, "SELECT COUNT(*) FROM sys.indexes i CROSS APPLY sys.dm_db_stats_properties(i.object_id, i.index_id) s "+
		"WHERE i.object_id = OBJECT_ID(@p1) AND i.name = @p2 AND s.rows > 0", table.Quoted(), ix.Name) {
		t.Fatalf("hypothetical index %s has no sampled statistics", ix.Name)
	}

	stat := "godta_s'1"
	exec(tsql.CreateStatistics(table, stat, []string{"a", "b"}))
	statStream := readStream(ctx, t, c, tsql.ShowStatisticsStream(table, stat))
	ixStream := readStream(ctx, t, c, tsql.ShowStatisticsStream(table, ix.Name))
	// The stream read on the hypothetical index goes onto the statistics of the
	// same keys, and the statistics' stream onto the index (spec section 8.5).
	exec(tsql.UpdateStatisticsStream(table, stat, ixStream))
	exec(tsql.UpdateStatisticsStream(table, ix.Name, statStream))

	exec(tsql.DropStatistics(table, stat))
	if exists(ctx, t, c, "SELECT COUNT(*) FROM sys.stats WHERE object_id = OBJECT_ID(@p1) AND name = @p2", table.Quoted(), stat) {
		t.Errorf("statistics %s still there after DropStatistics", stat)
	}
	exec(tsql.DropIndex(table, ix.Name))
	if exists(ctx, t, c, "SELECT COUNT(*) FROM sys.indexes WHERE object_id = OBJECT_ID(@p1) AND name = @p2", table.Quoted(), ix.Name) {
		t.Errorf("index %s still there after DropIndex", ix.Name)
	}

	// A bit set ON then OFF, each read back, and MAXDOP.
	for _, sc := range []struct {
		cfg  tsql.ScopedConfig
		want string
	}{
		{tsql.ScopedConfig{Name: "LEGACY_CARDINALITY_ESTIMATION", Value: "1", BaseType: "bit"}, "1"},
		{tsql.ScopedConfig{Name: "LEGACY_CARDINALITY_ESTIMATION", Value: "0", BaseType: "bit"}, "0"},
		{tsql.ScopedConfig{Name: "MAXDOP", Value: "2", BaseType: "int"}, "2"},
	} {
		exec(tsql.SetScopedConfig(sc.cfg))
		var got, base string
		if err := c.QueryRowContext(ctx,
			"SELECT CONVERT(nvarchar(100), value), CONVERT(nvarchar(100), SQL_VARIANT_PROPERTY(value, 'BaseType')) "+
				"FROM sys.database_scoped_configurations WHERE name = @p1", sc.cfg.Name).Scan(&got, &base); err != nil {
			t.Fatal(err)
		}
		if got != sc.want || base != sc.cfg.BaseType {
			t.Errorf("%s: value %s of type %s, want %s of type %s", sc.cfg.Name, got, base, sc.want, sc.cfg.BaseType)
		}
	}
}

func exists(ctx context.Context, t *testing.T, c *sql.Conn, q string, args ...any) bool {
	t.Helper()
	var n int
	if err := c.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n > 0
}

// readStream returns the Stats_Stream column of DBCC SHOW_STATISTICS ... WITH
// STATS_STREAM, the first of its result set.
func readStream(ctx context.Context, t *testing.T, c *sql.Conn, stmt string) []byte {
	t.Helper()
	rows, err := c.QueryContext(ctx, stmt)
	if err != nil {
		t.Fatalf("%s: %v", stmt, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	if !rows.Next() {
		t.Fatalf("%s: no row (%v)", stmt, rows.Err())
	}
	var stream []byte
	dest := make([]any, len(cols))
	dest[0] = &stream
	for i := 1; i < len(dest); i++ {
		dest[i] = new(any)
	}
	if err := rows.Scan(dest...); err != nil {
		t.Fatal(err)
	}
	if len(stream) == 0 {
		t.Fatalf("%s: empty stream", stmt)
	}
	return stream
}
