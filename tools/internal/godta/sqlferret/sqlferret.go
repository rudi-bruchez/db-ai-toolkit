// Package sqlferret reads the actual plans of a sqlferret project (spec
// section 8.2): every plans/profile/run_<id>/index.json and the .sqlplan
// files it names. Each run_<id> is a separate ingestion run, not a version
// of the previous one, so every run is read; a plan found in several runs is
// returned once. The plans query-store-import writes directly under plans/
// are never read: the Query Store is read directly by collect.
package sqlferret

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/showplan"
)

// Source is one distinct plan of a sqlferret project. File is its path
// relative to the project directory, with forward slashes. Database is read
// from the three-part names of the plan's column references, empty when the
// plan has none.
type Source struct {
	File     string
	Database string
	Plan     *showplan.Plan
}

// index is the part of index.json goDTA reads (sqlferret
// docs/execution-plans.md, schema_version 1).
type index struct {
	SchemaVersion int `json:"schema_version"`
	Plans         []struct {
		PlanHash string `json:"plan_hash"`
		Files    struct {
			First string `json:"first"`
			Worst string `json:"worst"`
		} `json:"files"`
	} `json:"plans"`
}

type run struct {
	id  int
	dir string
}

// Load reads the plans of every ingestion run of the sqlferret project in
// projectDir, in run order then index order. A plan is identified by its
// plan_hash (by its file name when the hash is empty); only its first
// occurrence file is read, its worst occurrence being the same plan. It is
// an error for projectDir to hold no run, for an index.json to be unreadable
// or of another schema version, and for a plan it names to be missing or
// unreadable.
func Load(projectDir string) ([]Source, error) {
	profile := filepath.Join(projectDir, "plans", "profile")
	entries, err := os.ReadDir(profile)
	if err != nil {
		return nil, fmt.Errorf("sqlferret project %s: %w", projectDir, err)
	}
	var runs []run
	for _, e := range entries {
		rest, ok := strings.CutPrefix(e.Name(), "run_")
		if !e.IsDir() || !ok {
			continue
		}
		id, err := strconv.Atoi(rest)
		if err != nil || id < 0 {
			continue
		}
		runs = append(runs, run{id, e.Name()})
	}
	if len(runs) == 0 {
		return nil, fmt.Errorf("sqlferret project %s: no plans/profile/run_<id> directory", projectDir)
	}
	slices.SortFunc(runs, func(a, b run) int { return cmp.Compare(a.id, b.id) })

	var out []Source
	seen := map[string]bool{}
	for _, r := range runs {
		rel := path.Join("plans", "profile", r.dir)
		data, err := os.ReadFile(filepath.Join(projectDir, filepath.FromSlash(rel), "index.json"))
		if errors.Is(err, os.ErrNotExist) {
			continue // a run without plans
		}
		if err != nil {
			return nil, err
		}
		var ix index
		if err := json.Unmarshal(data, &ix); err != nil {
			return nil, fmt.Errorf("%s/index.json: %w", rel, err)
		}
		if ix.SchemaVersion != 1 {
			return nil, fmt.Errorf("%s/index.json: schema_version %d, only 1 is read", rel, ix.SchemaVersion)
		}
		for _, p := range ix.Plans {
			name := cmp.Or(p.Files.First, p.Files.Worst)
			if name == "" || strings.ContainsAny(name, `/\`) {
				return nil, fmt.Errorf("%s/index.json: plan %q has no plan file in its directory", rel, p.PlanHash)
			}
			key := cmp.Or(p.PlanHash, name)
			if seen[key] {
				continue
			}
			file := path.Join(rel, name)
			raw, err := os.ReadFile(filepath.Join(projectDir, filepath.FromSlash(file)))
			if err != nil {
				return nil, err
			}
			plan, err := showplan.Parse(raw)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", file, err)
			}
			seen[key] = true
			out = append(out, Source{File: file, Database: database(plan), Plan: plan})
		}
	}
	return out, nil
}

// database returns the first database named by a column reference of the
// plan, then by an object, empty when there is none.
func database(p *showplan.Plan) string {
	var objs []showplan.ObjRef
	for _, st := range p.Statements {
		for _, a := range st.Accesses {
			for _, list := range [][]showplan.ColRef{a.SeekCols, a.PredEq, a.PredRange, a.Output} {
				for _, c := range list {
					if c.Database != "" {
						return c.Database
					}
				}
			}
			objs = append(objs, a.Obj)
		}
		for _, wr := range st.Writes {
			for _, c := range wr.SetCols {
				if c.Database != "" {
					return c.Database
				}
			}
			objs = append(objs, wr.Objs...)
		}
	}
	for _, o := range objs {
		if o.Database != "" {
			return o.Database
		}
	}
	return ""
}
