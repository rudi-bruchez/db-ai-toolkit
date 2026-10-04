package sqlq

import (
	"strings"
	"unicode"
)

// Token is one lexical unit of sanitized SQL. Start and End are rune offsets,
// which Sanitize preserves, so they index the original text as well.
type Token struct {
	Text  string
	Line  int // 1-based
	Start int
	End   int
	Depth int // parenthesis depth the token sits at; "(" and ")" sit at the outer level
}

// twoCharOps are the operators that must not be split, because "+=" assigns
// and "=" may only compare.
var twoCharOps = map[string]bool{
	"+=": true, "-=": true, "*=": true, "/=": true, "%=": true, "&=": true,
	"|=": true, "^=": true, "<=": true, ">=": true, "<>": true, "!=": true,
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '@' || r == '#' || r == '$'
}

// wordEnd returns where the word starting at rs[i] ends, cutting it where SQL
// Server does: a numeric literal ends with its last digit, so 1DELETE is 1 then
// DELETE and 0x1FPRINT is 0x1F then PRINT. Measured on SQL Server 2025: digits,
// an optional fraction, an optional exponent with or without digits (1ePRINT is
// 1e then PRINT), 0x with hex digits, $ with digits for money. Any other word
// runs to the first non-word rune.
func wordEnd(rs []rune, i int) int {
	digits := func(j int, ok func(rune) bool) int {
		for j < len(rs) && ok(rs[j]) {
			j++
		}
		return j
	}
	isDigit := func(r rune) bool { return r >= '0' && r <= '9' }
	isHex := func(r rune) bool { return isDigit(r) || (r|0x20 >= 'a' && r|0x20 <= 'f') }
	at := func(j int, set string) bool { return j < len(rs) && strings.ContainsRune(set, rs[j]) }
	switch {
	case rs[i] == '0' && at(i+1, "xX"):
		return digits(i+2, isHex)
	case isDigit(rs[i]) || (rs[i] == '$' && i+1 < len(rs) && isDigit(rs[i+1])):
		j := digits(i+1, isDigit)
		if at(j, ".") {
			j = digits(j+1, isDigit)
		}
		if isDigit(rs[i]) && at(j, "eE") {
			j++
			if at(j, "+-") {
				j++
			}
			j = digits(j, isDigit)
		}
		return j
	}
	return digits(i, isWordRune)
}

// Lex splits sanitized SQL into words and punctuation. Unlike tokens(), it keeps
// operators, commas, parentheses and semicolons, which is what the parameter
// and override analyses reason about.
func Lex(sanitized string) []Token {
	rs := []rune(sanitized)
	var out []Token
	line, depth := 1, 0
	for i := 0; i < len(rs); {
		r := rs[i]
		switch {
		case r == '\n':
			line++
			i++
		case unicode.IsSpace(r):
			i++
		case isWordRune(r):
			j := wordEnd(rs, i)
			out = append(out, Token{Text: string(rs[i:j]), Line: line, Start: i, End: j, Depth: depth})
			i = j
		default:
			if i+1 < len(rs) && twoCharOps[string(rs[i:i+2])] {
				out = append(out, Token{Text: string(rs[i : i+2]), Line: line, Start: i, End: i + 2, Depth: depth})
				i += 2
				continue
			}
			if r == ')' && depth > 0 {
				depth--
			}
			out = append(out, Token{Text: string(r), Line: line, Start: i, End: i + 1, Depth: depth})
			if r == '(' {
				depth++
			}
			i++
		}
	}
	return out
}
