// Command godta proposes rowstore nonclustered indexes for the databases of a
// SQL Server instance, measures them with hypothetical indexes under
// AUTOPILOT, and writes for each database a report and a script
// (docs/superpowers/specs/2026-11-08-godta-design.md).
//
// Each subcommand is one phase. They read and write JSON files in a project
// directory (--project) and print one JSON object on stdout, on success and
// on failure alike:
//
//	godta collect     read the workload and the catalog (read only)
//	godta candidates  generate the candidates (offline)
//	godta evaluate    --target server --dry-run, then --allow-write --plan <file>
//	godta report      score and write rapport.md, indexes.sql, result.json (offline)
//	godta cleanup     --dry-run, then --allow-write --plan <file>
//
// A write phase needs a readwrite profile, --allow-write and the execution
// plan its --dry-run wrote, and executes that plan only (spec section 7).
package main

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/dotenv"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/candidate"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/cleanup"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/collect"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/conn"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/evaluate"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/report"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/runplan"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/score"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/sqlferret"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/suggest"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/workspace"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/sqlq"
)

// Exit codes (spec section 10).
const (
	exitOK      = 0
	exitUsage   = 1
	exitFailed  = 2 // a failure after the connection, a database that failed
	exitRefused = 3 // a write refused: profile, --allow-write, --plan
	exitConnect = 4 // the connection could not be opened
)

// Files of the workspace that only the binary reads and writes.
const (
	// evaluateCandidatesFile is the candidate list an evaluate --dry-run
	// planned, --suggestions included: the write phase reads it, never
	// candidates.json, so that what runs is what the plan was made from.
	evaluateCandidatesFile = "candidates-evaluate.json"
	// databasesFile, in the server directory, is what collect kept and
	// skipped.
	databasesFile = "databases.json"
)

// The command names, as the JSON output names them.
const (
	cmdCollect    = "collect"
	cmdCandidates = "candidates"
	cmdEvaluate   = "evaluate"
	cmdReport     = "report"
	cmdCleanup    = "cleanup"
	cmdClone      = "clone"
)

// output is the one JSON object a command prints.
type output struct {
	Command   string     `json:"command"`
	OK        bool       `json:"ok"`
	Databases []dbResult `json:"databases"`
	// Plan is the execution plan a --dry-run wrote, when it wrote one; with
	// several databases each plan is in the files of its database.
	Plan     string    `json:"plan,omitempty"`
	Files    []string  `json:"files,omitempty"`
	Warnings []string  `json:"warnings,omitempty"`
	Error    *errorOut `json:"error,omitempty"`
}

// dbResult is what a command did on one database.
type dbResult struct {
	Server   string   `json:"server,omitempty"`
	Name     string   `json:"name"`
	Status   string   `json:"status"`
	Files    []string `json:"files"`
	Warnings []string `json:"warnings"`
	Error    string   `json:"error,omitempty"`
}

// errorOut is the error of a command that stopped.
type errorOut struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

// codeError is an error that decides the exit code.
type codeError struct {
	code int
	err  error
}

func (e *codeError) Error() string { return e.err.Error() }

func usagef(format string, args ...any) error {
	return &codeError{exitUsage, fmt.Errorf(format, args...)}
}

func refusedf(format string, args ...any) error {
	return &codeError{exitRefused, fmt.Errorf(format, args...)}
}

func failedf(err error) error { return &codeError{exitFailed, err} }

func kindOf(code int) string {
	switch code {
	case exitUsage:
		return "usage"
	case exitRefused:
		return "refused"
	case exitConnect:
		return "connection"
	}
	return "failed"
}

// opener opens a pool on database (the profile's when empty) with the
// already resolved secret. conn.Open in the binary; a fake in the tests.
type opener func(ctx context.Context, p sqlq.Profile, secret, database string) (*sql.DB, error)

// deps is what run needs from the outside.
type deps struct {
	stdout  io.Writer
	open    opener
	resolve func(sqlq.Profile) (string, error)
}

func realDeps() deps {
	r := sqlq.NewResolver(os.Getenv, credentialsPath(), func(msg string) {
		fmt.Fprintln(os.Stderr, "godta:", msg)
	})
	return deps{
		stdout: os.Stdout,
		open: func(ctx context.Context, p sqlq.Profile, secret, database string) (*sql.DB, error) {
			return conn.Open(ctx, p, func(sqlq.Profile) (string, error) { return secret, nil }, database)
		},
		resolve: r.Resolve,
	}
}

func credentialsPath() string {
	if v := os.Getenv("MSSQL_CREDENTIALS"); v != "" {
		return v
	}
	return sqlq.DefaultCredentialPath()
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := run(ctx, os.Args[1:], realDeps())
	stop()
	os.Exit(code)
}

// listFlag is a repeatable string flag.
type listFlag []string

