# Matching recovered records and putting them back

Load this when step 5 or 7 of the `page-salvage` skill is reached. The queries
below are the ones that worked on the test runs; adapt the column lists, keep
the shape.

## Matching against a sibling copy

The staging table from `page_records.py --format sql` has every column in
`nvarchar(max) COLLATE DATABASE_DEFAULT`. Compare in the target types, and
compare strings in a binary collation on both sides:

- `TRY_CAST`, not `CAST`: a damaged value may no longer fit its type, and one
  failing `CAST` aborts the whole query.
- `COLLATE Latin1_General_BIN2` on both sides avoids error 468 when the sibling
  column has another collation, and avoids a case- or accent-insensitive
  collation calling two different strings equal.
- `EXISTS (SELECT ... INTERSECT SELECT ...)` compares NULL with NULL as equal,
  on every version (`IS NOT DISTINCT FROM` only from SQL Server 2022).

```sql
SELECT COUNT(*) AS records,
       SUM(CASE WHEN m.n = 1 THEN 1 ELSE 0 END) AS exact_once,
       SUM(CASE WHEN m.n > 1 THEN 1 ELSE 0 END) AS exact_many,
       SUM(CASE WHEN m.n = 0 THEN 1 ELSE 0 END) AS no_match
FROM #recovered AS r
CROSS APPLY (SELECT COUNT(*) AS n
             FROM dbo.Sibling AS s
             WHERE EXISTS (SELECT TRY_CAST(r.Id AS int),
                                  r.Name COLLATE Latin1_General_BIN2,
                                  TRY_CAST(r.BirthDate AS date)
                           INTERSECT
                           SELECT s.Id,
                                  s.Name COLLATE Latin1_General_BIN2,
                                  s.BirthDate)) AS m;
```

For each `no_match` record, look the original up by the columns that look
intact: the ones the parser did not flag and that have plausible values. List
the candidates and show them to the user; do not pick one silently when there
are several.

```sql
SELECT r.Slot, r.Status, r.Id, r.Name, s.Id AS sibling_id, s.BirthDate
FROM #recovered AS r
JOIN dbo.Sibling AS s ON s.Name = r.Name AND s.Code = r.Code
WHERE NOT EXISTS (SELECT 1 FROM dbo.Sibling AS x
                  WHERE x.Id = TRY_CAST(r.Id AS int)
                    AND x.BirthDate = TRY_CAST(r.BirthDate AS date));
```

A sibling is any copy that predates the damage: a twin table, a restored
backup in another database (`RESTORE DATABASE X_copy ... WITH MOVE`), an export.
On a heap with no key, match on all columns: the test heap matched 112 of 113
records exactly, and the 113th by two intact columns.

## Reinserting

Temp tables die with the session: run the staging script and the inserts in
the same `sqlcmd` invocation.

```sql
BEGIN TRANSACTION;

-- records that matched exactly: the page's own values
INSERT dbo.Target (Id, Name, BirthDate)
SELECT CAST(r.Id AS int), r.Name, CAST(r.BirthDate AS date)
FROM #recovered AS r
WHERE <exact match condition, as above>
ORDER BY r.Slot;

-- records repaired from the sibling, one explicit statement each
INSERT dbo.Target (Id, Name, BirthDate)
SELECT s.Id, s.Name, s.BirthDate FROM dbo.Sibling AS s WHERE s.Id = 373;

COMMIT;
ALTER TABLE dbo.Target REBUILD;   -- heap: makes row_count right again
```

Check before running it:

- `IDENTITY` column: `SET IDENTITY_INSERT dbo.Target ON` around the inserts,
  to keep the original values.
- Triggers on the table fire on these inserts. Read them first
  (`sys.triggers`); disable one only with the user's yes.
- Foreign keys pointing from the table: the parents must exist. Foreign keys
  pointing to it: rows elsewhere may have lost their parent with the page;
  `DBCC CHECKCONSTRAINTS (N'dbo.Child')` lists them after the reinsertion.
- A clustered key: the rows go back in key order whatever the insert order.
  A heap: they go to free space, usually the end of the heap, not to the old
  page. The physical location of these rows changes, nothing else.

## Checking the result

```sql
SELECT COUNT(*) FROM dbo.Target;
SELECT SUM(row_count) FROM sys.dm_db_partition_stats
WHERE object_id = OBJECT_ID(N'dbo.Target') AND index_id IN (0, 1);
SELECT COUNT(*) FROM (SELECT Id, Name, BirthDate FROM dbo.Target
                      EXCEPT SELECT Id, Name, BirthDate FROM dbo.Sibling) AS d;
```

Report the three counts: rows taken from the page, rows taken from the sibling,
rows from neither.
