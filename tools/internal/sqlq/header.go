package sqlq

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// StripBOM removes a UTF-8 byte order mark. Windows editors add one, and it
// would hide the first line of a header from every rule below.
func StripBOM(b []byte) []byte {
	return bytes.TrimPrefix(b, []byte("\xEF\xBB\xBF"))
}

func splitLines(text string) []string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	return lines
}

// BlockHeader is what the catalogue reads from a bundled or personal query.
type BlockHeader struct {
	Summary       string
	Params        []string // from a Parameter(s): line, lower-cased, without @
	HasParamsLine bool
	Heavy         bool
}

var paramsLine = regexp.MustCompile(`(?i)^parameters?:`)
var atName = regexp.MustCompile(`@([A-Za-z_][A-Za-z0-9_]*)`)

// ParseBlockHeader reads the /* ... */ block that opens a bundled or personal
// query. The summary is the block's first paragraph, joined on one line.
func ParseBlockHeader(text string) (BlockHeader, error) {
	trimmed := strings.TrimLeft(text, " \t\r\n")
	if !strings.HasPrefix(trimmed, "/*") {
		return BlockHeader{}, errors.New("no header comment")
	}
	end := strings.Index(trimmed, "*/")
	if end < 0 {
		return BlockHeader{}, errors.New("header comment is not closed")
	}
	var h BlockHeader
	var para []string
	inSummary := true
	for _, line := range splitLines(trimmed[2:end]) {
		l := strings.TrimSpace(line)
		switch {
		case inSummary && l == "" && len(para) > 0:
			inSummary = false
		case inSummary && l != "":
			para = append(para, l)
		}
		if paramsLine.MatchString(l) {
			h.HasParamsLine = true
			for _, m := range atName.FindAllStringSubmatch(l, -1) {
				h.Params = append(h.Params, strings.ToLower(m[1]))
			}
		}
		if strings.EqualFold(l, "heavy: yes") {
			h.Heavy = true
		}
	}
	h.Summary = strings.Join(para, " ")
	if h.Summary == "" {
		return BlockHeader{}, errors.New("header has no summary")
	}
	if h.HasParamsLine && h.Params == nil {
		h.Params = []string{}
	}
	return h, nil
}

// Marker is the parsed "-- sqlq:" line of a tsql-scripts file.
type Marker struct {
	Name   string
	Params []string
	Heavy  bool
	Line   int
}

// MarkedHeader is what the catalogue reads from a tsql-scripts file.
type MarkedHeader struct {
	Summary string
	Marker  Marker
}

var markerAttempt = regexp.MustCompile(`(?i)^\s*--\s*sqlq\s*:(.*)$`)
var commentLine = regexp.MustCompile(`^\s*--`)

// ParseMarkedHeader finds the sqlq marker of a tsql-scripts file. found is
// false only when no line even looks like a marker: such a file is not in the
// catalogue. Every other problem is an error, so a misplaced or misspelt marker
// shows up as rejected instead of making the script vanish.
func ParseMarkedHeader(text string) (MarkedHeader, bool, error) {
	lines := splitLines(text)
	headerEnd := 0
	for headerEnd < len(lines) && (strings.TrimSpace(lines[headerEnd]) == "" || commentLine.MatchString(lines[headerEnd])) {
		headerEnd++
	}
	var at []int
	for i, l := range lines {
		if markerAttempt.MatchString(l) {
			at = append(at, i)
		}
	}
	switch {
	case len(at) == 0:
		return MarkedHeader{}, false, nil
	case len(at) > 1:
		return MarkedHeader{}, true, fmt.Errorf("more than one sqlq marker (lines %d and %d)", at[0]+1, at[1]+1)
	case at[0] >= headerEnd:
		return MarkedHeader{}, true, fmt.Errorf("marker outside the header at line %d", at[0]+1)
	}
	m, err := parseMarker(markerAttempt.FindStringSubmatch(lines[at[0]])[1])
	if err != nil {
		return MarkedHeader{}, true, err
	}
	m.Line = at[0] + 1
	for i := at[0] - 1; i >= 0; i-- {
		c := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[i]), "--"))
		if c == "" || strings.Trim(c, "-") == "" || strings.HasPrefix(c, "http://") || strings.HasPrefix(c, "https://") {
			continue
		}
		return MarkedHeader{Summary: c, Marker: m}, true, nil
	}
	return MarkedHeader{}, true, errors.New("no summary above the marker")
}

func parseMarker(rest string) (Marker, error) {
	var m Marker
	for _, field := range strings.Fields(rest) {
		key, value, hasValue := strings.Cut(field, "=")
		switch {
		case key == "name" && hasValue:
			m.Name = value
		case key == "params" && hasValue:
			for _, p := range strings.Split(value, ",") {
				p = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(p), "@"))
				if p == "" {
					return Marker{}, errors.New("empty name in params=")
				}
				for _, seen := range m.Params {
					if seen == p {
						return Marker{}, fmt.Errorf("parameter %q listed twice in params=", p)
					}
				}
				m.Params = append(m.Params, p)
			}
		case key == "heavy" && !hasValue:
			m.Heavy = true
		default:
			return Marker{}, fmt.Errorf("unknown marker key %q", field)
		}
	}
	if m.Name == "" {
		return Marker{}, errors.New("marker has no name=")
	}
	return m, nil
}
