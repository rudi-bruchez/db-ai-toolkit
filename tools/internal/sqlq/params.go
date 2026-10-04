package sqlq

import "strings"

// statementStarters end a DECLARE written without its semicolon. Without them,
// the comma of a following SELECT list would declare the next variable.
var statementStarters = map[string]bool{
	"SELECT": true, "SET": true, "IF": true, "WHILE": true, "WITH": true, "DECLARE": true,
	"BEGIN": true, "RETURN": true, "PRINT": true, "RAISERROR": true, "THROW": true,
	"INSERT": true, "UPDATE": true, "DELETE": true, "MERGE": true, "EXEC": true, "EXECUTE": true,
}

// QueryParams returns the variables a batch references without declaring them:
// the parameters a bundled or personal query expects through -param.
// A variable is declared when it is the target of a DECLARE: the first @x after
// DECLARE, and the first @x after each depth-zero comma of the same statement.
func QueryParams(sql string) []string {
	toks := Lex(Sanitize(sql))
	declared := map[string]bool{}
	for i := 0; i < len(toks); i++ {
		if !strings.EqualFold(toks[i].Text, "DECLARE") {
			continue
		}
		base := toks[i].Depth
		expectTarget := true
		for j := i + 1; j < len(toks); j++ {
			t := toks[j]
			if t.Depth == base && (t.Text == ";" || statementStarters[strings.ToUpper(t.Text)]) {
				break
			}
			if expectTarget && strings.HasPrefix(t.Text, "@") && !strings.HasPrefix(t.Text, "@@") {
				declared[strings.ToLower(t.Text)] = true
				expectTarget = false
				continue
			}
			if t.Text == "," && t.Depth == base {
				expectTarget = true
			}
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, t := range toks {
		name := strings.ToLower(t.Text)
		if !strings.HasPrefix(name, "@") || strings.HasPrefix(name, "@@") || declared[name] || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, strings.TrimPrefix(name, "@"))
	}
	return out
}

// DirtyReads reports whether a batch reads uncommitted data on its own: a
// NOLOCK or READUNCOMMITTED hint, or the READ UNCOMMITTED isolation level.
func DirtyReads(sql string) bool {
	toks := Lex(Sanitize(sql))
	for i, t := range toks {
		switch strings.ToUpper(t.Text) {
		case "NOLOCK", "READUNCOMMITTED":
			return true
		case "READ":
			if i+1 < len(toks) && strings.EqualFold(toks[i+1].Text, "UNCOMMITTED") {
				return true
			}
		}
	}
	return false
}
