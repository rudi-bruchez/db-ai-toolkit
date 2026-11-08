package main

// lab.go has the two phases of the lab mode (spec sections 8.4 and 8.5):
// clone on the source, by its execution plan, and evaluate --target lab,
// which restores the clone on the lab when it is not there yet and measures
// the candidates on it.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/candidate"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/conn"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/evaluate"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/lab"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/runplan"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/suggest"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/tsql"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/workspace"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/sqlq"
)

// parseBytes reads a size: a number of bytes, or a number followed by KiB,
// MiB or GiB.
func parseBytes(s string) (int64, error) {
	t := strings.TrimSpace(s)
	mult := int64(1)
	for _, u := range []struct {
		suffix string
		mult   int64
	}{{"KiB", 1 << 10}, {"MiB", 1 << 20}, {"GiB", 1 << 30}, {"B", 1}} {
		if strings.HasSuffix(t, u.suffix) {
			t, mult = strings.TrimSpace(strings.TrimSuffix(t, u.suffix)), u.mult
			break
		}
	}
	n, err := strconv.ParseInt(t, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("size %q: want a positive number of bytes, or a number followed by KiB, MiB or GiB", s)
	}
	if n > (1<<62)/mult {
		return 0, fmt.Errorf("size %q is too large", s)
	}
	return n * mult, nil
}

