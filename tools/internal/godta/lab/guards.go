// Package lab holds goDTA's lab mode. guards.go has the lab guards of spec
// section 7: every one is mandatory, and every one refuses rather than warns,
// except a difference of patch level between source and lab.
package lab

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/conn"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/tsql"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/sqlq"
)

// LabEnvironment is the only environment value a lab profile may declare. The
// field is optional in profiles, so the check is an allow list: an absent or
// other value is refused, "Lab" included.
const LabEnvironment = "lab"

// CheckProfile refuses a lab profile that does not declare exactly
// "environment": "lab", or that is not readwrite. main calls it on the lab
// profile before opening the connection.
func CheckProfile(p sqlq.Profile) error {
	if p.Environment != LabEnvironment {
		return fmt.Errorf("lab profile %q: environment %q, the lab must declare %q", p.Name, p.Environment, LabEnvironment)
	}
	if p.ReadOnly() {
		return fmt.Errorf("lab profile %q: mode %q, the lab must be %q", p.Name, p.Mode, sqlq.ModeReadWrite)
	}
	return nil
}

// CheckDistinct refuses a lab that is the source instance: @@SERVERNAME and
// SERVERPROPERTY('MachineName') must both differ, compared case-insensitively
// (Windows names are). Two named instances on one host share the machine name
// and are refused. An empty name (Identify reads a NULL as "") cannot be
// compared and is refused too.
func CheckDistinct(src, labi conn.Instance) error {
	for _, f := range []struct{ what, src, lab string }{
		{"@@SERVERNAME", src.ServerName, labi.ServerName},
		{"MachineName", src.MachineName, labi.MachineName},
	} {
		if f.src == "" || f.lab == "" {
			return fmt.Errorf("lab guard: %s is empty on the source (%q) or the lab (%q), cannot tell the instances apart", f.what, f.src, f.lab)
		}
		if strings.EqualFold(f.src, f.lab) {
			return fmt.Errorf("lab guard: the lab has the source's %s %q, it must be another instance", f.what, f.lab)
		}
	}
	return nil
}

// CheckSameVersion refuses a lab of another major version than the source: the
// optimizer, hence the estimates, change between them. A difference of level
// or build is returned as a warning, err nil.
func CheckSameVersion(src, labi conn.Instance) (warning string, err error) {
	if src.Major != labi.Major {
		return "", fmt.Errorf("lab guard: the source is SQL Server major version %d, the lab %d; they must be equal", src.Major, labi.Major)
	}
	if src.Level != labi.Level || src.Version != labi.Version {
		return fmt.Sprintf("the lab is %s %s, the source %s %s: estimates may differ between builds",
			labi.Version, labi.Level, src.Version, src.Level), nil
	}
	return "", nil
}

// ErrCloneDBMissing is what CheckCloneDB wraps when the database is not on
// the lab at all, as opposed to there and not a clone: evaluate --target lab
// then restores it again from the host file.
var ErrCloneDBMissing = errors.New("does not exist")

// isCloneQuery reads IsClone, NULL when the database does not exist (measured).
const isCloneQuery = `SELECT CAST(DATABASEPROPERTYEX(@p1, 'IsClone') AS int);`

// CheckCloneDB refuses a database whose name is not of goDTA's run form or
// that is not a DBCC CLONEDATABASE clone (IsClone = 1; the property survives
// a backup and a restore, measured). The name is checked first, without a
// query. It runs right after the RESTORE on the lab and before every
// evaluate --target lab: before the RESTORE the database does not exist.
func CheckCloneDB(ctx context.Context, q conn.Querier, db string) error {
	if !tsql.IsRunName(db) {
		return fmt.Errorf("lab guard: database %q is not named like a goDTA object (godta_<run ID>_...)", db)
	}
	var isClone sql.NullInt64
	if err := q.QueryRowContext(ctx, isCloneQuery, db).Scan(&isClone); err != nil {
		return fmt.Errorf("lab guard: reading IsClone of %q: %w", db, err)
	}
	switch {
	case !isClone.Valid:
		return fmt.Errorf("lab guard: database %q %w", db, ErrCloneDBMissing)
	case isClone.Int64 != 1:
		return fmt.Errorf("lab guard: database %q is not a clone (IsClone = %d)", db, isClone.Int64)
	}
	return nil
}
