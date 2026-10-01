# Review of docs/superpowers/plans/2026-10-01-missing-indexes.md (Claude, neutral prompt)

Scratch work: a scratch directory outside the repository (a copy of `tools/` and `plugins/sqlserver-toolkit/skills/live-query/` plus the plugin README). The plan's Go test (plan lines 102-149) and query (plan lines 168-584) were extracted verbatim with `sed -n` and run unmodified. Nothing in the repository was touched, and no database was contacted.

Overall: the query itself holds up well. I found no compile-level or join-grain defect in the SQL. The defects are in the test that is meant to protect the query, and in a validation task that, as written, cannot fail on two of the properties it is there to prove. There are 8 defects below, ranked by how expensive each would be to find after the work is done. I set aside 6 minor ones, listed at the end.

## Verified

**1. The contract test lets the bounds drift without failing, which is the exact failure it exists to catch.** The needles are plain substrings: `"MISS_SEQ <= 8"`, `"IDX_SEQ <= 15"` and `">= 500"` are also substrings of `<= 80`, `<= 150` and `>= 5000`. I changed the plan's query with `sed -e 's/m.miss_seq <= 8/m.miss_seq <= 80/' -e 's/e.idx_seq <= 15/e.idx_seq <= 150/' -e 's/groups_on_instance >= 500/groups_on_instance >= 5000/'` and re-ran `go test ./internal/sqlq/ -run TestMissingIndexesQueryContract -v`. The output was `--- PASS: TestMissingIndexesQueryContract`. A later edit that widens the per-table caps breaks the spec's central guarantee (`TOP (70)` = `-maxrows 70`, so `truncated` never cuts a table block): sqlq then cuts the third table's `existing` rows first. A threshold of 5000 means `collection_capped` never trips. Both would pass this test and every other check before a live run. The plan's own Step 5 mutation (deleting the `DB_ID()` line) fails correctly, which is why the weakness does not show up in the plan's own steps. Fix: anchor each needle with a word boundary (a regexp such as `\bMISS_SEQ <= 8\b`), and add a Step 5 mutation that widens a bound instead of only deleting a line. (`TOP (70)` is safe because the closing parenthesis anchors it: `TOP (700)` was caught.)

**2. sqlq returns `bit` as JSON `true`/`false`, but the header, the protocol and Task 3 all say `= 1`.** go-mssqldb v1.11.0 `types.go` has `case typeBit: return buf[0] != 0` and `case typeBitN: ... return buf[0] != 0`, and `normalise` in `tools/cmd/sqlq/main.go` passes the resulting Go `bool` through unchanged. So `collection_capped`, `is_auto_close_on`, `has_filter`, `is_disabled`, `memory_optimized` and `usage_not_tracked` arrive as `true`/`false`. The reading protocol's stop rule is written as "`collection_capped = 1`", and the V0 and M3/M10/M11 expectations ("`is_disabled = 1`", "`is_auto_close_on = 1`") will be compared against `true`. A model will usually map one to the other, so this is unlikely to produce a wrong answer. It is still a mismatch between the contract and the actual output, in the one place, the stop rule, where a mechanical reading matters. Either say "true (1)" in the protocol and header, or note it once in the protocol.

**3. Several commands in the plan do not do what the plan says (each fails loudly or checks nothing).**
- **Task 2 Step 4.** `rtk grep -n "missing-index-reading.md\|missing-indexes.sql" plugins/sqlserver-toolkit/skills/live-query` does not search the directory. The same form run in the scratch copy printed `/usr/bin/grep: plugins/sqlserver-toolkit/skills/live-query: Is a directory` and exited 2. It needs `-r`.
- **Task 1 Step 2.** The expected failure text "The system cannot find the file specified" is not what this machine prints. The real output is `open ..\..\..\plugins\sqlserver-toolkit\skills\live-query\queries\missing-indexes.sql: Le fichier spécifié est introuvable.` The failure is the right one; only the quoted text is wrong.
- **Task 1 Step 5.** `rtk git diff --stat` cannot show anything for `missing-indexes.sql`, because the file is untracked until Step 7. The check is vacuous. The re-run to PASS in the same step is what actually catches a line that was not put back.
- **Formatting.** The test file is not gofmt-clean. `gofmt -l` lists it, because the `"OPTION (RECOMPILE, MAXDOP 1)"` key breaks the map's alignment. `go vet` does not check this, and the repo has no CI that would.

