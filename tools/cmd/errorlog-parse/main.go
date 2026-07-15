// Command errorlog-parse reads one or more SQL Server ERRORLOG files and writes
// a compact digest to stdout, dropping routine noise so an AI skill spends
// tokens only on entries that matter.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/errorlog"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "errorlog-parse:", err)
		os.Exit(1)
	}
}

func run() error {
	sev := flag.Int("severity", 16, "keep entries with Severity >= n")
	format := flag.String("format", "text", "output format: text or md")
	redact := flag.Bool("redact", false, "pseudonymize databases, logins and IPs")
	noLegend := flag.Bool("no-legend", false, "with -redact, hide the mapping legend")
	noAggregate := flag.Bool("no-aggregate", false, "do not collapse repeated entries")
	noSummary := flag.Bool("no-summary", false, "omit the instance summary")
	rulesDir := flag.String("rules", "", "extra directory of *.rules packs")
	fromStr := flag.String("from", "", `keep entries >= datetime, e.g. 2006-01-02 or "2006-01-02 15:04[:05]"`)
	toStr := flag.String("to", "", "keep entries <= datetime (same formats as -from)")
	flag.Parse()

	if flag.NArg() != 1 {
		flag.Usage()
		return fmt.Errorf("expected exactly one <path> argument")
	}

	from, err := parseWhen(*fromStr)
	if err != nil {
		return fmt.Errorf("bad -from: %w", err)
	}
	to, err := parseWhen(*toStr)
	if err != nil {
		return fmt.Errorf("bad -to: %w", err)
	}

	sources, err := errorlog.Acquire(flag.Arg(0))
	if err != nil {
		return err
	}

	out, err := errorlog.Process(sources, errorlog.Options{
		MinSeverity: *sev,
		Format:      *format,
		Redact:      *redact,
		ShowLegend:  *redact && !*noLegend,
		Aggregate:   !*noAggregate,
		ShowSummary: !*noSummary,
		From:        from,
		To:          to,
		RulesDir:    *rulesDir,
	})
	if err != nil {
		return err
	}
	fmt.Print(out)
	return nil
}

// parseWhen accepts an empty string (zero time), a date (2006-01-02), or a
// datetime at minute or second precision with a space or 'T' separator, e.g.
// "2006-01-02 15:04" or "2006-01-02T15:04:05". A trailing fractional second is
// tolerated, so an ERRORLOG timestamp ("2006-01-02 15:04:05.99") pastes in as
// given.
func parseWhen(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{
		"2006-01-02",
		"2006-01-02 15:04",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04",
		"2006-01-02T15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized datetime %q", s)
}