// clone runs clone on the source: --dry-run writes plan-clone.json for each
// collected database, --allow-write --plan executes a plan and brings the
// backup file of the clone to --host-dir.
func (a *app) clone(args []string) error {
	fs := flag.NewFlagSet(cmdClone, flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "write the clone plan, plan-clone.json, and execute nothing")
	allowWrite := fs.Bool("allow-write", false, "permit the writes of the plan; needs a readwrite profile and --plan")
	var plans listFlag
	fs.Var(&plans, "plan", "clone plan written by --dry-run; repeatable, one per database")
	backupDir := fs.String("backup-dir", "", "directory of the server for the backup of the clone (default: the instance's default backup directory)")
	maxBytes := fs.String("max-bytes", "2GiB", "largest backup file accepted; a value above 2 GiB - 1, the ceiling of varbinary(max), is lowered to it")
	chunk := fs.String("chunk", "256MiB", "size of each read of the backup file; each read reads the whole file again on the server")
	hostDir := fs.String("host-dir", "", "host directory the backup file is written into, the one the lab container mounts (execution)")
	if err := a.parse(fs, args); err != nil {
		return err
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if *dryRun {
		if *allowWrite || len(plans) > 0 {
			return usagef("--dry-run writes the plan and executes nothing: it takes neither --allow-write nor --plan")
		}
		if set["host-dir"] {
			return usagef("--host-dir is a flag of the execution, not of --dry-run")
		}
	} else {
		for _, name := range []string{"backup-dir", "max-bytes", "chunk"} {
			if set[name] {
				// The three are in the plan, under its checksum: the plan
				// shown is the plan run.
				return usagef("--%s goes into the plan at --dry-run, not at the execution", name)
			}
		}
	}
	maxB, err := parseBytes(*maxBytes)
	if err != nil {
		return usagef("--max-bytes: %v", err)
	}
	chunkB, err := parseBytes(*chunk)
	if err != nil {
		return usagef("--chunk: %v", err)
	}
	p, err := a.selectProfile()
	if err != nil {
		return err
	}
	if !*dryRun {
		if err := writeGuard(p, *allowWrite, plans); err != nil {
			return err
		}
		if *hostDir == "" {
			return usagef("--host-dir is required: the host directory the lab container mounts")
		}
		return a.cloneWrite(p, plans, *hostDir)
	}
	return a.cloneDryRun(p, *backupDir, maxB, chunkB)
}

// cloneDryRun writes, for each collected database of the server, the clone
// plan of its candidates, with an estimate of the size of the file.
func (a *app) cloneDryRun(p sqlq.Profile, backupDir string, maxBytes, chunk int64) error {
	server, inst, err := a.server(p)
	if err != nil {
		return err
	}
	if backupDir == "" {
		backupDir = inst.DefaultBackupPath
	}
	if backupDir == "" {
		// Before 2019 the property is NULL; the registry holds the setting.
		backupDir, _ = conn.RegistryBackupDir(a.ctx, server)
	}
	if backupDir == "" {
		return usagef("the instance has no default backup directory: pass --backup-dir")
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
		planPath, warnings, err := a.clonePlanOne(p, server, inst, l, backupDir, maxBytes, chunk)
		r.Warnings = warnings
		if err != nil {
			a.dbFailed(&r, err)
			a.out.Databases = append(a.out.Databases, r)
			continue
		}
		r.Status = "planned"
		r.Files = []string{planPath}
		written = append(written, planPath)
		a.out.Databases = append(a.out.Databases, r)
	}
	if len(written) == 1 {
		a.out.Plan = written[0]
	}
	return nil
}

func (a *app) clonePlanOne(p sqlq.Profile, server *sql.DB, inst conn.Instance, l loaded, backupDir string, maxBytes, chunk int64) (string, []string, error) {
	w := l.w
	if w.Server.ServerName != inst.ServerName || w.Server.MachineName != inst.MachineName {
		return "", nil, fmt.Errorf("collected on server %q (machine %q), connected to %q (machine %q)",
			w.Server.ServerName, w.Server.MachineName, inst.ServerName, inst.MachineName)
	}
	var cands []model.Candidate
	if err := a.ws.ReadJSON(l.dir, workspace.CandidatesFile, &cands); err != nil {
		return "", nil, fmt.Errorf("%w: run candidates first", err)
	}
	db, err := a.connect(p, w.Database.Name)
	if err != nil {
		return "", nil, err
	}
	fp, err := runplan.ReadFingerprints(a.ctx, db, runplan.Tables(w))
	if err != nil {
		return "", nil, err
	}
	est, err := readBackupEstimate(a.ctx, server, w.Database.Name)
	if err != nil {
		return "", nil, fmt.Errorf("reading msdb.dbo.backupset: %w", err)
	}
	plan := runplan.ForClone(w, cands, workspace.NewRunID(), fp, backupDir, inst.CanCompress, maxBytes).WithChunk(chunk)
	// The identity the plan is verified against at execution is the
	// server's, read now, as for cleanup.
	plan.Server, plan.MachineName = inst.ServerName, inst.MachineName
	plan.SideEffects = append(plan.SideEffects, est.text(w.Database.Name, plan.MaxBytes))
	plan = plan.Seal()
	warnings := slices.Clone(plan.Skipped)
	name := workspace.PlanFile(runplan.PhaseClone)
	if err := a.ws.WriteJSON(l.dir, name, plan); err != nil {
		return "", warnings, err
	}
	return filepath.Join(l.dir, name), warnings, nil
}

// backupEstimate is the last full backup of a database in msdb.dbo.backupset,
// Found false when there is none.
type backupEstimate struct {
	Found                  bool
	Finished               time.Time
	Bytes, CompressedBytes int64
}

const backupEstimateQuery = `SELECT TOP (1) CAST(backup_size AS bigint), CAST(ISNULL(compressed_backup_size, backup_size) AS bigint), backup_finish_date
FROM msdb.dbo.backupset
WHERE database_name = @p1 AND type = 'D' AND backup_finish_date IS NOT NULL
ORDER BY backup_finish_date DESC;`

func readBackupEstimate(ctx context.Context, q conn.Querier, db string) (backupEstimate, error) {
	var e backupEstimate
	err := q.QueryRowContext(ctx, backupEstimateQuery, db).Scan(&e.Bytes, &e.CompressedBytes, &e.Finished)
	if errors.Is(err, sql.ErrNoRows) {
		return backupEstimate{}, nil
	}
	if err != nil {
		return backupEstimate{}, err
	}
	e.Found = true
	return e, nil
}

// text is the side effect of the plan that gives the estimate. The sizes of
// msdb.dbo.backupset are not the size of the file (3 039 232 against
// 3 067 904 bytes, measured), and the full backup has the data the clone
// does not: it is an estimate, said as one, and the transfer reads the real
// size before writing anything.
func (e backupEstimate) text(db string, maxBytes int64) string {
	const real = " The real size of the clone's file is read before the transfer and checked against max_bytes (%d)."
	if !e.Found {
		return fmt.Sprintf("Size estimate: msdb.dbo.backupset has no full backup of %s, so no estimate."+real, tsql.QuoteName(db), maxBytes)
	}
	return fmt.Sprintf("Size estimate only: the last full backup of %s in msdb.dbo.backupset (%s) was %d bytes, %d compressed. "+
		"The clone has the schema and the statistics without the data, so its file is expected to be smaller; "+
		"backupset sizes are not file sizes either."+real,
		tsql.QuoteName(db), e.Finished.UTC().Format(time.RFC3339), e.Bytes, e.CompressedBytes, maxBytes)
}

// cloneWrite executes each clone plan: lab.Clone verifies it against the
// server and the live fingerprints before it sends anything.
func (a *app) cloneWrite(p sqlq.Profile, plans []string, hostDir string) error {
	type item struct {
		file string
		plan runplan.Plan
	}
	var items []item
	for _, f := range plans {
		plan, err := readPlan(f, runplan.PhaseClone)
		if err != nil {
			return err
		}
		items = append(items, item{f, plan})
	}
	if info, err := os.Stat(hostDir); err != nil || !info.IsDir() {
		return usagef("--host-dir %s: not a directory", hostDir)
	}
	if _, _, err := a.server(p); err != nil {
		return err
	}
	for _, it := range items {
		r := dbResult{Server: it.plan.Server, Name: it.plan.Database}
		db, err := a.connect(p, it.plan.Database)
		if err != nil {
			return err
		}
		file, err := lab.Clone(a.ctx, db, a.ws, it.plan, hostDir)
		err = a.interrupted(err, cmdClone)
		if file.HostPath != "" {
			r.Files = []string{file.HostPath, filepath.Join(a.ws.DBDir(it.plan.Server, it.plan.Database), workspace.LabFile)}
		}
		switch {
		case err == nil:
			r.Status = fmt.Sprintf("cloned: %d bytes", file.Bytes)
		case errors.Is(err, lab.ErrLeftForCleanup) && file.HostPath != "":
			// The file is usable; what stayed on the source is in the
			// journal for cleanup, and noteLeft says so in the status and
			// by the exit code.
			a.failed = true
			r.Status = fmt.Sprintf("cloned: %d bytes", file.Bytes)
			r.Warnings = []string{err.Error()}
		case errors.Is(err, runplan.ErrChecksum), errors.Is(err, runplan.ErrWrongServer),
			errors.Is(err, runplan.ErrWrongDatabase), errors.Is(err, runplan.ErrSchemaDrift):
			a.refused = true
			r.Status = "refused"
			r.Error = fmt.Sprintf("--plan %s refused: %v", it.file, err)
		default:
			a.dbFailed(&r, err)
		}
		a.noteLeft(&r, it.plan.RunID)
		a.out.Databases = append(a.out.Databases, r)
	}
	return nil
}

// labOptions are the flags of evaluate --target lab.
type labOptions struct {
	profile, hostDir, containerDir, dataDir, suggestions string
	allowWrite                                           bool
	compileTimeout                                       time.Duration
	lockMS                                               int
}

// evaluateLab measures the candidates on the lab (plan, task 19): the lab
// profile checked before any connection, then, for each collected database,
// the guards against the source of collect.json, the restore of the clone
// when lab.json has no Prepared yet (CheckCloneDB otherwise), and
// evaluate.Run without a plan, with the hook that sets the multicolumn
// statistics streams on the hypothetical indexes.
func (a *app) evaluateLab(o labOptions) error {
	if o.profile == "" {
		return usagef("--target lab needs --lab-profile")
	}
	if o.hostDir == "" || o.containerDir == "" {
		return usagef("--target lab needs --host-dir and --container-dir: the directory clone wrote into, and the same as the lab container sees it")
	}
	lp, err := a.profileNamed(o.profile)
	if err != nil {
		return err
	}
	if err := lab.CheckProfile(lp); err != nil {
		return refusedf("%v", err)
	}
	if !o.allowWrite {
		return refusedf("evaluate --target lab writes on the lab (RESTORE, hypothetical indexes): pass --allow-write")
	}
	list, err := a.workloads("")
	if err != nil {
		return failedf(err)
	}
	if len(list) == 0 {
		return usagef("no collected database in %s matches: run collect first", a.ws.Root)
	}
	labServer, labi, err := a.server(lp)
	if err != nil {
		return err
	}
	for _, l := range list {
		r := newResult(l.w)
		file, runID, err := a.labOne(lp, labServer, labi, l, o, &r)
		if file != "" {
			r.Files = []string{file, filepath.Join(l.dir, labCandidatesFile)}
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
			a.noteLeft(&r, runID)
			a.out.Databases = append(a.out.Databases, r)
			continue
		}
		r.Status = "evaluated on the lab"
		a.noteLeft(&r, runID)
		a.out.Databases = append(a.out.Databases, r)
	}
	return nil
}

func (a *app) labOne(lp sqlq.Profile, labServer *sql.DB, labi conn.Instance, l loaded, o labOptions, r *dbResult) (file, runID string, err error) {
	w := l.w
	if err := lab.CheckDistinct(w.Server, labi); err != nil {
		return "", "", refusedf("%v", err)
	}
	warning, err := lab.CheckSameVersion(w.Server, labi)
	if err != nil {
		return "", "", refusedf("%v", err)
	}
	var warnings []string
	addWarning := func(list ...string) {
		for _, s := range list {
			if s != "" && !slices.Contains(warnings, s) {
				warnings = append(warnings, s)
			}
		}
	}
	addWarning(warning)
	defer func() { r.Warnings = warnings }()
	state, err := lab.LoadState(a.ws, l.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", "", fmt.Errorf("%w: run clone first", err)
		}
		return "", "", err
	}
	if state.Prepared == nil {
		if err := checkHostFile(state.File, o.hostDir); err != nil {
			return "", "", refusedf("%v", err)
		}
		db := strings.TrimSuffix(filepath.Base(state.File.HostPath), ".bak")
		open := func(name string) (*sql.DB, error) {
			pool, err := a.d.open(a.ctx, lp, a.secret, name)
			if err != nil {
				return nil, conn.Redact(err, a.secret)
			}
			return pool, nil
		}
		prep, err := lab.Restore(a.ctx, a.ws, l.dir, labServer, open, w.Server, labi, w, state.File, o.containerDir, o.dataDir, db)
		if err != nil {
			return "", "", err
		}
		state.Prepared = &prep
	} else if err := lab.CheckCloneDB(a.ctx, labServer, state.Prepared.Database); err != nil {
		return "", "", refusedf("%v", err)
	}
	addWarning(state.Prepared.Warnings...)

	var cands []model.Candidate
	if err := a.ws.ReadJSON(l.dir, workspace.CandidatesFile, &cands); err != nil {
		return "", "", fmt.Errorf("%w: run candidates first", err)
	}
	if o.suggestions != "" {
		ai, rejected, err := suggest.Load(o.suggestions, w)
		if err != nil {
			return "", "", err
		}
		addWarning(rejected...)
		cands = mergeCandidates(cands, candidate.ConfrontAll(candidate.BindQueries(ai, w), w))
	}
	if err := a.ws.WriteJSON(l.dir, labCandidatesFile, cands); err != nil {
		return "", "", err
	}
	db, err := a.connect(lp, state.Prepared.Database)
	if err != nil {
		return "", "", err
	}
	s, err := evaluate.NewSession(a.ctx, db, labi, o.compileTimeout, o.lockMS)
	if err != nil {
		return "", "", err
	}
	defer s.Close()
	res, runErr := evaluate.Run(a.ctx, s, a.ws, nil, w, cands, lab.StatsHook(*state.Prepared, w))
	runErr = a.interrupted(runErr, cmdEvaluate)
	addWarning(res.Warnings...)
	ev := evaluateFile{
		RunID:            runIDOf(state.Prepared.Database),
		Measures:         res.Measures,
		AutoCreatedStats: res.AutoCreatedStats,
		Warnings:         warnings,
	}
	if runErr != nil {
		ev.Error = runErr.Error()
	}
	if err := a.ws.WriteJSON(l.dir, workspace.EvaluateLabFile, ev); err != nil {
		return "", res.RunID, errors.Join(runErr, err)
	}
	return filepath.Join(l.dir, workspace.EvaluateLabFile), res.RunID, runErr
}

