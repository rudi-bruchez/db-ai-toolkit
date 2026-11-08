package ittest

import (
	"context"
	"testing"
	"time"
)

func TestDatasetAndWorkload(t *testing.T) {
	p, r := Profile(t, "GODTA_TEST_PROFILE")
	server := OpenDB(t, p, r, "")
	name := NewDatabase(t, server, "")
	db := OpenDB(t, p, r, name)
	LoadDataset(t, db)
	const n = 3
	RunWorkload(t, db, n)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	var rows int64
	if err := db.QueryRowContext(ctx, "SELECT COUNT_BIG(*) FROM dbo.godta_t;").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1000000 {
		t.Errorf("godta_t has %d rows, want 1000000", rows)
	}

	var mode string
	if err := db.QueryRowContext(ctx,
		"SELECT query_capture_mode_desc FROM sys.database_query_store_options;").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "ALL" {
		t.Errorf("query capture mode %q, want ALL", mode)
	}

	const text = "(@a int,@b int)SELECT c FROM dbo.godta_t WHERE a = @a AND b = @b"
	var execs int64
	if err := db.QueryRowContext(ctx, `SELECT ISNULL(SUM(rs.count_executions), 0)
FROM sys.query_store_query_text qt
JOIN sys.query_store_query q ON q.query_text_id = qt.query_text_id
JOIN sys.query_store_plan pl ON pl.query_id = q.query_id
JOIN sys.query_store_runtime_stats rs ON rs.plan_id = pl.plan_id
WHERE qt.query_sql_text = @p1;`, text).Scan(&execs); err != nil {
		t.Fatal(err)
	}
	if execs < n {
		t.Errorf("%d executions of %q in the Query Store, want at least %d", execs, text, n)
	}
}
