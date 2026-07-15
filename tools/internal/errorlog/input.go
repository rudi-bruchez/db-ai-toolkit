package errorlog

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Source is one acquired ERRORLOG stream, still raw (undecoded) bytes.
type Source struct {
	Name string
	Data []byte
}

// Acquire resolves a path to one or more raw ERRORLOG byte streams. It handles
// stdin ("-"), .zip archives, .gz files, plain files, and directories of
// ERRORLOG* files.
func Acquire(path string) ([]Source, error) {
	if path == "-" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, fmt.Errorf("reading stdin: %w", err)
		}
		return []Source{{Name: "<stdin>", Data: data}}, nil
	}

	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	if info.IsDir() {
		return acquireDir(path)
	}

	switch strings.ToLower(filepath.Ext(path)) {
	case ".zip":
		return acquireZip(path)
	case ".gz":
		data, err := acquireGzip(path)
		if err != nil {
			return nil, err
		}
		return []Source{{Name: path, Data: data}}, nil
	default:
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		return []Source{{Name: path, Data: data}}, nil
	}
}

func acquireDir(dir string) ([]Source, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "ERRORLOG*"))
	if err != nil {
		return nil, fmt.Errorf("globbing %s: %w", dir, err)
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("no ERRORLOG* files in %s", dir)
	}
	sort.Strings(matches)
	srcs := make([]Source, 0, len(matches))
	for _, m := range matches {
		data, err := os.ReadFile(m)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", m, err)
		}
		srcs = append(srcs, Source{Name: m, Data: data})
	}
	return srcs, nil
}

func acquireZip(path string) ([]Source, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("opening zip %s: %w", path, err)
	}
	defer zr.Close()

	readEntries := func(prefixOnly bool) ([]Source, error) {
		var srcs []Source
		for _, f := range zr.File {
			if f.FileInfo().IsDir() {
				continue
			}
			if prefixOnly && !strings.Contains(strings.ToUpper(filepath.Base(f.Name)), "ERRORLOG") {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("opening zip entry %s: %w", f.Name, err)
			}
			data, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				return nil, fmt.Errorf("reading zip entry %s: %w", f.Name, err)
			}
			srcs = append(srcs, Source{Name: f.Name, Data: data})
		}
		return srcs, nil
	}

	srcs, err := readEntries(true)
	if err != nil {
		return nil, err
	}
	if len(srcs) == 0 {
		srcs, err = readEntries(false) // fall back to all entries
		if err != nil {
			return nil, err
		}
	}
	if len(srcs) == 0 {
		return nil, fmt.Errorf("zip %s has no readable entries", path)
	}
	sort.Slice(srcs, func(i, j int) bool { return srcs[i].Name < srcs[j].Name })
	return srcs, nil
}

func acquireGzip(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("gzip %s: %w", path, err)
	}
	defer gr.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, gr); err != nil {
		return nil, fmt.Errorf("decompressing %s: %w", path, err)
	}
	return buf.Bytes(), nil
}
