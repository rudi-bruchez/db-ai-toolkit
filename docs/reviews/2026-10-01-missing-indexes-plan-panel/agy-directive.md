## Verified

- **The Go contract test passes as written and catches removals:** Running the provided Go test code against the exact sanitized query correctly asserts the required text bounds. Removing `WHERE d.database_id = DB_ID()` triggers the expected failure `missing "D.DATABASE_ID = DB_ID()"`.
- **The query is read-only and passes the guard:** Running the `sqlq` write guard logic against the query yields zero write violations. The word `user_updates` is safely treated as a single token and does not trip the filter.
- **The 70-row bound is perfectly sized:** 1 context row + 3 tables × (8 suggestions + 15 indexes) = 70 rows exactly. `-maxrows 70` will never abruptly cut off a table block.
- **Number formatting is preserved:** The `used_mb` and `instance_uptime_days` columns are correctly cast and computed as `float`, which `sqlq` will safely render as native JSON numbers, sidestepping the `decimal`/`numeric` string serialization limit.
- **XML unescaping for special characters:** The `FOR XML PATH(''), TYPE).value('.', 'nvarchar(max)')` pattern correctly guarantees that column names containing `<` or `&` (case M4) are safely unescaped and kept intact.
- **Valid SQL Server 2012 syntax:** `DB_ID()`, `sys.databases.replica_id`, the `VALUES` table constructor, and the `ORDER BY` referencing columns in the derived table's scope are all supported and valid constructs.

## Concluded by reasoning

**Task 3 Setup (M3b) execution order (Silent false negative):** To reach the M3b state ("une table avec 10 suggestions et 17 index"), the plan does not constrain the execution order. As the plan's query header correctly notes, creating an index clears the missing index suggestions for that table. If the tester generates the 10 suggestions first and creates the 17 indexes afterwards, the suggestions will be silently wiped out, causing the validation to fail. The setup must explicitly instruct the tester to create the 17 indexes *before* running the workload that generates the suggestions.

**Task 3 Setup batching limits (Loud failure):** The plan states that "Un appel peut contenir plusieurs instructions d'un même cas (un seul batch, sans GO)" and lists `CREATE PARTITION FUNCTION`, `CREATE DATABASE`, and `ALTER DATABASE` as examples. This is invalid T-SQL. These structural DDL statements must be the *only* statement in their execution batch. Because `sqlq` refuses batches containing `GO`, the tester cannot group them; they will be forced to make multiple separate calls to `sqlq -allow-write`.

**Task 3 Cleanup incompleteness (Resource leak):** The cleanup instructions explicitly state "Les deux bases et les logins temporaires sont supprimés". However, the setup in Task 3 partitions the tests across at least eight distinct databases (`mi_val_a` through `mi_val_g`, plus `mi_val_other`). Following the cleanup literally will leave six test databases orphaned on the instance.

## Not a problem

- `ORDER BY table_rank, kind_order, seq` is fully deterministic and properly sequences the CTE results.
- `OBJECTPROPERTY` safely degrades to returning NULL on SQL Server 2012 instead of throwing an error.
- `sys.dm_db_index_usage_stats` and `sys.dm_db_missing_index_details` correctly restrict to the current database with `DB_ID()`.
- The `M5` cross-database validation behaves perfectly because `suggestion_groups_on_instance` spans the instance while the rows stay constrained.

(0 defects set aside.)
