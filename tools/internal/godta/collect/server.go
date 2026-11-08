// Package collect reads, on the source and read only, what goDTA needs of
// each database: the workload from the Query Store or the plan cache, the
// tables the plans touch with their indexes and statistics, the scoped
// configurations and the missing index groups (spec section 8.2).
package collect

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/conn"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/tsql"
)

// Options drive the collection.
type Options struct {
	// Window defaults to the 24 hours up to now.
	Window model.Window
	// TopN defaults to 50.
	TopN int
	// Include and Exclude are path.Match patterns on the database name,
	// case-insensitive. No Include means every database.
	Include, Exclude []string
}

// Defaults.
const (
	DefaultTopN   = 50
	DefaultWindow = 24 * time.Hour
)

// withDefaults fills what opt leaves out.
func (opt Options) withDefaults() Options {
	if opt.Window.Until.IsZero() {
		opt.Window.Until = time.Now()
	}
	if opt.Window.From.IsZero() {
		opt.Window.From = opt.Window.Until.Add(-DefaultWindow)
	}
	if opt.TopN <= 0 {
		opt.TopN = DefaultTopN
	}
	return opt
}

// Skip is a database left out, with the reason.
type Skip struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// Skip reasons.
const (
	ReasonSystem              = "system"
	ReasonFiltered            = "filtered"
	ReasonClone               = "clone"
	ReasonRunName             = "godta object"
	ReasonOffline             = "offline"
	ReasonUnreadableSecondary = "unreadable secondary"
	ReasonReadOnly            = "read_only"
	// SINGLE_USER and RESTRICTED_USER are maintenance states a DBA sets:
	// connecting could take the only connection, or one meant for the DBA.
	ReasonSingleUser     = "single_user"
	ReasonRestrictedUser = "restricted_user"
)

// dbRow is one row of sys.databases, with what classify decides on.
type dbRow struct {
	info                model.DBInfo
	system              bool
	state               string // state_desc
	readOnly            bool
	unreadableSecondary bool
	userAccess          string // user_access_desc
}

// databasesQuery lists the databases. A database is an unreadable secondary
// when the local replica of its availability group is secondary and does
// not allow connections; the HADR views are empty without availability
// groups. DATABASEPROPERTYEX returns NULL for an offline database.
const databasesQuery = `SELECT /* godta:internal */ d.database_id, d.name, ISNULL(d.collation_name, N''), d.compatibility_level,
  d.state_desc, d.is_read_only, d.user_access_desc,
  ISNULL(CAST(DATABASEPROPERTYEX(d.name, 'IsClone') AS int), 0),
  CASE WHEN d.database_id <= 4 OR d.is_distributor = 1 THEN 1 ELSE 0 END,
  CASE WHEN EXISTS (SELECT 1 FROM sys.dm_hadr_database_replica_states rs
         JOIN sys.availability_replicas ar ON ar.replica_id = rs.replica_id
         WHERE rs.database_id = d.database_id AND rs.is_local = 1
           AND rs.is_primary_replica = 0 AND ar.secondary_role_allow_connections = 0)
       THEN 1 ELSE 0 END
FROM sys.databases d
ORDER BY d.name;`

// Databases lists the databases of the server q is connected to, and keeps
// those to collect. Left out, with the reason: system databases, those the
// patterns filter out, clones (IsClone), names of goDTA's own objects
// (tsql.IsRunName: its clones), offline, unreadable secondaries, SINGLE_USER
// and RESTRICTED_USER, READ_ONLY.
// A clone is also READ_ONLY: the clone reason is checked first.
func Databases(ctx context.Context, q conn.Querier, opt Options) (keep []model.DBInfo, skipped []Skip, err error) {
	if err := checkPatterns(opt); err != nil {
		return nil, nil, err
	}
	rows, err := q.QueryContext(ctx, databasesQuery)
	if err != nil {
		return nil, nil, fmt.Errorf("listing the databases: %w", err)
	}
	defer rows.Close()
	var dbs []dbRow
	for rows.Next() {
		var r dbRow
		var clone, system, secondary int
		if err := rows.Scan(&r.info.ID, &r.info.Name, &r.info.Collation, &r.info.Compat,
			&r.state, &r.readOnly, &r.userAccess, &clone, &system, &secondary); err != nil {
			return nil, nil, fmt.Errorf("listing the databases: %w", err)
		}
		r.info.IsClone, r.system, r.unreadableSecondary = clone == 1, system == 1, secondary == 1
		dbs = append(dbs, r)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("listing the databases: %w", err)
	}
	return classify(dbs, opt)
}

func checkPatterns(opt Options) error {
	for _, list := range [][]string{opt.Include, opt.Exclude} {
		for _, p := range list {
			if _, err := path.Match(strings.ToLower(p), ""); err != nil {
				return fmt.Errorf("database pattern %q: %w", p, err)
			}
		}
	}
	return nil
}

// matches reports whether name matches one of the patterns, ignoring case.
func matches(name string, patterns []string) bool {
	name = strings.ToLower(name)
	for _, p := range patterns {
		if ok, _ := path.Match(strings.ToLower(p), name); ok {
			return true
		}
	}
	return false
}

// classify splits the databases into those kept and those skipped.
func classify(dbs []dbRow, opt Options) (keep []model.DBInfo, skipped []Skip, err error) {
	if err := checkPatterns(opt); err != nil {
		return nil, nil, err
	}
	for _, d := range dbs {
		reason := ""
		switch {
		case d.system:
			reason = ReasonSystem
		case len(opt.Include) > 0 && !matches(d.info.Name, opt.Include),
			matches(d.info.Name, opt.Exclude):
			reason = ReasonFiltered
		case d.info.IsClone:
			reason = ReasonClone
		case tsql.IsRunName(d.info.Name):
			reason = ReasonRunName
		case d.state == "OFFLINE":
			reason = ReasonOffline
		case d.state != "ONLINE":
			reason = strings.ToLower(d.state)
		case d.unreadableSecondary:
			reason = ReasonUnreadableSecondary
		case d.userAccess == "SINGLE_USER":
			reason = ReasonSingleUser
		case d.userAccess == "RESTRICTED_USER":
			reason = ReasonRestrictedUser
		case d.readOnly:
			reason = ReasonReadOnly
		}
		if reason != "" {
			skipped = append(skipped, Skip{Name: d.info.Name, Reason: reason})
			continue
		}
		keep = append(keep, d.info)
	}
	return keep, skipped, nil
}
