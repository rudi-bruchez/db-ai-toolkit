//go:build ignore

// Command capture writes the showplan test fixtures of
// tools/internal/godta/showplan/testdata from the lab instance.
//
// It creates a throwaway database godta_it_<runID> with Query Store in capture
// mode ALL, loads ittest.Dataset, captures every fixture on one pinned
// connection, runs ittest.Workload for the Query Store plan, and drops the
// database at the end, failed or not.
//
// Run it from tools/, naming the file (go run on the directory skips a file
// tagged ignore), with the integration test variables set:
//
//	GODTA_TEST_PROFILES=<profiles.json> GODTA_TEST_PROFILE=<readwrite lab profile> \
//	  go run ./internal/godta/showplan/testdata/capture/main.go
//
// Fixtures captured under AUTOPILOT follow spec section 8.5: reset the
// session list, declare every real nonclustered index of godta_t, the indexed
// view (type 6) and a hypothetical index (c) INCLUDE (a), then SET AUTOPILOT
// ON alone in its batch, the statement, SET AUTOPILOT OFF. The same statement
// is first compiled under SHOWPLAN_XML, which creates and refreshes the
// statistics: the statistic of pk_godta_t, built on the empty table, is stale
// otherwise and the narrow plans come out wide.
package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/conn"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/ittest"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/sqlq"
)

const outDir = "internal/godta/showplan/testdata"

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	path, name := os.Getenv("GODTA_TEST_PROFILES"), os.Getenv("GODTA_TEST_PROFILE")
	if path == "" || name == "" {
		return fmt.Errorf("GODTA_TEST_PROFILES and GODTA_TEST_PROFILE must be set")
	}
	profiles, err := sqlq.LoadProfiles(path)
	if err != nil {
		return err
	}
	p, err := profiles.Get(name)
	if err != nil {
		return err
	}
	if p.ReadOnly() || p.Environment != "lab" {
		return fmt.Errorf("profile %q must be readwrite with environment lab", name)
	}
	resolve := sqlq.NewResolver(os.Getenv, "", func(string) {}).Resolve

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	server, err := conn.Open(ctx, p, resolve, "")
	if err != nil {
		return err
	}
	defer server.Close()

	dbName := "godta_it_" + randomID()
	if _, err := server.ExecContext(ctx, "CREATE DATABASE ["+dbName+"]"); err != nil {
		return err
	}
	defer func() {
		if _, err := server.ExecContext(context.Background(),
			"ALTER DATABASE ["+dbName+"] SET SINGLE_USER WITH ROLLBACK IMMEDIATE; DROP DATABASE ["+dbName+"];"); err != nil {
			log.Printf("dropping %s: %v", dbName, err)
		}
	}()
	if _, err := server.ExecContext(ctx, "ALTER DATABASE ["+dbName+
		"] SET QUERY_STORE = ON (OPERATION_MODE = READ_WRITE, QUERY_CAPTURE_MODE = ALL);"); err != nil {
		return err
	}

	db, err := conn.Open(ctx, p, resolve, dbName)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, ittest.Dataset); err != nil {
		return fmt.Errorf("loading the data set: %w", err)
	}

	c, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer c.Close()

	plain := []struct{ file, stmt string }{
		{"seek_lookup.sqlplan", "SELECT y FROM dbo.godta_h WHERE x = 42;"},
		{"join.sqlplan", "SELECT t.c, h.y FROM dbo.godta_t t JOIN dbo.godta_h h ON h.x = t.a;"},
		{"sort.sqlplan", "SELECT TOP 10 c FROM dbo.godta_t WHERE a = 0 ORDER BY b DESC;"},
		{"view.sqlplan", "SELECT a, COUNT_BIG(*) FROM dbo.godta_t GROUP BY a;"},
	}
	for _, f := range plain {
		if err := showplanTo(ctx, c, f.stmt, f.file); err != nil {
			return err
		}
	}

	if err := exec(ctx, c, "CREATE PROCEDURE #godta_scan (@a int, @b int) AS SELECT c FROM dbo.godta_t WHERE a = @a AND b = @b;"); err != nil {
		return err
	}
	if err := showplanTo(ctx, c, "EXEC #godta_scan @a = 933, @b = 9332;", "scan_param.sqlplan"); err != nil {
		return err
	}

	if err := autopilotFixtures(ctx, c); err != nil {
		return err
	}

	// The Query Store plan of the parameterized workload query
	for _, stmt := range ittest.Workload {
		for range 3 {
			if err := drain(ctx, db, stmt); err != nil {
				return fmt.Errorf("workload %q: %w", stmt, err)
			}
		}
	}
	if _, err := db.ExecContext(ctx, "EXEC sys.sp_query_store_flush_db;"); err != nil {
		return err
	}
	var qsPlan string
	if err := db.QueryRowContext(ctx, `SELECT TOP (1) pl.query_plan
FROM sys.query_store_query_text qt
JOIN sys.query_store_query q ON q.query_text_id = qt.query_text_id
JOIN sys.query_store_plan pl ON pl.query_id = q.query_id
WHERE qt.query_sql_text = N'(@a int,@b int)SELECT c FROM dbo.godta_t WHERE a = @a AND b = @b'
ORDER BY pl.plan_id;`).Scan(&qsPlan); err != nil {
		return fmt.Errorf("reading the Query Store plan: %w", err)
	}
	return write("qs_plan.sqlplan", qsPlan)
}

