// Package model holds the types every goDTA phase shares: what collect reads,
// the candidates, the measures, the recommendations and the summary rows.
// They are written to the workspace as JSON, with snake_case keys.
//
// Every shared type of the plan is declared here at once (task 5): Go cannot
// declare a type in another package, and the later tasks use these without
// adding to them unless they say so.
package model

import (
	"slices"
	"strings"
	"time"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/conn"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/showplan"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/tsql"
)

// Sources of a workload.
const (
	SourceQueryStore = "query_store"
	SourcePlanCache  = "plan_cache"
)

// Workload is everything collect reads from one database.
type Workload struct {
	// CollectID identifies the collect run that wrote the workload: an
	// evaluation records it, and report refuses to score the measures of
	// another collection with this one's counters.
	CollectID string        `json:"collect_id,omitempty"`
	Server    conn.Instance `json:"server"`
	Database  DBInfo        `json:"database"`
	Window    Window        `json:"window"`
	// Measured is the period the Query Store counters cover: from the start
	// of the first runtime-stats interval summed to the end of the last. The
	// Query Store counts by interval and cannot split one, so an interval
	// that overlaps Window is counted whole and Measured may reach outside
	// Window. Nil from the plan cache.
	Measured *Window `json:"measured,omitempty"`
	// Source is SourceQueryStore or SourcePlanCache.
	Source string `json:"source"`
	// CaptureMode is the Query Store's query_capture_mode_desc, empty when
	// the Query Store is off.
	CaptureMode string `json:"capture_mode"`
	// Queries are the reads of the top N, Writes every INSERT, UPDATE,
	// DELETE and MERGE of the window.
	Queries []Query `json:"queries"`
	Writes  []Query `json:"writes"`
	// Tables is indexed by object_id: the tables the plans read or write,
	// the tables of the missing index groups, and their indexed views.
	Tables         map[int]*Table      `json:"tables"`
	ScopedConfigs  []tsql.ScopedConfig `json:"scoped_configs"`
	MissingIndexes []MissingIndexDMV   `json:"missing_indexes"`
	// Hypotheticals are the hypothetical indexes found in the database
	// (leftovers of the DTA or of goDTA), never in Table.Indexes.
	Hypotheticals []Index  `json:"hypotheticals"`
	Skipped       []string `json:"skipped"`
	Warnings      []string `json:"warnings"`
}

// DBInfo identifies a database.
type DBInfo struct {
	ID              int    `json:"id"`
	Name            string `json:"name"`
	Collation       string `json:"collation"`
	Compat          int    `json:"compat"`
	QueryStoreState string `json:"query_store_state"`
	IsClone         bool   `json:"is_clone"`
	// QueryStoreIntervalMinutes is the Query Store's interval_length_minutes,
	// the length of one runtime-stats interval; 0 when the Query Store is off.
	QueryStoreIntervalMinutes int `json:"query_store_interval_minutes,omitempty"`
	// PlanNames are the other names the database is known by in its plans:
	// a plan keeps the database name of its compilation, so the Query Store
	// of a database restored or renamed under another name still names the
	// old one. Collect records the name when it is the only other one under
	// which the plans name user objects (schemas other than sys and
	// INFORMATION_SCHEMA, outside the system databases), and every table they
	// name under it is in this database, whether or not the current name has
	// plans too.
	PlanNames []string `json:"plan_names,omitempty"`
}

// Matches reports whether name, a database name as a plan or a source gives
// it, is this database: its current name or one of its PlanNames. Database
// names follow the instance collation, which the workload does not hold, so
// they are compared without case. The empty name never matches: an
// unqualified reference is the caller's to decide.
func (d DBInfo) Matches(name string) bool {
	if name == "" {
		return false
	}
	if strings.EqualFold(name, d.Name) {
		return true
	}
	return slices.ContainsFunc(d.PlanNames, func(n string) bool { return strings.EqualFold(name, n) })
}

// Window is the collection window, both bounds explicit.
type Window struct {
	From  time.Time `json:"from"`
	Until time.Time `json:"until"`
}

