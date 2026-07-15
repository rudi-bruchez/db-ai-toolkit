package errorlog

import (
	"fmt"
	"sort"
	"strings"
)

// Report is the fully assembled digest of one ERRORLOG run: the instance
// summary, any advisories raised, entry counts, and the aggregated events
// to show. It is the single input to Render.
type Report struct {
	Summary      InstanceSummary
	Advisories   []Advisory
	Total        int
	Kept         int
	Dropped      int
	DroppedByCat map[string]int
	Events       []Event
}

// RenderOptions controls output formatting.
type RenderOptions struct {
	Format      string // "text" (default) or "md"
	ShowSummary bool
	Legend      []string // redaction legend lines; empty when redaction is off
}

// Render turns a Report into a compact text digest or a Markdown report.
func Render(r Report, opts RenderOptions) string {
	if opts.Format == "md" {
		return renderMarkdown(r, opts)
	}
	return renderText(r, opts)
}

func eventLine(e Event) string {
	const f = "2006-01-02 15:04"
	if e.Count > 1 {
		// defensive: aggregate already single-lined this Count>1 text
		return fmt.Sprintf("[×%d] %s–%s  %s", e.Count, e.First.Format(f), e.Last.Format(f), firstLine(e.Text))
	}
	// singleton: keep the full (possibly multi-line) text — continuation
	// lines carry the diagnostic detail this tool must preserve.
	return e.Text
}

// indentContinuation leaves the first line flush and indents every subsequent
// line by two spaces so multi-line singleton events do not read as separate
// top-level events in the compact text digest.
func indentContinuation(s string) string {
	lines := strings.Split(s, "\n")
	for i := 1; i < len(lines); i++ {
		lines[i] = "  " + lines[i]
	}
	return strings.Join(lines, "\n")
}

func sortedCats(m map[string]int) []string {
	cats := make([]string, 0, len(m))
	for c := range m {
		cats = append(cats, c)
	}
	sort.Strings(cats)
	return cats
}

func renderText(r Report, opts RenderOptions) string {
	var b strings.Builder
	if opts.ShowSummary {
		s := r.Summary
		b.WriteString("=== INSTANCE ===\n")
		fmt.Fprintf(&b, "%s %s\n", s.Product, s.Edition)
		fmt.Fprintf(&b, "RAM %d MB · collation %s · auth %s · UTC %s\n", s.RAMMB, s.Collation, s.AuthMode, s.UTCAdjust)
		if !s.FirstTime.IsZero() {
			fmt.Fprintf(&b, "Window: %s\n", s.window())
		}
	}
	if len(r.Advisories) > 0 {
		b.WriteString("=== ADVISORIES ===\n")
		for _, a := range r.Advisories {
			fmt.Fprintf(&b, "[%s] %s\n   → %s\n", a.ID, a.Message, a.URL)
		}
	}
	b.WriteString("=== COUNTS ===\n")
	fmt.Fprintf(&b, "%d entries → %d kept, %d dropped\n", r.Total, r.Kept, r.Dropped)
	if len(r.DroppedByCat) > 0 {
		var parts []string
		for _, c := range sortedCats(r.DroppedByCat) {
			parts = append(parts, fmt.Sprintf("%s=%d", c, r.DroppedByCat[c]))
		}
		fmt.Fprintf(&b, "dropped: %s\n", strings.Join(parts, ", "))
	}
	b.WriteString("=== EVENTS ===\n")
	for _, e := range r.Events {
		b.WriteString(indentContinuation(eventLine(e)) + "\n")
	}
	if len(opts.Legend) > 0 {
		b.WriteString("=== REDACTION ===\n")
		b.WriteString(strings.Join(opts.Legend, "\n") + "\n")
	}
	return b.String()
}

func renderMarkdown(r Report, opts RenderOptions) string {
	var b strings.Builder
	if opts.ShowSummary {
		s := r.Summary
		b.WriteString("## Instance\n\n")
		b.WriteString("| Field | Value |\n| --- | --- |\n")
		fmt.Fprintf(&b, "| Product | %s |\n", s.Product)
		fmt.Fprintf(&b, "| Edition | %s |\n", s.Edition)
		fmt.Fprintf(&b, "| OS | %s |\n", s.OS)
		fmt.Fprintf(&b, "| CPU | %s |\n", s.CPU)
		fmt.Fprintf(&b, "| RAM (MB) | %d |\n", s.RAMMB)
		fmt.Fprintf(&b, "| Collation | %s |\n", s.Collation)
		fmt.Fprintf(&b, "| Auth mode | %s |\n", s.AuthMode)
		fmt.Fprintf(&b, "| UTC adjustment | %s |\n", s.UTCAdjust)
		fmt.Fprintf(&b, "| Service account | %s |\n", s.ServiceAcct)
		fmt.Fprintf(&b, "| TCP ports | %s |\n", strings.Join(s.TCPPorts, ", "))
		fmt.Fprintf(&b, "| SPN | %s |\n", s.SPN)
		fmt.Fprintf(&b, "| IFI | %s |\n", s.IFI)
		fmt.Fprintf(&b, "| AG listeners | %s |\n", strings.Join(s.AGListeners, ", "))
		if !s.FirstTime.IsZero() {
			fmt.Fprintf(&b, "| Log window | %s |\n", s.window())
		}
		b.WriteString("\n")
	}
	if len(r.Advisories) > 0 {
		b.WriteString("## Advisories\n\n")
		for _, a := range r.Advisories {
			fmt.Fprintf(&b, "- **%s** — %s ([ref](%s))\n", a.ID, a.Message, a.URL)
		}
		b.WriteString("\n")
	}
	b.WriteString("## Counts\n\n")
	fmt.Fprintf(&b, "%d entries → **%d kept**, %d dropped\n\n", r.Total, r.Kept, r.Dropped)
	if len(r.DroppedByCat) > 0 {
		b.WriteString("| Category | Dropped |\n| --- | --- |\n")
		for _, c := range sortedCats(r.DroppedByCat) {
			fmt.Fprintf(&b, "| %s | %d |\n", c, r.DroppedByCat[c])
		}
		b.WriteString("\n")
	}
	b.WriteString("## Events\n\n")
	// A fenced code block preserves multi-line singleton detail verbatim and
	// stops CommonMark from misparsing lines that start with '#', '|', or '-'.
	b.WriteString("```text\n")
	for _, e := range r.Events {
		b.WriteString(eventLine(e) + "\n")
	}
	b.WriteString("```\n")
	if len(opts.Legend) > 0 {
		b.WriteString("\n## Redaction legend\n\n")
		for _, l := range opts.Legend {
			b.WriteString("- " + l + "\n")
		}
	}
	return b.String()
}
