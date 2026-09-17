package sqlq

import (
	"regexp"
	"strings"
	"unicode"
)

// writeKeywords are the statement keywords that make a batch something other
// than a read. INTO is in the list so that SELECT ... INTO is caught: INSERT
// INTO is already rejected on its own keyword.
//
// The list is deliberately blunt. EXEC and DBCC are refused even though some
// of their uses are read-only (sp_helptext, DBCC SHOW_STATISTICS), because a
// lexical filter cannot tell those apart from the destructive ones. Use
// OBJECT_DEFINITION() instead of sp_helptext.
var writeKeywords = map[string]bool{
	"INSERT": true, "UPDATE": true, "DELETE": true, "MERGE": true,
	"TRUNCATE": true, "DROP": true, "ALTER": true, "CREATE": true,
	"GRANT": true, "REVOKE": true, "DENY": true,
	"BACKUP": true, "RESTORE": true,
	"EXEC": true, "EXECUTE": true, "DBCC": true,
	"KILL": true, "SHUTDOWN": true, "RECONFIGURE": true,
	"WRITETEXT": true, "UPDATETEXT": true, "BULK": true,
	"INTO": true,
	// Pass-through constructs carry their remote statement as a string
	// literal, and Sanitize blanks literals before this scan runs - which is
	// exactly what makes SELECT 'DROP TABLE x' safe. Their contents can
	// therefore never be inspected here, so the constructs are refused
	// wholesale. Without this,
	//   SELECT * FROM OPENQUERY(L, 'DELETE FROM t')
	// passes the guard and deletes rows on the linked server.
	"OPENQUERY": true, "OPENROWSET": true, "OPENDATASOURCE": true,
}

// WriteViolation reports a statement that would write, and the keyword that
// gave it away.
type WriteViolation struct {
	Statement string
	Keyword   string
}

// Sanitize blanks out comments, string literals and quoted identifiers,
// replacing them with spaces so that token positions and line breaks survive.
// Everything that remains is executable SQL text, which is what the guard and
// the batch splitter reason about.
//
// An unterminated literal or comment swallows the rest of the input: better to
// analyse too little than to be fooled by a half-open quote.
func Sanitize(sql string) string {
	rs := []rune(sql)
	var b strings.Builder
	b.Grow(len(sql))

	blank := func(r rune) {
		if r == '\n' {
			b.WriteRune('\n')
		} else {
			b.WriteRune(' ')
		}
	}
	// skipDelimited blanks a run terminated by close, where a doubled close
	// character is an escape rather than the end (as in 'it''s' and [a]]b]).
	skipDelimited := func(i int, close rune) int {
		blank(rs[i])
		i++
		for i < len(rs) {
			if rs[i] == close {
				if i+1 < len(rs) && rs[i+1] == close {
					blank(rs[i])
					blank(rs[i+1])
					i += 2
					continue
				}
				blank(rs[i])
				return i + 1
			}
			blank(rs[i])
			i++
		}
		return i
	}

	for i := 0; i < len(rs); {
		r := rs[i]
		switch {
		case r == '-' && i+1 < len(rs) && rs[i+1] == '-':
			for i < len(rs) && rs[i] != '\n' {
				blank(rs[i])
				i++
			}
		case r == '/' && i+1 < len(rs) && rs[i+1] == '*':
			depth := 1
			blank(rs[i])
			blank(rs[i+1])
			i += 2
			for i < len(rs) && depth > 0 {
				switch {
				case rs[i] == '/' && i+1 < len(rs) && rs[i+1] == '*':
					depth++
					blank(rs[i])
					blank(rs[i+1])
					i += 2
				case rs[i] == '*' && i+1 < len(rs) && rs[i+1] == '/':
					depth--
					blank(rs[i])
					blank(rs[i+1])
					i += 2
				default:
					blank(rs[i])
					i++
				}
			}
		case r == '\'':
			i = skipDelimited(i, '\'')
		case r == '"':
			i = skipDelimited(i, '"')
		case r == '[':
			i = skipDelimited(i, ']')
		default:
			b.WriteRune(r)
			i++
		}
	}
	return b.String()
}

