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
	"io"
	"net/url"
	"os"
	"runtime"
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
	// ContinueOnError, not ExitOnError: the promise is one JSON object on stdout
	// on success and on failure alike, and flag's own exit path honours neither
	// that nor the exit codes below - it leaves usage text on stderr and exits
	// 2, which here means "SQL error".
	fs := flag.NewFlagSet("sqlq", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := defineFlags(fs)
	if err := fs.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(emitUsage(fs))
		}
		os.Exit(fail(exitUsage, err))
	}
	if err := o.validate(); err != nil {
		os.Exit(fail(exitUsage, err))
	}
	os.Exit(run(*o))
}

// emitUsage answers -help in the contract's own shape rather than as free text
// on stderr, so a caller parsing sqlq never meets a second format.
func emitUsage(fs *flag.FlagSet) int {
	var b strings.Builder
	fs.SetOutput(&b)
	fs.PrintDefaults()
	_ = emit(map[string]any{"usage": "sqlq -profile <name> -query <sql>", "flags": b.String()})
	return exitOK
}

// validate rejects the option values that would otherwise be accepted and then
// quietly mean something else.
func (o options) validate() error {
	if o.maxRows < 0 {
		return fmt.Errorf("-maxrows must be 0 (unlimited) or a positive number, got %d", o.maxRows)
	}
	if o.timeoutSec <= 0 {
		// A negative or zero duration builds a context that has already
		// expired, so the query is cancelled before it is sent and the error
		// says nothing about why.
		return fmt.Errorf("-timeout must be a positive number of seconds, got %d", o.timeoutSec)
	}
	return nil
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
		// Not the full list: the profile names are the estate map, and a bare
		// mistake should not publish it. -list-profiles is the way to look.
		return fail(exitUsage, fmt.Errorf(
			"-profile is required (%d defined; use -list-profiles)", len(profiles)))
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

	// USE writes nothing, so the write guard passes it - and it silently makes
	// two of this tool's own statements false at once: the "database" field of
	// the answer still names the profile's catalog, and -database is overridden
	// from inside the text it was meant to govern.
	if changes := sqlq.FindContextChanges(sqlText); len(changes) > 0 {
		return fail(exitRefused, fmt.Errorf(
			"%s changes the database for the rest of the batch, so the reported database "+
				"would no longer be the one queried: statement %q. Select the database with "+
				"-database, or name it in the object (Other.dbo.T).",
			changes[0].Keyword, truncate(changes[0].Statement, 120)))
	}

	// GO is a client batch separator, not T-SQL. The guard above already knows
	// how to see it - it splits statements on it - but the execution path sends
	// the text through untouched, so the server answers with a syntax error
	// that explains nothing. Refuse rather than split: a correct splitter has to
	// respect string literals, both comment forms and bracketed identifiers,
	// which is real work for a need (several read-only batches at once) that
	// does not arise.
	if lines := sqlq.FindBatchSeparators(sqlText); len(lines) > 0 {
		return fail(exitUsage, fmt.Errorf(
			"GO is a client batch separator, not T-SQL (line %d). Send one batch per call.",
			lines[0]))
	}

	named, err := namedArgs(o.params)
	if err != nil {
		return fail(exitUsage, err)
	}

	resolver := sqlq.NewResolver(os.Getenv, resolveCredentialsPath(), func(msg string) {
		fmt.Fprintln(os.Stderr, "sqlq:", msg)
	})

	result, code := execute(profile, sqlText, named, o, resolver.Resolve)
	_ = emit(result)
	return code
}

