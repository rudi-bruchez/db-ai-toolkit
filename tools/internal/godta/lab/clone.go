// clone.go has the clone phase on the source (spec section 8.4, steps 1 to
// 6): statistics, DBCC CLONEDATABASE, drop of the statistics, BACKUP, the
// transfer of the file to the host through the SQL connection, and the drop
// of the clone and of the file. It also holds the lab state that the later
// phases read back from lab.json.
package lab

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/conn"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/runplan"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/tsql"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/workspace"
)

// StatSpec is one statistics object clone creates on the source; see
// runplan.StatSpec, where it is declared so that runplan.ForClone can use it.
type StatSpec = runplan.StatSpec

// StatsToCreate lists the statistics clone creates on the source (spec
// section 8.4, step 1): runplan.CloneStats.
func StatsToCreate(w *model.Workload, cands []model.Candidate) []StatSpec {
	return runplan.CloneStats(w, cands)
}

// File is the clone's backup file once on the host: its path, its size, its
// SHA-256 in lower-case hex, and the number of chunks the transfer read.
type File struct {
	HostPath string `json:"host_path"`
	Bytes    int64  `json:"bytes"`
	SHA256   string `json:"sha256"`
	Chunks   int    `json:"chunks"`
}

// Prepared is the lab once the clone is restored and prepared (task 18): the
// restored database, the streams of the multicolumn statistics by StreamKey,
// and the warnings of the preparation.
type Prepared struct {
	Database string            `json:"database"`
	Streams  map[string][]byte `json:"streams"`
	Warnings []string          `json:"warnings,omitempty"`
}

// State is what lab.json of a database's directory holds: the file clone
// brought to the host and, once the lab is prepared, what Restore did. It is
// how evaluate --target lab, run later, finds them.
type State struct {
	File     File      `json:"file"`
	Prepared *Prepared `json:"prepared,omitempty"`
}

// SaveState writes s to dir/lab.json.
func SaveState(ws workspace.WS, dir string, s State) error {
	return ws.WriteJSON(dir, workspace.LabFile, s)
}

// LoadState reads dir/lab.json.
func LoadState(ws workspace.WS, dir string) (State, error) {
	var s State
	err := ws.ReadJSON(dir, workspace.LabFile, &s)
	return s, err
}

// Errors Clone wraps.
var (
	ErrNotClonePlan = errors.New("not a clone plan")
	// ErrTooBig: the backup file is larger than the plan's max_bytes.
	ErrTooBig = errors.New("backup file too big")
	// ErrHashMismatch: the file on the host is not the server's.
	ErrHashMismatch = errors.New("SHA-256 mismatch between the host file and the server's")
	// ErrLeftForCleanup: an object could not be removed and stays in the
	// journal for cleanup. Clone returns it with a usable File when only a
	// statistics drop on the source failed.
	ErrLeftForCleanup = errors.New("objects left for cleanup")
)

// dropAttempts and dropPause are the attempts of a DROP STATISTICS, as for
// evaluate's DROP INDEX (spec section 8.5): three, 2 s apart.
const dropAttempts = 3

var dropPause = 2 * time.Second

// hostFileMode is the mode of the file written on the host. The lab container
// reads it through its mount as another user (mssql), so it must be readable
// by others; the directory's own permissions are what restrict it.
const hostFileMode = 0o644