// goSeparator matches a batch separator: GO alone on its line, optionally
// followed by a repeat count.
var goSeparator = regexp.MustCompile(`(?i)^\s*go(\s+\d+)?\s*$`)

// Statements splits SQL into individual statements, honouring both the
// semicolon and the GO batch separator. The returned statements are sanitized
// text, not the caller's original spelling.
func Statements(sql string) []string {
	var out []string
	for _, batch := range strings.Split(sanitizeBatches(Sanitize(sql)), "\x00") {
		for _, stmt := range strings.Split(batch, ";") {
			if s := strings.TrimSpace(stmt); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// sanitizeBatches replaces GO separator lines with a NUL marker.
func sanitizeBatches(sql string) string {
	lines := strings.Split(sql, "\n")
	for i, line := range lines {
		if goSeparator.MatchString(line) {
			lines[i] = "\x00"
		}
	}
	return strings.Join(lines, "\n")
}

// FindWrites returns every statement that would write, in the order they
// appear. An empty result means the batch is safe to run against a read-only
// profile.
func FindWrites(sql string) []WriteViolation {
	var out []WriteViolation
	for _, stmt := range Statements(sql) {
		for _, tok := range tokens(stmt) {
			upper := strings.ToUpper(tok)
			if writeKeywords[upper] {
				out = append(out, WriteViolation{Statement: stmt, Keyword: upper})
				break
			}
		}
	}
	return out
}

// tokens splits text into SQL word tokens. Underscores, @, # and $ are word
// characters, so create_date and @param stay whole and are never mistaken for
// the CREATE keyword.
func tokens(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) ||
			r == '_' || r == '@' || r == '#' || r == '$')
	})
}

// containsToken reports whether token appears as a whole word in s,
// case-insensitively.
func containsToken(s, token string) bool {
	for _, t := range tokens(s) {
		if strings.EqualFold(t, token) {
			return true
		}
	}
	return false
}

// FindBatchSeparators reports the 1-based lines carrying a GO separator.
//
// GO is a client convention, not T-SQL: SSMS and sqlcmd split on it, and a
// server sent the word itself answers with a syntax error that says nothing
// about why. Agents produce it spontaneously, having learnt SQL from scripts
// written for those tools.
//
// The text is sanitized first, so a GO inside a string literal or a comment is
// not mistaken for a separator.
func FindBatchSeparators(sql string) []int {
	var lines []int
	for i, line := range strings.Split(Sanitize(sql), "\n") {
		if goSeparator.MatchString(line) {
			lines = append(lines, i+1)
		}
	}
	return lines
}

// contextKeywords change what the rest of the batch runs against, without
// writing anything. They are refused separately from writes because the reason
// differs, and so does the message the caller needs.
var contextKeywords = map[string]bool{
	"USE": true,
}

// FindContextChanges returns every statement that would move the batch to a
// different execution context.
//
// USE is the one that matters. It writes nothing, so the write guard lets it
// through, and it silently invalidates two things at once: the "database" field
// of the JSON still names the profile's catalog while the query ran somewhere
// else, and -database - whose whole purpose is to choose the catalog for this
// invocation - is overridden from inside the text it was meant to govern.
//
// Multi-part names (Other.dbo.T) are deliberately NOT refused: they are
// legitimate reads, they are explicit about where they point, and refusing them
// would break ordinary cross-database work. What they do mean is that
// "database" names the connection's catalog, never a claim about every object
// the query touched.
func FindContextChanges(sql string) []WriteViolation {
	var out []WriteViolation
	for _, stmt := range Statements(sql) {
		for _, tok := range tokens(stmt) {
			upper := strings.ToUpper(tok)
			if contextKeywords[upper] {
				out = append(out, WriteViolation{Statement: stmt, Keyword: upper})
				break
			}
		}
	}
	return out
}