func (l *listFlag) String() string { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error {
	*l = append(*l, v)
	return nil
}

// common holds the flags every subcommand takes.
type common struct {
	project, profile, profiles, env string
	include, exclude                listFlag
}

func (c *common) define(fs *flag.FlagSet) {
	fs.StringVar(&c.project, "project", "", "project directory, the workspace of the run (required)")
	fs.StringVar(&c.profile, "profile", "", "connection profile; without it, the GODTA_* variables")
	fs.StringVar(&c.profiles, "profiles", "", "profiles file (default $MSSQL_PROFILES, then "+sqlq.DefaultProfilePath()+")")
	fs.StringVar(&c.env, "env", ".env", "file of KEY=VALUE variables loaded at start; a variable already set wins")
	fs.Var(&c.include, "include", "database name pattern to keep (path.Match, case-insensitive); repeatable")
	fs.Var(&c.exclude, "exclude", "database name pattern to leave out; repeatable")
}

// app is one invocation.
type app struct {
	ctx    context.Context
	d      deps
	c      common
	ws     workspace.WS
	out    output
	secret string
	pools  []*sql.DB
	// failed and refused decide the exit code of a run that went on past a
	// database's failure.
	failed, refused bool
}

// run executes one invocation and returns its exit code. It prints exactly
// one JSON object on d.stdout.
func run(ctx context.Context, args []string, d deps) int {
	a := &app{ctx: ctx, d: d, out: output{Databases: []dbResult{}}}
	defer a.closePools()
	if len(args) == 0 {
		return a.finish(usagef("a subcommand is required: %s", strings.Join(subcommands, ", ")))
	}
	a.out.Command = args[0]
	var err error
	switch args[0] {
	case cmdCollect:
		err = a.collect(args[1:])
	case cmdCandidates:
		err = a.candidates(args[1:])
	case cmdEvaluate:
		err = a.evaluate(args[1:])
	case cmdReport:
		err = a.report(args[1:])
	case cmdCleanup:
		err = a.cleanup(args[1:])
	case cmdClone:
		err = usagef("clone is not available yet")
	case "-h", "-help", "--help", "help":
		a.out.Command = "help"
		a.out.OK = true
		a.out.Warnings = []string{"usage: godta <" + strings.Join(subcommands, "|") + "> --project <dir> [flags]; godta <subcommand> --help lists its flags"}
		return a.emit(exitOK)
	default:
		err = usagef("unknown subcommand %q: want one of %s", args[0], strings.Join(subcommands, ", "))
	}
	return a.finish(err)
}

var subcommands = []string{cmdCollect, cmdCandidates, cmdEvaluate, cmdReport, cmdCleanup}

// finish turns the result of a command into the exit code and prints.
func (a *app) finish(err error) int {
	if err != nil {
		code := exitFailed
		var ce *codeError
		if errors.As(err, &ce) {
			code = ce.code
		}
		if errors.Is(err, flag.ErrHelp) {
			a.out.OK = true
			return a.emit(exitOK)
		}
		a.out.OK = false
		a.out.Error = &errorOut{Kind: kindOf(code), Message: err.Error()}
		return a.emit(code)
	}
	switch {
	case a.refused:
		a.out.OK = false
		return a.emit(exitRefused)
	case a.failed:
		a.out.OK = false
		return a.emit(exitFailed)
	}
	a.out.OK = true
	return a.emit(exitOK)
}

// emit prints the output, every error and warning text scrubbed of the
// secret first: stdout ends up in an agent transcript.
func (a *app) emit(code int) int {
	r := func(s string) string {
		if a.secret == "" || s == "" {
			return s
		}
		return conn.Redact(errors.New(s), a.secret).Error()
	}
	rs := func(list []string) []string {
		for i := range list {
			list[i] = r(list[i])
		}
		return list
	}
	if a.out.Error != nil {
		a.out.Error.Message = r(a.out.Error.Message)
	}
	a.out.Warnings = rs(a.out.Warnings)
	for i := range a.out.Databases {
		db := &a.out.Databases[i]
		db.Error = r(db.Error)
		db.Status = r(db.Status)
		db.Warnings = rs(db.Warnings)
		if db.Files == nil {
			db.Files = []string{}
		}
		if db.Warnings == nil {
			db.Warnings = []string{}
		}
	}
	enc := json.NewEncoder(a.d.stdout)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(a.out); err != nil {
		fmt.Fprintln(os.Stderr, "godta: writing output:", err)
		return exitUsage
	}
	return code
}

// parse parses the flags of a subcommand, then loads the .env file and opens
// the workspace.
func (a *app) parse(fs *flag.FlagSet, args []string) error {
	fs.SetOutput(io.Discard)
	a.c.define(fs)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			var b strings.Builder
			fs.SetOutput(&b)
			fs.PrintDefaults()
			a.out.Warnings = []string{b.String()}
			return err
		}
		return usagef("%v", err)
	}
	if fs.NArg() > 0 {
		return usagef("unexpected argument %q", fs.Arg(0))
	}
	if a.c.project == "" {
		return usagef("--project is required")
	}
	for _, list := range [][]string{a.c.include, a.c.exclude} {
		for _, p := range list {
			if _, err := path.Match(strings.ToLower(p), ""); err != nil {
				return usagef("database pattern %q: %v", p, err)
			}
		}
	}
	if err := dotenv.Load(a.c.env); err != nil {
		return usagef("--env %s: %v", a.c.env, err)
	}
	ws, err := workspace.Open(a.c.project)
	if err != nil {
		return usagef("%v", err)
	}
	a.ws = ws
	return nil
}

