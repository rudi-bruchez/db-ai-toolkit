package sqlq

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// SavedFileContent builds what -save-query writes: a header with the summary
// and a Parameters line derived from the SQL, then the executed bytes unchanged.
// A summary that could open or close a comment is refused: "/*" would turn the
// whole file into one unterminated comment, verified and returning nothing;
// "*/" would inject SQL. The summary is written trimmed, which is how the
// catalogue reads it back.
func SavedFileContent(summary, sqlText string) (string, error) {
	s := strings.TrimSpace(summary)
	switch {
	case s == "":
		return "", errors.New("-summary is empty")
	case !utf8.ValidString(summary):
		return "", errors.New("-summary is not UTF-8 text")
	case strings.IndexFunc(summary, breaksLine) >= 0:
		// A NUL would get the file rejected as binary; U+2028 and the like are
		// one line to the parser but not to an editor.
		return "", errors.New("-summary must fit on one line, without control characters")
	case strings.Contains(summary, "/*") || strings.Contains(summary, "*/"):
		return "", errors.New("-summary must not contain /* or */")
	case paramsLine.MatchString(s) || heavyLine.MatchString(s):
		// The summary line is also read for these keys: "Heavy: yes" would set
		// heavy, "Parameters: @x" would contradict the generated line.
		return "", errors.New("-summary must not start with Parameters: or Heavy:")
	}
	params := "none."
	if ps := QueryParams(sqlText); len(ps) > 0 {
		params = "@" + strings.Join(ps, ", @") + "."
	}
	return "/*  " + s + "\n\n    Parameters: " + params + "\n*/\n" + sqlText, nil
}

func breaksLine(r rune) bool {
	return unicode.IsControl(r) || unicode.In(r, unicode.Zl, unicode.Zp)
}
