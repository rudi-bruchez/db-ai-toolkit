// Package sqlplan turns execution plans into a self-contained HTML page that draws
// them graphically: SQL Server showplan XML, and PostgreSQL EXPLAIN output.
//
// The drawing is done in the browser, by html-query-plan as built in
// github.com/rudi-bruchez/query-plan for SQL Server, and by Dalibo's pev2 for
// PostgreSQL, both embedded under assets/ (see its README).
package sqlplan

import (
	"bytes"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"regexp"
	"strings"
	"unicode/utf16"
)

//go:embed assets/qp.min.js assets/qp.css assets/qp_icons.png
//go:embed assets/vue.global.prod.js assets/pev2.umd.js assets/pev2.css assets/bootstrap.min.css
var assets embed.FS

// The database engines whose plans can be drawn.
const (
	SQLServer = "sqlserver"
	Postgres  = "postgres"
)

// Plan is one execution plan to draw, with the name shown above it.
type Plan struct {
	Name string
	// Text is showplan XML for SQL Server, EXPLAIN output (JSON or text) for PostgreSQL.
	Text   string
	Engine string
}

// The text format of EXPLAIN: every node line carries its cost or its actual figures
var explainText = regexp.MustCompile(`\((cost=\d|actual (time|rows)=)`)

// Decode reads a plan and tells which engine it comes from: a .sqlplan file (UTF-8,
// or UTF-16 with a byte order mark, as SSMS writes it), the JSON that `sqlq -plan`
// prints, or the output of PostgreSQL's EXPLAIN in JSON or text format.
func Decode(data []byte) (text, engine string, err error) {
	text, err = decodeText(data)
	if err != nil {
		return "", "", err
	}
	trimmed := strings.TrimSpace(text)
	switch {
	case strings.HasPrefix(trimmed, "["):
		// EXPLAIN (FORMAT JSON): an array of one object per statement, each with a "Plan"
		var stmts []map[string]json.RawMessage
		if json.Unmarshal([]byte(trimmed), &stmts) != nil || len(stmts) == 0 || stmts[0]["Plan"] == nil {
			return "", "", errors.New(`a JSON array without a "Plan": not a PostgreSQL EXPLAIN`)
		}
		return trimmed, Postgres, nil
	case strings.HasPrefix(trimmed, "{"):
		var out struct {
			Plan  json.RawMessage `json:"plan"`
			PG    json.RawMessage `json:"Plan"`
			Error json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
			return "", "", fmt.Errorf("not a sqlq result: %w", err)
		}
		// auto_explain logs one statement as an object with a "Plan"
		if out.PG != nil && out.Plan == nil {
			return trimmed, Postgres, nil
		}
		var plan string
		if len(out.Plan) > 0 && string(out.Plan) != "null" {
			if err := json.Unmarshal(out.Plan, &plan); err != nil {
				return "", "", fmt.Errorf("not a sqlq result: %w", err)
			}
		}
		if plan == "" {
			if len(out.Error) > 0 && string(out.Error) != "null" {
				return "", "", fmt.Errorf("sqlq returned an error, no plan: %s", out.Error)
			}
			return "", "", errors.New("the sqlq result has no plan: run sqlq with -plan")
		}
		xml, err := checkXML(plan)
		return xml, SQLServer, err
	case strings.HasPrefix(trimmed, "<"):
		xml, err := checkXML(trimmed)
		return xml, SQLServer, err
	case explainText.MatchString(trimmed):
		return trimmed, Postgres, nil
	default:
		return "", "", errors.New("neither showplan XML, a PostgreSQL EXPLAIN, nor a sqlq JSON result")
	}
}

func nodeJS(id string) string {
	if id == "" {
		return "null"
	}
	return `"` + id + `"`
}

func checkXML(s string) (string, error) {
	if strings.Contains(s, "postgresql.org/2009/explain") {
		return "", errors.New("PostgreSQL EXPLAIN in XML format: run it with FORMAT JSON")
	}
	if !strings.Contains(s, "<ShowPlanXML") {
		return "", errors.New("XML without a <ShowPlanXML> element: not an execution plan")
	}
	return s, nil
}

func decodeText(data []byte) (string, error) {
	switch {
	case bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}):
		return string(data[3:]), nil
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE}):
		return utf16String(data[2:], false)
	case bytes.HasPrefix(data, []byte{0xFE, 0xFF}):
		return utf16String(data[2:], true)
	}
	return string(data), nil
}

func utf16String(b []byte, bigEndian bool) (string, error) {
	if len(b)%2 != 0 {
		return "", errors.New("truncated UTF-16 text")
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		if bigEndian {
			u[i] = uint16(b[2*i])<<8 | uint16(b[2*i+1])
		} else {
			u[i] = uint16(b[2*i+1])<<8 | uint16(b[2*i])
		}
	}
	return string(utf16.Decode(u)), nil
}

// Options shape the page.
type Options struct {
	// Theme is "light", "dark", or "auto" to follow the system.
	Theme string
	// NodeID, when set, draws only the operator of that NodeId and the operators under it.
	// SQL Server plans only.
	NodeID string
	// ForImage drops the plan names and makes the page report its size in its title,
	// as "size:<width>x<height>", for a screenshot cut to the plan. SQL Server plans only:
	// pev2 is an interactive viewer filling its window, not a drawing with a size.
	ForImage bool
}

