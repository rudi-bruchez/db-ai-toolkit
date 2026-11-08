// Package replay turns a query of the workload into what goDTA sends to
// compile it again: the session options, a temporary procedure when the query
// has sniffed parameter values, the call, and the cleanup (spec section 8.5).
// A query that cannot be replayed safely is marked unreplayable, with the
// reason, and nothing of it is ever sent.
package replay

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/showplan"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/sqlq"
)

// Kind is how a query is replayed.
type Kind string

// The kinds of replay.
const (
	// KindAdhoc sends the text as is.
	KindAdhoc Kind = "adhoc"
	// KindProc wraps the text in a temporary procedure called with the
	// compiled parameter values.
	KindProc Kind = "proc"
	// KindBatchLocals declares the variables without a value before the
	// text, as the original ad hoc batch did.
	KindBatchLocals Kind = "batch_locals"
	// KindUnreplayable is never sent; Reason says why.
	KindUnreplayable Kind = "unreplayable"
)

// Replay is what the evaluation sends for one query: SetOptions each in its
// own batch, then Setup, then Call under SHOWPLAN_XML or AUTOPILOT, then
// Teardown.
type Replay struct {
	Kind       Kind
	Setup      []string
	Call       string
	Teardown   []string
	SetOptions []string
	Reason     string
}

// setBits are the set_options bits goDTA replays, in this order. The other
// bits (parallel plan, ANSI_NULL_DFLT_ON and the rest) do not change what the
// optimizer may use.
var setBits = []struct {
	bit  int64
	name string
}{
	{1, "ANSI_PADDING"},
	{8, "CONCAT_NULL_YIELDS_NULL"},
	{16, "ANSI_WARNINGS"},
	{32, "ANSI_NULLS"},
	{64, "QUOTED_IDENTIFIER"},
	{4096, "ARITHABORT"},
	{8192, "NUMERIC_ROUNDABORT"},
}

// SetStatements returns one SET statement per replayed option of the
// set_options bit mask opts, ON or OFF.
func SetStatements(opts int64) []string {
	out := make([]string, 0, len(setBits))
	for _, b := range setBits {
		v := "OFF"
		if opts&b.bit != 0 {
			v = "ON"
		}
		out = append(out, "SET "+b.name+" "+v)
	}
	return out
}

// dateFormats maps sys.query_context_settings.date_format (and the plan
// attribute of the same name) to SET DATEFORMAT. The documentation gives no
// table; measured on 2022 through sys.dm_exec_plan_attributes, each SET
// DATEFORMAT in its own batch.
var dateFormats = map[int]string{1: "mdy", 2: "dmy", 3: "ymd", 4: "ydm", 5: "myd", 6: "dym"}

// startVerbs are the keywords a replayed statement may begin with. SET is
// allowed only as the assignment of a local variable (SET @x = ...).
var startVerbs = map[string]bool{
	"SELECT": true, "WITH": true, "INSERT": true, "UPDATE": true, "DELETE": true,
	"MERGE": true, "DECLARE": true, "SET": true,
}

