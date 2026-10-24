// Package sqlplan turns SQL Server execution plans (showplan XML) into a
// self-contained HTML page that draws them graphically.
//
// The drawing is done in the browser by html-query-plan, as built in
// github.com/rudi-bruchez/query-plan, embedded under assets/ (see its README).
package sqlplan

import (
	"bytes"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"strings"
	"unicode/utf16"
)

//go:embed assets/qp.min.js assets/qp.css assets/qp_icons.png
var assets embed.FS

// Plan is one execution plan to draw, with the name shown above it.
type Plan struct {
	Name string
	XML  string
}

// Decode reads a plan from the bytes of a .sqlplan file (UTF-8, or UTF-16 with a
// byte order mark, as SSMS writes it) or from the JSON that `sqlq -plan` prints.
func Decode(data []byte) (string, error) {
	text, err := decodeText(data)
	if err != nil {
		return "", err
	}
	trimmed := strings.TrimSpace(text)
	switch {
	case strings.HasPrefix(trimmed, "{"):
		var out struct {
			Plan  *string         `json:"plan"`
			Error json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
			return "", fmt.Errorf("not a sqlq result: %w", err)
		}
		if out.Plan == nil || *out.Plan == "" {
			if len(out.Error) > 0 && string(out.Error) != "null" {
				return "", fmt.Errorf("sqlq returned an error, no plan: %s", out.Error)
			}
			return "", errors.New("the sqlq result has no plan: run sqlq with -plan")
		}
		return checkXML(*out.Plan)
	case strings.HasPrefix(trimmed, "<"):
		return checkXML(trimmed)
	default:
		return "", errors.New("neither showplan XML nor a sqlq JSON result")
	}
}

func checkXML(s string) (string, error) {
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

// Page returns a standalone HTML page drawing the plans, in the given theme:
// "light", "dark", or "auto" to follow the system.
func Page(title string, plans []Plan, theme string) (string, error) {
	switch theme {
	case "light", "dark", "auto":
	default:
		return "", fmt.Errorf("unknown theme %q: light, dark or auto", theme)
	}
	js, err := assets.ReadFile("assets/qp.min.js")
	if err != nil {
		return "", err
	}
	css, err := assets.ReadFile("assets/qp.css")
	if err != nil {
		return "", err
	}
	icons, err := assets.ReadFile("assets/qp_icons.png")
	if err != nil {
		return "", err
	}
	// One file: the sprite goes inline as a data URI
	inlineCSS := strings.ReplaceAll(string(css), "url('qp_icons.png')",
		"url('data:image/png;base64,"+base64.StdEncoding.EncodeToString(icons)+"')")

	type item struct {
		Name string `json:"name"`
		XML  string `json:"xml"`
	}
	items := make([]item, len(plans))
	for i, p := range plans {
		items[i] = item{p.Name, p.XML}
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
<style>
%s
html { color-scheme: light dark; }
html[data-theme="light"] { color-scheme: light; }
html[data-theme="dark"] { color-scheme: dark; }
body { margin: 0; padding: 1rem; background: #fff; color: #1f2328; font: 14px system-ui, sans-serif; }
html[data-theme="dark"] body { background: #0d1117; color: #e6edf3; }
@media (prefers-color-scheme: dark) { html[data-theme="auto"] body { background: #0d1117; color: #e6edf3; } }
h2 { font-size: 1rem; font-weight: 600; margin: 1.5rem 0 .5rem; }
h2:first-child { margin-top: 0; }
</style>
<script>
%s
</script>
</head>
<body>
<script>
(function () {
  var plans = %s;
  plans.forEach(function (p) {
    var h = document.body.appendChild(document.createElement("h2"));
    h.textContent = p.name;
    var c = document.body.appendChild(document.createElement("div"));
    QP.showPlan(c, p.xml, { theme: %q });
  });
})();
</script>
</body>
</html>
`, theme, html.EscapeString(title), inlineCSS, js, data, theme)
	return b.String(), nil
}
