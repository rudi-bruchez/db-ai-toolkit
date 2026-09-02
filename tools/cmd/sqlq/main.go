// Command sqlq runs a read-only query against a SQL Server instance named by a
// connection profile and prints one JSON object on stdout.
//
// It exists so that an AI agent asking questions about a live instance does not
// have to rebuild connection ceremony on every turn, and cannot accidentally
// write to production. The guard here is accident prevention, not security:
// the control that matters is the login itself (db_datareader + VIEW
// DEFINITION + VIEW SERVER STATE).
package main

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	mssql "github.com/microsoft/go-mssqldb"
	_ "github.com/microsoft/go-mssqldb/azuread"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/sqlq"
)

// Exit codes, so a caller can tell the failure kinds apart without parsing.
const (
	exitOK        = 0
	exitUsage     = 1
	exitSQL       = 2
	exitRefused   = 3
	exitConnected = 4 // connection or transport failure
)

// preamble runs before every batch. LOCK_TIMEOUT is the anti-blocking guard:
// sqlq gives up waiting for a lock rather than queueing behind production work.
//
// The isolation level is deliberately NOT changed here. READ UNCOMMITTED would
// also avoid waiting on shared locks, but it permits dirty reads, missing rows
// and duplicated rows during page splits - and the questions this tool exists
// to answer ("why does this view not return my data") are correctness
// questions. Answering one from a dirty read produces a confidently wrong
// answer. Callers who want it have to ask, with -dirty-reads.
const preamble = `SET NOCOUNT ON;
SET LOCK_TIMEOUT 5000;`

// dirtyReadsPreamble is appended only on explicit request.
const dirtyReadsPreamble = "\nSET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;"

type paramList []string

func (p *paramList) String() string { return strings.Join(*p, ",") }
func (p *paramList) Set(v string) error {
	if !strings.Contains(v, "=") {
		return fmt.Errorf("expected name=value, got %q", v)
	}
	*p = append(*p, v)
	return nil
}

// defineFlags declares every command-line flag on fs and returns the options
// they will fill in. It is separate from main so a test can walk the flag set.
func defineFlags(fs *flag.FlagSet) *options {
	o := &options{}
	fs.StringVar(&o.profileName, "profile", "", "connection profile name (required)")
	fs.StringVar(&o.profilesPath, "profiles", "", "path to the profiles file (default $MSSQL_PROFILES, then "+sqlq.DefaultProfilePath()+")")
	fs.StringVar(&o.query, "query", "", "SQL to run")
	fs.StringVar(&o.file, "file", "", "file containing the SQL to run")
	fs.StringVar(&o.database, "database", "", "override the profile's database")
	fs.IntVar(&o.maxRows, "maxrows", 50, "maximum rows to return; 0 means unlimited")
	fs.IntVar(&o.timeoutSec, "timeout", 30, "query timeout in seconds")
	fs.BoolVar(&o.wantPlan, "plan", false, "capture the actual execution plan (SET STATISTICS XML ON)")
	fs.BoolVar(&o.listProfiles, "list-profiles", false, "list the configured profiles and exit")
	fs.BoolVar(&o.allowWrite, "allow-write", false, "permit writing statements; the profile must also be in readwrite mode")
	fs.BoolVar(&o.dirtyReads, "dirty-reads", false, "run at READ UNCOMMITTED: avoids waiting on locks, but permits dirty reads and missing or duplicated rows")
	fs.Var(&o.params, "param", "SQL parameter as name=value; repeatable")
	return o
}

func main() {
	fs := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	o := defineFlags(fs)
	_ = fs.Parse(os.Args[1:])
	os.Exit(run(*o))
}

// options is what one invocation was asked to do.
type options struct {
	profileName  string
	profilesPath string
	query        string
	file         string
	database     string
	maxRows      int
	timeoutSec   int
	wantPlan     bool
	listProfiles bool
	allowWrite   bool
	dirtyReads   bool
	params       paramList
}

