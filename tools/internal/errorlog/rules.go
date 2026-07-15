package errorlog

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

//go:embed rules/*.rules
var rulesFS embed.FS

type ruleKind int

const (
	kindSignal ruleKind = iota
	kindNoise
	kindSeverity
)

type rule struct {
	category string
	kind     ruleKind
	re       *regexp.Regexp
}

// RuleSet is the merged set of classification rules from one or more packs.
type RuleSet struct {
	rules []rule
}

var tabRe = regexp.MustCompile(`\t+`)

func parseKind(s string) (ruleKind, error) {
	switch s {
	case "signal":
		return kindSignal, nil
	case "noise":
		return kindNoise, nil
	case "severity":
		return kindSeverity, nil
	default:
		return 0, fmt.Errorf("unknown kind %q", s)
	}
}

func parseRuleLines(name, content string) ([]rule, error) {
	var out []rule
	for i, raw := range strings.Split(content, "\n") {
		line := strings.TrimRight(raw, "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		cols := tabRe.Split(strings.TrimSpace(line), 3)
		if len(cols) != 3 {
			return nil, fmt.Errorf("%s:%d: expected 3 TAB-separated columns, got %d", name, i+1, len(cols))
		}
		kind, err := parseKind(cols[1])
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", name, i+1, err)
		}
		re, err := regexp.Compile(cols[2])
		if err != nil {
			return nil, fmt.Errorf("%s:%d: bad regexp: %w", name, i+1, err)
		}
		out = append(out, rule{category: cols[0], kind: kind, re: re})
	}
	return out, nil
}

// LoadEmbeddedRules loads every embedded rules/*.rules pack except the advisory
// data files (advice.rules, checks.rules), which have their own loaders.
func LoadEmbeddedRules() (*RuleSet, error) {
	rs := &RuleSet{}
	entries, err := rulesFS.ReadDir("rules")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".rules") || name == "advice.rules" || name == "checks.rules" {
			continue
		}
		content, err := fs.ReadFile(rulesFS, "rules/"+name)
		if err != nil {
			return nil, err
		}
		rules, err := parseRuleLines(name, string(content))
		if err != nil {
			return nil, err
		}
		rs.rules = append(rs.rules, rules...)
	}
	return rs, nil
}

// LoadDir adds packs from a user-supplied directory (rules/*.rules), letting
// contributors extend or override the embedded ones without recompiling.
func (rs *RuleSet) LoadDir(dir string) error {
	matches, err := filepath.Glob(filepath.Join(dir, "*.rules"))
	if err != nil {
		return err
	}
	for _, path := range matches {
		base := filepath.Base(path)
		if base == "advice.rules" || base == "checks.rules" {
			continue
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rules, err := parseRuleLines(base, string(content))
		if err != nil {
			return err
		}
		rs.rules = append(rs.rules, rules...)
	}
	return nil
}

// Classify decides whether to keep an entry and, if dropped, its noise
// category. Precedence: severity >= threshold, then any signal, then noise,
// else keep (fail-safe for unknown entries).
func (rs *RuleSet) Classify(e Entry, minSeverity int) (keep bool, category string) {
	text := e.Text()
	for _, r := range rs.rules {
		if r.kind == kindSeverity {
			if m := r.re.FindStringSubmatch(text); len(m) > 1 {
				if n, err := strconv.Atoi(m[1]); err == nil && n >= minSeverity {
					return true, ""
				}
			}
		}
	}
	for _, r := range rs.rules {
		if r.kind == kindSignal && r.re.MatchString(text) {
			return true, ""
		}
	}
	for _, r := range rs.rules {
		if r.kind == kindNoise && r.re.MatchString(text) {
			return false, r.category
		}
	}
	return true, ""
}
