// Package errorlog parses SQL Server ERRORLOG files and separates
// signal (errors, severities, security and integrity events) from the
// large volume of routine informational noise, so an AI skill can read a
// compact digest instead of the raw multi-megabyte log.
package errorlog

import (
	"regexp"
	"strings"
	"time"
)

const tsLayout = "2006-01-02 15:04:05.00"

// lineRe matches a top-level entry: timestamp, source column, message.
var lineRe = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{2})\s+(\S+)\s+(.*)$`)

// Entry is one logical ERRORLOG record: a timestamped first line plus any
// continuation lines that followed it (stack dumps, startup parameters, ...).
type Entry struct {
	Time    time.Time
	RawTime string
	Source  string
	Lines   []string
}

// Text returns the entry's raw lines joined with newlines.
func (e Entry) Text() string { return strings.Join(e.Lines, "\n") }

// Message returns the entry text without the "timestamp + source" prefix on
// the first line; continuation lines are appended unchanged.
func (e Entry) Message() string {
	if len(e.Lines) == 0 {
		return ""
	}
	m := lineRe.FindStringSubmatch(e.Lines[0])
	if m == nil {
		return e.Text()
	}
	parts := append([]string{m[3]}, e.Lines[1:]...)
	return strings.Join(parts, "\n")
}

// Parse splits UTF-8/LF text into entries, attaching non-timestamped lines to
// the preceding entry.
func Parse(text string) []Entry {
	var entries []Entry
	var cur *Entry
	flush := func() {
		if cur == nil {
			return
		}
		for len(cur.Lines) > 0 && strings.TrimSpace(cur.Lines[len(cur.Lines)-1]) == "" {
			cur.Lines = cur.Lines[:len(cur.Lines)-1]
		}
		if len(cur.Lines) > 0 {
			entries = append(entries, *cur)
		}
		cur = nil
	}
	for _, line := range strings.Split(text, "\n") {
		if m := lineRe.FindStringSubmatch(line); m != nil {
			flush()
			t, _ := time.Parse(tsLayout, m[1])
			cur = &Entry{Time: t, RawTime: m[1], Source: m[2], Lines: []string{line}}
			continue
		}
		if cur == nil {
			cur = &Entry{Lines: []string{line}}
			continue
		}
		cur.Lines = append(cur.Lines, line)
	}
	flush()
	return entries
}
