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
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/golang-sql/sqlexp"
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
	fs.BoolVar(&o.listQueries, "list-queries", false, "print the query catalogue as JSON and exit; -profile adds that profile's saved queries")
	fs.StringVar(&o.saved, "saved", "", "run the catalogue query of that name")
	fs.StringVar(&o.queriesDir, "queries", "", "directory of the bundled queries (default: next to the binary)")
	fs.StringVar(&o.tsqlScriptsDir, "tsql-scripts", "", "local clone of tsql-scripts (default $DB_AI_TOOLKIT_TSQL_SCRIPTS)")
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

	listQueries    bool
	saved          string
	queriesDir     string
	tsqlScriptsDir string
}

func run(o options) int {
	if o.listQueries {
		return listQueries(o)
	}
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

	sqlText, err := readQuery(o.query, o.file, o.saved)
	if err != nil {
		return fail(exitUsage, err)
	}

	var named []any
	var saved *sqlq.SavedRun
	var entryHash, registryMsg string
	if o.saved != "" {
		if !sqlq.ValidQueryName(o.saved) {
			return fail(exitUsage, fmt.Errorf("query name %q must match ^[a-z][a-z0-9-]{1,48}$", o.saved))
		}
		cfg := catalogConfig(o, profiles.Names())
		cfg.Registry, registryMsg = sqlq.LoadRegistry(registryPath())
		cat := sqlq.LoadCatalog(cfg)
		e, ok := cat.Find(o.saved)
		if !ok {
			return fail(exitUsage, missingQueryError(cat, cfg, o.saved))
		}
		sqlText, named, saved, err = prepareSaved(e, o.params)
		if err != nil {
			return fail(exitUsage, err)
		}
		entryHash = e.Hash
	} else {
		named, err = namedArgs(o.params)
		if err != nil {
			return fail(exitUsage, err)
		}
	}

	// The guard runs on the text actually sent, rewritten overrides included.
	if code, err := guard(sqlText, profile, o.allowWrite); err != nil {
		return fail(code, err)
	}

	resolver := sqlq.NewResolver(os.Getenv, resolveCredentialsPath(), func(msg string) {
		fmt.Fprintln(os.Stderr, "sqlq:", msg)
	})

	result, code := execute(profile, sqlText, named, o, resolver.Resolve)
	result.Saved = saved
	if registryMsg != "" {
		result.Messages = append(result.Messages, registryMsg)
	}
	if code == exitOK && entryHash != "" {
		recordRun(&result, entryHash, profile)
	}
	_ = emit(result)
	return code
}

// recordRun notes a successful run in the registry. A failure to record is
// reported, not fatal: the query did run, and the cost is a later "verified": null.
func recordRun(result *sqlq.Result, hash string, profile sqlq.Profile) {
	err := sqlq.RecordVerified(registryPath(), hash, sqlq.Verified{
		Date: time.Now().Format("2006-01-02"), Profile: profile.Name})
	if err != nil {
		result.Messages = append(result.Messages, "verification not recorded: "+err.Error())
	}
}

// listQueries prints the catalogue. It needs no profile file unless -profile
// is given, so the catalogue can be inspected on a machine with none.
func listQueries(o options) int {
	profiles, perr := sqlq.LoadProfiles(resolveProfilesPath(o.profilesPath))
	if o.profileName != "" {
		if perr != nil {
			return fail(exitUsage, perr)
		}
		if _, err := profiles.Get(o.profileName); err != nil {
			return fail(exitUsage, err)
		}
	}
	cfg := catalogConfig(o, profiles.Names())
	reg, msg := sqlq.LoadRegistry(registryPath())
	cfg.Registry = reg
	cat := sqlq.LoadCatalog(cfg)
	if msg != "" {
		cat.Messages = append(cat.Messages, msg)
	}
	return emit(cat)
}

func catalogConfig(o options, profileNames []string) sqlq.CatalogConfig {
	cfg := sqlq.CatalogConfig{
		BundledDir:     o.queriesDir,
		PersonalDir:    envOr("DB_AI_TOOLKIT_QUERIES", sqlq.DefaultQueriesDir()),
		TsqlScriptsDir: o.tsqlScriptsDir,
		Profile:        o.profileName,
		ProfileNames:   profileNames,
	}
	if cfg.BundledDir == "" {
		cfg.BundledDir = bundledQueriesDir()
	}
	if cfg.TsqlScriptsDir == "" {
		cfg.TsqlScriptsDir = os.Getenv("DB_AI_TOOLKIT_TSQL_SCRIPTS")
	}
	return cfg
}

// envOr reads a development and test entry point: DB_AI_TOOLKIT_QUERIES and
// DB_AI_TOOLKIT_REGISTRY move the personal layer and the registry elsewhere.
func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func registryPath() string { return envOr("DB_AI_TOOLKIT_REGISTRY", sqlq.DefaultRegistryPath()) }

// bundledQueriesDir finds the skill's queries relative to the binary, which the
// plugin installs in its bin/ directory.
func bundledQueriesDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Join(filepath.Dir(exe), "..", "skills", "live-query", "queries")
}

