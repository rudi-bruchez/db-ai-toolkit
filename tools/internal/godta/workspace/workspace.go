// Package workspace holds the goDTA project directory: one subdirectory per
// server and database, the JSON files each phase reads and writes, and the
// journal of the objects goDTA creates (spec §7.2, §8.1).
package workspace

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// File names fixed by the plan. Plan files are named by PlanFile.
const (
	CollectFile        = "collect.json"
	CandidatesFile     = "candidates.json"
	EvaluateServerFile = "evaluate-server.json"
	EvaluateLabFile    = "evaluate-lab.json"
	JournalFile        = "journal.jsonl"
	LabFile            = "lab.json"
)

// PlanFile returns the name of the execution plan of a phase: plan-<phase>.json.
func PlanFile(phase string) string { return "plan-" + phase + ".json" }

const runIDAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// NewRunID draws an 8-character run identifier in [a-z0-9] from crypto/rand.
func NewRunID() string {
	const n = 8
	// 252 is the largest multiple of 36 below 256: bytes at or above it are
	// rejected so that every character is equally likely.
	const limit = 256 - 256%len(runIDAlphabet)
	out := make([]byte, 0, n)
	buf := make([]byte, 16)
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			panic("workspace: crypto/rand: " + err.Error())
		}
		for _, b := range buf {
			if int(b) >= limit {
				continue
			}
			out = append(out, runIDAlphabet[int(b)%len(runIDAlphabet)])
			if len(out) == n {
				break
			}
		}
	}
	return string(out)
}

// WS is an opened workspace rooted at Root.
type WS struct {
	Root string
}

// Open creates the workspace root (mode 0700) if needed and returns it.
func Open(root string) (WS, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return WS{}, fmt.Errorf("workspace %s: %w", root, err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return WS{}, fmt.Errorf("workspace %s: %w", abs, err)
	}
	return WS{Root: abs}, nil
}

// SafeName turns a server or database name into a single path component.
// Every character outside [A-Za-z0-9._-] becomes '_'; when anything was
// replaced, the first 8 hex digits of the SHA-256 of the original name are
// appended so that two different names never share a directory. An empty
// name, "." and ".." are treated as replaced and become "_-<hash>".
func SafeName(name string) string {
	if name == "" || name == "." || name == ".." {
		return "_-" + shortHash(name)
	}
	var b strings.Builder
	replaced := false
	for _, r := range name {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
			replaced = true
		}
	}
	if !replaced {
		return name
	}
	return b.String() + "-" + shortHash(name)
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:4])
}

// DBDir returns <root>/<server>/<db>, both names passed through SafeName. It
// does not create the directory; WriteJSON does.
func (ws WS) DBDir(server, db string) string {
	return filepath.Join(ws.Root, SafeName(server), SafeName(db))
}

func checkName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("workspace: invalid file name %q", name)
	}
	return nil
}

// WriteJSON writes v as indented JSON to dir/name (mode 0600), through a
// temporary file in the same directory and os.Rename, so that a reader never
// sees a half-written file.
func (ws WS) WriteJSON(dir, name string, v any) error {
	if err := checkName(name); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("workspace: encode %s: %w", name, err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	tmp, err := os.CreateTemp(dir, name+".tmp*")
	if err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("workspace: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("workspace: write %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("workspace: sync %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("workspace: close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, filepath.Join(dir, name)); err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	return nil
}

// ReadJSON decodes dir/name into v.
func (ws WS) ReadJSON(dir, name string, v any) error {
	if err := checkName(name); err != nil {
		return err
	}
	path := filepath.Join(dir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("workspace: decode %s: %w", path, err)
	}
	return nil
}
