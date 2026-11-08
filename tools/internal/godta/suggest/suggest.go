// Package suggest reads the index suggestions an AI wrote for goDTA (spec
// section 9). A suggestion is a structure, never SQL text: the file is
// refused when it holds a field this package does not know, such as "sql",
// and the DDL is generated later from the structure, like any candidate's.
//
// The format, documented in the index-advisor skill:
//
//	{"suggestions": [{"table": "dbo.godta_t",
//	  "keys": [{"column": "a"}, {"column": "b", "desc": true}],
//	  "include": ["c"], "reason": "..."}]}
package suggest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/candidate"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
)

type file struct {
	Suggestions []suggestion `json:"suggestions"`
}

type suggestion struct {
	Table   string   `json:"table"`
	Keys    []key    `json:"keys"`
	Include []string `json:"include"`
	Reason  string   `json:"reason"`
}

type key struct {
	Column string `json:"column"`
	Desc   bool   `json:"desc"`
}

// Load reads the suggestions file at path and turns each suggestion into a
// candidate of Origin "ai" on the tables of w, its Reason kept, its queries
// bound by candidate.BindQueries. Names are resolved against the catalog
// collect read, as the database collation compares them. A suggestion whose
// table or one of whose columns does not exist, or that cannot be an index,
// is rejected: the second result gives each rejection with its reason, and
// the other suggestions are kept. The error is for a file that cannot be
// read or is not exactly the format above.
//
// EqCount is 0: the file does not say which keys are equalities, so the
// coverage model compares the keys of a suggestion in their order.
func Load(path string, w *model.Workload) ([]model.Candidate, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var f file
	if err := dec.Decode(&f); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, nil, fmt.Errorf("%s: data after the suggestions object", path)
	}

	var cands []model.Candidate
	var rejected []string
	from := map[string]int{} // candidate ID to suggestion number
	for i, s := range f.Suggestions {
		label := fmt.Sprintf("suggestion %d (%s)", i+1, s.Table)
		c, why := build(s, w)
		if why == "" {
			if j, dup := from[c.ID]; dup {
				why = fmt.Sprintf("same index as suggestion %d", j)
			}
		}
		if why != "" {
			rejected = append(rejected, label+": "+why)
			continue
		}
		from[c.ID] = i + 1
		cands = append(cands, c)
	}
	return candidate.BindQueries(cands, w), rejected, nil
}

// build checks one suggestion and returns its candidate, or the reason it is
// rejected.
func build(s suggestion, w *model.Workload) (model.Candidate, string) {
	var c model.Candidate
	parts, err := splitName(s.Table)
	if err != nil {
		return c, err.Error()
	}
	if len(parts) != 2 {
		return c, "table must be named schema.table"
	}
	t := candidate.TableByName(w, parts[0], parts[1])
	switch {
	case t == nil:
		return c, "table not found"
	case t.HasColumnstore:
		return c, candidate.RuleColumnstore
	case slices.ContainsFunc(t.Indexes, func(ix model.Index) bool { return ix.Kind == model.KindViewClustered }):
		return c, "indexed view: not evaluated"
	}
	if len(s.Keys) == 0 {
		return c, "no key column"
	}
	column := func(name string) (int, string) {
		id := candidate.ColumnByName(w, t, name)
		if id == 0 {
			return 0, fmt.Sprintf("column %q not found", name)
		}
		return id, ""
	}
	for _, k := range s.Keys {
		id, why := column(k.Column)
		if why != "" {
			return c, why
		}
		if slices.ContainsFunc(c.Keys, func(o model.KeyCol) bool { return o.ColumnID == id }) {
			return c, fmt.Sprintf("column %q twice in the keys", k.Column)
		}
		c.Keys = append(c.Keys, model.KeyCol{ColumnID: id, Desc: k.Desc})
	}
	var ck []int
	for _, ix := range t.Indexes {
		if ix.Kind == model.KindClustered {
			for _, k := range ix.Keys {
				ck = append(ck, k.ColumnID)
			}
		}
	}
	for _, name := range s.Include {
		id, why := column(name)
		if why != "" {
			return c, why
		}
		switch {
		case slices.ContainsFunc(c.Keys, func(o model.KeyCol) bool { return o.ColumnID == id }):
		case slices.Contains(ck, id):
			// In every nonclustered index already; naming it again is error 1910.
			if !slices.Contains(c.Rules, candidate.RuleClusteredKeyImplicit) {
				c.Rules = append(c.Rules, candidate.RuleClusteredKeyImplicit)
			}
		case !slices.Contains(c.Include, id):
			c.Include = append(c.Include, id)
		}
	}
	slices.Sort(c.Include)
	c.Table = t.ObjectID
	c.Origin = candidate.OriginAI
	c.Reason = s.Reason
	c.ID = candidate.ID(c)
	return c, ""
}

// splitName splits a multi-part name, each part bare or in brackets with ]
// doubled inside, as QUOTENAME writes it.
func splitName(s string) ([]string, error) {
	var parts []string
	for rest := strings.TrimSpace(s); ; {
		var part string
		if strings.HasPrefix(rest, "[") {
			var b strings.Builder
			i := 1
			for ; i < len(rest); i++ {
				if rest[i] == ']' {
					if i+1 < len(rest) && rest[i+1] == ']' {
						b.WriteByte(']')
						i++
						continue
					}
					break
				}
				b.WriteByte(rest[i])
			}
			if i >= len(rest) {
				return nil, fmt.Errorf("unclosed bracket in table name %q", s)
			}
			part, rest = b.String(), rest[i+1:]
		} else {
			end := strings.IndexByte(rest, '.')
			if end < 0 {
				end = len(rest)
			}
			part, rest = rest[:end], rest[end:]
		}
		if part == "" {
			return nil, fmt.Errorf("empty part in table name %q", s)
		}
		parts = append(parts, part)
		if rest == "" {
			return parts, nil
		}
		if rest[0] != '.' {
			return nil, fmt.Errorf("malformed table name %q", s)
		}
		rest = rest[1:]
	}
}
