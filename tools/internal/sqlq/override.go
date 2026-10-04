package sqlq

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
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
	}
	var out []OverrideParam
	for _, name := range names {
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
	typ, eq, err := parseDeclaredType(lineToks)
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
	if err := checkNotAssigned(toks, decl+1, target); err != nil {
		return OverrideParam{}, err
	}
	return OverrideParam{Name: name, Type: typ, Default: def, start: start, end: end}, nil
}

// parseDeclaredType reads "DECLARE @p <type> =" and returns the type and the
// index of "=" in the line's tokens.
func parseDeclaredType(lt []Token) (ParamType, int, error) {
	if len(lt) < 5 {
		return ParamType{}, 0, errors.New("incomplete declaration")
	}
	base := strings.ToLower(lt[2].Text)
	i := 3
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
		if next != "=" || toks[i].Depth > 0 {
			continue
		}
		if i > 0 && comparisonContext[strings.ToUpper(toks[i-1].Text)] {
			continue
		}
		return fmt.Errorf("assigned at line %d", toks[i].Line)
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
		return civil.DateOf(d), nil
	default: // datetime, datetime2, smalldatetime
		layouts := []string{"2006-01-02", "2006-01-02T15:04", "2006-01-02T15:04:05"}
		if t.Base == "smalldatetime" {
			// smalldatetime rounds seconds to the minute: refuse them rather
			// than report 10:30:30 for a variable holding 10:31.
			layouts = layouts[:2]
		}
		for _, layout := range layouts {
			if d, err := time.Parse(layout, value); err == nil {
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
