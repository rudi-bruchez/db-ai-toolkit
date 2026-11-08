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
	"regexp"
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

// SafeName turns a server or database name into a single path component
// that no other name shares, even on a file system that ignores case
// (Windows, macOS) or reserves device names (Windows).
//
// A plain name, made of [a-z0-9._-] only, is kept as it is. Every other name
// gets the first 8 hex digits of the SHA-256 of the original name appended,
// after its characters outside [A-Za-z0-9._-] became '_'. So are hashed: a
// name with an upper-case letter (Sales and SALES, two databases of a
// case-sensitive instance, would otherwise share a directory on a
// case-insensitive file system), a name ending with '.' (Windows drops it),
// a name already of the form <x>-<8 hex digits> (it could be another name's
// hashed form), and a Windows device name (CON, PRN, AUX, NUL, COM0 to 9,
// LPT0 to 9, alone or before an extension), which also gets a leading '_'
// since the device name stays reserved before any extension. An empty name,
// "." and ".." become "_-<hash>".
func SafeName(name string) string {
	if name == "" || name == "." || name == ".." {
		return "_-" + shortHash(name)
	}
	var b strings.Builder
	hash := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
			hash = true
		default:
			b.WriteByte('_')
			hash = true
		}
	}
	out := b.String()
	reserved := reservedDevice(out)
	if !hash && !reserved && !strings.HasSuffix(out, ".") && !hashedForm.MatchString(out) {
		return out
	}
	if reserved {
		out = "_" + out
	}
	return out + "-" + shortHash(name)
}

// hashedForm is the tail SafeName appends: a plain name ending like it is
// hashed too, so that it never equals another name's hashed form.
var hashedForm = regexp.MustCompile(`-[0-9a-fA-F]{8}$`)

// reservedDevice reports whether name is a Windows device name, alone or
// before an extension ("nul", "CON.db"), compared without case.
func reservedDevice(name string) bool {
	stem, _, _ := strings.Cut(name, ".")
	stem = strings.ToUpper(stem)
	switch stem {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	return len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) &&
		stem[3] >= '0' && stem[3] <= '9'
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
