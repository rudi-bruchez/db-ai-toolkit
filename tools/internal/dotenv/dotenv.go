// Package dotenv loads a .env file (KEY=VALUE lines) into the process
// environment without overwriting variables already set. No dependency.
//
// Copied from atelier/outils/sylloge/internal/dotenv, messages in English.
package dotenv

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// Parse reads KEY=VALUE lines. Blank lines and comments (#) are ignored. An
// "export " prefix is tolerated. One pair of single or double quotes around
// the value is removed. A non-blank line without '=' is an error.
func Parse(r io.Reader) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(r)
	line := 0
	for sc.Scan() {
		line++
		s := strings.TrimSpace(sc.Text())
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		s = strings.TrimPrefix(s, "export ")
		k, v, ok := strings.Cut(s, "=")
		if !ok {
			return nil, fmt.Errorf("line %d: no '=': %q", line, s)
		}
		k = strings.TrimSpace(k)
		if k == "" {
			return nil, fmt.Errorf("line %d: empty key", line)
		}
		out[k] = unquote(strings.TrimSpace(v))
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading: %w", err)
	}
	return out, nil
}

func unquote(v string) string {
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
}

// Load reads the .env file at path and sets in the environment the keys that
// are not already there (real environment variables win). A missing file is
// not an error: Load does nothing.
func Load(path string) error { return LoadFiles(path) }

// LoadFiles reads the files in order; for the same key the last file wins,
// and a variable already present in the environment wins over all of them. A
// missing file is skipped.
func LoadFiles(paths ...string) error {
	vars := map[string]string{}
	for _, path := range paths {
		f, err := os.Open(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("opening %s: %w", path, err)
		}
		parsed, parseErr := Parse(f)
		closeErr := f.Close()
		if parseErr != nil {
			return fmt.Errorf("parsing %s: %w", path, parseErr)
		}
		if closeErr != nil {
			return fmt.Errorf("closing %s: %w", path, closeErr)
		}
		for k, v := range parsed {
			vars[k] = v
		}
	}
	for k, v := range vars {
		if _, exists := os.LookupEnv(k); !exists {
			if err := os.Setenv(k, v); err != nil {
				return fmt.Errorf("setting %s: %w", k, err)
			}
		}
	}
	return nil
}