func autopilotFixtures(ctx context.Context, c *sql.Conn) error {
	var dbID, tableID, viewID, filtID, hypID int
	if err := c.QueryRowContext(ctx, `SELECT DB_ID(), OBJECT_ID(N'dbo.godta_t'), OBJECT_ID(N'dbo.godta_v'),
  (SELECT index_id FROM sys.indexes WHERE object_id = OBJECT_ID(N'dbo.godta_t') AND name = N'godta_filt');`).
		Scan(&dbID, &tableID, &viewID, &filtID); err != nil {
		return err
	}
	if err := exec(ctx, c, "CREATE NONCLUSTERED INDEX godta_hyp_c ON dbo.godta_t (c) INCLUDE (a) WITH STATISTICS_ONLY = -1;"); err != nil {
		return err
	}
	defer func() {
		if err := exec(context.Background(), c, "DROP INDEX godta_hyp_c ON dbo.godta_t;"); err != nil {
			log.Printf("dropping the hypothetical index: %v", err)
		}
	}()
	if err := c.QueryRowContext(ctx, `SELECT index_id FROM sys.indexes WHERE object_id = OBJECT_ID(N'dbo.godta_t') AND name = N'godta_hyp_c';`).Scan(&hypID); err != nil {
		return err
	}

	fixtures := []struct{ file, stmt string }{
		{"update.sqlplan", "UPDATE dbo.godta_t SET c = c + 'x' WHERE a = 500;"},
		{"update_narrow.sqlplan", "UPDATE dbo.godta_t SET c = c + 'x' WHERE id BETWEEN 5 AND 50;"},
		{"insert_narrow.sqlplan", "INSERT dbo.godta_t (a, b, c) VALUES (1, 2, '3');"},
	}
	for _, f := range fixtures {
		// SHOWPLAN_XML first: it creates and refreshes the statistics
		if _, err := showplan(ctx, c, f.stmt); err != nil {
			return err
		}
		for _, s := range []string{
			fmt.Sprintf("DBCC AUTOPILOT(5, %d, 0, 0, 0, 0, 0);", dbID),
			fmt.Sprintf("DBCC AUTOPILOT(0, %d, %d, %d, 0);", dbID, tableID, filtID),
			fmt.Sprintf("DBCC AUTOPILOT(6, %d, %d, 1, 0);", dbID, viewID),
			fmt.Sprintf("DBCC AUTOPILOT(0, %d, %d, %d, 3308);", dbID, tableID, hypID),
			"SET AUTOPILOT ON;",
		} {
			if err := exec(ctx, c, s); err != nil {
				return fmt.Errorf("%s: %w", s, err)
			}
		}
		plan, err := single(ctx, c, f.stmt)
		if offErr := exec(ctx, c, "SET AUTOPILOT OFF;"); offErr != nil && err == nil {
			err = offErr
		}
		if err != nil {
			return fmt.Errorf("%s under AUTOPILOT: %w", f.file, err)
		}
		if err := write(f.file, plan); err != nil {
			return err
		}
	}
	return nil
}

func showplanTo(ctx context.Context, c *sql.Conn, stmt, file string) error {
	plan, err := showplan(ctx, c, stmt)
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	return write(file, plan)
}

// showplan compiles stmt under SET SHOWPLAN_XML ON, each SET in its own batch.
func showplan(ctx context.Context, c *sql.Conn, stmt string) (string, error) {
	if err := exec(ctx, c, "SET SHOWPLAN_XML ON;"); err != nil {
		return "", err
	}
	plan, err := single(ctx, c, stmt)
	if offErr := exec(ctx, c, "SET SHOWPLAN_XML OFF;"); offErr != nil && err == nil {
		err = offErr
	}
	return plan, err
}

// single runs stmt and returns the one plan document it yields.
func single(ctx context.Context, c *sql.Conn, stmt string) (string, error) {
	rows, err := c.QueryContext(ctx, stmt)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var plans []string
	for {
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				return "", err
			}
			plans = append(plans, s)
		}
		if !rows.NextResultSet() {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(plans) != 1 {
		return "", fmt.Errorf("%d plan documents, want 1", len(plans))
	}
	return plans[0], nil
}

func exec(ctx context.Context, c *sql.Conn, stmt string) error {
	_, err := c.ExecContext(ctx, stmt)
	return err
}

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
	return rows.Err()
}

func write(file, plan string) error {
	p := filepath.Join(outDir, file)
	log.Printf("writing %s (%d bytes)", p, len(plan))
	return os.WriteFile(p, []byte(plan), 0o644)
}

func randomID() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}
