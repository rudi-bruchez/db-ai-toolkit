package sqlq

import "unicode"

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
			j := i
			for j < len(rs) && isWordRune(rs[j]) {
				j++
			}
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