// selected reports whether the database name passes --include and --exclude,
// by the rule of collect: path.Match, case-insensitive.
func (a *app) selected(name string) bool {
	m := func(patterns []string) bool {
		n := strings.ToLower(name)
		for _, p := range patterns {
			if ok, _ := path.Match(strings.ToLower(p), n); ok {
				return true
			}
		}
		return false
	}
	if len(a.c.include) > 0 && !m(a.c.include) {
		return false
	}
	return !m(a.c.exclude)
}

// selectProfile picks the profile: --profile from the profiles file, or the
// GODTA_* variables.
func (a *app) selectProfile() (sqlq.Profile, error) {
	profiles := a.c.profiles
	if profiles == "" {
		profiles = os.Getenv("MSSQL_PROFILES")
	}
	p, err := conn.Select(profiles, a.c.profile, os.Getenv)
	if err != nil {
		return sqlq.Profile{}, usagef("%v", err)
	}
	return p, nil
}

// writeGuard refuses a write phase before any connection (spec section 7):
// a readonly profile, no --allow-write, no --plan.
func writeGuard(p sqlq.Profile, allowWrite bool, plans []string) error {
	switch {
	case p.ReadOnly():
		return refusedf("profile %q is read-only: a write phase needs a readwrite profile", p.Name)
	case !allowWrite:
		return refusedf("this phase writes on the server: pass --allow-write once the execution plan of --dry-run has been shown and accepted")
	case len(plans) == 0:
		return refusedf("this phase executes the execution plan of its --dry-run only: pass it with --plan")
	}
	return nil
}

// connect resolves the profile's secret once and opens a pool on database.
// The secret is kept for the redaction of the output.
func (a *app) connect(p sqlq.Profile, database string) (*sql.DB, error) {
	if a.secret == "" {
		secret, err := a.d.resolve(p)
		if err != nil {
			return nil, usagef("profile %q: %v", p.Name, err)
		}
		a.secret = secret
	}
	db, err := a.d.open(a.ctx, p, a.secret, database)
	if err != nil {
		return nil, &codeError{exitConnect, conn.Redact(err, a.secret)}
	}
	a.pools = append(a.pools, db)
	return db, nil
}

// server opens the server connection on master and reads the identity of
// the instance, refusing an unsupported version.
func (a *app) server(p sqlq.Profile) (*sql.DB, conn.Instance, error) {
	db, err := a.connect(p, cleanup.ServerDatabase)
	if err != nil {
		return nil, conn.Instance{}, err
	}
	inst, err := conn.Identify(a.ctx, db)
	if err != nil {
		return nil, conn.Instance{}, failedf(err)
	}
	if err := conn.CheckSupported(inst); err != nil {
		return nil, conn.Instance{}, failedf(err)
	}
	return db, inst, nil
}

func (a *app) closePools() {
	for _, db := range a.pools {
		db.Close()
	}
	a.pools = nil
}

// dbFailed records a database that failed: the run goes on with the others
// and exits 2.
func (a *app) dbFailed(r *dbResult, err error) {
	a.failed = true
	r.Status = "failed"
	r.Error = err.Error()
}

// loaded is one collected database of the workspace.
type loaded struct {
	dir string
	w   *model.Workload
}

// workloads reads collect.json of every database of the workspace, of server
// (any when empty), selected by --include and --exclude, sorted.
func (a *app) workloads(server string) ([]loaded, error) {
	var out []loaded
	servers, err := os.ReadDir(a.ws.Root)
	if err != nil {
		return nil, err
	}
	for _, s := range servers {
		if !s.IsDir() {
			continue
		}
		dbs, err := os.ReadDir(filepath.Join(a.ws.Root, s.Name()))
		if err != nil {
			return nil, err
		}
		for _, d := range dbs {
			dir := filepath.Join(a.ws.Root, s.Name(), d.Name())
			if !d.IsDir() {
				continue
			}
			if _, err := os.Stat(filepath.Join(dir, workspace.CollectFile)); err != nil {
				continue
			}
			w := &model.Workload{}
			if err := a.ws.ReadJSON(dir, workspace.CollectFile, w); err != nil {
				return nil, err
			}
			if server != "" && w.Server.ServerName != server {
				continue
			}
			if !a.selected(w.Database.Name) {
				continue
			}
			collect.ParsePlans(w)
			out = append(out, loaded{dir: dir, w: w})
		}
	}
	slices.SortFunc(out, func(x, y loaded) int {
		return cmp.Or(cmp.Compare(x.w.Server.ServerName, y.w.Server.ServerName),
			cmp.Compare(x.w.Database.Name, y.w.Database.Name))
	})
	return out, nil
}