// Query is one query of the workload. From the Query Store, QueryID is the
// query_id and the counters are totals over the window; from the plan cache,
// one Query groups the cached statements of a query_hash, QueryID is that
// hash as a bigint, QueryIDs lists the Query Store query_ids of the same hash
// (spec section 8.2), and the counters are cumulated since compilation.
type Query struct {
	QueryID           int64   `json:"query_id"`
	QueryIDs          []int64 `json:"query_ids,omitempty"`
	Hash              string  `json:"hash"`
	Text              string  `json:"text"`
	ObjectID          int64   `json:"object_id"`
	ContextSettingsID int64   `json:"context_settings_id"`
	SetOptions        int64   `json:"set_options"`
	DateFirst         int     `json:"date_first"`
	DateFormat        int     `json:"date_format"`
	Executions        int64   `json:"executions"`
	// CPUms and Duration are in milliseconds, Reads in logical reads, all
	// totals.
	CPUms       float64 `json:"cpu_ms"`
	Reads       float64 `json:"reads"`
	Duration    float64 `json:"duration"`
	AvgRowcount float64 `json:"avg_rowcount"`
	PlanXML     string  `json:"plan_xml"`
	IsWrite     bool    `json:"is_write"`
	// Plan is PlanXML parsed; not written, parsed again on reading.
	Plan *showplan.Plan `json:"-"`
}

// Table is a table or an indexed view.
type Table struct {
	ObjectID       int       `json:"object_id"`
	Schema         string    `json:"schema"`
	Name           string    `json:"name"`
	Rows           int64     `json:"rows"`
	DataPages      int64     `json:"data_pages"`
	IsHeap         bool      `json:"is_heap"`
	HasColumnstore bool      `json:"has_columnstore"`
	Columns        []Column  `json:"columns"`
	Indexes        []Index   `json:"indexes"`
	Stats          []Stat    `json:"stats"`
	ModifyDate     time.Time `json:"modify_date"`
	// Views lists the object_ids of the indexed views over this table.
	Views []int `json:"views,omitempty"`
}

// Column is one column of a table.
type Column struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	MaxLength int    `json:"max_length"`
	// AvgWidth comes from the density vector of a statistics object led by
	// the column when there is one, from max_length otherwise.
	AvgWidth float64 `json:"avg_width"`
	Nullable bool    `json:"nullable"`
	Computed bool    `json:"computed"`
}

// Index kinds.
const (
	KindClustered               = "clustered"
	KindNonclustered            = "nonclustered"
	KindHeap                    = "heap"
	KindClusteredColumnstore    = "clustered columnstore"
	KindNonclusteredColumnstore = "nonclustered columnstore"
	KindViewClustered           = "view clustered"
)

// Index is an index, real or hypothetical. Keys and Include hold column_ids.
type Index struct {
	ObjectID         int      `json:"object_id"`
	IndexID          int      `json:"index_id"`
	Name             string   `json:"name"`
	Kind             string   `json:"kind"`
	Keys             []KeyCol `json:"keys"`
	Include          []int    `json:"include"`
	Unique           bool     `json:"unique"`
	PrimaryKey       bool     `json:"primary_key"`
	UniqueConstraint bool     `json:"unique_constraint"`
	Disabled         bool     `json:"disabled"`
	Hypothetical     bool     `json:"hypothetical"`
	Filter           string   `json:"filter"`
	// DataPages is in_row_data_page_count summed over the partitions.
	DataPages   int64  `json:"data_pages"`
	Compression string `json:"compression"`
	FillFactor  int    `json:"fill_factor"`
	// DataSpace is the name of the index's filegroup or partition scheme;
	// PartitionColumn, the column_id of its partitioning column, is set only
	// on a partition scheme, which an ON clause names with that column.
	DataSpace       string `json:"data_space"`
	PartitionColumn int    `json:"partition_column,omitempty"`
	// NamedIn says where the index is named: "query_store_text",
	// "sql_module:<schema.object>", "plan_guide:<name>",
	// "forced_plan:<query_id>".
	NamedIn []string    `json:"named_in,omitempty"`
	Usage   *IndexUsage `json:"usage,omitempty"`
}

// IndexUsage is read from sys.dm_db_index_usage_stats and
// sys.dm_db_index_operational_stats, for information: the report shows it,
// the score never uses it.
type IndexUsage struct {
	Seeks       int64 `json:"seeks"`
	Scans       int64 `json:"scans"`
	Lookups     int64 `json:"lookups"`
	Updates     int64 `json:"updates"`
	LeafInserts int64 `json:"leaf_inserts"`
	LeafUpdates int64 `json:"leaf_updates"`
	LeafDeletes int64 `json:"leaf_deletes"`
}

// MissingIndexDMV is one group of sys.dm_db_missing_index_*; columns are
// column_ids.
type MissingIndexDMV struct {
	Table      int     `json:"table"`
	Equality   []int   `json:"equality"`
	Inequality []int   `json:"inequality"`
	Include    []int   `json:"include"`
	AvgImpact  float64 `json:"avg_impact"`
	UserSeeks  int64   `json:"user_seeks"`
}

