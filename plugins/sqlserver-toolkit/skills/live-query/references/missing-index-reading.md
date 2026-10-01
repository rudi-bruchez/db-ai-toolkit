# Reading missing-indexes.sql

The result answers one question: which indexes to propose on the three tables that ask for
them most. It does not answer which indexes to drop. Read the header of
`queries/missing-indexes.sql` before the rows.

Flags are bit columns, returned by sqlq as JSON `true` / `false`.

## 1. The context row first

Stop, and say why, if `instance_uptime_days < 1` or `collection_capped` is true: no conclusion
is possible in either direction.

Say, whenever it holds:

- `is_auto_close_on` or `database_in_ag` is true: the counters may cover far less than
  `instance_uptime_days`, and on an availability group they describe this replica only;
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
column can only filter, not seek. Remember the clustered key (on the clustered row of the same
table): every nonclustered index carries it.

1. Group the table's suggestions. Suggestions with the same equality set and the same
   inequality column are one index. Suggestions with the same equality set but different
   inequality columns are not automatically one index: only one of those columns can be used
   to seek.
2. For each group, compare it with each existing rowstore index that is not clustered and not
   hash (`index_type` `NONCLUSTERED`). Let N be the size of the equality set:
   - **every key column of the existing index is in the equality set** (an index on `[a]`, a
     suggestion on `[a], [b]`): widen that index by appending the other equality columns, then
     the inequality column, to its key, and the included columns to its `INCLUDE`;
   - **the first N key columns of the existing index are exactly the equality set**, in any
     order: the key already serves the equalities; add what is missing to `INCLUDE`. An
     unrelated key column among the first N (`[a], [x], [b]` for `{a, b}`) breaks the seek:
     this case does not apply;
   - an inequality column is served only if it is key column N + 1 of the existing index.
3. Prefer widening to creating, with these limits:
   - a unique index or a primary key (`is_unique`, `is_primary_key` true) is widened through
     `INCLUDE` only. Adding a key column changes what is unique;
   - a clustered index is never widened. `INCLUDE` does not exist for it, its leaf already
     holds every column, and a change to its key rebuilds every nonclustered index of the
     table. A suggestion its key does not serve calls for a nonclustered index;
   - a filtered index (`has_filter` true) covers only the rows of its filter; if
     `filter_definition` is null, the filter is unreadable and coverage is unknown;
   - a disabled index (`is_disabled` true) covers nothing.
4. Memory-optimized table (`memory_optimized` true): ignore the suggestion's
   `included_columns`. A hash index (`index_type` `NONCLUSTERED HASH`) seeks only on an
   equality on every one of its key columns; the prefix rules above do not apply to it, and an
   inequality column needs a memory-optimized nonclustered index instead.

## 4. Weigh the writes

Before adding an index to a table, compare the clustered or heap row's `user_updates` with the
reads (`user_seeks + user_scans + user_lookups`) of its indexes. These counters cover this
replica only, over a window of at most `instance_uptime_days`.

## 5. Write the proposal

One consolidated proposal per table: which index to widen and how, or which index to create,
and which suggestions it serves. The order of the equality columns is for someone who knows
their selectivity to choose; the result does not carry it. Quote `instance_uptime_days` as an
upper bound, and say that index maintenance clears a table's suggestions.

A proposal is not finished when the index is created. Say how it will be checked afterwards:
the new index's usage counters, and, where the Query Store is enabled, whether the queries that
asked for it now use it and run faster (Microsoft, *Tune nonclustered indexes with missing index
suggestions*, "Verify if your index change is successful").

## Never

- paste a suggestion as `CREATE INDEX`;
- conclude "no index is missing" from an absence of rows;
- recommend dropping an index from this result;
- add a key column to a unique index or a primary key.
