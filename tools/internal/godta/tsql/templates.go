package tsql

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Obj is a schema-qualified object name, unquoted.
type Obj struct {
	Schema, Name string
}

// Quoted returns [schema].[name]. Templates stay with two-part names, the
// database coming from the connection's context (plan, global constraints).
func (o Obj) Quoted() string {
	return QuoteName(o.Schema) + "." + QuoteName(o.Name)
}

// KeyCol is one key column of an index.
type KeyCol struct {
	Name string
	Desc bool
}

// IndexDef describes a rowstore nonclustered index.
//
// Filter and OnTarget are T-SQL fragments written as they are, not names:
// Filter is a filter predicate as sys.indexes.filter_definition gives it, and
// OnTarget the target of the ON clause, built by the caller with QuoteName (a
// filegroup, [PRIMARY], or a partition scheme and its column, [ps]([c])).
// DataCompression is NONE, ROW or PAGE (empty means the default); FillFactor 0
// means the default.
type IndexDef struct {
	Table           Obj
	Name            string
	Keys            []KeyCol
	Include         []string
	Unique          bool
	Filter          string
	DataCompression string
	FillFactor      int
	OnTarget        string
}

// indexHead writes CREATE [UNIQUE] NONCLUSTERED INDEX ... ON t (keys)
// [INCLUDE (...)] [WHERE ...].
func indexHead(b *strings.Builder, ix IndexDef) {
	b.WriteString("CREATE ")
	if ix.Unique {
		b.WriteString("UNIQUE ")
	}
	fmt.Fprintf(b, "NONCLUSTERED INDEX %s ON %s (", QuoteName(ix.Name), ix.Table.Quoted())
	for i, k := range ix.Keys {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(QuoteName(k.Name))
		if k.Desc {
			b.WriteString(" DESC")
		} else {
			b.WriteString(" ASC")
		}
	}
	b.WriteString(")")
	if len(ix.Include) > 0 {
		fmt.Fprintf(b, " INCLUDE (%s)", quoteList(ix.Include))
	}
	if ix.Filter != "" {
		b.WriteString(" WHERE " + ix.Filter)
	}
}

func quoteList(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = QuoteName(n)
	}
	return strings.Join(q, ", ")
}

// CreateHypothetical creates a hypothetical index. STATISTICS_ONLY = -1 is
// required: without a value the index has no statistics and the estimate is
// wrong (spec section 8.5). Physical options do not apply.
func CreateHypothetical(ix IndexDef) string {
	var b strings.Builder
	indexHead(&b, ix)
	b.WriteString(" WITH STATISTICS_ONLY = -1")
	return b.String()
}

// CreateIndex creates the real index, for indexes.sql, with its physical
// options.
func CreateIndex(ix IndexDef) string {
	return createIndex(ix, false)
}

// CreateIndexDropExisting rebuilds an existing index of the same name with the
// definition ix (widening an existing index, spec section 8.3), keeping the
// physical options ix carries.
func CreateIndexDropExisting(ix IndexDef) string {
	return createIndex(ix, true)
}

func createIndex(ix IndexDef, dropExisting bool) string {
	var b strings.Builder
	indexHead(&b, ix)
	var opts []string
	if dropExisting {
		opts = append(opts, "DROP_EXISTING = ON")
	}
	switch c := strings.ToUpper(ix.DataCompression); c {
	case "", "NONE":
	case "ROW", "PAGE":
		opts = append(opts, "DATA_COMPRESSION = "+c)
	default:
		panic(fmt.Sprintf("tsql: unsupported data compression %q", ix.DataCompression))
	}
	if ix.FillFactor < 0 || ix.FillFactor > 100 {
		panic(fmt.Sprintf("tsql: fill factor %d out of range", ix.FillFactor))
	}
	if ix.FillFactor > 0 {
		opts = append(opts, "FILLFACTOR = "+strconv.Itoa(ix.FillFactor))
	}
	if len(opts) > 0 {
		b.WriteString(" WITH (" + strings.Join(opts, ", ") + ")")
	}
	if ix.OnTarget != "" {
		b.WriteString(" ON " + ix.OnTarget)
	}
	return b.String()
}