// missingQueryError explains why a name did not resolve in this profile's view.
// The name has been validated by ValidQueryName before it reaches a path.
func missingQueryError(cat sqlq.Catalog, cfg sqlq.CatalogConfig, name string) error {
	for _, e := range cat.Entries {
		if e.Name == name && e.Rejected != "" {
			return fmt.Errorf("query %q (%s:%s) is rejected: %s", name, e.Source, e.Path, e.Rejected)
		}
	}
	var bound string
	root := filepath.Join(cfg.PersonalDir, "profiles")
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == name+".sql" && bound == "" {
			rel, _ := filepath.Rel(root, filepath.Dir(p))
			bound = filepath.ToSlash(rel)
		}
		return nil
	})
	if bound != "" {
		return fmt.Errorf("query %q is bound to profile directory %q and cannot run on profile %q", name, bound, cfg.Profile)
	}
	return fmt.Errorf("no query named %q (use -list-queries -profile %s)", name, cfg.Profile)
}

// prepareSaved turns a catalogue entry and the -param values into the text to
// send and the arguments to bind, refusing before any connection everything
// that would make the run differ from what the agent will report.
func prepareSaved(e sqlq.Entry, params paramList) (string, []any, *sqlq.SavedRun, error) {
	run := &sqlq.SavedRun{Name: e.Name, Source: e.Source, Path: e.Path, Params: map[string]string{}, Defaults: []string{}, Verified: e.Verified}
	passed := map[string]string{}
	for _, p := range params {
		name, value, _ := strings.Cut(p, "=")
		name = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(name), "@"))
		if _, dup := passed[name]; dup {
			return "", nil, nil, fmt.Errorf("-param %q given more than once", name)
		}
		passed[name] = value
	}
	if e.Source == sqlq.SourceTsqlScripts {
		known := map[string]sqlq.OverrideParam{}
		for _, o := range e.Overrides {
			known[o.Name] = o
		}
		var args []any
		use := map[string]bool{}
		for name, value := range passed {
			o, ok := known[name]
			if !ok {
				return "", nil, nil, fmt.Errorf("query %q has no parameter %q (declared: %s)", e.Name, name, overrideNames(e.Overrides))
			}
			v, err := sqlq.BindValue(o.Type, value)
			if err != nil {
				return "", nil, nil, fmt.Errorf("-param %s: %w", name, err)
			}
			args = append(args, sql.Named("sqlq_"+name, v))
			use[name] = true
			run.Params[name] = value
		}
		for _, o := range e.Overrides {
			if !use[o.Name] {
				run.Defaults = append(run.Defaults, o.Name)
			}
		}
		sort.Slice(args, func(i, j int) bool { return args[i].(sql.NamedArg).Name < args[j].(sql.NamedArg).Name })
		return sqlq.Rewrite(e.SQL, e.Overrides, use), args, run, nil
	}
	want := map[string]bool{}
	for _, q := range e.QueryParams {
		want[q] = true
		if _, ok := passed[q]; !ok {
			return "", nil, nil, fmt.Errorf("parameter %q required by query %q", q, e.Name)
		}
	}
	var args []any
	for _, q := range e.QueryParams {
		args = append(args, sql.Named(q, passed[q]))
		run.Params[q] = passed[q]
	}
	for name := range passed {
		if !want[name] {
			return "", nil, nil, fmt.Errorf("query %q has no parameter %q", e.Name, name)
		}
	}
	return e.SQL, args, run, nil
}

