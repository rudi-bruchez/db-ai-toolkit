// supersede.go handles the clones a newer clone of the same database
// replaced (State.Superseded). Each clone leaves a restored database on the
// lab and a backup file in the host directory; without this, they pile up.
package lab

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/tsql"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/workspace"
)

// Supersede runs once the lab is prepared with the current clone s.Prepared.
// For each entry of s.Superseded:
//
//   - its restored database, when still on the lab and CheckCloneDB says it
//     is a goDTA clone, is dropped: the lab may be written to freely (spec
//     section 7), and nothing reads that database again;
//   - its file, when still on the host, is left there, since the host
//     directory is the user's disk, and reported with the command that
//     removes it.
//
// An entry whose database and file are both gone leaves the state, which is
// saved in dir. The notices say what was dropped and what is left.
func Supersede(ctx context.Context, server *sql.DB, ws workspace.WS, dir string, s *State) ([]string, error) {
	if len(s.Superseded) == 0 {
		return nil, nil
	}
	current := ""
	if s.Prepared != nil {
		current = s.Prepared.Database
	}
	var notices []string
	var keep []Superseded
	for _, e := range s.Superseded {
		if e.Database != "" && e.Database != current {
			n, gone := dropSuperseded(ctx, server, e.Database, current)
			notices = append(notices, n...)
			if gone {
				e.Database = ""
			}
		}
		if e.File.HostPath != "" {
			if _, err := os.Stat(e.File.HostPath); err == nil {
				notices = append(notices, fmt.Sprintf(
					"%s, the file of a clone that a newer clone replaced, is still in the host directory; goDTA does not delete files on the host, remove it with: rm -- %s",
					e.File.HostPath, shellQuote(e.File.HostPath)))
			} else if errors.Is(err, os.ErrNotExist) {
				e.File = File{}
			} else {
				notices = append(notices, fmt.Sprintf("%s, the file of a replaced clone: %v", e.File.HostPath, err))
			}
		}
		if e.Database != "" || e.File.HostPath != "" {
			keep = append(keep, e)
		}
	}
	s.Superseded = keep
	return notices, SaveState(ws, dir, *s)
}

// dropSuperseded drops db from the lab when it is there and a goDTA clone.
// gone is true when db is no longer on the lab afterwards.
func dropSuperseded(ctx context.Context, server *sql.DB, db, current string) (notices []string, gone bool) {
	err := CheckCloneDB(ctx, server, db)
	switch {
	case errors.Is(err, ErrCloneDBMissing):
		return nil, true
	case err != nil:
		return []string{fmt.Sprintf("the restored database %s of a replaced clone is not dropped: %v", db, err)}, false
	}
	if err := dropIfThere(server, db); err != nil {
		return []string{fmt.Sprintf("the restored database %s of a replaced clone could not be dropped: %v; drop it with: %s",
			db, err, tsql.DropDatabase(db))}, false
	}
	return []string{fmt.Sprintf("dropped %s from the lab: the restored database of a clone that %s replaced", db, current)}, true
}

// shellQuote quotes s for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// SupersededNotices are what clone says of the clones it replaced, before
// any lab preparation: what evaluate --target lab will drop, and the files
// it leaves to the user.
func SupersededNotices(s State) []string {
	var out []string
	for _, e := range s.Superseded {
		if e.Database != "" {
			out = append(out, fmt.Sprintf("this clone replaces an earlier one: its restored database %s is dropped from the lab by the next evaluate --target lab", e.Database))
		}
		if e.File.HostPath != "" {
			if _, err := os.Stat(e.File.HostPath); err == nil {
				out = append(out, fmt.Sprintf("%s, the file of a replaced clone, stays in the host directory; goDTA does not delete files on the host, remove it with: rm -- %s",
					e.File.HostPath, shellQuote(e.File.HostPath)))
			}
		}
	}
	return out
}
