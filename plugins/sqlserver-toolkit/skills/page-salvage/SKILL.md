---
name: page-salvage
description: Recover the rows of a SQL Server data page whose checksum is wrong (errors 824, 8928, 8939) before DBCC CHECKDB REPAIR_ALLOW_DATA_LOSS deallocates it. Use when CHECKDB reports a damaged heap or clustered index data page, a restore is not wanted or not possible, and the user wants the repair without losing the rows on that page. Expects a database name, sysadmin access through sqlcmd, and ideally a sibling copy of the data (another table, a restored backup) to check the recovered rows against.
---

# Salvaging the rows of a damaged page

`REPAIR_ALLOW_DATA_LOSS` fixes a page whose checksum fails by deallocating it:
every row on it is lost. But `DBCC PAGE (db, file, page, 3)` still decodes the
records of that page, checksum or not. Dumped before the repair, parsed, and
checked against a copy, those rows can be put back.

Measured on SQL Server 2022 CU26 and 2025 CU7 (Linux): 78 of 78 rows of a heap
page recovered, of which one had overwritten bytes and was taken from a sibling
table; on a test database, a heap page and a clustered leaf page recovered
in full.

## Access and approvals

`DBCC PAGE`, `DBCC CHECKDB` and the repair are refused by `sqlq` on read-only
profiles, and they need `sysadmin`. The scripts here are `sqlcmd` scripts the
user runs, or that you run in a session the user controls, with each writing
step shown and approved first: the backup, the repair, the reinsertion, the
rebuild. Pass the password through `SQLCMDPASSWORD`, never `-P`, which a
process listing shows.

Before anything, ask whether restoring the page from a backup is possible
(`RESTORE DATABASE ... PAGE = '1:339'` with the log chain). That loses nothing
and is the right answer when the backups exist. This skill is for when they do
not, or are older than the data.

## Workflow

Scripts are in `${CLAUDE_PLUGIN_ROOT}/skills/page-salvage/scripts/`.

1. Inventory, read-only: `sqlcmd -v DB="X" -i inventory.sql`. It prints
   `msdb.dbo.suspect_pages` for the database, then `DBCC CHECKDB ... TABLERESULTS`.
   Save the whole output to a file: `suspect_pages` is history (a later repair
   rewrites its rows to `event_type = 7`), and some teams or courses query it.
   Never delete from `suspect_pages`. List the damaged pages from the
   `File` and `Page` columns of each 8928 and 8939 row.

2. Identify each page: `sqlcmd -v DB="X" FILE=1 PAGE=339 -i page-info.sql`
   (SQL Server 2019 and later; it reads a page with a bad checksum without
   error). Note `page_type_desc`, the object and `index_id`. This skill covers
   `DATA_PAGE` of a heap (`index_id = 0`) or of a clustered index
   (`index_id = 1`). A damaged nonclustered index page needs no salvage: drop
   and recreate the index. An IAM, PFS, GAM or system page is out of scope:
   stop and say so.

3. Back up: `sqlcmd -v DB="X" BAK="/path/X_before_repair.bak" -i backup-before-repair.sql`.
   `COPY_ONLY`, without `CHECKSUM` (a checksum-verifying backup stops on the
   damaged page). This is what makes the whole procedure reversible.

4. Dump each page: `sqlcmd -v DB="X" FILE=1 PAGE=339 -i dump-page.sql -o page-1-339.txt`.
   Do it for every damaged page before step 6: afterwards the page is gone.

5. Parse and match:
   - `python3 page_records.py page-1-339.txt --types Nom=nvarchar,Ville=varchar --format tsv`
     Declare every string column (`varchar`, `char`, `nvarchar`, `nchar`) and
     binary column. The script reads string values from the record bytes, so
     trailing spaces and UTF-16 survive; `--codepage` sets the code page of
     `varchar` (default `cp1252`, right for Latin1 and French collations).
   - Read the `status` column. `ok` means the record decoded, not that it is
     right: overwritten bytes inside a string or an integer decode cleanly. In
     the clustered test, `Libellé 1550` came back as `LibeZZZZ1550` with status
     `ok`. Only `invalid:`, `undecodable:`, `offrow:`, `short:` and
     `record-type` are detected by the parser itself.
   - `--format sql --table '#recovered'` writes a staging table in
     `nvarchar(max) COLLATE DATABASE_DEFAULT` columns and one `INSERT` per
     record. `COLLATE DATABASE_DEFAULT` matters: `tempdb` often has another
     collation than the database, and comparing would fail on error 468.
   - Match every record against the sibling copy, column by column. The query
     shape is in `references/match-and-reinsert.md`. Expect three outcomes per
     record: one exact match (take the page's values), none (find the original
     by the columns that survived, and take the sibling's values), several (a
     duplicate in the sibling: decide with the user).
   - Without a sibling copy, only the parser's own detection is left. Say so,
     show the records whose status is not `ok`, and let the user decide what to
     reinsert.

6. Repair, after the user's yes: `sqlcmd -b -v DB="X" -i repair.sql`.
   `SINGLE_USER WITH ROLLBACK IMMEDIATE` ends every other session on the
   database. What it did, measured:
   - heap: the page is deallocated; `COUNT(*)` drops but
     `sys.dm_db_partition_stats.row_count` still shows the old figure until
     `ALTER TABLE ... REBUILD` (`DBCC UPDATEUSAGE` did not fix it);
   - clustered index: the page is deallocated and the repair rebuilds the
     clustered index itself (message 8945, then "The Clustered index
     successfully rebuilt"); `row_count` was right afterwards.

7. Reinsert, in one transaction, then rebuild: see
   `references/match-and-reinsert.md` (identity columns, triggers, foreign
   keys, ordering). For a heap, finish with `ALTER TABLE <t> REBUILD`.

8. Verify: `sqlcmd -v DB="X" -i verify.sql`. `CHECKDB` with `NO_INFOMSGS`
   prints nothing when the database is clean; `suspect_pages` should still list
   the pages, now with `event_type = 7`. Compare the table with the sibling
   (`EXCEPT` both ways) and give the counts before and after.

## Limits

- Off-row values are not on the damaged page: a `varchar(max)` stored as a LOB
  root, a row-overflow column, a `text`/`ntext`/`image` pointer. The parser
  marks them `offrow:<col>`. Whether they survive the repair has not been
  measured here; do not promise it.
- A forwarded record, a forwarding stub or a ghost record shows up with
  `record-type` in `status`. Not tested: inspect such records by hand.
- Fixed-width values are taken as `DBCC PAGE` displays them; a damaged integer
  displays as a plausible integer.
- Tested on rowstore pages without compression. A `ROW` or `PAGE` compressed
  page, a columnstore segment, a memory-optimized table: out of scope.
- `DBCC PAGE` is undocumented. Its text format was read on 2022 CU26 and
  2025 CU7; on another version, run `test_page_records.py`, then compare one
  parsed record with a `SELECT` of the same row on a healthy page.

## Do not

- Do not run step 6 before every damaged page is dumped and parsed.
- Do not delete from `msdb.dbo.suspect_pages`, before or after.
- Do not reinsert a record whose status is `ok` without checking it against a
  copy when a copy exists.
- Do not present the parser's output as the original data: say how many rows
  matched exactly, how many came from the sibling, and how many from neither.

## Test

```bash
python3 ${CLAUDE_PLUGIN_ROOT}/skills/page-salvage/scripts/test_page_records.py
```

Fixtures are slots cut from real `DBCC PAGE` output of test data
(`scripts/testdata/`).