// dmlVerbs are the verbs that make the main part of a statement.
var dmlVerbs = map[string]bool{"SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true, "MERGE": true}

// otherStatements are reserved words that can only begin a statement of
// another kind. Found anywhere, they reveal a statement that was not
// separated by a semicolon (SELECT 1 EXEC p). USE (OPTION (USE HINT)), FETCH
// (OFFSET ... FETCH) and BULK (OPENROWSET(BULK ...)) also appear inside
// statements, and are checked at a statement start only.
var otherStatements = map[string]bool{
	"CREATE": true, "ALTER": true, "DROP": true, "EXEC": true, "EXECUTE": true, "TRUNCATE": true,
	"RESTORE": true, "BACKUP": true, "GRANT": true, "DENY": true, "REVOKE": true, "DBCC": true,
	"PRINT": true, "RAISERROR": true, "IF": true, "WHILE": true, "BEGIN": true, "COMMIT": true,
	"ROLLBACK": true, "SAVE": true, "WAITFOR": true, "KILL": true, "SHUTDOWN": true,
	"RECONFIGURE": true, "CHECKPOINT": true, "OPEN": true, "CLOSE": true, "DEALLOCATE": true,
	"RETURN": true, "GOTO": true, "BREAK": true, "CONTINUE": true,
}

// variable is one declared variable: its name and its type as written.
type variable struct {
	name, typ string
}

// Build decides how q is replayed. st is the statement of q's plan that has
// a plan (Root not nil), w the workload q comes from, tempProc the full name
// of the temporary procedure (#godta_<run>_r).
func Build(q model.Query, st *showplan.Statement, w *model.Workload, tempProc string) Replay {
	if st == nil {
		st = &showplan.Statement{}
	}
	df, ok := dateFormats[q.DateFormat]
	if !ok {
		return unreplayable(fmt.Sprintf("unknown date_format %d", q.DateFormat))
	}
	if q.DateFirst < 1 || q.DateFirst > 7 {
		return unreplayable(fmt.Sprintf("unknown date_first %d", q.DateFirst))
	}
	setOpts := append(SetStatements(q.SetOptions), "SET DATEFIRST "+strconv.Itoa(q.DateFirst), "SET DATEFORMAT "+df)

	// Rule 1: the declaration.
	vars, body, hasPrefix := splitPrefix(q.Text)
	if !hasPrefix && q.ObjectID > 0 {
		// The statement of a procedure: its parameters come from the plan,
		// whose ParameterList is in reverse declaration order.
		for i := len(st.Params) - 1; i >= 0; i-- {
			vars = append(vars, variable{st.Params[i].Name, st.Params[i].DataType})
		}
	}

	// Rules 2 and 3: the kind of each statement of the body.
	toks := sqlq.Lex(sqlq.Sanitize(body))
	if reason := checkStatements(toks); reason != "" {
		return unreplayable(reason)
	}

	// Rule 4: the objects of the plan.
	if reason := checkObjects(st, w); reason != "" {
		return unreplayable(reason)
	}

	// Rule 5: the PSP's OPTION (PLAN PER VALUE(...)).
	body = removePlanPerValue(body)

	// Rule 6: no declaration.
	if len(vars) == 0 {
		return Replay{Kind: KindAdhoc, Call: body, SetOptions: setOpts}
	}

	declared := declaredByBody(sqlq.Lex(sqlq.Sanitize(body)))
	values := make([]string, len(vars))
	allValued := true
	for i, v := range vars {
		val, ok := compiledValue(st.Params, v.name)
		if !ok || declared[strings.ToUpper(v.name)] {
			allValued = false
			break
		}
		values[i] = argument(v.typ, val)
	}

	// Rule 7: every variable has a sniffed value.
	if allValued {
		decl := make([]string, len(vars))
		args := make([]string, len(vars))
		for i, v := range vars {
			decl[i] = v.name + " " + v.typ
			args[i] = v.name + " = " + values[i]
		}
		return Replay{
			Kind:       KindProc,
			Setup:      []string{"CREATE PROCEDURE " + tempProc + " (" + strings.Join(decl, ", ") + ") AS " + body},
			Call:       "EXEC " + tempProc + " " + strings.Join(args, ", "),
			Teardown:   []string{"DROP PROCEDURE " + tempProc},
			SetOptions: setOpts,
		}
	}

	// Rule 8: local variables, declared without a value unless the body
	// declares them itself.
	var b strings.Builder
	for _, v := range vars {
		if declared[strings.ToUpper(v.name)] {
			continue
		}
		b.WriteString("DECLARE " + v.name + " " + v.typ + "; ")
	}
	b.WriteString(body)
	return Replay{Kind: KindBatchLocals, Call: b.String(), SetOptions: setOpts}
}

func unreplayable(reason string) Replay {
	return Replay{Kind: KindUnreplayable, Reason: reason}
}

// splitPrefix detaches the (@p type, ...) prefix the Query Store writes before
// a parameterized text, and splits it at the commas of its first level of
// parentheses, so decimal(10,2) stays whole.
func splitPrefix(text string) ([]variable, string, bool) {
	rs := []rune(text)
	toks := sqlq.Lex(sqlq.Sanitize(text))
	if len(toks) < 2 || toks[0].Text != "(" || !strings.HasPrefix(toks[1].Text, "@") {
		return nil, text, false
	}
	// Lex puts "(" and ")" at the outer level: the prefix's own parentheses
	// sit at depth 0, its commas at depth 1, those of decimal(10,2) deeper.
	var vars []variable
	start := toks[0].End
	for _, tk := range toks[1:] {
		end := tk.Text == ")" && tk.Depth == 0
		if !end && (tk.Text != "," || tk.Depth != 1) {
			continue
		}
		if part := strings.TrimSpace(string(rs[start:tk.Start])); part != "" {
			name, typ := part, ""
			if k := strings.IndexFunc(part, unicode.IsSpace); k >= 0 {
				name, typ = part[:k], strings.TrimSpace(part[k:])
			}
			vars = append(vars, variable{name, typ})
		}
		start = tk.End
		if end {
			return vars, string(rs[tk.End:]), true
		}
	}
	return nil, text, false
}

// statements cuts tokens at the semicolons of depth 0.
func statements(toks []sqlq.Token) [][]sqlq.Token {
	var out [][]sqlq.Token
	cur := []sqlq.Token{}
	for _, tk := range toks {
		if tk.Text == ";" && tk.Depth == 0 {
			out = append(out, cur)
			cur = []sqlq.Token{}
			continue
		}
		cur = append(cur, tk)
	}
	return append(out, cur)
}

// checkStatements applies rules 2 and 3 and returns the reason a statement
// is not replayable, or "".
func checkStatements(toks []sqlq.Token) string {
	for _, stmt := range statements(toks) {
		i := 0
		for i < len(stmt) && stmt[i].Text == "(" {
			i++
		}
		if i == len(stmt) {
			continue // an empty statement: ;WITH, a trailing semicolon
		}
		first := strings.ToUpper(stmt[i].Text)
		if !startVerbs[first] || (first == "SET" && (i+1 == len(stmt) || !strings.HasPrefix(stmt[i+1].Text, "@"))) {
			return "statement kind: " + first
		}
		base := stmt[i].Depth
		verb := ""
		for _, tk := range stmt[i:] {
			w := strings.ToUpper(tk.Text)
			if otherStatements[w] {
				return "statement kind: " + w
			}
			if tk.Depth != base {
				continue
			}
			if verb == "" && dmlVerbs[w] {
				verb = w
			}
			if w == "INTO" && verb == "SELECT" {
				return "select into"
			}
		}
	}
	return ""
}

// checkObjects applies rule 4 to the reads and the writes of the plan.
func checkObjects(st *showplan.Statement, w *model.Workload) string {
	var objs []showplan.ObjRef
	for _, a := range st.Accesses {
		objs = append(objs, a.Obj)
	}
	for _, wr := range st.Writes {
		objs = append(objs, wr.Objs...)
	}
	coll := strings.ToUpper(w.Database.Collation)
	same := strings.EqualFold
	if strings.Contains(coll, "_CS") || strings.Contains(coll, "_BIN") {
		same = func(a, b string) bool { return a == b }
	}
	for _, o := range objs {
		switch {
		case strings.HasPrefix(o.Table, "#"):
			return "temp table"
		case strings.HasPrefix(o.Table, "@"):
			return "table variable"
		// Database names follow the instance's collation, which the
		// workload does not hold: compared without case.
		case o.Database != "" && !w.Database.Matches(o.Database):
			return "other database: " + o.Database
		}
		found := false
		for _, t := range w.Tables {
			if (o.Schema == "" || same(o.Schema, t.Schema)) && same(o.Table, t.Name) {
				found = true
				break
			}
		}
		if !found {
			return "missing object"
		}
	}
	return ""
}

// removePlanPerValue removes the PLAN PER VALUE(...) hint the Query Store adds
// to the variants of a PSP query, keeping the other hints of its OPTION
// clause, or the whole clause when it was the only one.
func removePlanPerValue(body string) string {
	rs := []rune(body)
	toks := sqlq.Lex(sqlq.Sanitize(body))
	for i := 0; i+1 < len(toks); i++ {
		if !strings.EqualFold(toks[i].Text, "OPTION") || toks[i+1].Text != "(" {
			continue
		}
		d := toks[i+1].Depth + 1
		// The hints of the clause, as [first, last] token ranges.
		type hint struct{ first, last int }
		var hints []hint
		closing := -1
		h := hint{first: i + 2}
		for j := i + 2; j < len(toks); j++ {
			if toks[j].Depth == d && toks[j].Text == "," {
				hints = append(hints, hint{h.first, j - 1})
				h = hint{first: j + 1}
				continue
			}
			if toks[j].Text == ")" && toks[j].Depth == d-1 {
				hints = append(hints, hint{h.first, j - 1})
				closing = j
				break
			}
		}
		if closing < 0 {
			return body
		}
		ppv := -1
		for k, hn := range hints {
			if hn.last >= hn.first+2 && strings.EqualFold(toks[hn.first].Text, "PLAN") &&
				strings.EqualFold(toks[hn.first+1].Text, "PER") && strings.EqualFold(toks[hn.first+2].Text, "VALUE") {
				ppv = k
			}
		}
		if ppv < 0 {
			continue
		}
		var from, to int // rune range to cut
		switch {
		case len(hints) == 1:
			from = toks[i].Start
			for from > 0 && (rs[from-1] == ' ' || rs[from-1] == '\t' || rs[from-1] == '\r' || rs[from-1] == '\n') {
				from--
			}
			to = toks[closing].End
		case ppv == 0:
			from, to = toks[hints[0].first].Start, toks[hints[1].first].Start
		default:
			from, to = toks[hints[ppv-1].last].End, toks[hints[ppv].last].End
		}
		return string(rs[:from]) + string(rs[to:])
	}
	return body
}

// declaredByBody returns the variables (upper case) the body declares itself.
func declaredByBody(toks []sqlq.Token) map[string]bool {
	out := map[string]bool{}
	for i := 0; i < len(toks); i++ {
		if !strings.EqualFold(toks[i].Text, "DECLARE") {
			continue
		}
		d := toks[i].Depth
		expect := true
		for j := i + 1; j < len(toks); j++ {
			tk := toks[j]
			if tk.Depth == d && (tk.Text == ";" || startVerbs[strings.ToUpper(tk.Text)]) {
				break
			}
			if expect && strings.HasPrefix(tk.Text, "@") {
				out[strings.ToUpper(tk.Text)] = true
			}
			expect = tk.Depth == d && tk.Text == ","
		}
	}
	return out
}

// compiledValue returns the ParameterCompiledValue of the variable name.
func compiledValue(params []showplan.Param, name string) (string, bool) {
	for _, p := range params {
		if strings.EqualFold(p.Name, name) && p.CompiledValue != "" {
			return p.CompiledValue, true
		}
	}
	return "", false
}

// planDatetime is how a plan writes a datetime or smalldatetime value.
var planDatetime = regexp.MustCompile(`^'(\d{4}-\d{2}-\d{2}) (\d{2}:\d{2}:\d{2}(\.\d+)?)'$`)

// argument turns a compiled value into an EXEC argument: (9332) becomes 9332,
// a datetime or smalldatetime is written in ISO 8601 with a T, which reads
// the same under every DATEFORMAT (the plan's form does not), and anything
// else passes as is. datetime2 and date read yyyy-mm-dd whatever DATEFORMAT
// says, and would lose their seven decimals through datetime (measured).
func argument(typ, val string) string {
	if len(val) >= 2 && val[0] == '(' && val[len(val)-1] == ')' {
		return val[1 : len(val)-1]
	}
	t := strings.ToLower(strings.TrimSpace(typ))
	if t == "datetime" || t == "smalldatetime" {
		if m := planDatetime.FindStringSubmatch(val); m != nil {
			return "'" + m[1] + "T" + m[2] + "'"
		}
	}
	return val
}
