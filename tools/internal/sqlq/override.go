package sqlq

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"

	"github.com/golang-sql/civil"
)

// ParamType is a declared type an override knows how to validate and bind.
type ParamType struct {
	Base   string // lower case
	Length int    // characters for string types, -1 for max, 0 otherwise
}

func (t ParamType) String() string {
	switch {
	case t.Length == -1:
		return t.Base + "(max)"
	case t.Length > 0 && t.Base != "sysname":
		return fmt.Sprintf("%s(%d)", t.Base, t.Length)
	default:
		return t.Base
	}
}

var stringTypes = map[string]bool{"nvarchar": true, "nchar": true, "varchar": true, "char": true}
var intRanges = map[string][2]int64{
	"tinyint": {0, 255}, "smallint": {-32768, 32767},
	"int": {-2147483648, 2147483647}, "bigint": {-9223372036854775808, 9223372036854775807},
}
var otherTypes = map[string]bool{"bit": true, "date": true, "datetime": true, "datetime2": true, "smalldatetime": true, "sysname": true}

// OverrideParam is one marker parameter, located in the batch.
type OverrideParam struct {
	Name    string // lower case, without @
	Type    ParamType
	Default string // initializer as written in the file, trimmed
	start   int    // rune offset of the initializer, just after "="
	end     int    // rune offset of the closing ";"
}

// comparisonContext are the tokens after which "@p =" compares rather than assigns.
var comparisonContext = map[string]bool{
	"WHERE": true, "AND": true, "OR": true, "NOT": true, "ON": true, "WHEN": true,
	"HAVING": true, "IF": true, "WHILE": true, "THEN": true, "ELSE": true,
}

var compoundOps = map[string]bool{"+=": true, "-=": true, "*=": true, "/=": true, "%=": true, "&=": true, "|=": true, "^=": true}

// AnalyseOverrides locates each marker parameter's declaration and checks that
// replacing its initializer is safe. Any doubt is an error: a refused script is
// visible in the catalogue, a wrong override is not.
func AnalyseOverrides(sql string, names []string) ([]OverrideParam, error) {
	rs := []rune(sql)
	toks := Lex(Sanitize(sql))
	for _, t := range toks {
		if strings.HasPrefix(strings.ToLower(t.Text), "@sqlq_") {
			return nil, fmt.Errorf("identifier %s at line %d uses the reserved @sqlq_ prefix", t.Text, t.Line)
		}
		// SQL Server folds @ｐ onto @p and @strasse onto @straße under a
		// width-insensitive collation: a name match cannot be decided here.
		if strings.Contains(t.Text, "@") && !isASCII(t.Text) {
			return nil, fmt.Errorf("identifier %s at line %d is not ASCII", t.Text, t.Line)
		}
	}
	var out []OverrideParam
	for _, name := range names {
		if !isPlainName(name) {
			return nil, fmt.Errorf("parameter %q: name must be ASCII letters, digits or _", name)
		}
		p, err := analyseOne(rs, toks, name)
		if err != nil {
			return nil, fmt.Errorf("parameter %q: %w", name, err)
		}
		out = append(out, p)
	}
	return out, nil
}

