package errorlog

import "time"

// Options configures the full processing pipeline.
type Options struct {
	MinSeverity int
	Format      string // "text" | "md"
	Redact      bool
	ShowLegend  bool
	Aggregate   bool
	ShowSummary bool
	From, To    time.Time
	RulesDir    string
}

// Process runs the pipeline over the acquired sources and returns the rendered
// digest.
func Process(sources []Source, opts Options) (string, error) {
	rs, err := LoadEmbeddedRules()
	if err != nil {
		return "", err
	}
	if opts.RulesDir != "" {
		if err := rs.LoadDir(opts.RulesDir); err != nil {
			return "", err
		}
	}
	advisor, err := LoadEmbeddedAdvisories()
	if err != nil {
		return "", err
	}

	var allEntries []Entry
	for _, src := range sources {
		allEntries = append(allEntries, Parse(Decode(src.Data))...)
	}

	report := Report{DroppedByCat: map[string]int{}}
	report.Summary = Summarize(allEntries)

	var kept []Entry
	for _, e := range allEntries {
		report.Total++
		if !inWindow(e, opts.From, opts.To) {
			continue
		}
		keep, cat := rs.Classify(e, opts.MinSeverity)
		if keep {
			kept = append(kept, e)
		} else {
			report.DroppedByCat[cat]++
		}
	}
	report.Kept = len(kept)
	report.Dropped = report.Total - report.Kept

	if opts.Aggregate {
		report.Events = Aggregate(kept)
	} else {
		report.Events = make([]Event, 0, len(kept))
		for _, e := range kept {
			report.Events = append(report.Events, Event{First: e.Time, Last: e.Time, Count: 1, Text: e.Text()})
		}
	}

	report.Advisories = advisor.Evaluate(report.DroppedByCat, BootText(allEntries, 400))

	var legend []string
	if opts.Redact {
		red := NewRedactor()
		if report.Summary.ServiceAcct != "" {
			red.AddLogin(report.Summary.ServiceAcct)
		}
		for i := range report.Events {
			red.Scan(report.Events[i].Text)
		}
		red.Scan(report.Summary.ServiceAcct)
		for i := range report.Events {
			report.Events[i].Text = red.Apply(report.Events[i].Text)
		}
		report.Summary.ServiceAcct = red.Apply(report.Summary.ServiceAcct)
		if opts.ShowLegend {
			legend = red.Legend()
		}
	}

	return Render(report, RenderOptions{
		Format:      opts.Format,
		ShowSummary: opts.ShowSummary,
		Legend:      legend,
	}), nil
}

// inWindow reports whether an entry's timestamp is within [From, To]. Zero
// bounds are open; entries with an unparseable timestamp are always included
// (they are usually boot/continuation context).
func inWindow(e Entry, from, to time.Time) bool {
	if e.Time.IsZero() {
		return true
	}
	if !from.IsZero() && e.Time.Before(from) {
		return false
	}
	if !to.IsZero() && e.Time.After(to) {
		return false
	}
	return true
}
