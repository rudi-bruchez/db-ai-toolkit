// Command errorlog-parse reads one or more SQL Server ERRORLOG files and
// writes a compact digest to stdout, dropping routine informational noise
// so an AI skill spends tokens only on entries that matter.
//
// Usage:
//
//	errorlog-parse [flags] <path>
//
// <path> is a single ERRORLOG file, a directory (all ERRORLOG* files are
// processed in order), or "-" to read from stdin.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/errorlog"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "errorlog-parse:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	opts := errorlog.Options{MinSeverity: 16}
	var path string

	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-h" || a == "--help":
			usage()
			return nil
		case a == "-severity" || a == "--severity":
			if i+1 >= len(args) {
				return fmt.Errorf("%s requires a value", a)
			}
			i++
			n, err := atoiStrict(args[i])
			if err != nil {
				return fmt.Errorf("invalid -severity %q: %w", args[i], err)
			}
			opts.MinSeverity = n
		case strings.HasPrefix(a, "-"):
			if a == "-" {
				path = a
				continue
			}
			return fmt.Errorf("unknown flag %q (try --help)", a)
		default:
			path = a
		}
	}

	if path == "" {
		usage()
		return fmt.Errorf("missing <path> argument")
	}

	sources, err := collectSources(path)
	if err != nil {
		return err
	}

	total := errorlog.Result{DroppedByCat: map[string]int{}}
	var kept []errorlog.Entry
	for _, src := range sources {
		entries, err := parseSource(src)
		if err != nil {
			return err
		}
		res := errorlog.Filter(entries, opts)
		total.Total += res.Total
		kept = append(kept, res.Kept...)
		for cat, n := range res.DroppedByCat {
			total.DroppedByCat[cat] += n
		}
	}

	writeDigest(os.Stdout, path, opts, total, kept)
	return nil
}

// collectSources returns the list of files to read. A directory expands to
// its ERRORLOG* files sorted so the current log comes before archives.
func collectSources(path string) ([]string, error) {
	if path == "-" {
		return []string{"-"}, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []string{path}, nil
	}
	matches, err := filepath.Glob(filepath.Join(path, "ERRORLOG*"))
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("no ERRORLOG* files found in %s", path)
	}
	sort.Strings(matches)
	return matches, nil
}

func parseSource(src string) ([]errorlog.Entry, error) {
	if src == "-" {
		return errorlog.Parse(os.Stdin)
	}
	f, err := os.Open(src)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return errorlog.Parse(f)
}

func writeDigest(w *os.File, path string, opts errorlog.Options, res errorlog.Result, kept []errorlog.Entry) {
	dropped := res.Total - len(kept)
	fmt.Fprintf(w, "# SQL Server ERRORLOG digest\n")
	fmt.Fprintf(w, "# source: %s\n", path)
	fmt.Fprintf(w, "# entries: %d total, %d kept, %d dropped (severity threshold: %d)\n",
		res.Total, len(kept), dropped, opts.MinSeverity)
	if dropped > 0 {
		cats := make([]string, 0, len(res.DroppedByCat))
		for c := range res.DroppedByCat {
			cats = append(cats, c)
		}
		sort.Strings(cats)
		parts := make([]string, 0, len(cats))
		for _, c := range cats {
			parts = append(parts, fmt.Sprintf("%s=%d", c, res.DroppedByCat[c]))
		}
		fmt.Fprintf(w, "# dropped by category: %s\n", strings.Join(parts, ", "))
	}
	fmt.Fprintln(w, "#")
	for _, e := range kept {
		fmt.Fprintln(w, e.Text())
	}
}

// atoiStrict parses a non-negative integer without importing strconv's
// broader surface; keeps the CLI dependency-free and explicit.
func atoiStrict(s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not a number")
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

func usage() {
	fmt.Fprint(os.Stderr, `errorlog-parse - compact a SQL Server ERRORLOG for AI analysis

Usage:
  errorlog-parse [flags] <path>

Arguments:
  <path>            An ERRORLOG file, a directory of ERRORLOG* files, or "-" for stdin.

Flags:
  -severity <n>     Keep entries with Severity >= n even if they look like noise (default 16).
  -h, --help        Show this help.

Output:
  A digest on stdout: comment header (# ...) with counts, then the kept entries verbatim.
`)
}