// KeyCol is one key column of an index, by column_id.
type KeyCol struct {
	ColumnID int  `json:"column_id"`
	Desc     bool `json:"desc"`
}

// Stat is one statistics object; Columns are column_ids in order.
type Stat struct {
	StatsID             int    `json:"stats_id"`
	Name                string `json:"name"`
	Columns             []int  `json:"columns"`
	AutoCreated         bool   `json:"auto_created"`
	UserCreated         bool   `json:"user_created"`
	Rows                int64  `json:"rows"`
	RowsSampled         int64  `json:"rows_sampled"`
	ModificationCounter int64  `json:"modification_counter"`
}

// Candidate is a proposed index (task 7). EqCount is the number of leading
// keys that come from equality predicates, which the coverage model of spec
// section 8.3 may permute; Reason is the reason an AI suggestion gave.
type Candidate struct {
	ID       string   `json:"id"`
	Table    int      `json:"table"`
	Keys     []KeyCol `json:"keys"`
	EqCount  int      `json:"eq_count"`
	Include  []int    `json:"include"`
	Origin   string   `json:"origin"`
	Reason   string   `json:"reason,omitempty"`
	Rules    []string `json:"rules,omitempty"`
	Queries  []int64  `json:"queries"`
	Decision Decision `json:"decision"`
	Existing string   `json:"existing,omitempty"`
}

// Decision is what the confrontation with the existing indexes decided
// (task 8).
type Decision string

// The decisions, and no other.
const (
	DecisionCovered Decision = "covered"
	DecisionWiden   Decision = "widen"
	DecisionReplace Decision = "replace"
	DecisionDiscuss Decision = "discuss"
	DecisionNew     Decision = "new"
)

// Measure is the evaluation of one query against one candidate, or against
// a combination (Candidate empty, Combined filled) (task 10).
// UsedHypothetical names the hypothetical indexes the plan after reads, by
// candidate ID like Combined (not by index name): score matches it against
// Candidate.ID.
// A combined measure records no Maintenance (0 even for a write): score
// reads only its UsedHypothetical, its Status and its Notes, never its
// costs, so it is never compared with the measures of one candidate.
// Status is "measured" or one of the "not evaluated: ..." statuses.
type Measure struct {
	QueryID   int64    `json:"query_id"`
	Candidate string   `json:"candidate"`
	Combined  []string `json:"combined,omitempty"`
	Before    float64  `json:"before"`
	After     float64  `json:"after"`
	// BeforeRows and AfterRows are the estimated rows of the root operator
	// of the reference plan and of the plan with the candidate: what the
	// comparison of the lab with the source reads next to the costs.
	BeforeRows       float64  `json:"before_rows,omitempty"`
	AfterRows        float64  `json:"after_rows,omitempty"`
	Shape            string   `json:"shape"`
	UsedHypothetical []string `json:"used_hypothetical,omitempty"`
	Status           string   `json:"status"`
	Maintenance      float64  `json:"maintenance"`
	Notes            []string `json:"notes,omitempty"`
	Error            string   `json:"error,omitempty"`
}

// Recommendation is the scored outcome of one candidate (task 11). Decision
// is "recommended", "discuss" or "rejected".
type Recommendation struct {
	Candidate   Candidate `json:"candidate"`
	Gain        float64   `json:"gain"`
	Maintenance float64   `json:"maintenance"`
	// RowsWritten is executions x average rows of the writes of the
	// candidate's table that could not be measured. Rows, not cost units:
	// it is never added to Maintenance.
	RowsWritten float64   `json:"rows_written,omitempty"`
	SizeMB      float64   `json:"size_mb"`
	Decision    string    `json:"decision"`
	Formula     string    `json:"formula"`
	Queries     []Measure `json:"queries"`
	Notes       []string  `json:"notes,omitempty"`
}

// SummaryRow is one database of the summary across databases (task 12).
type SummaryRow struct {
	Server       string `json:"server"`
	Database     string `json:"database"`
	Status       string `json:"status"`
	Recommended  int    `json:"recommended"`
	Discuss      int    `json:"discuss"`
	Rejected     int    `json:"rejected"`
	NotEvaluated int    `json:"not_evaluated"`
	// Gain is the sum of the gains of the recommended candidates.
	Gain     float64  `json:"gain"`
	Warnings []string `json:"warnings,omitempty"`
}
