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
		got, err := Decode(c.in)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if strings.TrimSpace(got) != plan {
			t.Errorf("%s: got %.60q…", c.name, got)
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
	}
	for _, c := range cases {
		_, err := Decode([]byte(c.in))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want an error containing %q", c.name, err, c.want)
		}
	}
}

func TestPage(t *testing.T) {
	page, err := Page(`a <b> & "c"`, []Plan{{Name: "one.sqlplan", XML: plan}, {Name: "two</script>.sqlplan", XML: plan}}, "dark")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<title>a &lt;b&gt; &amp; &#34;c&#34;</title>`,
		`data-theme="dark"`,
		`theme: "dark"`,
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
	if _, err := Page("x", []Plan{{Name: "p", XML: plan}}, "blue"); err == nil {
		t.Error("theme blue accepted")
	}
}
