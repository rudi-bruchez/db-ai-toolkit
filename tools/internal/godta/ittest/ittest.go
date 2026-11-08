// Package ittest holds the helpers of goDTA's integration tests: the test
// profile, a throwaway database with Query Store in capture mode ALL, the
// spike's data set and the test workload (plan, annexes A and A bis).
//
// The tests run without a build tag and skip cleanly when GODTA_TEST_PROFILES
// or the profile variable is unset.
package ittest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/conn"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/sqlq"
)

// Profile loads the profile named by the variable envName from the file named
// by GODTA_TEST_PROFILES, or skips. The profile must be readwrite and declare
// environment "lab": these tests create and drop databases.
func Profile(t *testing.T, envName string) (sqlq.Profile, sqlq.SecretResolver) {
	t.Helper()
	path, name := os.Getenv("GODTA_TEST_PROFILES"), os.Getenv(envName)
	if path == "" || name == "" {
		t.Skipf("GODTA_TEST_PROFILES and %s not set; no instance to test against", envName)
	}
	profiles, err := sqlq.LoadProfiles(path)
	if err != nil {
		t.Fatal(err)
	}
	p, err := profiles.Get(name)
	if err != nil {
		t.Fatal(err)
	}
	if p.ReadOnly() {
		t.Fatalf("profile %q must be readwrite for the goDTA integration tests", name)
	}
	if p.Environment != "lab" {
		t.Fatalf("profile %q must declare \"environment\": \"lab\", has %q", name, p.Environment)
	}
	r := sqlq.NewResolver(os.Getenv, "", func(string) {})
	return p, r.Resolve
}

// OpenDB opens a pool on database db (the profile's own when empty) through
// conn.Open, closed in t.Cleanup.
func OpenDB(t *testing.T, p sqlq.Profile, r sqlq.SecretResolver, db string) *sql.DB {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := conn.Open(ctx, p, r, db)
	if err != nil {
		t.Fatalf("opening %q: %v", db, err)
	}
	t.Cleanup(func() { pool.Close() })
	return pool
}

const runAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

func runID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	for i := range b {
		b[i] = runAlphabet[int(b[i])%len(runAlphabet)]
	}
	return string(b)
}