func analyseOne(rs []rune, toks []Token, name string) (OverrideParam, error) {
	target := "@" + name
	decl := -1
	for i := 0; i+1 < len(toks); i++ {
		if strings.EqualFold(toks[i].Text, "DECLARE") && strings.EqualFold(toks[i+1].Text, target) {
			if decl >= 0 {
				return OverrideParam{}, errors.New("declared more than once")
			}
			decl = i
		}
	}
	if decl < 0 {
		return OverrideParam{}, fmt.Errorf("no \"DECLARE @%s <type> = <value>;\" line", name)
	}
	if err := checkUnconditional(toks[:decl]); err != nil {
		return OverrideParam{}, err
	}
	if err := checkNotGlued(toks, target); err != nil {
		return OverrideParam{}, err
	}
	line := toks[decl].Line
	var lineToks []Token
	for _, t := range toks {
		if t.Line == line {
			lineToks = append(lineToks, t)
		}
	}
	if lineToks[0] != toks[decl] {
		return OverrideParam{}, fmt.Errorf("line %d: the declaration must stand alone on its line", line)
	}
	last := lineToks[len(lineToks)-1]
	if last.Text != ";" || last.Depth != toks[decl].Depth {
		return OverrideParam{}, fmt.Errorf("line %d: the declaration must end with ';' on its own line", line)
	}
	typ, eq, err := parseDeclaredType(rs, lineToks)
	if err != nil {
		return OverrideParam{}, fmt.Errorf("line %d: %w", line, err)
	}
	expr := lineToks[eq+1 : len(lineToks)-1]
	for _, t := range expr {
		if t.Text == ";" || (t.Text == "," && t.Depth == toks[decl].Depth) {
			return OverrideParam{}, fmt.Errorf("line %d: one variable per declaration", line)
		}
		// "DECLARE @n int = 20 SELECT ...;" ends with a semicolon too: the
		// statement that follows would be swallowed by the rewrite.
		if t.Depth == toks[decl].Depth && statementStarters[strings.ToUpper(t.Text)] {
			return OverrideParam{}, fmt.Errorf("line %d: another statement follows the declaration", line)
		}
	}
	balance := 0
	for _, t := range expr {
		switch t.Text {
		case "(":
			balance++
		case ")":
			balance--
		}
		if balance < 0 {
			break
		}
	}
	if balance != 0 {
		// Lex never lets depth go negative, so "(1 + 2))" must be counted here.
		return OverrideParam{}, fmt.Errorf("line %d: unbalanced parentheses in the initializer", line)
	}
	start, end := lineToks[eq].End, last.Start
	def := strings.TrimSpace(string(rs[start:end]))
	if def == "" {
		return OverrideParam{}, fmt.Errorf("line %d: empty initializer", line)
	}
	if err := checkOneExpression(rs, expr, start, end); err != nil {
		return OverrideParam{}, fmt.Errorf("line %d: %w", line, err)
	}
	if err := checkNotAssigned(toks, decl+1, target); err != nil {
		return OverrideParam{}, err
	}
	return OverrideParam{Name: name, Type: typ, Default: def, start: start, end: end}, nil
}

// parseDeclaredType reads "DECLARE @p <type> =" and returns the type and the
// index of "=" in the line's tokens.
func parseDeclaredType(rs []rune, lt []Token) (ParamType, int, error) {
	i := 2
	if len(lt) > i && strings.EqualFold(lt[i].Text, "AS") {
		i++ // DECLARE @p AS int = 1; is the same declaration
	}
	if len(lt) > i && lt[i].Text == "=" && strings.TrimSpace(string(rs[lt[i-1].End:lt[i].Start])) != "" {
		// Sanitize blanks [int] and "int" like any quoted identifier.
		return ParamType{}, 0, errors.New("type written in brackets or quotes, write it bare")
	}
	if len(lt) < i+3 {
		return ParamType{}, 0, errors.New("incomplete declaration")
	}
	base := strings.ToLower(lt[i].Text)
	i++
	t := ParamType{Base: base}
	if lt[i].Text == "(" {
		if i+2 >= len(lt) || lt[i+2].Text != ")" {
			return ParamType{}, 0, fmt.Errorf("type %s: unsupported length", base)
		}
		arg := strings.ToLower(lt[i+1].Text)
		if arg == "max" {
			t.Length = -1
		} else if n, err := strconv.Atoi(arg); err == nil && n >= 0 {
			t.Length = n // datetime2(0) is a precision of 0, not a missing length
		} else {
			return ParamType{}, 0, fmt.Errorf("type %s: unsupported length %q", base, arg)
		}
		i += 3
	}
	switch {
	case base == "sysname":
		t.Length = 128
	case stringTypes[base]:
		if t.Length == 0 {
			t.Length = 1 // nvarchar without length is nvarchar(1) in a DECLARE
		}
	case base == "datetime2":
		t.Length = 0 // the precision is the server's business
	case intRanges[base] != [2]int64{} || otherTypes[base]:
		if t.Length != 0 {
			return ParamType{}, 0, fmt.Errorf("type %s takes no length", base)
		}
	default:
		return ParamType{}, 0, fmt.Errorf("type %s not supported", base)
	}
	if i >= len(lt) || lt[i].Text != "=" {
		return ParamType{}, 0, errors.New("no initializer")
	}
	return t, i, nil
}

