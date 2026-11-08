package report

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/ittest"
)

// batches splits a script on its GO lines.
func batches(script string) []string {
	var out []string
	var cur []string
	for _, line := range strings.Split(script, "\n") {
		if strings.TrimSpace(line) == "GO" {
			out = append(out, strings.Join(cur, "\n"))
			cur = nil
			continue
		}
		cur = append(cur, line)
	}
	if rest := strings.TrimSpace(strings.Join(cur, "\n")); rest != "" && !onlyComments(rest) {
		out = append(out, rest)
	}
	return out
}

func onlyComments(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		if l := strings.TrimSpace(line); l != "" && !strings.HasPrefix(l, "--") {
			return false
		}
	}
	return true
}

func indexExists(t *testing.T, ctx context.Context, c *sql.Conn, table, name string) bool {
	t.Helper()
	var n int
	if err := c.QueryRowContext(ctx, "SELECT COUNT(*) FROM sys.indexes WHERE object_id = OBJECT_ID(@p1) AND name = @p2",
		table, name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

// TestScriptRunsOnLab runs every batch of the generated indexes.sql on the
// data set, inside a transaction rolled back: the script parses, executes,
// and does what it says (new index, widened index, replacement then drop).
func TestScriptRunsOnLab(t *testing.T) {
	p, r := ittest.Profile(t, "GODTA_TEST_PROFILE")
	server := ittest.OpenDB(t, p, r, "")
	name := ittest.NewDatabase(t, server, "")
	db := ittest.OpenDB(t, p, r, name)
	ittest.LoadDataset(t, db)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if _, err := db.ExecContext(ctx, "CREATE INDEX nix_t_b ON dbo.godta_t(b)"); err != nil {
		t.Fatal(err)
	}

	w := fixture(name)
	delete(w.Tables, tblR)
	_, script, _ := write(t, w, recs())
	bs := batches(script)
	if len(bs) < 5 {
		t.Fatalf("%d batches, want at least 5:\n%s", len(bs), script)
	}

	c, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.ExecContext(ctx, "BEGIN TRANSACTION"); err != nil {
		t.Fatal(err)
	}
	rolledBack := false
	defer func() {
		if !rolledBack {
			c.ExecContext(context.Background(), "IF @@TRANCOUNT > 0 ROLLBACK")
		}
	}()
	for i, b := range bs {
		if _, err := c.ExecContext(ctx, b); err != nil {
			t.Fatalf("batch %d failed: %v\n%s", i+1, err, b)
		}
	}
	for _, ix := range [][2]string{
		{"dbo.godta_t", "IX_godta_t_a_b_a1b2c3"},
		{"dbo.godta_t", "nix_t_b"},
		{"dbo.godta_h", "IX_godta_h_x_y_abcabc"},
	} {
		if !indexExists(t, ctx, c, ix[0], ix[1]) {
			t.Errorf("after the script, %s.%s does not exist", ix[0], ix[1])
		}
	}
	if indexExists(t, ctx, c, "dbo.godta_h", "nix_h_x") {
		t.Errorf("after the script, the replaced nix_h_x still exists")
	}
	var inc int
	if err := c.QueryRowContext(ctx, `SELECT COUNT(*) FROM sys.index_columns ic JOIN sys.indexes i
  ON i.object_id = ic.object_id AND i.index_id = ic.index_id
WHERE i.object_id = OBJECT_ID(N'dbo.godta_t') AND i.name = N'nix_t_b' AND ic.is_included_column = 1`).Scan(&inc); err != nil {
		t.Fatal(err)
	}
	if inc != 1 {
		t.Errorf("widened nix_t_b has %d included columns, want 1", inc)
	}
	if _, err := c.ExecContext(ctx, "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	rolledBack = true
	if !indexExists(t, ctx, c, "dbo.godta_h", "nix_h_x") || indexExists(t, ctx, c, "dbo.godta_t", "IX_godta_t_a_b_a1b2c3") {
		t.Errorf("the rollback did not restore the indexes")
	}
}