func overrideNames(ps []sqlq.OverrideParam) string {
	var names []string
	for _, p := range ps {
		names = append(names, p.Name)
	}
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

// guard applies sqlq's three refusals in their historical order and returns the
// exit code and message of the first that applies. The messages quote the
// statement: they answer the caller of a batch it just wrote, never the catalogue.
//
// Writing takes two independent yeses: the profile must permit it, and this
// invocation must intend it. A profile is a persistent property of a file
// somebody edited once; -allow-write is a statement about right now.
//
// USE writes nothing, so the write guard passes it - and it silently makes two
// of this tool's own statements false at once: the "database" field of the
// answer still names the profile's catalog, and -database is overridden from
// inside the text it was meant to govern.
//
// GO is a client batch separator, not T-SQL. The guard already knows how to see
// it - it splits statements on it - but the execution path sends the text
// through untouched, so the server answers with a syntax error that explains
// nothing. Refuse rather than split: a correct splitter has to respect string
// literals, both comment forms and bracketed identifiers, which is real work
// for a need (several read-only batches at once) that does not arise.
func guard(sqlText string, profile sqlq.Profile, allowWrite bool) (int, error) {
	for _, r := range sqlq.Refusals(sqlText) {
		switch r.Kind {
		case sqlq.RefusalWrite:
			switch {
			case profile.ReadOnly():
				return exitRefused, fmt.Errorf(
					"profile %q is read-only and this batch would write: statement %q uses %s",
					profile.Name, truncate(r.Statement, 120), r.Keyword)
			case !allowWrite:
				return exitRefused, fmt.Errorf(
					"this batch would write (statement %q uses %s) and profile %q permits it, but "+
						"-allow-write was not given; pass it only once the user has confirmed the write",
					truncate(r.Statement, 120), r.Keyword, profile.Name)
			}
		case sqlq.RefusalContext:
			return exitRefused, fmt.Errorf(
				"%s changes the database for the rest of the batch, so the reported database "+
					"would no longer be the one queried: statement %q. Select the database with "+
					"-database, or name it in the object (Other.dbo.T).",
				r.Keyword, truncate(r.Statement, 120))
		case sqlq.RefusalSeparator:
			return exitUsage, fmt.Errorf(
				"GO is a client batch separator, not T-SQL (line %d). Send one batch per call.", r.Line)
		}
	}
	return exitOK, nil
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

	retmsg := &sqlexp.ReturnMessage{}
	rows, err := conn.QueryContext(ctx, sqlText, append(args, retmsg)...)
	if err != nil {
		result.ElapsedMS = time.Since(started).Milliseconds()
		result.Error = sqlError(err, secret)
		return result, exitSQL
	}
	defer rows.Close()

	sqlErrs, err := collect(ctx, rows, retmsg, &result, o.maxRows)
	result.ElapsedMS = time.Since(started).Milliseconds()
	// Errors after the first go to messages, so none is lost; Error keeps the
	// first, which is the one that explains the others.
	for i, e := range sqlErrs {
		if i == 0 {
			continue
		}
		later := sqlError(e, secret)
		result.Messages = append(result.Messages, fmt.Sprintf("error %d: %s", later.Number, later.Message))
	}
	switch {
	case err != nil:
		result.Error = sqlError(err, secret)
		return result, exitSQL
	case len(sqlErrs) > 0:
		result.Error = sqlError(sqlErrs[0], secret)
		return result, exitSQL
	}
	return result, exitOK
}

// collect reads every result set and every informational message, in the order
// the server sends them. The first non-showplan set fills the result's own
// fields, later ones go to MoreResults, and a showplan goes to Plan. SQL errors
// are returned separately, in order, so the sets read before them are kept. A
// set still open when an error arrives is marked incomplete: the driver ends
// it early and cleanly, and its rows would otherwise pass for the whole set.
func collect(ctx context.Context, rows *sql.Rows, retmsg *sqlexp.ReturnMessage,
	result *sqlq.Result, maxRows int) (sqlErrs []error, err error) {
	first := true
	open := -1 // the set the server has not closed yet: 0 the first, k MoreResults[k-1]
	for active := true; active; {
		switch m := retmsg.Message(ctx).(type) {
		case sqlexp.MsgNotice:
			result.Messages = append(result.Messages, m.Message.String())
		case sqlexp.MsgError:
			sqlErrs = append(sqlErrs, m.Error)
			switch {
			case open == 0:
				result.Incomplete = true
			case open > 0:
				result.MoreResults[open-1].Incomplete = true
			}
		case sqlexp.MsgNext:
			cols, err := rows.ColumnTypes()
			if err != nil {
				return sqlErrs, err
			}
			if isShowplan(cols) {
				plan, err := readSingleString(rows)
				if err != nil {
					return sqlErrs, err
				}
				result.Plan = plan
				continue
			}
			set := sqlq.NewRowSet(maxRows)
			columns := make([]sqlq.Column, len(cols))
			for i, c := range cols {
				columns[i] = sqlq.Column{Name: c.Name(), Type: c.DatabaseTypeName()}
			}
			if err := scanAll(rows, cols, set); err != nil {
				return sqlErrs, err
			}
			if first {
				result.Columns, result.Rows = columns, set.Rows
				result.RowCount, result.Truncated = set.RowCount, set.Truncated
				first = false
				open = 0
			} else {
				result.MoreResults = append(result.MoreResults, sqlq.ResultSet{
					Columns: columns, Rows: set.Rows, RowCount: set.RowCount, Truncated: set.Truncated})
				open = len(result.MoreResults)
			}
		case sqlexp.MsgNextResultSet:
			open = -1
			active = rows.NextResultSet()
		}
	}
	// Defensive: with go-mssqldb v1.11 a timeout already ends the loop through
	// a failing NextResultSet and surfaces in rows.Err(), but a query cut short
	// by -timeout must never exit 0 with partial rows if that ever changes.
	if ctx.Err() != nil {
		return sqlErrs, ctx.Err()
	}
	return sqlErrs, rows.Err()
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

func readQuery(query, file, saved string) (string, error) {
	given := 0
	for _, s := range []string{query, file, saved} {
		if s != "" {
			given++
		}
	}
	switch {
	case given > 1:
		return "", fmt.Errorf("give exactly one of -query, -file or -saved")
	case given == 0:
		return "", fmt.Errorf("one of -query, -file or -saved is required")
	case saved != "":
		return "", nil // the text comes from the catalogue
	case query != "":
		return query, nil
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("reading -file: %w", err)
	}
	return string(raw), nil
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