// DropIndex drops index name of table t.
func DropIndex(t Obj, name string) string {
	return fmt.Sprintf("DROP INDEX %s ON %s", QuoteName(name), t.Quoted())
}

// CreateStatistics creates statistics on cols, in order, with the engine's
// default sampling.
func CreateStatistics(t Obj, name string, cols []string) string {
	return fmt.Sprintf("CREATE STATISTICS %s ON %s (%s)", QuoteName(name), t.Quoted(), quoteList(cols))
}

// DropStatistics drops statistics name of table t.
func DropStatistics(t Obj, name string) string {
	return fmt.Sprintf("DROP STATISTICS %s.%s", t.Quoted(), QuoteName(name))
}

// UpdateStatisticsStream sets the statistics of target (an index or a
// statistics object of t) from a stream DBCC SHOW_STATISTICS ... WITH
// STATS_STREAM returned, written as an upper-case 0x literal.
func UpdateStatisticsStream(t Obj, target string, stream []byte) string {
	return fmt.Sprintf("UPDATE STATISTICS %s(%s) WITH STATS_STREAM = 0x%X", t.Quoted(), QuoteName(target), stream)
}

// ShowStatisticsStream reads the stream of statistics name of t. The table
// goes as a string and the statistics as an identifier: [dbo].[t] as first
// argument gives error 102, and a bracketed name inside a string as second
// argument error 2767 (measured).
func ShowStatisticsStream(t Obj, name string) string {
	return fmt.Sprintf("DBCC SHOW_STATISTICS(%s, %s) WITH STATS_STREAM", QuoteString(t.Quoted()), QuoteName(name))
}

// AutopilotReset empties the session's AUTOPILOT list for database dbID.
func AutopilotReset(dbID int) string {
	return fmt.Sprintf("DBCC AUTOPILOT(5, %d, 0, 0, 0, 0, 0)", dbID)
}

// AutopilotIndex declares an index to the session. pages 0 lets the engine
// take the real size; a hypothetical index needs its estimated data pages.
func AutopilotIndex(dbID, objectID, indexID, pages int) string {
	return fmt.Sprintf("DBCC AUTOPILOT(0, %d, %d, %d, %d)", dbID, objectID, indexID, pages)
}

// AutopilotView declares an indexed view (type 6; type 0 leaves it invisible).
func AutopilotView(dbID, viewID int) string {
	return fmt.Sprintf("DBCC AUTOPILOT(6, %d, %d, 1, 0)", dbID, viewID)
}

// SetLockTimeout sets the session's lock timeout in milliseconds.
func SetLockTimeout(ms int) string {
	return "SET LOCK_TIMEOUT " + strconv.Itoa(ms)
}

// CloneDatabase clones src into dst, statistics included, without the Query
// Store.
func CloneDatabase(src, dst string) string {
	return fmt.Sprintf("DBCC CLONEDATABASE(%s, %s) WITH NO_QUERYSTORE, NO_INFOMSGS", QuoteName(src), QuoteName(dst))
}

// BackupDatabase backs db up to path, overwriting, outside the backup chain.
// compress is false on Express, which refuses COMPRESSION.
func BackupDatabase(db, path string, compress bool) string {
	s := fmt.Sprintf("BACKUP DATABASE %s TO DISK = %s WITH INIT, COPY_ONLY", QuoteName(db), QuoteString(path))
	if compress {
		s += ", COMPRESSION"
	}
	return s
}

// ReadFile reads a file the server can see as one varbinary(max) column,
// BulkColumn.
func ReadFile(path string) string {
	return fmt.Sprintf("SELECT BulkColumn FROM OPENROWSET(BULK %s, SINGLE_BLOB) AS x", QuoteString(path))
}