func newResult(w *model.Workload) dbResult {
	return dbResult{Server: w.Server.ServerName, Name: w.Database.Name}
}

// collect reads the workload and the catalog of each selected database.
func (a *app) collect(args []string) error {
	fs := flag.NewFlagSet(cmdCollect, flag.ContinueOnError)
	window := fs.Duration("window", collect.DefaultWindow, "length of the collection window")
	until := fs.String("until", "", "end of the window, RFC 3339 (default: now)")
	top := fs.Int("top", collect.DefaultTopN, "number of queries kept per ranking")
	if err := a.parse(fs, args); err != nil {
		return err
	}
	if *window <= 0 {
		return usagef("--window must be positive, got %s", *window)
	}
	if *top <= 0 {
		return usagef("--top must be positive, got %d", *top)
	}
	end := time.Now()
	if *until != "" {
		t, err := time.Parse(time.RFC3339, *until)
		if err != nil {
			return usagef("--until: %v", err)
		}
		end = t
	}
	opt := collect.Options{
		Window:  model.Window{From: end.Add(-*window), Until: end},
		TopN:    *top,
		Include: a.c.include,
		Exclude: a.c.exclude,
	}
	p, err := a.selectProfile()
	if err != nil {
		return err
	}
	server, inst, err := a.server(p)
	if err != nil {
		return err
	}
	keep, skipped, err := collect.Databases(a.ctx, server, opt)
	if err != nil {
		return failedf(err)
	}
	serverDir := filepath.Join(a.ws.Root, workspace.SafeName(inst.ServerName))
	if err := a.ws.WriteJSON(serverDir, databasesFile, map[string]any{
		"server": inst.ServerName, "kept": keep, "skipped": skipped,
	}); err != nil {
		return failedf(err)
	}
	a.out.Files = append(a.out.Files, filepath.Join(serverDir, databasesFile))
	for _, s := range skipped {
		if s.Reason == collect.ReasonFiltered || s.Reason == collect.ReasonSystem {
			continue
		}
		a.out.Databases = append(a.out.Databases, dbResult{Server: inst.ServerName, Name: s.Name, Status: "skipped: " + s.Reason})
	}
	for _, info := range keep {
		r := dbResult{Server: inst.ServerName, Name: info.Name}
		w, err := a.collectOne(p, inst, info, opt)
		if err != nil {
			a.dbFailed(&r, err)
			a.out.Databases = append(a.out.Databases, r)
			continue
		}
		dir := a.ws.DBDir(inst.ServerName, info.Name)
		if err := a.ws.WriteJSON(dir, workspace.CollectFile, w); err != nil {
			a.dbFailed(&r, err)
			a.out.Databases = append(a.out.Databases, r)
			continue
		}
		r.Status = "collected"
		r.Files = []string{filepath.Join(dir, workspace.CollectFile)}
		r.Warnings = w.Warnings
		a.out.Databases = append(a.out.Databases, r)
	}
	return nil
}

func (a *app) collectOne(p sqlq.Profile, inst conn.Instance, info model.DBInfo, opt collect.Options) (*model.Workload, error) {
	db, err := a.connect(p, info.Name)
	if err != nil {
		return nil, err
	}
	return collect.Database(a.ctx, db, inst, info, opt)
}

// buildCandidates generates, binds and confronts the candidates of w: the
// algorithm's, sqlferret's and the AI's.
func buildCandidates(w *model.Workload, srcs []sqlferret.Source, suggestions string) ([]model.Candidate, []string, error) {
	gen := candidate.Generate(w)
	var fromSrc []model.Candidate
	if len(srcs) > 0 {
		fromSrc = candidate.FromSources(srcs, w)
	}
	var ai []model.Candidate
	var rejected []string
	if suggestions != "" {
		var err error
		ai, rejected, err = suggest.Load(suggestions, w)
		if err != nil {
			return nil, nil, err
		}
	}
	all := slices.Concat(gen, fromSrc, ai)
	all = candidate.ConfrontAll(candidate.BindQueries(all, w), w)
	return mergeCandidates(all), rejected, nil
}

// mergeCandidates keeps one candidate per ID, the first, with the queries of
// all: the same index reached by two sources is one candidate.
func mergeCandidates(lists ...[]model.Candidate) []model.Candidate {
	seen := map[string]int{}
	var out []model.Candidate
	for _, list := range lists {
		for _, c := range list {
			if c.ID == "" {
				c.ID = candidate.ID(c)
			}
			if i, ok := seen[c.ID]; ok {
				qs := append(slices.Clone(out[i].Queries), c.Queries...)
				slices.Sort(qs)
				out[i].Queries = slices.Compact(qs)
				continue
			}
			seen[c.ID] = len(out)
			out = append(out, c)
		}
	}
	return out
}

