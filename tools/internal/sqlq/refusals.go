package sqlq

import (
	"fmt"
	"strings"
)

// RefusalKind says which guard refused a batch. The kinds need different exit
// codes and messages on the command line.
type RefusalKind int

const (
	RefusalWrite RefusalKind = iota
	RefusalContext
	RefusalSeparator
)

// Refusal is one reason sqlq will not run a batch.
type Refusal struct {
	Kind      RefusalKind
	Keyword   string
	Line      int
	Statement string // sanitized statement text; for interactive messages only, never the catalogue
}

// Reason names the keyword and the line, never the text: the catalogue prints
// it on every session, and a statement can carry identifiers.
func (r Refusal) Reason() string {
	switch r.Kind {
	case RefusalWrite:
		return fmt.Sprintf("write keyword %s at line %d", r.Keyword, r.Line)
	case RefusalContext:
		return fmt.Sprintf("%s at line %d", r.Keyword, r.Line)
	default:
		return fmt.Sprintf("batch separator GO at line %d", r.Line)
	}
}

// Refusals lists why sqlq will not run this text: at most one refusal per kind,
// writes first, then context changes, then batch separators. It is the single
// place main.go, the bundled-query test and the catalogue ask.
func Refusals(sql string) []Refusal {
	var out []Refusal
	toks := Lex(Sanitize(sql))
	firstOf := func(set map[string]bool) (Token, bool) {
		for _, t := range toks {
			if set[strings.ToUpper(t.Text)] {
				return t, true
			}
		}
		return Token{}, false
	}
	if v := FindWrites(sql); len(v) > 0 {
		t, _ := firstOf(writeKeywords)
		out = append(out, Refusal{Kind: RefusalWrite, Keyword: v[0].Keyword, Line: t.Line, Statement: v[0].Statement})
	}
	if c := FindContextChanges(sql); len(c) > 0 {
		t, _ := firstOf(contextKeywords)
		out = append(out, Refusal{Kind: RefusalContext, Keyword: c[0].Keyword, Line: t.Line, Statement: c[0].Statement})
	}
	if lines := FindBatchSeparators(sql); len(lines) > 0 {
		out = append(out, Refusal{Kind: RefusalSeparator, Keyword: "GO", Line: lines[0]})
	}
	return out
}
