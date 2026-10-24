# PostgreSQL Toolkit

Claude Code plugin: diagnostics, performance tuning and query design for
PostgreSQL.

Components are added as needed (an `explain-analysis` skill and a log-preprocessing
tool mirroring the SQL Server plugin are still to come). Kept as a
separate plugin so it can evolve and be published independently.

## Seeing a plan

`sqlplan` (built from `tools/`, shared with the SQL Server plugin) draws the output of
`EXPLAIN` in the browser with [pev2](https://github.com/dalibo/pev2), embedded: the page
is one offline file, and the plan never goes to explain.dalibo.com. It reads the JSON
format, best with `ANALYZE` and `BUFFERS`, the JSON object auto_explain logs, and the
text format as psql prints it.

```bash
psql -XAtc "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) SELECT ..." | sqlplan
sqlplan -theme dark slow.pgplan
```

`EXPLAIN ANALYZE` runs the statement: wrap a write in `BEGIN` and `ROLLBACK`.
`-png` and `-node` are for SQL Server plans only, pev2 being an interactive viewer.
