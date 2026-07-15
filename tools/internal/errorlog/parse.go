// Package errorlog parses SQL Server ERRORLOG files and separates
// signal (errors, severities, security and integrity events) from the
// large volume of routine informational noise, so an AI skill can read a
// compact digest instead of the raw multi-megabyte log.
package errorlog

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// entryStart matches the timestamp prefix that begins every top-level
// ERRORLOG entry, e.g. "2024-01-15 08:23:45.67 spid51      ...".
// Lines that do not match are continuation lines of the previous entry
// (stack dumps, DBCC output, multi-line messages).
var entryStart = regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{2} `)

// severityRe captures the numeric severity in "Severity: NN" messages.
var severityRe = regexp.MustCompile(`(?i)Severity:\s*(\d{1,2})`)

// signalPatterns mark an entry as worth keeping regardless of any noise
// match. Kept deliberately broad: missing a real error costs a DBA far
// more than keeping a benign line.
var signalPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bError:\s*\d+`),
	regexp.MustCompile(`(?i)\bLogin failed\b`),
	regexp.MustCompile(`(?i)\bdeadlock\b`),
	regexp.MustCompile(`(?i)SQL Server Assertion`),
	regexp.MustCompile(`(?i)Stack (Dump|Signature)|Dump thread|SqlDumpExceptionHandler|BugCheck|\bAppDomain\b.*unload`),
	regexp.MustCompile(`(?i)I/O requests taking longer than`),
	regexp.MustCompile(`(?i)has been paged out|significant part of sql server process memory`),
	regexp.MustCompile(`(?i)consistency-based I/O error|torn page|incorrect checksum|\bcorrupt|\bsuspect\b|DBCC.*found.*error`),
	regexp.MustCompile(`(?i)out of memory|insufficient memory|failed to allocate`),
	regexp.MustCompile(`(?i)AlwaysOn.*(error|fail)|availability (group|replica).*(error|fail|offline)`),
	regexp.MustCompile(`(?i)\b(could not|cannot|unable to|failed to)\b`),
	regexp.MustCompile(`(?i)\bwas killed\b|non-yielding|latch|scheduler \d+`),
}

// noisePatterns mark routinely benign entries. An entry is dropped only
// when it matches noise AND matches no signal pattern and no high severity.
var noisePatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{"backup", regexp.MustCompile(`(?i)Database backed up|Log was backed up|BACKUP (DATABASE|LOG).*successfully|Backup(ed)? database`)},
	{"login-success", regexp.MustCompile(`(?i)Login succeeded for user`)},
	{"db-option", regexp.MustCompile(`(?i)Setting database option .* to`)},
	{"startup", regexp.MustCompile(`(?i)Starting up database|is starting up|SQL Server is now ready|The SQL Server Network Interface|Server is listening on|Server name is`)},
	{"checkdb-ok", regexp.MustCompile(`(?i)CHECKDB for database .* finished without errors|found 0 errors`)},
	{"recovery-info", regexp.MustCompile(`(?i)Recovery is complete|Recovery completed|recovery of database .* is .* complete|Analysis of database`)},
	{"deprecation", regexp.MustCompile(`(?i)will be removed in a future version|deprecated`)},
	{"clr-config", regexp.MustCompile(`(?i)Common language runtime \(CLR\) functionality initialized`)},
	{"informational", regexp.MustCompile(`(?i)informational message only`)},
}

// Entry is a single logical ERRORLOG record, including any continuation
// lines that followed its timestamped first line.
type Entry struct {
	Lines []string // raw lines, first is the timestamped line
}

// Text returns the entry joined back into its original multi-line form.
func (e Entry) Text() string { return strings.Join(e.Lines, "\n") }

// Options controls filtering behaviour.
type Options struct {
	// MinSeverity keeps any entry whose "Severity: NN" is >= this value,
	// even if it also matches a noise pattern. Typical DBA threshold: 16.
	MinSeverity int
}

// Result holds the outcome of filtering one or more ERRORLOG sources.
type Result struct {
	Total       int
	Kept        []Entry
	DroppedByCat map[string]int
}

// Parse reads an ERRORLOG stream and groups it into logical entries,
// attaching continuation lines to their preceding timestamped entry.
func Parse(r io.Reader) ([]Entry, error) {
	var entries []Entry
	sc := bufio.NewScanner(r)
	// ERRORLOG lines (stack dumps, query text) can be long; grow the buffer.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var cur *Entry
	for sc.Scan() {
		line := sc.Text()
		if entryStart.MatchString(line) {
			entries = append(entries, Entry{Lines: []string{line}})
			cur = &entries[len(entries)-1]
			continue
		}
		if cur == nil {
			// Preamble before the first timestamp (rare); keep as its own entry.
			entries = append(entries, Entry{Lines: []string{line}})
			cur = &entries[len(entries)-1]
			continue
		}
		cur.Lines = append(cur.Lines, line)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scanning errorlog: %w", err)
	}
	return entries, nil
}

// classify decides whether to keep an entry and, if dropped, under which
// noise category. Order of precedence: high severity > any signal > noise.
func classify(e Entry, opts Options) (keep bool, category string) {
	text := e.Text()

	if m := severityRe.FindStringSubmatch(text); m != nil {
		sev := 0
		for _, c := range m[1] {
			sev = sev*10 + int(c-'0')
		}
		if sev >= opts.MinSeverity {
			return true, ""
		}
	}
	for _, re := range signalPatterns {
		if re.MatchString(text) {
			return true, ""
		}
	}
	for _, n := range noisePatterns {
		if n.re.MatchString(text) {
			return false, n.name
		}
	}
	// Unknown entries are kept by default (fail safe toward signal).
	return true, ""
}

// Filter classifies every entry and returns the kept entries plus a
// per-category count of what was dropped.
func Filter(entries []Entry, opts Options) Result {
	res := Result{Total: len(entries), DroppedByCat: map[string]int{}}
	for _, e := range entries {
		keep, cat := classify(e, opts)
		if keep {
			res.Kept = append(res.Kept, e)
		} else {
			res.DroppedByCat[cat]++
		}
	}
	return res
}
