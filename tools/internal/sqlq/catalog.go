package sqlq

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type Source string

const (
	SourceBundled     Source = "bundled"
	SourcePersonal    Source = "personal"
	SourceTsqlScripts Source = "tsql-scripts"
)

var queryName = regexp.MustCompile(`^[a-z][a-z0-9-]{1,48}$`)
var profileSegment = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// ValidQueryName checks a name before it is ever turned into a path.
func ValidQueryName(name string) bool { return queryName.MatchString(name) }

// DefaultQueriesDir is the personal layer's root.
func DefaultQueriesDir() string {
	return filepath.Join(filepath.Dir(DefaultProfilePath()), "queries")
}

// CatalogParam is what the catalogue publishes about a parameter. The default is
// deliberately absent: tsql-scripts defaults include local paths and SQL.
type CatalogParam struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
}

type Entry struct {
	Name, Summary string
	Params        []CatalogParam
	Scope         string
	Source        Source
	Path          string
	Verified      *Verified
	DirtyReads    bool
	Heavy         bool
	Rejected      string

	SQL         string          // the bytes read, BOM stripped: what -saved executes
	Hash        string          // registry key
	Overrides   []OverrideParam // tsql-scripts only
	QueryParams []string        // bundled and personal only
}

func (e Entry) MarshalJSON() ([]byte, error) {
	if e.Rejected != "" {
		return json.Marshal(struct {
			Name     string `json:"name"`
			Source   Source `json:"source"`
			Path     string `json:"path"`
			Rejected string `json:"rejected"`
		}{e.Name, e.Source, e.Path, e.Rejected})
	}
	params := e.Params
	if params == nil {
		params = []CatalogParam{}
	}
	return json.Marshal(struct {
		Name       string         `json:"name"`
		Summary    string         `json:"summary"`
		Params     []CatalogParam `json:"params"`
		Scope      string         `json:"scope"`
		Source     Source         `json:"source"`
		Path       string         `json:"path"`
		Verified   *Verified      `json:"verified"`
		DirtyReads bool           `json:"dirty_reads"`
		Heavy      bool           `json:"heavy"`
	}{e.Name, e.Summary, params, e.Scope, e.Source, e.Path, e.Verified, e.DirtyReads, e.Heavy})
}

type CatalogConfig struct {
	BundledDir, PersonalDir, TsqlScriptsDir string
	Profile                                 string   // "" for the profile-free view
	ProfileNames                            []string // every configured profile, for case twins
	Registry                                Registry
}

type Catalog struct {
	Entries  []Entry  `json:"queries"`
	Messages []string `json:"messages"`
}

func (c Catalog) MarshalJSON() ([]byte, error) {
	type alias Catalog
	out := alias(c)
	if out.Entries == nil {
		out.Entries = []Entry{}
	}
	if out.Messages == nil {
		out.Messages = []string{}
	}
	return json.Marshal(out)
}

// Find returns the valid entry of that name in the view.
func (c Catalog) Find(name string) (Entry, bool) {
	for _, e := range c.Entries {
		if e.Name == name && e.Rejected == "" {
			return e, true
		}
	}
	return Entry{}, false
}

// ProfileDir maps a profile name to its personal directory. The names
// registered-servers generates carry "/" for SSMS groups, which become
// subdirectories; anything that could leave the directory, or share it with
// another profile on a case-insensitive file system, is refused.
func ProfileDir(personalDir, profile string, all []string) (string, error) {
	lower := strings.ToLower(profile)
	segs := strings.Split(lower, "/")
	for _, s := range segs {
		if s == "." || s == ".." || !profileSegment.MatchString(s) {
			return "", fmt.Errorf("profile %q cannot name a directory (segment %q)", profile, s)
		}
	}
	for _, other := range all {
		if other != profile && strings.EqualFold(other, profile) {
			return "", fmt.Errorf("profiles %q and %q differ only by case and would share a directory", profile, other)
		}
	}
	return filepath.Join(append([]string{personalDir, "profiles"}, segs...)...), nil
}

// LoadCatalog reads every source of the view and resolves name collisions.
func LoadCatalog(cfg CatalogConfig) Catalog {
	var c Catalog
	if cfg.BundledDir != "" {
		if st, err := os.Stat(cfg.BundledDir); err != nil || !st.IsDir() {
			// Without this, a binary built outside the plugin lists no canon and
			// the agent concludes there is none.
			c.Messages = append(c.Messages, "bundled queries not found")
		} else {
			c.Entries = append(c.Entries, loadBlockDir(cfg, cfg.BundledDir, "", SourceBundled, "generic")...)
		}
	}
	if cfg.PersonalDir != "" {
		c.Entries = append(c.Entries, loadBlockDir(cfg, filepath.Join(cfg.PersonalDir, "_generic"), "_generic", SourcePersonal, "generic")...)
		if cfg.Profile != "" {
			dir, err := ProfileDir(cfg.PersonalDir, cfg.Profile, cfg.ProfileNames)
			if err != nil {
				c.Messages = append(c.Messages, "personal queries disabled for this profile: "+err.Error())
			} else {
				rel, _ := filepath.Rel(cfg.PersonalDir, dir)
				c.Entries = append(c.Entries, loadBlockDir(cfg, dir, filepath.ToSlash(rel), SourcePersonal, cfg.Profile)...)
			}
		}
	}
	switch {
	case cfg.TsqlScriptsDir == "":
		c.Messages = append(c.Messages, "tsql-scripts source not configured")
	default:
		if st, err := os.Stat(cfg.TsqlScriptsDir); err != nil || !st.IsDir() {
			c.Messages = append(c.Messages, "tsql-scripts source not found")
		} else {
			c.Entries = append(c.Entries, loadTsqlScripts(cfg)...)
		}
	}
	resolveCollisions(c.Entries)
	return c
}