func run(o options) int {
	profiles, err := sqlq.LoadProfiles(resolveProfilesPath(o.profilesPath))
	if err != nil {
		return fail(exitUsage, err)
	}

	if o.listProfiles {
		return emit(map[string]any{"profiles": describeProfiles(profiles)})
	}

	if o.profileName == "" {
		return fail(exitUsage, fmt.Errorf("-profile is required (available: %s)",
			strings.Join(profiles.Names(), ", ")))
	}
	profile, err := profiles.Get(o.profileName)
	if err != nil {
		return fail(exitUsage, err)
	}
	if o.database != "" {
		profile.Database = o.database
	}

	sqlText, err := readQuery(o.query, o.file)
	if err != nil {
		return fail(exitUsage, err)
	}

	// Writing takes two independent yeses: the profile must permit it, and
	// this invocation must intend it. A profile is a persistent property of a
	// file somebody edited once; -allow-write is a statement about right now.
	if violations := sqlq.FindWrites(sqlText); len(violations) > 0 {
		switch {
		case profile.ReadOnly():
			return fail(exitRefused, fmt.Errorf(
				"profile %q is read-only and this batch would write: statement %q uses %s",
				profile.Name, truncate(violations[0].Statement, 120), violations[0].Keyword))
		case !o.allowWrite:
			return fail(exitRefused, fmt.Errorf(
				"this batch would write (statement %q uses %s) and profile %q permits it, but "+
					"-allow-write was not given; pass it only once the user has confirmed the write",
				truncate(violations[0].Statement, 120), violations[0].Keyword, profile.Name))
		}
	}

	named, err := namedArgs(o.params)
	if err != nil {
		return fail(exitUsage, err)
	}

	result, code := execute(profile, sqlText, named, o)
	_ = emit(result)
	return code
}

func execute(profile sqlq.Profile, sqlText string, args []any, o options) (sqlq.Result, int) {

	result := sqlq.Result{
		Profile:  profile.Name,
		Server:   profile.Server,
		Database: profile.Database,
	}

	// The DSN carries the password. Every error text from here on is redacted
	// before it reaches stdout, because stdout ends up in an agent transcript.
	secret := passwordOf(profile)

	driver, dsn, err := profile.DSN(os.Getenv)
	if err != nil {
		result.Error = &sqlq.SQLError{Message: redact(err.Error(), secret)}
		return result, exitUsage
	}

	db, err := sql.Open(string(driver), dsn)
	if err != nil {
		result.Error = &sqlq.SQLError{Message: redact(err.Error(), secret)}
		return result, exitConnected
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(o.timeoutSec)*time.Second)
	defer cancel()

	// One connection for the whole run: the preamble sets session state, and
	// session state does not survive a trip back to the pool.
	conn, err := db.Conn(ctx)
	if err != nil {
		result.Error = redactErr(toSQLError(err), secret)
		return result, exitConnected
	}
	defer conn.Close()

	started := time.Now()

	setup := preamble
	if o.dirtyReads {
		setup += dirtyReadsPreamble
	}
	if o.wantPlan {
		setup += "\nSET STATISTICS XML ON;"
	}
	if _, err := conn.ExecContext(ctx, setup); err != nil {
		result.ElapsedMS = time.Since(started).Milliseconds()
		result.Error = redactErr(toSQLError(err), secret)
		return result, exitSQL
	}

	rows, err := conn.QueryContext(ctx, sqlText, args...)
	if err != nil {
		result.ElapsedMS = time.Since(started).Milliseconds()
		result.Error = redactErr(toSQLError(err), secret)
		return result, exitSQL
	}
	defer rows.Close()

	if err := collect(rows, &result, o.maxRows); err != nil {
		result.ElapsedMS = time.Since(started).Milliseconds()
		result.Error = redactErr(toSQLError(err), secret)
		return result, exitSQL
	}

	result.ElapsedMS = time.Since(started).Milliseconds()
	return result, exitOK
}

// collect reads the first result set into the result, then looks through any
// further result sets for a showplan, which SET STATISTICS XML ON returns
// after the data.
func collect(rows *sql.Rows, result *sqlq.Result, maxRows int) error {
	first := true
	for {
		cols, err := rows.ColumnTypes()
		if err != nil {
			return err
		}
		if isShowplan(cols) {
			plan, err := readSingleString(rows)
			if err != nil {
				return err
			}
			result.Plan = plan
		} else if first {
			set := sqlq.NewRowSet(maxRows)
			result.Columns = make([]sqlq.Column, len(cols))
			for i, c := range cols {
				result.Columns[i] = sqlq.Column{Name: c.Name(), Type: c.DatabaseTypeName()}
			}
			if err := scanAll(rows, cols, set); err != nil {
				return err
			}
			result.Rows = set.Rows
			result.RowCount = set.RowCount
			result.Truncated = set.Truncated
			first = false
		} else {
			// Extra result sets beyond the first are drained, not reported:
			// one query, one answer.
			for rows.Next() {
			}
		}
		if !rows.NextResultSet() {
			break
		}
	}
	return rows.Err()
}

