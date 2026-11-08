package workspace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Kinds of journaled objects.
const (
	KindHypotheticalIndex = "hypothetical-index"
	KindStatistics        = "statistics"
	KindCloneDatabase     = "clone-database"
	KindBackupFile        = "backup-file"
	KindTempProcedure     = "temp-procedure"
)

// States of a journaled object.
const (
	StatePlanned = "planned"
	StateCreated = "created"
	StateRemoved = "removed"
)

// Entry is one line of the journal: the identity of an object goDTA is about
// to create, has created or has removed. It is written before the creation.
type Entry struct {
	RunID    string    `json:"run_id"`
	Server   string    `json:"server"`
	Database string    `json:"database"`
	Kind     string    `json:"kind"`
	Schema   string    `json:"schema"`
	Table    string    `json:"table"`
	Name     string    `json:"name"`
	Path     string    `json:"path"`
	At       time.Time `json:"at"`
	State    string    `json:"state"`
}

// key identifies an object across its entries. RunID is left out: the name
// (or the path of a backup file) already carries it, and a later cleanup run
// may record the removal under its own run ID.
type key struct {
	Server, Database, Kind, Schema, Table, Name, Path string
}

func (e Entry) key() key {
	return key{e.Server, e.Database, e.Kind, e.Schema, e.Table, e.Name, e.Path}
}

// Warnf receives the journal warnings (a truncated last line). It writes to
// standard error, never to standard output, which carries the JSON result.
var Warnf = func(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "godta: warning: "+format+"\n", args...)
}

func (ws WS) journalPath() string { return filepath.Join(ws.Root, JournalFile) }

// Record appends e as one JSON line to <root>/journal.jsonl and syncs the
// file before returning. A partial last line left by a crash is cut off
// first, so that the new line does not join it: that write never returned,
// so the object it described was never created.
func (ws WS) Record(e Entry) error {
	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("journal: encode: %w", err)
	}
	line = append(line, '\n')
	f, err := os.OpenFile(ws.journalPath(), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("journal: %w", err)
	}
	defer f.Close()
	end, err := cutPartialLine(f)
	if err != nil {
		return fmt.Errorf("journal: %w", err)
	}
	if _, err := f.WriteAt(line, end); err != nil {
		return fmt.Errorf("journal: write: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("journal: sync: %w", err)
	}
	return f.Close()
}

// cutPartialLine truncates f after its last newline when it does not end
// with one, and returns the resulting size.
func cutPartialLine(f *os.File) (int64, error) {
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	size := info.Size()
	if size == 0 {
		return 0, nil
	}
	data, err := io.ReadAll(io.NewSectionReader(f, 0, size))
	if err != nil {
		return 0, err
	}
	if data[len(data)-1] == '\n' {
		return size, nil
	}
	keep := int64(bytes.LastIndexByte(data, '\n') + 1)
	Warnf("journal %s: dropping a truncated last line (%d bytes)", f.Name(), size-keep)
	if err := f.Truncate(keep); err != nil {
		return 0, err
	}
	return keep, nil
}

// Pending returns, for every object of the journal whose last state is not
// "removed", its last entry, in the order objects first appear. A truncated
// last line (a crash during Record) is skipped with a warning; any other line
// that does not decode is an error, since skipping it could hide a created
// object.
func (ws WS) Pending() ([]Entry, error) {
	data, err := os.ReadFile(ws.journalPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("journal: %w", err)
	}
	last := map[key]Entry{}
	var order []key
	for n := 1; len(data) > 0; n++ {
		line := data
		complete := false
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			line, data, complete = data[:i], data[i+1:], true
		} else {
			data = nil
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			if !complete {
				Warnf("journal %s: ignoring truncated last line %d", ws.journalPath(), n)
				continue
			}
			return nil, fmt.Errorf("journal %s: line %d: %w", ws.journalPath(), n, err)
		}
		k := e.key()
		if _, ok := last[k]; !ok {
			order = append(order, k)
		}
		last[k] = e
	}
	var out []Entry
	for _, k := range order {
		if e := last[k]; e.State != StateRemoved {
			out = append(out, e)
		}
	}
	return out, nil
}