// collationName is what a collation name looks like. The tsql quoting helpers
// come in a later task; a collation is not quotable anyway, so it is checked.
var collationName = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// NewDatabase creates godta_it_<runID> on server, with the given collation
// (the instance's when empty) and Query Store READ_WRITE in capture mode ALL,
// and drops it in t.Cleanup, failed test or not. In capture mode AUTO three
// executions of a cheap query are not all captured, and some queries are not
// captured at all (measured on 2022).
func NewDatabase(t *testing.T, server *sql.DB, collation string) string {
	t.Helper()
	if collation != "" && !collationName.MatchString(collation) {
		t.Fatalf("invalid collation name %q", collation)
	}
	name := "godta_it_" + runID(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	create := "CREATE DATABASE [" + name + "]"
	if collation != "" {
		create += " COLLATE " + collation
	}
	if _, err := server.ExecContext(ctx, create); err != nil {
		t.Fatalf("creating %s: %v", name, err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if _, err := server.ExecContext(ctx,
			"ALTER DATABASE ["+name+"] SET SINGLE_USER WITH ROLLBACK IMMEDIATE; DROP DATABASE ["+name+"];"); err != nil {
			t.Errorf("dropping %s: %v", name, err)
		}
	})
	if _, err := server.ExecContext(ctx, "ALTER DATABASE ["+name+
		"] SET QUERY_STORE = ON (OPERATION_MODE = READ_WRITE, QUERY_CAPTURE_MODE = ALL);"); err != nil {
		t.Fatalf("enabling Query Store on %s: %v", name, err)
	}
	return name
}

// Dataset is the spike's build-small.sql (plan, annex A), verbatim. It needs
// QUOTED_IDENTIFIER ON, go-mssqldb's default: the indexed view fails without
// it (error 1935).
const Dataset = `ALTER DATABASE SCOPED CONFIGURATION SET MAXDOP = 2;
CREATE TABLE dbo.godta_t (id int IDENTITY CONSTRAINT pk_godta_t PRIMARY KEY, a int NOT NULL, b int NOT NULL, c varchar(20) NOT NULL, pad char(200) NOT NULL DEFAULT '');
WITH n AS (SELECT TOP (1000000) ROW_NUMBER() OVER (ORDER BY (SELECT NULL)) i FROM sys.all_columns x CROSS JOIN sys.all_columns y)
INSERT dbo.godta_t (a, b, c) SELECT a, a*10 + (i % 3), CONVERT(varchar(20), i % 50000) FROM n CROSS APPLY (SELECT CAST(POWER(ABS(CHECKSUM(NEWID())) % 1000 / 1000.0, 4) * 1000 AS int) a) z;
EXEC('CREATE VIEW dbo.godta_v WITH SCHEMABINDING AS SELECT a, COUNT_BIG(*) n FROM dbo.godta_t GROUP BY a');
CREATE UNIQUE CLUSTERED INDEX godta_v_cx ON dbo.godta_v(a);
CREATE INDEX godta_filt ON dbo.godta_t(c) WHERE a = 933;
CREATE TABLE dbo.godta_h (x int NOT NULL, y varchar(40) NOT NULL);
INSERT dbo.godta_h SELECT TOP (200000) ABS(CHECKSUM(NEWID())) % 10000, CONVERT(varchar(36), NEWID()) FROM sys.all_columns x CROSS JOIN sys.all_columns y;
CREATE INDEX nix_h_x ON dbo.godta_h(x);
CREATE TABLE dbo.godta_cs (k int PRIMARY KEY, v int);
INSERT dbo.godta_cs SELECT TOP 10000 ROW_NUMBER() OVER (ORDER BY (SELECT NULL)), 1 FROM sys.all_columns;
CREATE NONCLUSTERED COLUMNSTORE INDEX ncci_cs ON dbo.godta_cs(v);
`

// LoadDataset runs Dataset as one batch on db, a pool opened on the test
// database.
func LoadDataset(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if _, err := db.ExecContext(ctx, Dataset); err != nil {
		t.Fatalf("loading the data set: %v", err)
	}
}

// Workload is the test workload (plan, annex A bis), verbatim, one batch per
// statement. The parameterized query goes through sp_executesql and the rest
// as plain batches, never through a temporary procedure: the database's Query
// Store does not capture the statements of a # procedure (measured: no row,
// even in capture mode ALL).
var Workload = []string{
	`SELECT y FROM dbo.godta_h WHERE x = 42;`,
	`EXEC sys.sp_executesql N'SELECT c FROM dbo.godta_t WHERE a = @a AND b = @b', N'@a int, @b int', @a = 933, @b = 9332;`,
	`SELECT TOP 10 c FROM dbo.godta_t WHERE a = 0 ORDER BY b DESC;`,
	`SELECT a, COUNT_BIG(*) FROM dbo.godta_t GROUP BY a;`,
	`BEGIN TRAN; UPDATE dbo.godta_t SET c = c + 'x' WHERE a = 500; ROLLBACK;`,
	`SELECT COUNT(*) FROM dbo.godta_cs WHERE v = 1;`,
}

// RunWorkload runs every Workload statement n times on db, then flushes the
// Query Store.
func RunWorkload(t *testing.T, db *sql.DB, n int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	for range n {
		for _, stmt := range Workload {
			if err := drain(ctx, db, stmt); err != nil {
				t.Fatalf("workload %q: %v", stmt, err)
			}
		}
	}
	if _, err := db.ExecContext(ctx, "EXEC sys.sp_query_store_flush_db;"); err != nil {
		t.Fatalf("flushing the Query Store: %v", err)
	}
}

// drain runs a statement and reads every row of every result set, so the
// execution completes the way a client's would.
func drain(ctx context.Context, db *sql.DB, stmt string) error {
	rows, err := db.QueryContext(ctx, stmt)
	if err != nil {
		return err
	}
	defer rows.Close()
	for {
		for rows.Next() {
		}
		if !rows.NextResultSet() {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("reading results: %w", err)
	}
	return rows.Close()
}
