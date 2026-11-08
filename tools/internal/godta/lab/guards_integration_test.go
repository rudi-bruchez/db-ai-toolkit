package lab_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/conn"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/ittest"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/lab"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/tsql"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/workspace"
)

// dropOnCleanup drops name on server in t.Cleanup, failed test or not.
func dropOnCleanup(t *testing.T, server *sql.DB, name string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		q := tsql.QuoteName(name)
		if _, err := server.ExecContext(ctx,
			"IF DB_ID("+tsql.QuoteString(name)+") IS NOT NULL BEGIN ALTER DATABASE "+q+
				" SET SINGLE_USER WITH ROLLBACK IMMEDIATE; DROP DATABASE "+q+"; END"); err != nil {
			t.Errorf("dropping %s: %v", name, err)
		}
	})
}

func TestCheckCloneDBRefusesRealDB(t *testing.T) {
	p, r := ittest.Profile(t, "GODTA_TEST_PROFILE")
	server := ittest.OpenDB(t, p, r, "")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// The name has the run form, so only IsClone = 0 can refuse it.
	name := "godta_" + workspace.NewRunID() + "_r"
	if !tsql.IsRunName(name) {
		t.Fatalf("test name %q is not a run name", name)
	}
	if _, err := server.ExecContext(ctx, "CREATE DATABASE "+tsql.QuoteName(name)); err != nil {
		t.Fatalf("creating %s: %v", name, err)
	}
	dropOnCleanup(t, server, name)
	err := lab.CheckCloneDB(ctx, server, name)
	if err == nil {
		t.Fatalf("CheckCloneDB accepted %s, a database that is not a clone", name)
	}
	t.Logf("refused: %v", err)
}

func TestCheckCloneDBRefusesMissingDB(t *testing.T) {
	p, r := ittest.Profile(t, "GODTA_TEST_PROFILE")
	server := ittest.OpenDB(t, p, r, "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// DATABASEPROPERTYEX returns NULL for a database that does not exist.
	name := "godta_" + workspace.NewRunID() + "_missing"
	err := lab.CheckCloneDB(ctx, server, name)
	if err == nil {
		t.Fatalf("CheckCloneDB accepted %s, which does not exist", name)
	}
	t.Logf("refused: %v", err)
}

func TestCheckCloneDBAcceptsClone(t *testing.T) {
	p, r := ittest.Profile(t, "GODTA_TEST_PROFILE")
	server := ittest.OpenDB(t, p, r, "")
	src := ittest.NewDatabase(t, server, "")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	clone := "godta_" + workspace.NewRunID() + "_c"
	if _, err := server.ExecContext(ctx,
		"DBCC CLONEDATABASE ("+tsql.QuoteName(src)+", "+tsql.QuoteName(clone)+") WITH NO_INFOMSGS"); err != nil {
		t.Fatalf("cloning %s to %s: %v", src, clone, err)
	}
	// Registered after NewDatabase's cleanup, so it runs first.
	dropOnCleanup(t, server, clone)
	if err := lab.CheckCloneDB(ctx, server, clone); err != nil {
		t.Errorf("CheckCloneDB refused the clone %s: %v", clone, err)
	}
}

func TestCheckDistinctOnTwoContainers(t *testing.T) {
	sp, sr := ittest.Profile(t, "GODTA_TEST_PROFILE")
	lp, lr := ittest.Profile(t, "GODTA_TEST_LAB_PROFILE")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	src, err := conn.Identify(ctx, ittest.OpenDB(t, sp, sr, ""))
	if err != nil {
		t.Fatal(err)
	}
	labi, err := conn.Identify(ctx, ittest.OpenDB(t, lp, lr, ""))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("source %s/%s, lab %s/%s", src.ServerName, src.MachineName, labi.ServerName, labi.MachineName)
	if err := lab.CheckDistinct(src, labi); err != nil {
		t.Errorf("CheckDistinct(source, lab): %v, want nil", err)
	}
	if err := lab.CheckDistinct(src, src); err == nil {
		t.Error("CheckDistinct(source, source) accepted the same instance")
	}
}
