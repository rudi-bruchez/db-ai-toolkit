# Reading missing-indexes.sql

The result answers one question: which indexes to propose on the three tables that ask for
them most. It does not answer which indexes to drop. Read the header of
`queries/missing-indexes.sql` before the rows.

Flags are bit columns, returned by sqlq as JSON `true` / `false`.

## 1. The context row first

Stop, and say why, if `instance_uptime_days < 1` or `collection_capped` is true: no conclusion
is possible in either direction.

Say, whenever it holds:

- `is_auto_close_on` is true: every usage counter covers an unknown window, possibly hours;
  section 4 cannot be concluded;
- `database_in_ag` is true: counters and suggestions describe this replica only, and the result
  does not say whether it is the primary;
- `suggestions_on_unresolved_objects > 0`: some suggestions are on objects this login cannot
  see, or that no longer exist;
- `tables_with_suggestions > 3`: only the top three are shown.

## 2. Is the table complete?

For each table, count the `missing` and `existing` rows shown and compare them with
`table_suggestion_count` and `table_index_count`. If indexes are not shown, do not propose a
new index on that table until the missing ones have been listed by another query
(`sys.indexes` for that object). If a total is smaller than the rows shown, the data changed
during the read: run the query again.

## 3. Group, then compare

Treat `equality_columns` as a set: the DMV's order means nothing. In a key, equality columns
come first, then at most one useful inequality column; everything after the first inequality
column can only filter, not seek.

Every nonclustered index carries the clustered key (read on the clustered row of the same
table): after its declared key if it is nonunique, in its leaf if it is unique. Drop clustered
key columns from a suggestion's `included_columns`. The rules below compare the **declared**
key (`key_columns`); the clustered key counts only where they say so.

1. Group the table's suggestions. Suggestions with the same equality set and the same
   inequality column are one index. Suggestions with the same equality set but different
   inequality columns are not automatically one index: only one of those columns can be used
   to seek.
2. For each group, compare it with each existing rowstore index that is not clustered and not
   hash (`index_type` `NONCLUSTERED`). Let N be the size of the equality set:
   - **every declared key column of the existing index is in the equality set** (an index on
     `[a]`, a suggestion on `[a], [b]`): widen that index by appending the other equality
     columns, then the inequality column, to its key, and the included columns to its
     `INCLUDE`. Appending
     key columns pushes the implicit clustered key further right: a query that seeks or sorts
     on the old key followed by the clustered key loses that seek. If the index's `user_seeks`
     are not negligible, propose a new index instead, or say that the widening must be checked
     against the queries that use it;
   - **the first N columns of the existing key are exactly the equality set**, in any order,
     counting the clustered key after the declared key of a nonunique index: the key already
     serves the equalities; add what is missing to `INCLUDE`. An unrelated key column among
     the first N (`[a], [x], [b]` for `{a, b}`) breaks the seek: this case does not apply;
   - an inequality column is served only if it is column N + 1 of that same key, counted the
     same way.
3. Prefer widening to creating, with these limits:
   - a unique index or a primary key (`is_unique`, `is_primary_key` true) is widened through
     `INCLUDE` only. Adding a key column changes what is unique;
   - a clustered index is never widened. `INCLUDE` does not exist for it, its leaf already
     holds every column, and a change to its key rebuilds every nonclustered index of the
     table. A suggestion its key does not serve calls for a nonclustered index;
   - a filtered index (`has_filter` true) covers only the rows of its filter; if
     `filter_definition` is null, the filter is unreadable and coverage is unknown;
   - a disabled index (`is_disabled` true) covers nothing;
   - a key stays within 16 columns and 1,700 bytes. The result carries no column types: check
     `max_length` in `sys.columns` before widening a key, and put the extra columns in
     `INCLUDE` otherwise. An index over the limit is created with a warning, and later inserts
     or updates of a row that exceeds it fail.
4. Memory-optimized table (`memory_optimized` true): ignore the suggestion's
   `included_columns`. A hash index (`index_type` `NONCLUSTERED HASH`) seeks only on an
   equality on every one of its key columns; the prefix rules above do not apply to it, and an
   inequality column needs a memory-optimized nonclustered index instead.

## 4. Weigh the writes

Before adding an index to a table, report the clustered or heap row's `user_updates` beside the
reads (`user_seeks + user_scans + user_lookups`) of its indexes. Do not turn the ratio into a
verdict:

- `user_updates` counts statements, not rows: a load of millions of rows in one statement
  counts 1, so the cost of an extra index on a table loaded in bulk does not show here;
- on a readable secondary, writes arrive through redo and `user_updates` does not count them.
  When `database_in_ag` is true, check the replica's role
  (`sys.dm_hadr_database_replica_states.is_primary_replica`) before quoting any write figure;
  on a secondary, say the write cost is unknown;
- the counters cover this replica only, over a window of at most `instance_uptime_days`.

## 5. Write the proposal

One consolidated proposal per table: which index to widen and how, or which index to create,
and which suggestions it serves. The order of the equality columns is for someone who knows
their selectivity to choose; the result does not carry it. Quote `instance_uptime_days` as an
upper bound, and say that index maintenance (a rebuild at least) clears a table's suggestions,
and that a restore, a detach or an offline cycle of the database shortens the window without
leaving a date in this result.

Quote `used_mb` of the clustered or heap row and of any index to be widened. Building or
widening an index runs offline outside Enterprise edition (and Developer), and widening
rebuilds an index that queries use now.

A proposal is not finished when the index is created. Say how it will be checked afterwards:
the new index's usage counters, and, where the Query Store is enabled, whether the queries that
asked for it now use it and run faster (Microsoft, *Tune nonclustered indexes with missing index
suggestions*, "Verify if your index change is successful").

## Never

- paste a suggestion as `CREATE INDEX`;
- conclude "no index is missing" from an absence of rows;
- recommend dropping an index from this result;
- add a key column to a unique index or a primary key.
