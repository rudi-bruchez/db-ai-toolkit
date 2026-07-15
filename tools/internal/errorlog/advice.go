package errorlog

import (
	"fmt"
	"io/fs"
	"regexp"
	"strconv"
	"strings"
)

// Advisory is a single actionable recommendation surfaced to the user, either
// because a noise category crossed a volume threshold or because a boot-state
// pattern (e.g. Instant File Initialization disabled) was detected.
type Advisory struct {
	ID      string
	Message string
	URL     string
}

type countAdvice struct {
	category string
	minCount int
	template string
	url      string
}

type bootCheck struct {
	id      string
	re      *regexp.Regexp
	message string
	url     string
}

// Advisor holds the two advisory sources: category-volume and boot-state.
type Advisor struct {
	counts []countAdvice
	checks []bootCheck
}

// LoadEmbeddedAdvisories reads advice.rules and checks.rules from the embedded
// rules directory.
func LoadEmbeddedAdvisories() (*Advisor, error) {
	a := &Advisor{}
	adviceRaw, err := fs.ReadFile(rulesFS, "rules/advice.rules")
	if err != nil {
		return nil, err
	}
	if err := a.parseAdvice(string(adviceRaw)); err != nil {
		return nil, err
	}
	checksRaw, err := fs.ReadFile(rulesFS, "rules/checks.rules")
	if err != nil {
		return nil, err
	}
	if err := a.parseChecks(string(checksRaw)); err != nil {
		return nil, err
	}
	return a, nil
}

func dataLines(content string) []string {
	var out []string
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimRight(raw, "\r")
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		out = append(out, t)
	}
	return out
}

func (a *Advisor) parseAdvice(content string) error {
	for i, line := range dataLines(content) {
		cols := tabRe.Split(line, 4)
		if len(cols) != 4 {
			return fmt.Errorf("advice.rules:%d: expected 4 columns, got %d", i+1, len(cols))
		}
		n, err := strconv.Atoi(cols[1])
		if err != nil {
			return fmt.Errorf("advice.rules:%d: bad min_count: %w", i+1, err)
		}
		a.counts = append(a.counts, countAdvice{cols[0], n, cols[2], cols[3]})
	}
	return nil
}

func (a *Advisor) parseChecks(content string) error {
	for i, line := range dataLines(content) {
		cols := tabRe.Split(line, 4)
		if len(cols) != 4 {
			return fmt.Errorf("checks.rules:%d: expected 4 columns, got %d", i+1, len(cols))
		}
		re, err := regexp.Compile(cols[1])
		if err != nil {
			return fmt.Errorf("checks.rules:%d: bad regexp: %w", i+1, err)
		}
		a.checks = append(a.checks, bootCheck{cols[0], re, cols[2], cols[3]})
	}
	return nil
}

// Evaluate returns advisories: category-volume ones whose dropped count reaches
// the threshold, plus boot checks whose pattern is present in bootText.
func (a *Advisor) Evaluate(droppedByCat map[string]int, bootText string) []Advisory {
	var out []Advisory
	for _, c := range a.counts {
		if n := droppedByCat[c.category]; n >= c.minCount {
			msg := strings.ReplaceAll(c.template, "{count}", strconv.Itoa(n))
			out = append(out, Advisory{ID: c.category, Message: msg, URL: c.url})
		}
	}
	for _, ch := range a.checks {
		if ch.re.MatchString(bootText) {
			out = append(out, Advisory{ID: ch.id, Message: ch.message, URL: ch.url})
		}
	}
	return out
}