func execute(profile sqlq.Profile, sqlText string, args []any, o options,
	resolve sqlq.SecretResolver) (sqlq.Result, int) {

	result := sqlq.Result{
		Profile:  profile.Name,
		Server:   profile.Server,
		Database: profile.Database,
	}

	// Resolved once, here, and used twice: the connection string is built from
	// it and every error text below is scrubbed of it. Two separate lookups
	// would be two DPAPI decryptions, and a redaction pass holding a different
	// string than the one in the DSN silently redacts nothing - which is how a
	// protection stays in the code while ceasing to protect.
	secret, err := resolve(profile)
	if err != nil {
		result.Error = &sqlq.SQLError{Message: err.Error()}
		return result, exitUsage
	}

	// The DSN carries the password. Every error text from here on is redacted
	// before it reaches stdout, because stdout ends up in an agent transcript.
	driver, dsn, err := profile.DSN(secret)
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
		result.Error = sqlError(err, secret)
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
		result.Error = sqlError(err, secret)
		return result, exitSQL
	}

	rows, err := conn.QueryContext(ctx, sqlText, args...)
	if err != nil {
		result.ElapsedMS = time.Since(started).Milliseconds()
		result.Error = sqlError(err, secret)
		return result, exitSQL
	}
	defer rows.Close()

	if err := collect(rows, &result, o.maxRows); err != nil {
		result.ElapsedMS = time.Since(started).Milliseconds()
		result.Error = sqlError(err, secret)
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
		// Deliberately no server and no database. Every session starts with
		// -list-profiles, so whatever is here is published into an agent
		// transcript and on to a model provider on every run. A name is enough
		// to choose a profile; the host names, database names and group
		// taxonomy are the estate map, and they are in servers.json for a human
		// who wants them.
		entry := map[string]any{
			"name":     name,
			"auth":     string(p.Auth),
			"readonly": p.ReadOnly(),
		}
		if p.Environment != "" {
			entry["environment"] = p.Environment
		}
		if reason := unusableHere(p); reason != "" {
			entry["unusable"] = reason
		}
		out = append(out, entry)
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

// errLoginFailed is SQL Server's "Login failed for user".
const errLoginFailed = 18456

// loginFailedAdvice is attached to every 18456, because the obvious reaction -
// try again, maybe re-import - is the harmful one. sqlq itself never retries:
// one connection, one attempt, one process. The retry would come from the agent
// reading the error, so the error is where it has to be stopped.
//
// If the password was changed on the server and never re-entered in SSMS, the
// import copies the old one again and the loop runs: failure, re-import, retry,
// failure. Each turn is a failed authentication under an administrator account
// against a production instance. A SQL login created in SSMS has "Enforce
// password policy" ticked by default, which brings the lockout threshold with
// it; and a burst of failed admin logins from a workstation is the shape of a
// credential-stuffing attempt in the other team's security log.
const loginFailedAdvice = "Do not retry - repeated failures can lock the account out, and a burst " +
	"of failed administrator logins from a workstation looks like credential stuffing in the " +
	"server's security log. Fix the password in SSMS first, then re-run " +
	"Import-RegisteredServerCredentials.ps1."

// sqlError converts a driver error to the structured form, scrubs the secret
// out of it, and attaches whatever that error number needs said.
func sqlError(err error, secret string) *sqlq.SQLError {
	return adviseOn(redactErr(toSQLError(err), secret))
}

// adviseOn attaches to an error whatever that error number needs said. Kept
// apart from redaction so it can be driven directly in a test.
func adviseOn(e *sqlq.SQLError) *sqlq.SQLError {
	if e == nil {
		return nil
	}
	if e.Number == errLoginFailed {
		e.Message = e.Message + "\n" + loginFailedAdvice
	}
	return e
}

// unusableHere explains why a profile cannot be used on this machine, or
// returns "" when it can. A DPAPI-backed profile is Windows-only, and saying so
// in the listing is what lets the rest of the file stay usable elsewhere.
func unusableHere(p sqlq.Profile) string {
	if p.PasswordDpapi != "" && runtime.GOOS != "windows" {
		return "needs DPAPI, which is Windows-only; this is " + runtime.GOOS
	}
	return ""
}

// resolveCredentialsPath mirrors resolveProfilesPath: an environment override,
// then the location the import script writes to.
func resolveCredentialsPath() string {
	if fromEnv := os.Getenv("MSSQL_CREDENTIALS"); fromEnv != "" {
		return fromEnv
	}
	return sqlq.DefaultCredentialPath()
}

// redact removes a secret from text. It is a last line of defence: a driver
// error quoting the connection string would otherwise print the password on
// stdout, and from there into an agent transcript or a ticket.
func redact(text, secret string) string {
	if secret == "" {
		return text
	}
	for _, form := range secretForms(secret) {
		text = strings.ReplaceAll(text, form, "[redacted]")
	}
	return text
}

// secretForms lists every spelling of the secret that could appear in an error.
//
// The literal one is not enough, and assuming it was is what made this defence
// weaker than it looked. The DSN is a URL, so url.UserPassword percent-encodes
// the password into it: a password of Synthetic!Passw0rd-42 appears in a driver
// error as Synthetic%21Passw0rd-42, which no search for the literal string will
// find. Every password containing ! @ / ; # % or a space - which is to say most
// passwords chosen under a complexity policy - went through unredacted.
func secretForms(secret string) []string {
	forms := []string{secret}
	add := func(s string) {
		if s == "" {
			return
		}
		for _, seen := range forms {
			if seen == s {
				return
			}
		}
		forms = append(forms, s)
	}
	// Exactly how the DSN encodes it: build the userinfo and drop the colon.
	add(strings.TrimPrefix(url.UserPassword("", secret).String(), ":"))
	// And the query-string spelling, for a driver that reports a parameter.
	add(url.QueryEscape(secret))
	return forms
}

// redactErr scrubs a secret out of a structured SQL error.
func redactErr(e *sqlq.SQLError, secret string) *sqlq.SQLError {
	if e != nil {
		e.Message = redact(e.Message, secret)
	}
	return e
}