// candidates generates the candidates of each collected database (offline).
func (a *app) candidates(args []string) error {
	fs := flag.NewFlagSet(cmdCandidates, flag.ContinueOnError)
	suggestions := fs.String("suggestions", "", "AI suggestions file (format in the index-advisor skill)")
	ferret := fs.String("sqlferret", "", "sqlferret project directory whose plans propose more candidates")
	if err := a.parse(fs, args); err != nil {
		return err
	}
	var srcs []sqlferret.Source
	if *ferret != "" {
		var err error
		srcs, err = sqlferret.Load(*ferret)
		if err != nil {
			return usagef("--sqlferret: %v", err)
		}
	}
	list, err := a.workloads("")
	if err != nil {
		return failedf(err)
	}
	if len(list) == 0 {
		return usagef("no collected database in %s matches: run collect first", a.ws.Root)
	}
	for _, l := range list {
		r := newResult(l.w)
		cands, rejected, err := buildCandidates(l.w, srcs, *suggestions)
		if err != nil {
			a.dbFailed(&r, err)
			a.out.Databases = append(a.out.Databases, r)
			continue
		}
		if err := a.ws.WriteJSON(l.dir, workspace.CandidatesFile, cands); err != nil {
			a.dbFailed(&r, err)
			a.out.Databases = append(a.out.Databases, r)
			continue
		}
		r.Status = fmt.Sprintf("%d candidates", len(cands))
		r.Files = []string{filepath.Join(l.dir, workspace.CandidatesFile)}
		r.Warnings = rejected
		a.out.Databases = append(a.out.Databases, r)
	}
	return nil
}

// evaluateFile is evaluate-server.json.
type evaluateFile struct {
	RunID            string          `json:"run_id"`
	Plan             string          `json:"plan"`
	Measures         []model.Measure `json:"measures"`
	AutoCreatedStats []string        `json:"auto_created_stats"`
	Warnings         []string        `json:"warnings"`
	Error            string          `json:"error,omitempty"`
}

// evaluate measures the candidates on the server: --dry-run writes the
// execution plan, --allow-write --plan executes it.
func (a *app) evaluate(args []string) error {
	fs := flag.NewFlagSet(cmdEvaluate, flag.ContinueOnError)
	target := fs.String("target", "", "where to evaluate: server (lab is not available yet)")
	dryRun := fs.Bool("dry-run", false, "write the execution plan, plan-evaluate.json, and execute nothing")
	allowWrite := fs.Bool("allow-write", false, "permit the writes of the plan; needs a readwrite profile and --plan")
	var plans listFlag
	fs.Var(&plans, "plan", "execution plan written by --dry-run; repeatable, one per database")
	suggestions := fs.String("suggestions", "", "AI suggestions file, added to the candidates at --dry-run")
	maxCombined := fs.Int("max-combined", evaluate.DefaultMaxCombined, "largest group of candidates of a table measured together")
	lockTimeout := fs.Duration("lock-timeout", 2*time.Second, "lock timeout of the creation of the hypothetical indexes")
	compileTimeout := fs.Duration("compile-timeout", 60*time.Second, "time limit of each compilation")
	if err := a.parse(fs, args); err != nil {
		return err
	}
	switch *target {
	case "server":
	case "lab":
		return usagef("--target lab is not available yet")
	case "":
		return usagef("--target is required: server")
	default:
		return usagef("--target %q: want server", *target)
	}
	if *dryRun && (*allowWrite || len(plans) > 0) {
		return usagef("--dry-run writes the plan and executes nothing: it takes neither --allow-write nor --plan")
	}
	if !*dryRun && *suggestions != "" {
		return usagef("--suggestions enters the plan at --dry-run, never after it")
	}
	if *maxCombined < 1 {
		return usagef("--max-combined must be at least 1, got %d", *maxCombined)
	}
	if *lockTimeout <= 0 || *compileTimeout <= 0 {
		return usagef("--lock-timeout and --compile-timeout must be positive")
	}
	p, err := a.selectProfile()
	if err != nil {
		return err
	}
	if !*dryRun {
		if err := writeGuard(p, *allowWrite, plans); err != nil {
			return err
		}
		return a.evaluateWrite(p, plans, *compileTimeout)
	}
	return a.evaluateDryRun(p, *suggestions, *maxCombined, int(lockTimeout.Milliseconds()))
}