// Page returns a standalone HTML page drawing the plans. Only the libraries the plans
// need go into it.
func Page(title string, plans []Plan, opts Options) (string, error) {
	theme := opts.Theme
	switch theme {
	case "light", "dark", "auto":
	default:
		return "", fmt.Errorf("unknown theme %q: light, dark or auto", theme)
	}
	for _, r := range opts.NodeID {
		if r < '0' || r > '9' {
			return "", fmt.Errorf("node %q: a NodeId is a number", opts.NodeID)
		}
	}
	engines := map[string]bool{}
	for _, p := range plans {
		switch p.Engine {
		case SQLServer, Postgres:
			engines[p.Engine] = true
		default:
			return "", fmt.Errorf("%s: unknown engine %q", p.Name, p.Engine)
		}
	}
	if engines[Postgres] && (opts.ForImage || opts.NodeID != "") {
		return "", errors.New("an image or a single node is for SQL Server plans only: pev2 draws PostgreSQL plans as an interactive viewer")
	}

	var head strings.Builder
	if engines[SQLServer] {
		css, err := assets.ReadFile("assets/qp.css")
		if err != nil {
			return "", err
		}
		icons, err := assets.ReadFile("assets/qp_icons.png")
		if err != nil {
			return "", err
		}
		// One file: the sprite goes inline as a data URI
		css = []byte(strings.ReplaceAll(string(css), "url('qp_icons.png')",
			"url('data:image/png;base64,"+base64.StdEncoding.EncodeToString(icons)+"')"))
		style(&head, css)
		if err := scripts(&head, "assets/qp.min.js"); err != nil {
			return "", err
		}
	}
	if engines[Postgres] {
		for _, f := range []string{"assets/bootstrap.min.css", "assets/pev2.css"} {
			css, err := assets.ReadFile(f)
			if err != nil {
				return "", err
			}
			style(&head, css)
		}
		// pev2 is a Vue component: Vue comes first
		if err := scripts(&head, "assets/vue.global.prod.js", "assets/pev2.umd.js"); err != nil {
			return "", err
		}
	}

	type item struct {
		Name   string `json:"name"`
		Text   string `json:"text"`
		Engine string `json:"engine"`
	}
	items := make([]item, len(plans))
	for i, p := range plans {
		items[i] = item{p.Name, p.Text, p.Engine}
	}
	// json.Marshal escapes <, > and &, so no "</script>" can close the tag early
	data, err := json.Marshal(items)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<!doctype html>
<html lang="en" data-theme="%s">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>%s</title>
%s<style>
html { color-scheme: light dark; }
html[data-theme="light"] { color-scheme: light; }
html[data-theme="dark"] { color-scheme: dark; }
body { margin: 0; padding: 1rem; background: #fff; color: #1f2328; font: 14px system-ui, sans-serif; }
html[data-theme="dark"] body { background: #0d1117; color: #e6edf3; }
@media (prefers-color-scheme: dark) { html[data-theme="auto"] body { background: #0d1117; color: #e6edf3; } }
h2 { font-size: 1rem; font-weight: 600; margin: 1.5rem 0 .5rem; }
h2:first-child { margin-top: 0; }
.pev2-plan { display: flex; flex-direction: column; height: calc(100vh - 2rem); border: 1px solid var(--bs-border-color); }
h2 + .pev2-plan { height: calc(100vh - 5rem); }
</style>
</head>
<body>
<script>
(function () {
  var plans = %s, forImage = %t, options = { theme: %q, nodeId: %s };
  // pev2 follows Bootstrap's theme attribute; "auto" follows the system, as qp does
  var dark = window.matchMedia("(prefers-color-scheme: dark)");
  function bsTheme() {
    document.documentElement.setAttribute("data-bs-theme",
      options.theme === "auto" ? (dark.matches ? "dark" : "light") : options.theme);
  }
  bsTheme();
  dark.addEventListener("change", bsTheme);
  plans.forEach(function (p) {
    if (!forImage) {
      var h = document.body.appendChild(document.createElement("h2"));
      h.textContent = p.name;
    }
    var c = document.body.appendChild(document.createElement("div"));
    try {
      if (p.engine === "postgres") {
        c.className = "pev2-plan";
        Vue.createApp({ render: function () { return Vue.h(pev2.Plan, { planSource: p.text, planQuery: "" }); } }).mount(c);
      } else {
        c.style.display = "inline-block";
        QP.showPlan(c, p.text, options);
      }
    } catch (e) {
      c.textContent = p.name + ": " + e.message;
      document.title = "error:" + e.message;
      throw e;
    }
  });
  if (forImage) {
    // The drawn plans, not the document: its scroll size never goes below the window's
    var w = 0, h = 0;
    document.querySelectorAll("body > div").forEach(function (c) {
      var r = c.getBoundingClientRect();
      w = Math.max(w, r.right);
      h = Math.max(h, r.bottom);
    });
    var pad = parseFloat(getComputedStyle(document.body).paddingRight) || 0;
    document.title = "size:" + Math.ceil(w + pad) + "x" + Math.ceil(h + pad);
  }
})();
</script>
</body>
</html>
`, theme, html.EscapeString(title), head.String(), data, opts.ForImage, theme, nodeJS(opts.NodeID))
	return b.String(), nil
}

func style(b *strings.Builder, css []byte) {
	b.WriteString("<style>\n")
	b.Write(css)
	b.WriteString("\n</style>\n")
}

// scripts writes embedded scripts inline: the page is one file
func scripts(b *strings.Builder, files ...string) error {
	for _, f := range files {
		js, err := assets.ReadFile(f)
		if err != nil {
			return err
		}
		b.WriteString("<script>\n")
		b.Write(js)
		b.WriteString("\n</script>\n")
	}
	return nil
}