// Clone executes the sealed clone plan p (runplan.ForClone) on q, a
// connection to the source database, and writes the backup file into
// hostDir. It refuses a plan of another phase, or that runplan.Verify refuses
// (checksum, server, database, schema fingerprint). Every object is journaled
// "planned" before its creation, "created" after, "removed" after its drop.
//
// The steps run in the plan's order on one pinned connection (the lock
// timeout is session state). A DROP STATISTICS is tried three times 2 s
// apart; one that still fails stays "created" in the journal and Clone goes
// on, returning the File with an error wrapping ErrLeftForCleanup. Any other
// failure stops the phase and removes what it created so far (statistics,
// clone database, backup file, partial host file) before returning.
//
// The transfer reads the file in chunks of p.ChunkBytes: the first read also
// returns DATALENGTH, and a file larger than p.MaxBytes is refused
// (ErrTooBig) before anything is written on the host. The SHA-256 computed on
// the way is compared with the server's HASHBYTES of the file
// (ErrHashMismatch). On success Clone saves State{File} with SaveState in
// the database's directory of ws before returning.
func Clone(ctx context.Context, q conn.Querier, ws workspace.WS, p runplan.Plan, hostDir string) (File, error) {
	if p.Phase != runplan.PhaseClone {
		return File{}, fmt.Errorf("clone: %w: phase %q", ErrNotClonePlan, p.Phase)
	}
	type conner interface {
		Conn(ctx context.Context) (*sql.Conn, error)
	}
	if db, ok := q.(conner); ok {
		c, err := db.Conn(ctx)
		if err != nil {
			return File{}, fmt.Errorf("clone: %w", err)
		}
		defer c.Close()
		q = c
	}
	inst, err := conn.Identify(ctx, q)
	if err != nil {
		return File{}, fmt.Errorf("clone: %w", err)
	}
	var db string
	if err := q.QueryRowContext(ctx, "SELECT DB_NAME()").Scan(&db); err != nil {
		return File{}, fmt.Errorf("clone: %w", err)
	}
	ids := make([]int, len(p.Schema))
	for i, f := range p.Schema {
		ids[i] = f.ObjectID
	}
	live, err := runplan.ReadFingerprints(ctx, q, ids)
	if err != nil {
		return File{}, fmt.Errorf("clone: %w", err)
	}
	if err := runplan.Verify(p, inst, db, live); err != nil {
		return File{}, fmt.Errorf("clone: %w", err)
	}
	if info, err := os.Stat(hostDir); err != nil || !info.IsDir() {
		return File{}, fmt.Errorf("clone: host directory %q: not a directory (%v)", hostDir, err)
	}

	r := &cloneRun{q: q, ws: ws, p: p, hostDir: hostDir, chunk: p.ChunkBytes, max: p.MaxBytes}
	if r.chunk <= 0 {
		r.chunk = runplan.DefaultChunkBytes
	}
	if r.max <= 0 || r.max > runplan.MaxFileBytes {
		r.max = runplan.MaxFileBytes
	}
	for _, s := range p.Steps {
		if err := r.step(ctx, s); err != nil {
			return File{}, errors.Join(fmt.Errorf("clone: step %d: %w", s.N, err), r.rollback(ctx))
		}
	}
	if r.file.HostPath == "" {
		return File{}, errors.Join(errors.New("clone: the plan has no transfer step"), r.rollback(ctx))
	}
	if err := SaveState(ws, ws.DBDir(p.Server, p.Database), State{File: r.file}); err != nil {
		return File{}, fmt.Errorf("clone: %w", err)
	}
	if len(r.leftStats) > 0 {
		return r.file, fmt.Errorf("clone: %w: statistics %s not dropped on the source after %d attempts",
			ErrLeftForCleanup, strings.Join(r.leftStats, ", "), dropAttempts)
	}
	return r.file, nil
}

// cloneRun is one execution of a clone plan.
type cloneRun struct {
	q       conn.Querier
	ws      workspace.WS
	p       runplan.Plan
	hostDir string
	chunk   int64
	max     int64
	// pending are the objects journaled "planned" or "created" and not yet
	// removed, in creation order, with the step that drops each.
	pending []*object
	// backupPath is the file the BACKUP step wrote; the transfer step is
	// the one whose SQL is tsql.ReadFile of it.
	backupPath string
	file       File
	leftStats  []string
}

type object struct {
	e    workspace.Entry
	drop string // the plan's drop statement, set when its step is reached
}

func (r *cloneRun) record(e workspace.Entry, state string) error {
	e.State = state
	e.At = time.Now().UTC()
	return r.ws.Record(e)
}

func sameObject(a, b workspace.Entry) bool {
	return a.Kind == b.Kind && a.Database == b.Database && a.Schema == b.Schema &&
		a.Table == b.Table && a.Name == b.Name && a.Path == b.Path
}