// runIDOf is the run ID of a goDTA object name, godta_<run ID>_...
func runIDOf(name string) string {
	rest, ok := strings.CutPrefix(name, "godta_")
	if !ok {
		return ""
	}
	id, _, _ := strings.Cut(rest, "_")
	return id
}

// checkHostFile refuses a clone file that is not in hostDir, the directory
// the lab container mounts, or that is not the file clone wrote (size and
// SHA-256 of lab.json).
func checkHostFile(f lab.File, hostDir string) error {
	dir, err := filepath.Abs(hostDir)
	if err != nil {
		return err
	}
	fileDir, err := filepath.Abs(filepath.Dir(f.HostPath))
	if err != nil {
		return err
	}
	if dir != fileDir {
		return fmt.Errorf("lab guard: clone wrote %s, which is not in --host-dir %s", f.HostPath, hostDir)
	}
	h, err := os.Open(f.HostPath)
	if err != nil {
		return fmt.Errorf("lab guard: the clone's file: %w", err)
	}
	defer h.Close()
	sum := sha256.New()
	n, err := io.Copy(sum, h)
	if err != nil {
		return fmt.Errorf("lab guard: reading %s: %w", f.HostPath, err)
	}
	if n != f.Bytes || hex.EncodeToString(sum.Sum(nil)) != f.SHA256 {
		return fmt.Errorf("lab guard: %s is not the file clone wrote (%d bytes, SHA-256 %s in lab.json)", f.HostPath, f.Bytes, f.SHA256)
	}
	return nil
}
