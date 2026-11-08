package conn_test

import (
	"context"
	"testing"
	"time"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/conn"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/ittest"
)

func TestIdentifyLab(t *testing.T) {
	p, r := ittest.Profile(t, "GODTA_TEST_PROFILE")
	db := ittest.OpenDB(t, p, r, "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	i, err := conn.Identify(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%+v", i)
	if i.Major < 13 {
		t.Errorf("major %d, want >= 13", i.Major)
	}
	if err := conn.CheckSupported(i); err != nil {
		t.Errorf("CheckSupported: %v", err)
	}
	var app string
	if err := db.QueryRowContext(ctx, "SELECT APP_NAME();").Scan(&app); err != nil {
		t.Fatal(err)
	}
	if app != conn.AppName {
		t.Errorf("APP_NAME() %q, want %q", app, conn.AppName)
	}
}