// checkNotAssigned refuses any occurrence of the variable, after its
// declaration, that sits where a value is assigned rather than compared.
func checkNotAssigned(toks []Token, from int, target string) error {
	for i := from; i < len(toks); i++ {
		if !strings.EqualFold(toks[i].Text, target) || i+1 >= len(toks) {
			continue
		}
		next := toks[i+1].Text
		// OUT and OUTPUT make the variable a target. The guard already refuses
		// the EXEC and INTO that carry them; this keeps the rule true on its own.
		if compoundOps[next] || strings.EqualFold(next, "OUT") || strings.EqualFold(next, "OUTPUT") {
			return fmt.Errorf("assigned at line %d", toks[i].Line)
		}
		if next != "=" {
			continue
		}
		// Depth proves nothing: (SELECT @p = 2) assigns. Only the token before
		// tells a comparison, and "(" covers IIF(@p = 1, ...) and IF (@p = 1).
		if i > 0 && (toks[i-1].Text == "(" || comparisonContext[strings.ToUpper(toks[i-1].Text)]) {
			continue
		}
		return fmt.Errorf("assigned at line %d", toks[i].Line)
	}
	return nil
}

// isPlainName reports whether a marker name is ASCII letters, digits and _:
// the only names whose equality does not depend on the server's collation.
func isPlainName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}

// checkUnconditional refuses a declaration that might not run, or run more
// than once: after IF 1 = 0 the variable exists but holds NULL, whatever was
// bound. Any flow-control keyword or label before it is enough to refuse; an
// ELSE inside CASE ... END is an expression and does not count.
func checkUnconditional(before []Token) error {
	caseDepth := 0
	for i, t := range before {
		switch u := strings.ToUpper(t.Text); {
		case u == "CASE":
			caseDepth++
		case u == "END" && caseDepth > 0:
			caseDepth--
		case u == "ELSE" && caseDepth > 0:
		case u == "IF" || u == "ELSE" || u == "WHILE" || u == "BEGIN" || u == "GOTO":
			return fmt.Errorf("the declaration follows %s at line %d and may not run", u, t.Line)
		case i+1 < len(before) && before[i+1].Text == ":" && isWordRune([]rune(t.Text)[0]) &&
			(i+2 >= len(before) || before[i+2].Text != ":"):
			return fmt.Errorf("the declaration follows the label %s at line %d and may not run", t.Text, t.Line)
		}
	}
	return nil
}

// checkNotGlued refuses "TOP 1@p = ...": Lex reads 1@p as one word, SQL Server
// reads the number 1 then @p, which it assigns. A number never holds an @, so
// what follows the first @ of a word starting with a digit or $ is a variable.
// x@p is an identifier for both, and is left alone.
func checkNotGlued(toks []Token, target string) error {
	for _, t := range toks {
		if t.Text == "" || !(t.Text[0] >= '0' && t.Text[0] <= '9' || t.Text[0] == '$') {
			continue
		}
		if at := strings.IndexByte(t.Text, '@'); at > 0 && strings.EqualFold(t.Text[at:], target) {
			return fmt.Errorf("%s at line %d is read by SQL Server as a number followed by %s", t.Text, t.Line, target)
		}
	}
	return nil
}