func (r *cloneRun) step(ctx context.Context, s runplan.Step) error {
	if s.Journal == nil {
		if r.backupPath != "" && s.SQL == tsql.ReadFile(r.backupPath) {
			return r.transfer(ctx)
		}
		_, err := r.q.ExecContext(ctx, s.SQL)
		return err
	}
	e := *s.Journal
	switch e.State {
	case workspace.StatePlanned:
		if err := r.record(e, workspace.StatePlanned); err != nil {
			return err
		}
		o := &object{e: e}
		r.pending = append(r.pending, o)
		if _, err := r.q.ExecContext(ctx, s.SQL); err != nil {
			return err
		}
		if e.Kind == workspace.KindBackupFile {
			r.backupPath = e.Path
		}
		return r.record(e, workspace.StateCreated)
	case workspace.StateRemoved:
		o := r.find(e)
		if o == nil {
			return fmt.Errorf("drop of %s %s, which the plan did not create", e.Kind, e.Name)
		}
		o.drop = s.SQL
		if e.Kind != workspace.KindStatistics {
			if _, err := r.q.ExecContext(ctx, s.SQL); err != nil {
				return err
			}
			return r.removed(o)
		}
		var err error
		for attempt := 1; attempt <= dropAttempts; attempt++ {
			if _, err = r.q.ExecContext(ctx, s.SQL); err == nil {
				return r.removed(o)
			}
			if attempt < dropAttempts {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(dropPause):
				}
			}
		}
		// Left "created" in the journal for cleanup; the clone already has
		// the statistics, so the phase goes on.
		r.leftStats = append(r.leftStats, fmt.Sprintf("%s.%s (%v)", tsql.Obj{Schema: e.Schema, Name: e.Table}.Quoted(), tsql.QuoteName(e.Name), err))
		r.forget(o)
		return nil
	}
	return fmt.Errorf("journal state %q in a plan step", e.State)
}

func (r *cloneRun) find(e workspace.Entry) *object {
	for _, o := range r.pending {
		if sameObject(o.e, e) {
			return o
		}
	}
	return nil
}

func (r *cloneRun) forget(o *object) {
	for i, p := range r.pending {
		if p == o {
			r.pending = append(r.pending[:i], r.pending[i+1:]...)
			return
		}
	}
}

func (r *cloneRun) removed(o *object) error {
	r.forget(o)
	return r.record(o.e, workspace.StateRemoved)
}