// DeleteBackupFile deletes one backup file through the server (spec section
// 7.2: xp_delete_file, measured on 2016 to 2025).
func DeleteBackupFile(path string) string {
	return "EXEC master.dbo.xp_delete_file 0, " + QuoteString(path)
}

// DropDatabase drops db.
func DropDatabase(db string) string {
	return "DROP DATABASE " + QuoteName(db)
}

// RestoreFileList lists the logical files of a backup.
func RestoreFileList(path string) string {
	return "RESTORE FILELISTONLY FROM DISK = " + QuoteString(path)
}

// RestoreDatabase restores db from path, moving each logical file (key) to a
// physical path (value). The moves are written sorted by logical name, so the
// statement is the same from one run to the next.
func RestoreDatabase(db, path string, moves map[string]string) string {
	s := fmt.Sprintf("RESTORE DATABASE %s FROM DISK = %s", QuoteName(db), QuoteString(path))
	logical := make([]string, 0, len(moves))
	for l := range moves {
		logical = append(logical, l)
	}
	slices.Sort(logical)
	for i, l := range logical {
		if i == 0 {
			s += " WITH "
		} else {
			s += ", "
		}
		s += fmt.Sprintf("MOVE %s TO %s", QuoteString(l), QuoteString(moves[l]))
	}
	return s
}

// SetReadWrite makes db writable (a clone is restored read-only).
func SetReadWrite(db string) string {
	return fmt.Sprintf("ALTER DATABASE %s SET READ_WRITE", QuoteName(db))
}

// SetAutoStatsOff turns off automatic creation and update of statistics on
// db, two statements.
func SetAutoStatsOff(db string) []string {
	q := QuoteName(db)
	return []string{
		fmt.Sprintf("ALTER DATABASE %s SET AUTO_CREATE_STATISTICS OFF", q),
		fmt.Sprintf("ALTER DATABASE %s SET AUTO_UPDATE_STATISTICS OFF", q),
	}
}

// ScopedConfig is one row of sys.database_scoped_configurations: its name,
// its value as text, and the base type of the sql_variant value.
type ScopedConfig struct {
	Name, Value, BaseType string
}

// keyword is what a configuration name or a keyword value looks like. They
// are keywords, not identifiers: they cannot be bracketed, so they are
// checked instead.
var keyword = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// SetScopedConfig sets a database scoped configuration in the connection's
// database. A bit value is written ON or OFF (= 0 gives error 102, measured),
// an integer as a number, a character value as a keyword. Any other value
// panics: it cannot be written safely.
func SetScopedConfig(c ScopedConfig) string {
	if !keyword.MatchString(c.Name) {
		panic(fmt.Sprintf("tsql: invalid scoped configuration name %q", c.Name))
	}
	var v string
	switch strings.ToLower(c.BaseType) {
	case "bit":
		switch c.Value {
		case "0":
			v = "OFF"
		case "1":
			v = "ON"
		default:
			panic(fmt.Sprintf("tsql: bit value %q for %s", c.Value, c.Name))
		}
	case "tinyint", "smallint", "int", "bigint":
		n, err := strconv.ParseInt(c.Value, 10, 64)
		if err != nil {
			panic(fmt.Sprintf("tsql: integer value %q for %s", c.Value, c.Name))
		}
		v = strconv.FormatInt(n, 10)
	case "nvarchar", "varchar", "nchar", "char":
		if !keyword.MatchString(c.Value) {
			panic(fmt.Sprintf("tsql: value %q for %s is not a keyword", c.Value, c.Name))
		}
		v = c.Value
	default:
		panic(fmt.Sprintf("tsql: unsupported base type %q for %s", c.BaseType, c.Name))
	}
	return fmt.Sprintf("ALTER DATABASE SCOPED CONFIGURATION SET %s = %s", c.Name, v)
}