// checkOneExpression refuses an initializer that is not a single expression,
// because the rewrite replaces everything between "=" and ";": in
// "DECLARE @p int = 1 GOTO x;" it would delete the GOTO. At the declaration's
// depth, operands (literal, variable, name, parenthesized group) must alternate
// with binary operators; a name immediately followed by a group is a call.
// Strings, quoted names and comments are blanked by Sanitize, so they are read
// back from the original text rs. CASE and COLLATE are refused rather than parsed.
func checkOneExpression(rs []rune, expr []Token, start, end int) error {
	expect := true    // an operand is expected next
	callable := false // the last operand is a name a "(" may follow
	nPrefix := false  // the last operand is the word N, a Unicode literal prefix
	afterDot := false // a "." glued to the last name awaits the next part
	lastEnd := -1     // rune offset just after the last operand
	// operand records one operand from at to to. word is false for a string
	// literal and a group, which cannot follow a "."; name says a "(" may follow.
	operand := func(at, to int, word, name bool) error {
		if afterDot {
			if !word || at != lastEnd {
				return errors.New("the initializer is not one expression")
			}
			afterDot = false
		} else if !expect {
			return errors.New("the initializer is not one expression")
		}
		expect, callable, nPrefix, lastEnd = false, name, false, to
		return nil
	}
	for i, k := start, 0; i < end; {
		if k < len(expr) && expr[k].Start == i {
			t := expr[k]
			first := []rune(t.Text)[0]
			switch {
			case t.Text == "(":
				j := k + 1
				for expr[j].Text != ")" || expr[j].Depth != t.Depth {
					j++
				}
				if !expect && callable && !afterDot && lastEnd == t.Start {
					callable = false // a call: the group belongs to the name
				} else if err := operand(t.Start, expr[j].End, false, false); err != nil {
					return err
				}
				i, k = expr[j].End, j+1
				continue
			case strings.EqualFold(t.Text, "CASE") || strings.EqualFold(t.Text, "COLLATE"):
				return fmt.Errorf("%s in the initializer is not supported", strings.ToUpper(t.Text))
			case isWordRune(first):
				isName := unicode.IsLetter(first) || first == '_'
				if err := operand(t.Start, t.End, true, isName); err != nil {
					return err
				}
				nPrefix = strings.EqualFold(t.Text, "N")
			case t.Text == ".":
				glued := !expect && lastEnd == t.Start                                                                         // schema.function, or the decimals of 1.5
				leading := expect && k+1 < len(expr) && expr[k+1].Start == t.End && unicode.IsDigit([]rune(expr[k+1].Text)[0]) // .5
				if afterDot || !glued && !leading {
					return errors.New("the initializer is not one expression")
				}
				afterDot, expect, lastEnd = true, false, t.End
			case len(t.Text) == 1 && strings.Contains("+-~", t.Text) && expect && !afterDot:
				// unary
			case len(t.Text) == 1 && strings.Contains("+-*/%&|^", t.Text) && !expect && !afterDot:
				expect, callable, nPrefix = true, false, false
			default:
				return fmt.Errorf("%q in the initializer is not supported", t.Text)
			}
			i, k = t.End, k+1
			continue
		}
		r := rs[i]
		switch {
		case unicode.IsSpace(r):
			i++
		case r == '/' && i+1 < end && rs[i+1] == '*':
			for depth := 0; i < end; {
				if rs[i] == '/' && i+1 < end && rs[i+1] == '*' {
					depth, i = depth+1, i+2
				} else if rs[i] == '*' && i+1 < end && rs[i+1] == '/' {
					depth, i = depth-1, i+2
					if depth == 0 {
						break
					}
				} else {
					i++
				}
			}
		case r == '\'' || r == '"' || r == '[':
			closing := map[rune]rune{'\'': '\'', '"': '"', '[': ']'}[r]
			j := i + 1
			for j < end && (rs[j] != closing || j+1 < end && rs[j+1] == closing) {
				if rs[j] == closing {
					j++ // doubled, an escape
				}
				j++
			}
			if r == '\'' && nPrefix && lastEnd == i {
				nPrefix, callable, lastEnd = false, false, j+1 // N'...' is one literal
			} else if err := operand(i, j+1, r != '\'', r != '\''); err != nil {
				return err
			}
			i = j + 1
		default:
			return fmt.Errorf("%q in the initializer is not supported", string(r))
		}
	}
	if expect || afterDot {
		return errors.New("the initializer is not one expression")
	}
	return nil
}