// transfer copies the backup file to the host in chunks, then compares its
// SHA-256 with the server's. The host file is created only once the size is
// known to be within the limit, and removed on any failure.
func (r *cloneRun) transfer(ctx context.Context) (err error) {
	src := "OPENROWSET(BULK " + tsql.QuoteString(r.backupPath) + ", SINGLE_BLOB) AS x"
	var size int64
	var data []byte
	if err := r.q.QueryRowContext(ctx, "SELECT DATALENGTH(BulkColumn), SUBSTRING(BulkColumn, 1, @p1) FROM "+src,
		r.chunk).Scan(&size, &data); err != nil {
		return fmt.Errorf("reading %s: %w", r.backupPath, err)
	}
	if size > r.max {
		return fmt.Errorf("%w: %s is %d bytes, the limit is %d", ErrTooBig, r.backupPath, size, r.max)
	}
	name := r.backupPath[strings.LastIndexAny(r.backupPath, `/\`)+1:]
	host := filepath.Join(r.hostDir, name)
	f, err := os.OpenFile(host, os.O_WRONLY|os.O_CREATE|os.O_EXCL, hostFileMode)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(host)
		}
	}()
	// The umask may have narrowed the mode; the lab container must read it.
	if err := f.Chmod(hostFileMode); err != nil {
		return err
	}
	h := sha256.New()
	var written int64
	chunks := 0
	for {
		if len(data) == 0 && written < size {
			return fmt.Errorf("reading %s: empty chunk at offset %d of %d", r.backupPath, written, size)
		}
		if _, err := f.Write(data); err != nil {
			return err
		}
		h.Write(data)
		written += int64(len(data))
		chunks++
		if written >= size {
			break
		}
		data = nil
		if err := r.q.QueryRowContext(ctx, "SELECT SUBSTRING(BulkColumn, @p1, @p2) FROM "+src,
			written+1, r.chunk).Scan(&data); err != nil {
			return fmt.Errorf("reading %s at offset %d: %w", r.backupPath, written, err)
		}
	}
	if written != size {
		return fmt.Errorf("reading %s: %d bytes read, DATALENGTH %d", r.backupPath, written, size)
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	var server []byte
	if err := r.q.QueryRowContext(ctx, "SELECT HASHBYTES('SHA2_256', BulkColumn) FROM "+src).Scan(&server); err != nil {
		return fmt.Errorf("hashing %s on the server: %w", r.backupPath, err)
	}
	sum := h.Sum(nil)
	if hex.EncodeToString(server) != hex.EncodeToString(sum) {
		return fmt.Errorf("%w: host %x, server %x", ErrHashMismatch, sum, server)
	}
	r.file = File{HostPath: host, Bytes: size, SHA256: hex.EncodeToString(sum), Chunks: chunks}
	return nil
}

// rollback removes, newest first, every object the run journaled and did not
// remove: an object whose creation failed is looked up first, since a drop
// of a missing object (or xp_delete_file of a missing file) raises an error.
// Statistics are dropped under the short lock timeout, three attempts. It
// runs even when ctx is done. What it cannot remove stays in the journal.
func (r *cloneRun) rollback(ctx context.Context) error {
	if len(r.pending) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
	defer cancel()
	var errs []error
	if _, err := r.q.ExecContext(ctx, tsql.SetLockTimeout(runplan.DropLockTimeoutMS)); err != nil {
		errs = append(errs, err)
	}
	for i := len(r.pending) - 1; i >= 0; i-- {
		o := r.pending[i]
		present, err := r.exists(ctx, o.e)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s %s: %w", o.e.Kind, o.e.Name, err))
			continue
		}
		if present {
			stmt := o.drop
			if stmt == "" {
				stmt = dropOf(o.e)
			}
			attempts := 1
			if o.e.Kind == workspace.KindStatistics {
				attempts = dropAttempts
			}
			for a := 1; a <= attempts; a++ {
				if _, err = r.q.ExecContext(ctx, stmt); err == nil {
					break
				}
				if a < attempts {
					time.Sleep(dropPause)
				}
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("%w: %s %s: %v", ErrLeftForCleanup, o.e.Kind, o.e.Name, err))
				continue
			}
		}
		if err := r.record(o.e, workspace.StateRemoved); err != nil {
			errs = append(errs, err)
		}
	}
	r.pending = nil
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("clone: removing what the run created: %w", err)
	}
	return nil
}

// dropOf is the drop statement of an object whose drop step was not reached.
func dropOf(e workspace.Entry) string {
	switch e.Kind {
	case workspace.KindStatistics:
		return tsql.DropStatistics(tsql.Obj{Schema: e.Schema, Name: e.Table}, e.Name)
	case workspace.KindCloneDatabase:
		return tsql.DropDatabase(e.Name)
	case workspace.KindBackupFile:
		return tsql.DeleteBackupFile(e.Path)
	}
	panic("lab: no drop for kind " + e.Kind)
}

// exists reports whether a journaled object of the run is there.
func (r *cloneRun) exists(ctx context.Context, e workspace.Entry) (bool, error) {
	var n int
	switch e.Kind {
	case workspace.KindStatistics:
		err := r.q.QueryRowContext(ctx, "SELECT COUNT(*) FROM sys.stats WHERE object_id = OBJECT_ID(@p1) AND name = @p2",
			tsql.Obj{Schema: e.Schema, Name: e.Table}.Quoted(), e.Name).Scan(&n)
		return n > 0, err
	case workspace.KindCloneDatabase:
		err := r.q.QueryRowContext(ctx, "SELECT COUNT(*) FROM sys.databases WHERE name = @p1", e.Name).Scan(&n)
		return n > 0, err
	case workspace.KindBackupFile:
		var file, dir, parent int
		err := r.q.QueryRowContext(ctx, "EXEC master.dbo.xp_fileexist "+tsql.QuoteString(e.Path)).Scan(&file, &dir, &parent)
		return file == 1, err
	}
	return false, fmt.Errorf("unknown kind %q", e.Kind)
}