func loadBlockDir(cfg CatalogConfig, dir, relPrefix string, src Source, scope string) []Entry {
	paths, _ := filepath.Glob(filepath.Join(dir, "*.sql"))
	sort.Strings(paths)
	var out []Entry
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		rel := filepath.Base(p)
		if relPrefix != "" {
			rel = relPrefix + "/" + rel
		}
		e := Entry{Name: strings.TrimSuffix(filepath.Base(p), ".sql"), Source: src, Path: rel, Scope: scope}
		if err != nil {
			e.Rejected = "unreadable"
			out = append(out, e)
			continue
		}
		body := StripBOM(raw)
		e.SQL, e.Hash = string(body), ContentHash(body)
		e.Rejected = blockEntryProblem(&e)
		e.Verified = cfg.Registry.Lookup(e.Hash)
		out = append(out, e)
	}
	return out
}

func blockEntryProblem(e *Entry) string {
	if !ValidQueryName(e.Name) {
		return "invalid name"
	}
	h, err := ParseBlockHeader(e.SQL)
	if err != nil {
		return err.Error()
	}
	e.Summary, e.Heavy = h.Summary, h.Heavy
	e.QueryParams = QueryParams(e.SQL)
	if h.HasParamsLine && strings.Join(h.Params, ",") != strings.Join(e.QueryParams, ",") {
		return fmt.Sprintf("Parameters line names %v but the SQL references %v", h.Params, e.QueryParams)
	}
	for _, p := range e.QueryParams {
		e.Params = append(e.Params, CatalogParam{Name: p})
	}
	if r := Refusals(e.SQL); len(r) > 0 {
		return r[0].Reason()
	}
	e.DirtyReads = DirtyReads(e.SQL)
	return ""
}

func loadTsqlScripts(cfg CatalogConfig) []Entry {
	var out []Entry
	root := cfg.TsqlScriptsDir
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(p), ".sql") {
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		body := string(StripBOM(raw))
		h, found, herr := ParseMarkedHeader(body)
		if !found {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		e := Entry{Name: h.Marker.Name, Source: SourceTsqlScripts, Path: filepath.ToSlash(rel), Scope: "generic", SQL: body}
		e.Hash = ContentHash([]byte(withoutLine(body, h.Marker.Line)))
		switch {
		case herr != nil:
			e.Rejected = herr.Error()
		case !ValidQueryName(e.Name):
			e.Rejected = "invalid name"
		default:
			e.Rejected = tsqlEntryProblem(&e, h)
		}
		e.Verified = cfg.Registry.Lookup(e.Hash)
		out = append(out, e)
		return nil
	})
	return out
}

func tsqlEntryProblem(e *Entry, h MarkedHeader) string {
	e.Summary, e.Heavy = h.Summary, h.Marker.Heavy
	if r := Refusals(e.SQL); len(r) > 0 {
		return r[0].Reason()
	}
	ps, err := AnalyseOverrides(e.SQL, h.Marker.Params)
	if err != nil {
		return err.Error()
	}
	e.Overrides = ps
	for _, p := range ps {
		e.Params = append(e.Params, CatalogParam{Name: p.Name, Type: p.Type.String()})
	}
	e.DirtyReads = DirtyReads(e.SQL)
	return ""
}

// withoutLine drops one 1-based line, so editing the marker does not change
// the hash of the SQL it describes. Line 0 (no marker) returns the text as is.
func withoutLine(text string, line int) string {
	if line <= 0 {
		return text
	}
	lines := strings.SplitAfter(text, "\n")
	if line > len(lines) {
		return text
	}
	return strings.Join(append(lines[:line-1:line-1], lines[line:]...), "")
}

// resolveCollisions applies the rule of the spec's section 7: bundled keeps its
// name, anything else claiming it is rejected; among non-bundled sources, every
// holder of a duplicated name is rejected.
func resolveCollisions(entries []Entry) {
	holders := map[string][]int{}
	for i, e := range entries {
		if e.Name != "" {
			holders[e.Name] = append(holders[e.Name], i)
		}
	}
	for name, idx := range holders {
		if len(idx) < 2 {
			continue
		}
		bundled := -1
		for _, i := range idx {
			if entries[i].Source == SourceBundled && entries[i].Rejected == "" {
				bundled = i
			}
		}
		for _, i := range idx {
			switch {
			case entries[i].Rejected != "":
				// Keep the first reason: it is the one the author must fix.
			case i == bundled:
			case bundled >= 0:
				entries[i].Rejected = fmt.Sprintf("name %q is taken by bundled", name)
			default:
				entries[i].Rejected = fmt.Sprintf("name %q is defined more than once", name)
			}
		}
	}
}
