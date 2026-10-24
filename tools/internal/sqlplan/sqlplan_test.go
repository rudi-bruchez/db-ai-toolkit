package sqlplan

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf16"
)

const plan = `<?xml version="1.0" encoding="utf-16"?><ShowPlanXML xmlns="http://schemas.microsoft.com/sqlserver/2004/07/showplan"><BatchSequence><Batch><Statements><StmtSimple StatementText="SELECT 1 WHERE 'a' &lt; 'b' -- &lt;/script&gt;"/></Statements></Batch></BatchSequence></ShowPlanXML>`

func utf16LE(s string) []byte {
	b := []byte{0xFF, 0xFE}
	for _, u := range utf16.Encode([]rune(s)) {
		b = append(b, byte(u), byte(u>>8))
	}
	return b
}

func TestDecode(t *testing.T) {
	sqlq, _ := json.Marshal(map[string]any{"rows": []any{}, "plan": plan, "error": nil})
	cases := []struct {
		name string
		in   []byte
	}{
		{"utf-8", []byte(plan)},
		{"utf-8 with BOM and blank lines", append([]byte{0xEF, 0xBB, 0xBF}, []byte("\r\n"+plan+"\n")...)},
		{"utf-16 as SSMS writes it", utf16LE(plan)},
		{"sqlq -plan result", sqlq},
	}
	for _, c := range cases {
		got, engine, err := Decode(c.in)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if strings.TrimSpace(got) != plan || engine != SQLServer {
			t.Errorf("%s: got %s %.60q…", c.name, engine, got)
		}
	}
}

func TestDecodeRefuses(t *testing.T) {
	noPlan, _ := json.Marshal(map[string]any{"rows": []any{}, "plan": nil, "error": nil})
	sqlError, _ := json.Marshal(map[string]any{"plan": nil, "error": map[string]any{"number": 208, "message": "Invalid object name 'T'."}})
	cases := []struct {
		name, in, want string
	}{
		{"sqlq run without -plan", string(noPlan), "run sqlq with -plan"},
		{"sqlq error", string(sqlError), "Invalid object name"},
		{"other XML", "<root/>", "not an execution plan"},
		{"plain text", "SELECT 1", "neither showplan XML"},
		{"broken JSON", "{\"plan\":", "not a sqlq result"},
		{"JSON array that is no EXPLAIN", `[{"rows": 1}]`, `without a "Plan"`},
		{"EXPLAIN in XML format", `<explain xmlns="http://www.postgresql.org/2009/explain"><Query/></explain>`, "FORMAT JSON"},
	}
	for _, c := range cases {
		_, _, err := Decode([]byte(c.in))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want an error containing %q", c.name, err, c.want)
		}
	}
}

func TestPage(t *testing.T) {
	page, err := Page(`a <b> & "c"`, []Plan{{Name: "one.sqlplan", Text: plan, Engine: SQLServer}, {Name: "two</script>.sqlplan", Text: plan, Engine: SQLServer}}, Options{Theme: "dark"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<title>a &lt;b&gt; &amp; &#34;c&#34;</title>`,
		`data-theme="dark"`,
		`theme: "dark", nodeId: null`,
		`url('data:image/png;base64,`, // sprite inlined: the page is one file
		`"name":"one.sqlplan"`,
		`"name":"two\u003c/script\u003e.sqlplan"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(page, "url('qp_icons.png')") {
		t.Error("page still points to qp_icons.png, which is not next to it")
	}
	// A file name is the user's and may hold "</script>": only the page's own two
	// script blocks may close a script element
	if n := strings.Count(page, "</script>"); n != 2 {
		t.Errorf("%d </script> in the page, want 2", n)
	}
}

func TestPageRefusesUnknownTheme(t *testing.T) {
	if _, err := Page("x", []Plan{{Name: "p", Text: plan, Engine: SQLServer}}, Options{Theme: "blue"}); err == nil {
		t.Error("theme blue accepted")
	}
	// The NodeId goes into the script as a string literal: only digits may get there
	if _, err := Page("x", []Plan{{Name: "p", Text: plan, Engine: SQLServer}}, Options{Theme: "light", NodeID: `1"); alert(1); ("`}); err == nil {
		t.Error("a NodeId that is not a number was accepted")
	}
}

func TestPageForImage(t *testing.T) {
	page, err := Page("x", []Plan{{Name: "one.sqlplan", Text: plan, Engine: SQLServer}}, Options{Theme: "light", NodeID: "5", ForImage: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`forImage = true`, `nodeId: "5"`, `"size:"`} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

const pgJSON = `[
  {
    "Plan": {"Node Type": "Seq Scan", "Relation Name": "rental", "Total Cost": 1076.05, "Plan Rows": 51805}
  }
]`

func TestDecodePostgres(t *testing.T) {
	cases := []struct{ name, in string }{
		{"EXPLAIN (FORMAT JSON)", pgJSON},
		{"auto_explain JSON", `{"Query Text": "SELECT 1", "Plan": {"Node Type": "Result"}}`},
		{"EXPLAIN text, as psql prints it", "                         QUERY PLAN\n------------------------------------------------------------\n Seq Scan on rental  (cost=0.00..1076.05 rows=51805 width=36)\n(1 row)\n"},
		{"EXPLAIN ANALYZE text", "Seq Scan on rental  (actual time=0.010..4.211 rows=51805 loops=1)"},
	}
	for _, c := range cases {
		got, engine, err := Decode([]byte(c.in))
		if err != nil || engine != Postgres || got != strings.TrimSpace(c.in) {
			t.Errorf("%s: got %q, %v", c.name, engine, err)
		}
	}
}

func TestPageLoadsOnlyTheLibrariesItNeeds(t *testing.T) {
	ms, _ := Page("x", []Plan{{Name: "p", Text: plan, Engine: SQLServer}}, Options{Theme: "auto"})
	pg, err := Page("x", []Plan{{Name: "q", Text: pgJSON, Engine: Postgres}}, Options{Theme: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	// qp.css and Bootstrap each define names the other lacks
	if !strings.Contains(ms, "qp-root") {
		t.Error("SQL Server page lacks its library")
	}
	for _, want := range []string{`"engine":"postgres"`, "--bs-body-bg", `\"Seq Scan\"`} {
		if !strings.Contains(pg, want) {
			t.Errorf("PostgreSQL page lacks %q", want)
		}
	}
	if strings.Contains(pg, "qp-root") {
		t.Error("PostgreSQL page carries the SQL Server library")
	}
	if strings.Contains(ms, "--bs-body-bg") {
		t.Error("SQL Server page carries Bootstrap")
	}
	for _, opts := range []Options{{Theme: "light", ForImage: true}, {Theme: "light", NodeID: "3"}} {
		if _, err := Page("x", []Plan{{Name: "q", Text: pgJSON, Engine: Postgres}}, opts); err == nil {
			t.Errorf("%+v accepted for a PostgreSQL plan", opts)
		}
	}
}