// checkDateRange refuses what the driver or the server would change silently:
// year 0 becomes 0001, and datetime and smalldatetime cover less than date.
func checkDateRange(t ParamType, d time.Time) error {
	min, max := time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
	switch t.Base {
	case "datetime":
		min = time.Date(1753, 1, 1, 0, 0, 0, 0, time.UTC)
	case "smalldatetime":
		min, max = time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2079, 6, 6, 23, 59, 0, 0, time.UTC)
	}
	if d.Year() < 1 || d.Before(min) || d.After(max) {
		return fmt.Errorf("%s is out of the range of %s", d.Format("2006-01-02"), t)
	}
	return nil
}

// Rewrite replaces the initializer of each passed parameter by @sqlq_<name>.
// params come in marker order, which need not be source order: they are applied
// from the highest offset down, so an earlier replacement never shifts a later one.
func Rewrite(sql string, params []OverrideParam, passed map[string]bool) string {
	rs := []rune(sql)
	ordered := append([]OverrideParam(nil), params...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].start > ordered[j].start })
	for _, p := range ordered {
		if !passed[p.Name] {
			continue
		}
		repl := []rune(" @sqlq_" + p.Name)
		rs = append(rs[:p.start], append(repl, rs[p.end:]...)...)
	}
	return string(rs)
}

// BindValue validates value against the declared type and returns what to bind.
// SQL Server truncates an oversized string assigned to a variable without an
// error, so lengths are checked here; dates are bound as civil types so they
// do not depend on the session's DATEFORMAT.
func BindValue(t ParamType, value string) (any, error) {
	switch {
	case t.Base == "sysname" || stringTypes[t.Base]:
		// n counts UTF-16 code units, not runes: an emoji takes two.
		if t.Length > 0 && len(utf16.Encode([]rune(value))) > t.Length {
			return nil, fmt.Errorf("value longer than %s", t)
		}
		if (t.Base == "varchar" || t.Base == "char") && !isASCII(value) {
			return nil, fmt.Errorf("%s accepts ASCII only", t)
		}
		return value, nil
	case intRanges[t.Base] != [2]int64{}:
		n, err := strconv.ParseInt(value, 10, 64)
		r := intRanges[t.Base]
		if err != nil || n < r[0] || n > r[1] {
			return nil, fmt.Errorf("value is not a %s", t)
		}
		return n, nil
	case t.Base == "bit":
		switch strings.ToLower(value) {
		case "1", "true":
			return true, nil
		case "0", "false":
			return false, nil
		}
		return nil, errors.New("bit accepts 0, 1, true or false")
	case t.Base == "date":
		d, err := time.Parse("2006-01-02", value)
		if err != nil {
			return nil, errors.New("date must be YYYY-MM-DD")
		}
		if err := checkDateRange(t, d); err != nil {
			return nil, err
		}
		return civil.DateOf(d), nil
	default: // datetime, datetime2, smalldatetime
		// time.Parse accepts fractional seconds the layout does not name, and
		// the server rounds them: 23:59:59.999 as datetime is the next day.
		if strings.ContainsAny(value, ".,") {
			return nil, fmt.Errorf("%s takes no fractional seconds", t)
		}
		layouts := []string{"2006-01-02", "2006-01-02T15:04", "2006-01-02T15:04:05"}
		if t.Base == "smalldatetime" {
			// smalldatetime rounds seconds to the minute: refuse them rather
			// than report 10:30:30 for a variable holding 10:31.
			layouts = layouts[:2]
		}
		for _, layout := range layouts {
			if d, err := time.Parse(layout, value); err == nil {
				if err := checkDateRange(t, d); err != nil {
					return nil, err
				}
				return civil.DateTimeOf(d), nil
			}
		}
		return nil, fmt.Errorf("%s must be YYYY-MM-DD or YYYY-MM-DDTHH:MM[:SS]", t)
	}
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}