## Concluded by reasoning

**4. Case M5 cannot fail in the failure mode it is there for.** Review Focus #3 is "no foreign rows". M5's pass criterion is "no row naming `o1`" plus "`suggestion_groups_on_instance` increased". Suppose the `DB_ID()` filter were missing. `o1`'s suggestion would carry `mi_val_other`'s `object_id`, and the query resolves it against `mi_val_a`'s `sys.objects`. Only two outcomes are possible, and neither names `o1`:
- no matching object: the suggestion is silently counted in `suggestions_on_unresolved_objects`;
- the same `object_id` exists in `mi_val_a`: the suggestion is attributed to that table and shown under its name.

The second outcome is likely, not hypothetical. It rests on reasoning, not on documentation: user `object_id`s in databases freshly created from `model` follow the same allocation sequence. `o1` is the first table created in `mi_val_other` and `t1` the first in `mi_val_a`, and their suggestions have the same shape (`[a], [b]` include `[d]`). So M5 passes both with and without the filter. The expectation should instead be that `mi_val_a`'s result is unchanged by the `o1` reads: same `tables_with_suggestions`, same `table_suggestion_count` per table, same `suggestions_on_unresolved_objects`, and no new `missing` row. One more complication: M11 (`AUTO_CLOSE ON`) has probably already emptied `mi_val_a`'s suggestions by then, so the "before" snapshot has to be taken immediately before the `o1` reads, as the plan says, and compared row by row.

**5. Many validation cases may produce no suggestions at all, because their probe queries are probably trivial plans.** Learn, *Tune nonclustered indexes with missing index suggestions*, Limitations: "Suggestions aren't made for trivial query plans." The plan relies on "at least 20,000 rows so the plan is not trivial", but no documented rule ties triviality to row count. Every probe is a single-table `SELECT TOP (10) ... WHERE <unindexed column> = const`, and the only covering access path is the clustered index or heap scan. The one size-related way out of a trivial plan that I know of is a trivial plan cost above the cost threshold for parallelism. That rests on reasoning, not documentation. Even so, a 20,000-row table of this shape is roughly 130-150 pages, about 0.1-0.2 cost units, well below the default of 5, and `TOP (10)` lowers the estimate further. If the hypothesis holds:
- M2, M3, M3b, M4, M7 and M10 fail loudly, after the user has approved every setup write;
- M6 can pass vacuously ("no `missing` row for `r1`" after the rebuild) if `r1` never had one;
- M9 can be scored "unacceptable" (`v2` absent and not counted) and send the implementer to "fix" a correct query, as the Arrêt rule requires.

Cheap guards:
- run the first probe with `-plan` and require `StatementOptmLevel="FULL"` and a `<MissingIndexes>` element before generating the rest;
- or force full optimisation in the probe text (e.g. `AND 1 = (SELECT 1)`, which adds no column);
- either way, make "the table has ≥ 1 `missing` row" an explicit precondition of M6 and M9.

**6. Task 3 Step 6 contradicts itself on the passwords.** It promises the password "n'apparaît ni dans la conversation ni dans le dépôt", yet it lets the logins be created "par l'utilisateur ou avec son accord". The second route puts the password in the transcript. `CREATE LOGIN ... WITH PASSWORD` needs a literal, so `-param` cannot carry it; dynamic SQL would need `EXEC`, which is refused. If the agent runs the statement through `sqlq -allow-write`, the password lands in the `-query` argument and in the transcript. The step should say that the user runs the two `CREATE LOGIN` statements themselves (SSMS), and that the agent only runs the `GRANT` and `CREATE USER` lines.

**7. Review Focus #1 and the spec's M3b requirement "aucune autre table amputée" are not exercised, and the 70-row ceiling is never reached.** M3b runs alone in `mi_val_c` (expected `rowcount = 24`). With no other table present, nothing can be amputated, and the case where truncation would bite never occurs: three tables each at 8 + 15, 70 rows, `truncated = false`. Today the bound holds by arithmetic. But the arithmetic is exactly what finding 1 shows can drift unnoticed, and a live 70-row run is also where the spec's §7 size estimate (about 55,000 characters) would actually be measured. Put three tables at the caps in one database (the M3b model three times, or M3b next to two smaller capped tables), and expect `rowcount = 70`, `truncated = false`, and each table with 8 + 15 rows.

