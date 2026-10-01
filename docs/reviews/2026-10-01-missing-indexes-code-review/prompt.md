You are reviewing a finished change on branch feat/missing-indexes (base: main) before its owner
decides whether to merge it. Be a serious, sceptical reviewer: find real defects, not style
opinions. The directory you are standing in is a complete working copy; read it directly.
`git diff main...HEAD --stat` lists the change.

What it is. A bundled, read-only T-SQL query, plugins/sqlserver-toolkit/skills/live-query/queries/missing-indexes.sql,
run through the project's `sqlq` CLI as `sqlq -file ... -database <db> -maxrows 70`. It returns one
result set: one `context` row, then for the 3 tables with the highest SUM of missing-index scores,
at most 8 `missing` rows (sys.dm_db_missing_index_*) and at most 15 `existing` rows (sys.indexes
+ sys.dm_db_index_usage_stats + sys.dm_db_partition_stats). It claims: never more than 70 rows;
no table block cut; totals per table exact; restricted to the current database; only user tables,
no is_ms_shipped objects; works on SQL Server 2012+ (no STRING_AGG, no DECLARE, no INTO, single
batch, no GO/USE/EXEC). A Go test, tools/internal/sqlq/missing_indexes_query_test.go, pins the
textual contract. A reading protocol, plugins/sqlserver-toolkit/skills/live-query/references/missing-index-reading.md,
and two additions to that skill's SKILL.md tell an agent how to turn the rows into a proposal.

Read first, and judge against them rather than your own taste:
- docs/superpowers/specs/2026-10-01-missing-indexes-design.md (French; the binding spec, §4 above all)
- docs/superpowers/plans/2026-10-01-missing-indexes.md (the plan; its "Review Focus" section)
- AGENTS.md (rules for anything that talks to SQL Server through sqlq)
- tools/internal/sqlq/ (guard.go: what the write guard refuses; how rows are rendered: decimal as
  JSON strings, datetime with a false Z, bit as true/false, -maxrows keeps the first rows)

Concentrate on, in order:
1. SQL correctness against the spec and Microsoft's documentation. Join fan-out (can any join
   duplicate an index or a suggestion row? sys.dm_db_index_usage_stats, sys.dm_db_partition_stats
   on partitioned tables, missing_index_groups vs details), the ranking (TOP (3) and ROW_NUMBER on
   the same order), the per-table caps, the count columns versus the rows shown, NULL handling,
   heaps (index_id 0), memory-optimized tables, spatial indexes, hypothetical indexes, columnstore,
   the database filter on every DMV that is instance-wide, column types in each UNION ALL branch
   (they must agree, and no decimal/numeric may reach the output), the 2012 compatibility claim
   (name any function, column or syntax that does not exist in 2012).
2. Can the result exceed 70 rows, or can -maxrows 70 cut a table block? Do the arithmetic.
3. The reading protocol: is any rule wrong against Microsoft's "Tune nonclustered indexes with
   missing index suggestions"? Would following it produce a harmful recommendation (a changed
   uniqueness, a drop, a wrong key order)?
4. The Go test: would it catch the regressions it claims to catch? Try mutations of the SQL in your
   head or, if you can write files, in a scratch copy OUTSIDE this directory, and say which ones
   slip through.
5. Anything the query sends that sqlq's guard would refuse, or that a read-only login cannot run.

You may run: `cd tools && go test ./... && go vet ./...` (if your sandbox allows it).
There is NO SQL Server available. Do not connect to any database, do not run sqlq or sqlcmd, do not
look for profiles or credentials. Anything about engine behaviour must be argued from documentation
and labelled as such.

Report shape. Sections: "Verified" (established by running, or by quoting code or a documentation
passage), "Concluded by reading" (hypotheses), "Not a problem" (one line each, what you checked and
found fine). One paragraph of prose per defect, not tables, with file and line, the concrete wrong
output a user would get, and how you checked. Rank Critical / Important / Minor; at most six per
level, and say how many you left out. If you find nothing serious, say so in one sentence rather
than padding the list.

Do the work first, then write the report. The report must contain what you FOUND, never what you
intend to look at next.

Do NOT modify any file in this working copy. Do not commit.
