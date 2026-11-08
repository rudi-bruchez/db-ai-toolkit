package main

import (
	"crypto/sha256"
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
