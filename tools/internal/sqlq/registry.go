package sqlq

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Verified records the last successful run of a content.
type Verified struct {
	Date    string `json:"date"`
	Profile string `json:"profile"`
}

// Registry maps content hashes to their last successful run on this machine.
type Registry struct {
	entries map[string]Verified
}

// DefaultRegistryPath is where sqlq keeps the registry.
func DefaultRegistryPath() string {
	return filepath.Join(filepath.Dir(DefaultProfilePath()), "verified.json")
}

// ContentHash is the registry key of a content.
func ContentHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// LoadRegistry reads the registry. An absent file is an empty registry; an
// unreadable one is also empty, with a message, and is rewritten on the next
// success. Neither is an error: losing verifications errs on the side of caution.
func LoadRegistry(path string) (Registry, string) {
	r := Registry{entries: map[string]Verified{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return r, ""
	}
	if err != nil {
		// No err text: os errors carry the absolute path, and this message is
		// published by -list-queries.
		return r, "verification registry unreadable, treated as empty"
	}
	if err := json.Unmarshal(b, &r.entries); err != nil {
		r.entries = map[string]Verified{}
		return r, "verification registry is not valid JSON, treated as empty"
	}
	if r.entries == nil { // the file held "null"
		r.entries = map[string]Verified{}
	}
	return r, ""
}

// Lookup returns the verification of a content, or nil.
func (r Registry) Lookup(hash string) *Verified {
	if v, ok := r.entries[hash]; ok {
		return &v
	}
	return nil
}

// RecordVerified re-reads the registry, merges one entry and replaces the file
// atomically. Two runs finishing at the same instant can still lose one entry;
// the consequence is a query shown as unverified, which is the cautious side.
func RecordVerified(path, hash string, v Verified) error {
	r, _ := LoadRegistry(path)
	r.entries[hash] = v
	b, err := json.MarshalIndent(r.entries, "", "  ")
	if err != nil {
		return registryErr("encoding", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return registryErr("creating its directory", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "verified-*.json")
	if err != nil {
		return registryErr("creating a temporary file", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return registryErr("writing", err)
	}
	if err := tmp.Close(); err != nil {
		return registryErr("writing", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return registryErr("replacing", err)
	}
	return nil
}

// registryErr drops the path that os errors carry: the caller publishes the
// message in the run's messages, and spec section 12 forbids paths there.
func registryErr(step string, err error) error {
	var pe *os.PathError
	var le *os.LinkError
	switch {
	case errors.As(err, &pe):
		err = pe.Err
	case errors.As(err, &le):
		err = le.Err
	}
	return fmt.Errorf("verification registry, %s: %w", step, err)
}
