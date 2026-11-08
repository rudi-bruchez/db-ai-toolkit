package collect

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/tsql"
)

// CatalogDrift compares the real indexes of the tables of w, read live
// through db by the same query collect uses, with those collect.json holds,
// and names each difference: an index created, dropped or changed (keys,
// included columns, uniqueness, constraint, filter, disabled state) since
// collect. None means the catalog of the workload is still the live one.
// The candidates were confronted with the collected indexes: an index
// created since would not mark a candidate it covers (spec section 8.3).
func CatalogDrift(ctx context.Context, db *sql.DB, w *model.Workload) ([]string, error) {
	if len(w.Tables) == 0 {
		return nil, nil
	}
	ids := make([]int, 0, len(w.Tables))
	for id := range w.Tables {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	live, err := readIndexes(ctx, db, "i.object_id IN ("+idList(ids)+") AND i.is_hypothetical = 0")
	if err != nil {
		return nil, err
	}
	now := map[int][]model.Index{}
	for _, ix := range live {
		now[ix.ObjectID] = append(now[ix.ObjectID], ix)
	}
	var diffs []string
	for _, id := range ids {
		t := w.Tables[id]
		label := tsql.Obj{Schema: t.Schema, Name: t.Name}.Quoted()
		was := map[int]model.Index{}
		for _, ix := range t.Indexes {
			was[ix.IndexID] = ix
		}
		is := map[int]model.Index{}
		for _, ix := range now[id] {
			is[ix.IndexID] = ix
		}
		for _, ix := range now[id] {
			old, ok := was[ix.IndexID]
			switch {
			case !ok:
				diffs = append(diffs, fmt.Sprintf("%s: index %s created", label, indexLabel(ix)))
			case indexDefinition(old) != indexDefinition(ix):
				diffs = append(diffs, fmt.Sprintf("%s: index %s changed", label, indexLabel(ix)))
			}
		}
		for _, ix := range t.Indexes {
			if _, ok := is[ix.IndexID]; !ok {
				diffs = append(diffs, fmt.Sprintf("%s: index %s dropped", label, indexLabel(ix)))
			}
		}
	}
	return diffs, nil
}

func indexLabel(ix model.Index) string {
	if ix.Name == "" {
		return fmt.Sprintf("%d (%s)", ix.IndexID, ix.Kind)
	}
	return tsql.QuoteName(ix.Name)
}

// indexDefinition is what the confrontation and the measure depend on in an
// index: not its size, usage or physical options. A clustered index of an
// indexed view is read as clustered and stored as view clustered.
func indexDefinition(ix model.Index) string {
	kind := ix.Kind
	if kind == model.KindViewClustered {
		kind = model.KindClustered
	}
	inc := slices.Clone(ix.Include)
	slices.Sort(inc)
	return fmt.Sprintf("%s|%s|%v|%v|%t|%t|%t|%t|%s", ix.Name, kind, ix.Keys, inc,
		ix.Unique, ix.PrimaryKey, ix.UniqueConstraint, ix.Disabled, strings.TrimSpace(ix.Filter))
}