// evaluateDryRun writes, for each collected database of the server, the
// execution plan of its candidates and the list it was made from.
func (a *app) evaluateDryRun(p sqlq.Profile, suggestions string, maxCombined, lockMS int) error {
	_, inst, err := a.server(p)
	if err != nil {
		return err
	}
	list, err := a.workloads(inst.ServerName)
	if err != nil {
		return failedf(err)
	}
	if len(list) == 0 {
		return usagef("no collected database of server %s in %s matches: run collect first", inst.ServerName, a.ws.Root)
	}
	var written []string
	for _, l := range list {
		r := newResult(l.w)
		planPath, warnings, err := a.planOne(p, inst, l, suggestions, maxCombined, lockMS)
		r.Warnings = warnings
		if err != nil {
			a.dbFailed(&r, err)
			a.out.Databases = append(a.out.Databases, r)
			continue
		}
		r.Status = "planned"
		r.Files = []string{planPath, filepath.Join(l.dir, evaluateCandidatesFile)}
		written = append(written, planPath)
		a.out.Databases = append(a.out.Databases, r)
	}
	if len(written) == 1 {
		a.out.Plan = written[0]
	}
	return nil
}

func (a *app) planOne(p sqlq.Profile, inst conn.Instance, l loaded, suggestions string, maxCombined, lockMS int) (string, []string, error) {
	w := l.w
	if w.Server.ServerName != inst.ServerName || w.Server.MachineName != inst.MachineName {
		return "", nil, fmt.Errorf("collected on server %q (machine %q), connected to %q (machine %q)",
			w.Server.ServerName, w.Server.MachineName, inst.ServerName, inst.MachineName)
	}
	var cands []model.Candidate
	if err := a.ws.ReadJSON(l.dir, workspace.CandidatesFile, &cands); err != nil {
		return "", nil, fmt.Errorf("%w: run candidates first", err)
	}
	var warnings []string
	if suggestions != "" {
		ai, rejected, err := suggest.Load(suggestions, w)
		if err != nil {
			return "", nil, err
		}
		warnings = rejected
		cands = mergeCandidates(cands, candidate.ConfrontAll(candidate.BindQueries(ai, w), w))
	}
	db, err := a.connect(p, w.Database.Name)
	if err != nil {
		return "", warnings, err
	}
	fp, err := runplan.ReadFingerprints(a.ctx, db, runplan.Tables(w))
	if err != nil {
		return "", warnings, err
	}
	plan := runplan.ForEvaluate(w, cands, workspace.NewRunID(), fp, maxCombined, lockMS).Seal()
	warnings = append(warnings, plan.Skipped...)
	if err := a.ws.WriteJSON(l.dir, evaluateCandidatesFile, cands); err != nil {
		return "", warnings, err
	}
	name := workspace.PlanFile(runplan.PhaseEvaluate)
	if err := a.ws.WriteJSON(l.dir, name, plan); err != nil {
		return "", warnings, err
	}
	return filepath.Join(l.dir, name), warnings, nil
}

// readPlan reads an execution plan file of the given phase.
func readPlan(file, phase string) (runplan.Plan, error) {
	var plan runplan.Plan
	data, err := os.ReadFile(file)
	if err != nil {
		return plan, usagef("--plan: %v", err)
	}
	if err := json.Unmarshal(data, &plan); err != nil {
		return plan, refusedf("--plan %s: not an execution plan: %v", file, err)
	}
	if plan.Phase != phase {
		return plan, refusedf("--plan %s: a plan of phase %q, not %q", file, plan.Phase, phase)
	}
	return plan, nil
}

// evaluateWrite executes each plan, after runplan.Verify against the live
// fingerprints: nothing is written before the plan is checked.
func (a *app) evaluateWrite(p sqlq.Profile, plans []string, compileTimeout time.Duration) error {
	type item struct {
		file string
		plan runplan.Plan
	}
	var items []item
	for _, f := range plans {
		plan, err := readPlan(f, runplan.PhaseEvaluate)
		if err != nil {
			return err
		}
		items = append(items, item{f, plan})
	}
	_, inst, err := a.server(p)
	if err != nil {
		return err
	}
	for _, it := range items {
		r := dbResult{Server: it.plan.Server, Name: it.plan.Database}
		file, err := a.evaluateOne(p, inst, it.file, it.plan, compileTimeout, &r)
		if file != "" {
			r.Files = []string{file}
		}
		if err != nil {
			var ce *codeError
			if errors.As(err, &ce) && ce.code == exitRefused {
				a.refused = true
				r.Status = "refused"
				r.Error = err.Error()
			} else {
				a.dbFailed(&r, err)
			}
			a.out.Databases = append(a.out.Databases, r)
			continue
		}
		r.Status = "evaluated"
		a.out.Databases = append(a.out.Databases, r)
	}
	return nil
}

