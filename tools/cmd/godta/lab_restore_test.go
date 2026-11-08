package main

import (
	"crypto/sha256"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/lab"
)

// TestRestoreAgainRefused: once the restored clone is gone from the lab, a
// host file that is missing, or that is not the one clone wrote (size or
// SHA-256 of lab.json), is refused with exit code 3, and the message says
// what is wrong and that a new clone is needed. The file as clone wrote it
// passes.
func TestRestoreAgainRefused(t *testing.T) {
	const db = "godta_ab12cd34_c"
	content := []byte("the backup file clone wrote")
	sum := sha256.Sum256(content)
	setup := func(t *testing.T) (string, lab.File) {
		dir := t.TempDir()
		path := filepath.Join(dir, db+".bak")
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
		return dir, lab.File{HostPath: path, Bytes: int64(len(content)), SHA256: hex.EncodeToString(sum[:])}
	}

	t.Run("intact", func(t *testing.T) {
		dir, f := setup(t)
		if err := restoreAgainGuard(f, dir, db); err != nil {
			t.Fatalf("the file clone wrote is refused: %v", err)
		}
	})
	for _, c := range []struct {
		name   string
		change func(t *testing.T, f lab.File)
		want   string
	}{
		{"missing", func(t *testing.T, f lab.File) {
			if err := os.Remove(f.HostPath); err != nil {
				t.Fatal(err)
			}
		}, "is missing"},
		{"altered", func(t *testing.T, f lab.File) {
			// Same size, other bytes: only the SHA-256 tells.
			altered := []byte(strings.ToUpper(string(content)))
			if err := os.WriteFile(f.HostPath, altered, 0o600); err != nil {
				t.Fatal(err)
			}
		}, "is not the file clone wrote"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, f := setup(t)
			c.change(t, f)
			err := restoreAgainGuard(f, dir, db)
			var ce *codeError
			if !errors.As(err, &ce) || ce.code != exitRefused {
				t.Fatalf("restoreAgainGuard: %v, want exit code %d", err, exitRefused)
			}
			for _, w := range []string{db + " is no longer on the lab", c.want, "run clone again"} {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("message %q does not say %q", err.Error(), w)
				}
			}
		})
	}
}

// preparedLab is newEvalFix with lab.json saying the clone is already
// restored on the lab as godta_ab12cd34_c, so that labOne takes the path
// where its own guards are the only ones, and a fake whose IsClone read
// answers isClone. It returns the arguments of evaluate --target lab.
func preparedLab(t *testing.T, isClone int64) (*evalFix, []string) {
	t.Helper()
	f := newEvalFix(t)
	host := t.TempDir()
	st := lab.State{File: lab.File{HostPath: filepath.Join(host, "godta_ab12cd34_c.bak")},
		Prepared: &lab.Prepared{Database: "godta_ab12cd34_c"}}
	if err := lab.SaveState(f.ws, f.dir, st); err != nil {
		t.Fatal(err)
	}
	f.fake.answer = func(q string) (driver.Rows, bool) {
		if strings.Contains(q, "DATABASEPROPERTYEX(@p1, 'IsClone') AS int);") {
			return &fakeRows{cols: 1, rows: [][]driver.Value{{isClone}}}, true
		}
		return nil, false
	}
	args := append([]string{"evaluate", "--target", "lab", "--lab-profile", "rw", "--allow-write",
		"--host-dir", host, "--container-dir", "/var/opt/mssql/godta"}, f.args...)
	return f, args
}

// TestLabRefusesSourceAsLab: a lab that is the source instance is refused by
// labOne with exit 3 before anything is sent, also when the clone is
// already restored, where lab.Restore and its own check do not run.
func TestLabRefusesSourceAsLab(t *testing.T) {
	f, args := preparedLab(t, 1)
	code, out := invoke(t, f.fake.open, args...)
	if code != exitRefused || out.OK || len(out.Databases) != 1 {
		t.Fatalf("code %d, output %+v; want code 3, ok false, one database", code, out)
	}
	if r := out.Databases[0]; r.Status != "refused" || !strings.Contains(r.Error, "lab guard: the lab has the source's") {
		t.Errorf("database %+v: want refused by the distinct-instance guard", r)
	}
	if sent := f.sent(""); len(sent) != 0 {
		t.Errorf("statements executed: %q, want none", sent)
	}
}

// TestLabRefusesPreparedNotClone: when lab.json says the clone is restored
// and the lab holds a database of that name that is not a clone
// (IsClone = 0), evaluate --target lab refuses it with exit 3 and sends
// nothing, rather than evaluating in that database.
func TestLabRefusesPreparedNotClone(t *testing.T) {
	f, args := preparedLab(t, 0)
	f.fake.inst.ServerName, f.fake.inst.MachineName = "lab02", "LAB2M"
	code, out := invoke(t, f.fake.open, args...)
	if code != exitRefused || out.OK || len(out.Databases) != 1 {
		t.Fatalf("code %d, output %+v; want code 3, ok false, one database", code, out)
	}
	if r := out.Databases[0]; r.Status != "refused" || !strings.Contains(r.Error, "is not a clone (IsClone = 0)") {
		t.Errorf("database %+v: want refused as not a clone", r)
	}
	if sent := f.sent(""); len(sent) != 0 {
		t.Errorf("statements executed: %q, want none", sent)
	}
}