**8. Several steps describe code without showing it, and one spec requirement has no task.**
- `CREATE DATABASE` is shown only for `mi_val_a`. The databases `mi_val_b` to `mi_val_g` and `mi_val_other` are created implicitly. They are entries in the cleanup register, and each is a write that needs exact-text approval.
- M3b's SQL "est écrit à ce moment".
- M10's `<dir>` is taken from `physical_name`, which is a file path, not a directory.
- Step 8 does not say which catalog the cleanup calls connect to. A `DROP DATABASE` issued with `-database <base>` fails, because sqlq's own connection is using that database.
- Spec §8 *Revues* calls for `external-code-review` (agy, codex) and an adversarial risk review after implementation. The plan has no task for either.

## Not a problem

- Plan's test plus guard test, run verbatim: `PASS` for `TestMissingIndexesQueryContract` and `TestBundledQueriesPassTheReadOnlyGuard/missing-indexes.sql`. `go vet ./...` is clean. `go test ./...` is all `ok` once the plugin README is in the scratch layout.
- Step 5 mutation (deleting `WHERE d.database_id = DB_ID()`) fails with `missing "D.DATABASE_ID = DB_ID()"`, as the plan states.
- `Sanitize` blanks the header comment, so `DECLARE`, `decimal` and `OBJECT_NAME()` in the prose do not trip the forbidden list.
- Column arity and order: the outer list has 38 columns, matching Task 1's interface list and V0's "38 colonnes". All three union branches carry 38 + `kind_order` + `seq` in the same order. Checked position by position.
- Types: every non-literal output column is CAST. `used_mb`, `instance_uptime_days` and `score` are float (float × numeric literal stays float). No decimal output column.
- `ORDER BY table_rank, kind_order, seq` on columns of the derived table that are not in the outer select list is valid, and the order is total.
- `TOP (70)` = 1 + 3 × (8 + 15). M3b's `rowcount = 24` = 1 + 8 + 15. Index ids 1-15 are the PK plus `k01`-`k14`.
- Usage stats are joined per (database, object, index), and partition stats are aggregated per (object, index) before the join, so a partitioned index is not duplicated (M7b checks it).
- Score formula and join keys (`g.index_handle = d.index_handle`, `s.group_handle = g.index_group_handle`) match the Learn DMV page example.
- Learn confirms the plan's claims: a 600-group cap; "Performing an ALTER INDEX operation on an index on a table also clears missing index requests for that table"; `sys.dm_db_partition_stats` needs "VIEW DATABASE STATE and VIEW DEFINITION", or "VIEW DATABASE PERFORMANCE STATE and VIEW SECURITY DEFINITION" from 2022; `included_columns` is to be ignored for memory-optimized indexes.
- M9's design is sound: per GRANT Database Permissions, VIEW DATABASE STATE is "implied by server permission VIEW SERVER STATE", so M9 isolates the missing VIEW DEFINITION as intended.
- `CONVERT(varchar(19), x, 126)` truncates to `yyyy-mm-ddThh:mi:ss` whether or not the milliseconds are printed.
- `json.Encoder.SetEscapeHTML(false)` in `emit`, so M4's `&` and `<` reach the JSON raw and the "no `&amp;`/`&lt;`" check is meaningful.
- The SKILL.md anchors Task 2 relies on exist: the `## Decision tree` table, the `| Why is this query slow |` row, and the "Do not invent results" and "Do not assume the version" bullets under `## What not to do`.
- The spec says "Aucun test Go nouveau" and the plan adds one. This is an addition, not a contradiction of any rule.

Set aside (6):
- `ROUND(..., 1)` makes the `instance_uptime_days < 1` stop rule trip only below about 0.95 days.
- M1's `suggestions_on_system_objects = 0` could be broken by suggestions that `Q` itself generates on catalog base tables.
- The `suggestion` CTE is evaluated several times, so totals and rows are not one consistent DMV snapshot.
- `used_mb` is probably NULL for XML and spatial indexes, whose storage sits in internal tables.
- The skill's frontmatter `description` does not mention index questions.
- M2's "identical rows on two runs" can differ through `instance_uptime_days` rounding.