func (a *app) evaluateOne(p sqlq.Profile, inst conn.Instance, file string, plan runplan.Plan, compileTimeout time.Duration, r *dbResult) (string, error) {
	db, err := a.connect(p, plan.Database)
	if err != nil {
		return "", err
	}
	var current string
	if err := db.QueryRowContext(a.ctx, "SELECT DB_NAME()").Scan(&current); err != nil {
		return "", err
	}
	ids := make([]int, len(plan.Schema))
	for i, f := range plan.Schema {
		ids[i] = f.ObjectID
	}
	live, err := runplan.ReadFingerprints(a.ctx, db, ids)
	if err != nil {
		return "", err
	}
	if err := runplan.Verify(plan, inst, current, live); err != nil {
		return "", refusedf("--plan %s refused: %v", file, err)
	}
	dir := a.ws.DBDir(plan.Server, plan.Database)
	w := &model.Workload{}
	if err := a.ws.ReadJSON(dir, workspace.CollectFile, w); err != nil {
		return "", err
	}
	collect.ParsePlans(w)
	var cands []model.Candidate
	if err := a.ws.ReadJSON(dir, evaluateCandidatesFile, &cands); err != nil {
		return "", fmt.Errorf("%w: the candidates of the plan are written by evaluate --dry-run", err)
	}
	s, err := evaluate.NewSession(a.ctx, db, inst, compileTimeout, plan.LockTimeoutMS)
	if err != nil {
		return "", err
	}
	defer s.Close()
	res, runErr := evaluate.Run(a.ctx, s, a.ws, &plan, w, cands, evaluate.Hooks{})
	ev := evaluateFile{
		RunID:            plan.RunID,
		Plan:             file,
		Measures:         res.Measures,
		AutoCreatedStats: res.AutoCreatedStats,
		Warnings:         res.Warnings,
	}
	if runErr != nil {
		ev.Error = runErr.Error()
	}
	r.Warnings = res.Warnings
	if err := a.ws.WriteJSON(dir, workspace.EvaluateServerFile, ev); err != nil {
		return "", errors.Join(runErr, err)
	}
	return filepath.Join(dir, workspace.EvaluateServerFile), runErr
}

// report scores each collected database and writes its report, then the
// summary across databases (offline).
func (a *app) report(args []string) error {
	fs := flag.NewFlagSet(cmdReport, flag.ContinueOnError)
	if err := a.parse(fs, args); err != nil {
		return err
	}
	list, err := a.workloads("")
	if err != nil {
		return failedf(err)
	}
	if len(list) == 0 {
		return usagef("no collected database in %s matches: run collect first", a.ws.Root)
	}
	var rows []model.SummaryRow
	for _, l := range list {
		r := newResult(l.w)
		row, err := a.reportOne(l)
		if err != nil {
			a.dbFailed(&r, err)
			a.out.Databases = append(a.out.Databases, r)
			rows = append(rows, model.SummaryRow{Server: r.Server, Database: r.Name, Status: "failed"})
			continue
		}
		r.Status = row.Status
		r.Warnings = row.Warnings
		r.Files = []string{
			filepath.Join(l.dir, report.ReportFile),
			filepath.Join(l.dir, report.ScriptFile),
			filepath.Join(l.dir, report.ResultFile),
		}
		a.out.Databases = append(a.out.Databases, r)
		rows = append(rows, row)
	}
	rows = append(rows, a.skippedRows()...)
	if err := report.Summary(a.ws, rows); err != nil {
		return failedf(err)
	}
	a.out.Files = []string{filepath.Join(a.ws.Root, report.SummaryFile)}
	return nil
}

func (a *app) reportOne(l loaded) (model.SummaryRow, error) {
	row := model.SummaryRow{Server: l.w.Server.ServerName, Database: l.w.Database.Name}
	var ev evaluateFile
	var cands []model.Candidate
	evaluated := true
	if err := a.ws.ReadJSON(l.dir, workspace.EvaluateServerFile, &ev); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return row, err
		}
		evaluated = false
		if err := a.ws.ReadJSON(l.dir, workspace.CandidatesFile, &cands); err != nil && !errors.Is(err, os.ErrNotExist) {
			return row, err
		}
	} else if err := a.ws.ReadJSON(l.dir, evaluateCandidatesFile, &cands); err != nil {
		return row, err
	}
	recs := score.Score(l.w, cands, ev.Measures, score.DefaultThresholds())
	warnings := ev.Warnings
	if ev.Error != "" {
		warnings = append(warnings, "evaluation stopped: "+ev.Error)
	}
	if err := report.Write(a.ws, l.dir, l.w, recs, ev.AutoCreatedStats, warnings); err != nil {
		return row, err
	}
	for _, rec := range recs {
		switch {
		case !slices.ContainsFunc(rec.Queries, func(m model.Measure) bool { return m.Status == score.StatusMeasured }):
			row.NotEvaluated++
		case rec.Decision == score.Recommended:
			row.Recommended++
			row.Gain += rec.Gain
		case rec.Decision == score.Discuss:
			row.Discuss++
		default:
			row.Rejected++
		}
	}
	row.Status = "evaluated"
	if !evaluated {
		row.Status = "not evaluated"
	}
	row.Warnings = append(slices.Clone(l.w.Warnings), warnings...)
	return row, nil
}

