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

// splitAfterLines cuts text after each line break: CRLF, LF, or a lone CR
// (classic Mac editors), so that a file written with lone CRs is read line by
// line instead of as one long comment. Joined back, the pieces are the text,
// byte for byte.
func splitAfterLines(text string) []string {
	var out []string
	start := 0
	for i := 0; i < len(text); i++ {
		switch {
		case text[i] == '\n', text[i] == '\r' && (i+1 == len(text) || text[i+1] != '\n'):
			out = append(out, text[start:i+1])
			start = i + 1
		}
	}
	return append(out, text[start:])
}

func splitLines(text string) []string {
	lines := splitAfterLines(text)
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(strings.TrimSuffix(l, "\n"), "\r")
	}
	return lines
}

// hasLoneCR reports a CR not followed by LF. The guard and the lexer end a
// line on LF only: in such a file, "-- x<CR>DELETE" is one comment to them.
func hasLoneCR(text string) bool {
	return strings.Contains(strings.ReplaceAll(text, "\r\n", ""), "\r")
}

// BlockHeader is what the catalogue reads from a bundled or personal query.
type BlockHeader struct {
	Summary       string
	Params        []string // from a Parameter(s): line, lower-cased, without @
	HasParamsLine bool
	Heavy         bool
}

var paramsLine = regexp.MustCompile(`(?i)^parameters?:`)
var heavyLine = regexp.MustCompile(`(?i)^heavy\s*:(.*)$`)
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
		if m := heavyLine.FindStringSubmatch(l); m != nil {
			switch v := strings.TrimSpace(m[1]); {
			case strings.EqualFold(v, "yes"):
				h.Heavy = true
			case strings.EqualFold(v, "no"):
			default:
				// A typo must not pass for the absence of the line.
				return BlockHeader{}, errors.New("Heavy line must say yes or no")
			}
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

// markerAttempt is deliberately wider than markerLine: "--", any Unicode
// space, the word sqlq, colon or not. A line that is an attempt but not a
// well-formed marker rejects the entry instead of hiding the file.
var markerAttempt = regexp.MustCompile(`(?i)^[\s\p{Zs}]*--[\s\p{Zs}]*sqlq\b`)
var markerLine = regexp.MustCompile(`(?i)^[ \t]*--[ \t]*sqlq[ \t]*:(.*)$`)
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
	ml := markerLine.FindStringSubmatch(lines[at[0]])
	if ml == nil {
		return MarkedHeader{}, true, fmt.Errorf("malformed sqlq marker at line %d", at[0]+1)
	}
	m, err := parseMarker(ml[1])
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

// parseMarker never quotes a token that failed validation: the reason is
// published, and the author may have written a path or a server there.
func parseMarker(rest string) (Marker, error) {
	var m Marker
	for pos, field := range strings.Fields(rest) {
		key, value, hasValue := strings.Cut(field, "=")
		switch {
		case key == "name" && hasValue:
			m.Name = value
		case key == "params" && hasValue:
			for i, p := range strings.Split(value, ",") {
				p = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(p), "@"))
				if p == "" {
					return Marker{}, errors.New("empty name in params=")
				}
				if !isPlainName(p) {
					return Marker{}, fmt.Errorf("invalid parameter name at position %d in params=", i+1)
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
			return Marker{}, fmt.Errorf("unknown marker key at position %d", pos+1)
		}
	}
	if m.Name == "" {
		return Marker{}, errors.New("marker has no name=")
	}
	return m, nil
}
