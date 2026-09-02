# Review checklists

Work these against the module text returned by `proc-source.sql`,
`view-diagnose.sql` or `triggers-inventory.sql`. Report only what you can point
at in the source. A checklist item you cannot evidence is not a finding.

---

## A view returns the wrong rows, or none

Ordered by how often each one is the actual cause.

1. **INNER JOIN where LEFT JOIN was meant.** Any row whose match is missing
   disappears entirely. The tell: the row exists in the base table but not in
   the view.
2. **A predicate on a NULL column.** `WHERE status <> 'X'` drops rows where
   status is NULL, because `NULL <> 'X'` is unknown, not true. So does
   `NOT IN (subquery)` when the subquery returns a NULL.
3. **A LEFT JOIN filtered in the WHERE clause.** `LEFT JOIN b ON ... WHERE
   b.col = 1` silently becomes an inner join. The predicate belongs in the
   `ON`.
4. **Row-level security.** The view returns what *this login* is allowed to
   see. Different caller, different rows, same SQL. Check it (2016+):

   ```sql
   SELECT p.name AS policy_name, p.is_enabled, pr.predicate_type_desc,
          OBJECT_SCHEMA_NAME(pr.target_object_id) AS target_schema,
          OBJECT_NAME(pr.target_object_id)        AS target_object
   FROM sys.security_policies AS p
   JOIN sys.security_predicates AS pr ON pr.object_id = p.object_id;
   ```

   Also check `execute_as_principal_id` from `proc-source.sql`: a module with
   `EXECUTE AS` sees the rows of that principal, not of the caller.
5. **A stale non-schema-bound view.** `ALTER TABLE` on a source does not update
   the view's column list. `is_schema_bound = 0` plus a recently modified base
   table is the signature; `sp_refreshview` is the fix.
6. **Creation-time SET options.** `uses_ansi_nulls` and `uses_quoted_identifier`
   are frozen when the view is created, not read from your session. A view made
   with `ANSI_NULLS OFF` treats `= NULL` as a valid comparison. This is why the
   same text pasted into a query window can behave differently.
7. **Implicit conversion or collation.** Joining `varchar` to `nvarchar`, or
   columns with different collations, can change which rows match — and forces
   a scan while it does.
8. **`TOP` without `ORDER BY`.** Returns *some* n rows, not the top n. Which
   ones can change between runs.
9. **A synonym repointed elsewhere.** The view's text names an object that is a
   synonym; the synonym now points somewhere else. `synonyms_referenced > 0`
   in `view-diagnose.sql` is the flag.
10. **`unresolved_references > 0`.** The view names something that does not
    currently resolve — a dropped object, or a cross-database reference.

---

## Is this procedure well written

**Correctness first — these change results or lose data.**

- **No `SET NOCOUNT ON`.** Every statement sends a rowcount message to the
  client. Some data-access layers read that as a result set and break.
- **Error handling.** Is there a `TRY`/`CATCH`? Does the `CATCH` re-raise with
  `THROW`, or does it swallow the error and return success? A `CATCH` that only
  logs is how failures become silent.
- **Transactions without `SET XACT_ABORT ON`.** Without it, some errors leave
  the transaction open and the connection in a doomed state. Check that every
  path commits or rolls back — including the error path.
- **`@@ERROR` checked late.** `@@ERROR` is reset by the *next* statement. Read
  it immediately or not at all.
- **Concatenated dynamic SQL.** `EXEC('... WHERE name = ''' + @p + '''')` is a
  SQL injection hole and defeats plan reuse. `sp_executesql` with parameters is
  the fix. Distinguish a genuine injection risk (a value from the caller) from
  a schema name the procedure controls.
- **Missing schema prefix.** `FROM Orders` resolves per-user and can bind to a
  different object than intended. Always `dbo.Orders`.

**Performance — these change how it runs.**

- **Parameter sniffing.** The plan is compiled for the first parameter values
  seen. A procedure whose selectivity varies wildly between calls needs
  `OPTION (RECOMPILE)`, `OPTIMIZE FOR`, or local variable copies — each with its
  own cost. Do not prescribe one without seeing the plan.
- **Cursors and `WHILE` loops** doing row-by-row work that a set-based
  statement would do in one pass.
- **Table variables for large sets.** They carry no statistics; the optimiser
  assumes one row. `#temp` tables get statistics.
- **`NOLOCK` used as a performance setting.** It permits dirty reads, missing
  rows and duplicated rows during page splits. Acceptable for approximate
  reporting, not for anything that must reconcile.
- **Non-SARGable predicates**: `WHERE YEAR(order_date) = 2026`,
  `WHERE col + 0 = @x`, `WHERE LEFT(name, 3) = 'ABC'`. A function on the column
  disables the index seek. Rewrite as a range.
- **`SELECT *` in a module.** Breaks silently when the table gains a column,
  and reads more than needed.

---

## Are the triggers well built

The first item is, by a wide margin, the most common real defect.

1. **The single-row assumption.** `inserted` and `deleted` are *tables*. One
   `UPDATE` touching 500 rows fires the trigger **once**, with 500 rows in
   `inserted`. Any of these silently processes one arbitrary row and loses the
   other 499:

   ```sql
   SELECT @id = id FROM inserted;              -- keeps one arbitrary row
   SET @id = (SELECT id FROM inserted);        -- errors if more than one row
   IF @@ROWCOUNT = 1 ...                       -- assumes a single-row statement
   ```

   The correct shape is set-based: join `inserted` to the target table.
   `scalar_from_inserted = 1` in the inventory flags candidates; confirm by
   reading the body.

2. **`ROLLBACK` inside the trigger.** It aborts the entire outer transaction,
   not just the trigger, and the batch behaviour afterwards surprises most
   callers. Raising an error and letting the caller decide is usually right.

3. **An explicit `BEGIN TRANSACTION` inside a trigger.** The trigger already
   runs inside the caller's transaction; nesting adds a level without adding
   isolation, and `COMMIT` there does not commit anything.

4. **No `SET NOCOUNT ON`.** Same client-breaking rowcount messages as for
   procedures, and here the caller never asked for them.

5. **Heavy work inside the trigger.** It runs synchronously inside the caller's
   transaction, holding its locks. Sending mail, calling a linked server or
   looping with a cursor turns a fast `INSERT` into a slow one and lengthens
   every lock it holds.

6. **Recursion and nesting.** A trigger that updates its own table can re-fire
   (`RECURSIVE_TRIGGERS` database option), and triggers firing triggers nest up
   to 32 levels. Check whether the body writes back to its parent table.

7. **Multiple triggers on the same action.** Firing order is undefined unless
   set with `sp_settriggerorder`. If two triggers on one table both write, the
   result depends on an order nobody declared.

8. **`INSTEAD OF` triggers that do not perform the action.** An `INSTEAD OF
   INSERT` replaces the insert. If its body does not insert, the row silently
   never arrives.

9. **Disabled triggers.** `is_disabled = 1` means the rule it enforces is not
   being enforced. That is either a forgotten cleanup or a live data-integrity
   gap — worth reporting either way.