// skippedRows are the databases collect left out for a reason worth a line
// in the summary (offline, read only, ...), not the filtered or system ones.
func (a *app) skippedRows() []model.SummaryRow {
	var rows []model.SummaryRow
	servers, err := os.ReadDir(a.ws.Root)
	if err != nil {
		return nil
	}
	for _, s := range servers {
		if !s.IsDir() {
			continue
		}
		var f struct {
			Server  string         `json:"server"`
			Skipped []collect.Skip `json:"skipped"`
		}
		if err := a.ws.ReadJSON(filepath.Join(a.ws.Root, s.Name()), databasesFile, &f); err != nil {
			continue
		}
		for _, sk := range f.Skipped {
			if sk.Reason == collect.ReasonFiltered || sk.Reason == collect.ReasonSystem || !a.selected(sk.Name) {
				continue
			}
			rows = append(rows, model.SummaryRow{Server: f.Server, Database: sk.Name, Status: "skipped: " + sk.Reason})
		}
	}
	return rows
}

// cleanup finds what goDTA left on the server (--dry-run) and removes it by
// the plan (--allow-write --plan).
func (a *app) cleanup(args []string) error {
	fs := flag.NewFlagSet(cmdCleanup, flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "write the cleanup plan, plan-cleanup.json, and remove nothing")
	allowWrite := fs.Bool("allow-write", false, "permit the drops of the plan; needs a readwrite profile and --plan")
	var plans listFlag
	fs.Var(&plans, "plan", "cleanup plan written by --dry-run")
	if err := a.parse(fs, args); err != nil {
		return err
	}
	if *dryRun && (*allowWrite || len(plans) > 0) {
		return usagef("--dry-run writes the plan and removes nothing: it takes neither --allow-write nor --plan")
	}
	if len(plans) > 1 {
		return usagef("cleanup takes one --plan: a cleanup plan covers the whole server")
	}
	p, err := a.selectProfile()
	if err != nil {
		return err
	}
	if !*dryRun {
		if err := writeGuard(p, *allowWrite, plans); err != nil {
			return err
		}
	}
	var plan runplan.Plan
	if !*dryRun {
		plan, err = readPlan(plans[0], runplan.PhaseCleanup)
		if err != nil {
			return err
		}
	}
	server, inst, err := a.server(p)
	if err != nil {
		return err
	}
	open := func(db string) (*sql.DB, error) {
		pool, err := a.d.open(a.ctx, p, a.secret, db)
		if err != nil {
			return nil, conn.Redact(err, a.secret)
		}
		return pool, nil
	}
	if *dryRun {
		return a.cleanupDryRun(server, inst, open)
	}
	removed, err := cleanup.Run(a.ctx, server, open, a.ws, plan)
	byDB := groupEntries(removed)
	for _, db := range sortedKeys(byDB) {
		a.out.Databases = append(a.out.Databases, dbResult{Server: inst.ServerName, Name: db,
			Status: fmt.Sprintf("%d removed", len(byDB[db])), Warnings: byDB[db]})
	}
	if err != nil {
		if errors.Is(err, runplan.ErrChecksum) || errors.Is(err, runplan.ErrWrongServer) {
			return refusedf("--plan %s refused: %v", plans[0], err)
		}
		return failedf(err)
	}
	return nil
}

func (a *app) cleanupDryRun(server *sql.DB, inst conn.Instance, open func(string) (*sql.DB, error)) error {
	found, err := cleanup.Find(a.ctx, server, open, a.ws)
	if err != nil {
		return failedf(err)
	}
	var entries []workspace.Entry
	for _, e := range found.Entries() {
		if a.selected(e.Database) {
			entries = append(entries, e)
		}
	}
	plan := runplan.ForCleanup(entries)
	// The journal does not record the machine name, so ForCleanup leaves it
	// empty: it comes from the instance the plan was made on, which Verify
	// compares at execution.
	plan.Server = inst.ServerName
	plan.MachineName = inst.MachineName
	plan = plan.Seal()
	dir := filepath.Join(a.ws.Root, workspace.SafeName(inst.ServerName))
	name := workspace.PlanFile(runplan.PhaseCleanup)
	if err := a.ws.WriteJSON(dir, name, plan); err != nil {
		return failedf(err)
	}
	a.out.Plan = filepath.Join(dir, name)
	byDB := groupEntries(entries)
	for _, db := range sortedKeys(byDB) {
		a.out.Databases = append(a.out.Databases, dbResult{Server: inst.ServerName, Name: db,
			Status: fmt.Sprintf("%d to remove", len(byDB[db])), Warnings: byDB[db]})
	}
	a.out.Warnings = append(found.Unknown, plan.Skipped...)
	return nil
}

func groupEntries(entries []workspace.Entry) map[string][]string {
	by := map[string][]string{}
	for _, e := range entries {
		desc := e.Kind + " " + e.Name
		if e.Table != "" {
			desc = e.Kind + " " + e.Schema + "." + e.Table + "." + e.Name
		}
		if e.Path != "" {
			desc += " (" + e.Path + ")"
		}
		by[e.Database] = append(by[e.Database], desc)
	}
	return by
}

func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