func scanAll(rows *sql.Rows, cols []*sql.ColumnType, set *sqlq.RowSet) error {
	for rows.Next() {
		holders := make([]any, len(cols))
		for i := range holders {
			holders[i] = new(any)
		}
		if err := rows.Scan(holders...); err != nil {
			return err
		}
		row := make(sqlq.Row, len(cols))
		for i, c := range cols {
			row[c.Name()] = normalise(*(holders[i].(*any)), c.DatabaseTypeName())
		}
		set.Add(row)
	}
	return rows.Err()
}

// normalise turns driver values into things that survive JSON without losing
// their meaning: times as RFC3339, binary as hex, everything else as-is.
func normalise(v any, dbType string) any {
	switch value := v.(type) {
	case nil:
		return nil
	case []byte:
		switch strings.ToUpper(dbType) {
		case "BINARY", "VARBINARY", "IMAGE", "TIMESTAMP", "ROWVERSION":
			return "0x" + hex.EncodeToString(value)
		default:
			return string(value)
		}
	case time.Time:
		return value.Format(time.RFC3339Nano)
	default:
		return value
	}
}

func isShowplan(cols []*sql.ColumnType) bool {
	return len(cols) == 1 && strings.Contains(strings.ToLower(cols[0].Name()), "showplan")
}

func readSingleString(rows *sql.Rows) (*string, error) {
	for rows.Next() {
		var s sql.NullString
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		if s.Valid {
			value := s.String
			return &value, nil
		}
	}
	return nil, rows.Err()
}

// toSQLError keeps the SQL Server error detail that a bare error string throws
// away: number, severity, state, line and the procedure it fired from.
func toSQLError(err error) *sqlq.SQLError {
	var e mssql.Error
	if errors.As(err, &e) {
		return &sqlq.SQLError{
			Number:    e.Number,
			Severity:  e.Class,
			State:     e.State,
			Line:      e.LineNo,
			Procedure: e.ProcName,
			Message:   e.Message,
		}
	}
	return &sqlq.SQLError{Message: err.Error()}
}

func resolveProfilesPath(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if fromEnv := os.Getenv("MSSQL_PROFILES"); fromEnv != "" {
		return fromEnv
	}
	return sqlq.DefaultProfilePath()
}

func readQuery(query, file string) (string, error) {
	switch {
	case query != "" && file != "":
		return "", fmt.Errorf("give either -query or -file, not both")
	case query != "":
		return query, nil
	case file != "":
		raw, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("reading -file: %w", err)
		}
		return string(raw), nil
	default:
		return "", fmt.Errorf("one of -query or -file is required")
	}
}

func namedArgs(params paramList) ([]any, error) {
	args := make([]any, 0, len(params))
	for _, p := range params {
		name, value, _ := strings.Cut(p, "=")
		name = strings.TrimPrefix(strings.TrimSpace(name), "@")
		if name == "" {
			return nil, fmt.Errorf("-param %q has an empty name", p)
		}
		args = append(args, sql.Named(name, value))
	}
	return args, nil
}

func describeProfiles(profiles sqlq.Profiles) []map[string]any {
	out := make([]map[string]any, 0, len(profiles))
	for _, name := range profiles.Names() {
		p := profiles[name]
		out = append(out, map[string]any{
			"name":     name,
			"server":   p.Server,
			"database": p.Database,
			"auth":     string(p.Auth),
			"readonly": p.ReadOnly(),
		})
	}
	return out
}

func emit(v any) int {
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		fmt.Fprintln(os.Stderr, "sqlq: writing output:", err)
		return exitUsage
	}
	return exitOK
}

// fail reports an error in the same JSON shape as a success, so the caller
// never has to parse two formats.
func fail(code int, err error) int {
	_ = emit(sqlq.Result{Error: &sqlq.SQLError{Message: err.Error()}})
	return code
}

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// passwordOf returns the secret the profile will put into the connection
// string, or "" when there is none to hide.
func passwordOf(p sqlq.Profile) string {
	if p.PasswordEnv == "" {
		return ""
	}
	return os.Getenv(p.PasswordEnv)
}

// redact removes a secret from text. It is a last line of defence: a driver
// error quoting the connection string would otherwise print the password on
// stdout, and from there into an agent transcript or a ticket.
func redact(text, secret string) string {
	if secret == "" {
		return text
	}
	return strings.ReplaceAll(text, secret, "[redacted]")
}

// redactErr scrubs a secret out of a structured SQL error.
func redactErr(e *sqlq.SQLError, secret string) *sqlq.SQLError {
	if e != nil {
		e.Message = redact(e.Message, secret)
	}
	return e
}
