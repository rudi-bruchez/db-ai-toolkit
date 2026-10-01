# Index manquants et index existants — plan d'implémentation

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal :** livrer une requête en lecture seule, `missing-indexes.sql`, lancée par
`sqlq -file ... -database <base>`, qui rend pour les 3 tables les plus demandeuses de la base
courante leurs suggestions d'index manquants **et** leurs index existants avec leur usage,
précédées d'une ligne `context` qui dit si une conclusion est possible.

**Architecture :** un fichier SQL dans la bibliothèque du skill `live-query` (un batch, des CTE,
une union de trois sortes de lignes typées, `TOP (70)` final), un test Go qui fige le contrat
textuel de ce fichier, une ligne dans l'arbre de décision du skill et un protocole de lecture
chargé à la demande, puis une validation sur une instance de test consignée dans un document
versionné. Aucun changement de `sqlq`.

**Tech Stack :** T-SQL (SQL Server 2012+ et Azure SQL Managed Instance), Go (`go test`),
Markdown.

**Spec :** `docs/superpowers/specs/2026-10-01-missing-indexes-design.md` — à lire en entier
avant la première tâche, §4 en particulier. Relecture du panel :
`docs/superpowers/specs/2026-10-01-missing-indexes-design.md` §10 et
`docs/reviews/2026-10-01-missing-indexes-design-panel/`.

## Global Constraints

- Un seul batch : pas de `GO`, `USE`, `EXEC`, `INTO`, `DECLARE`, ni SQL dynamique.
- Pas de `STRING_AGG` : agrégation par `FOR XML PATH('')` + `, TYPE).value('.', 'nvarchar(max)')`.
- Aucun paramètre. Un seul jeu de résultats. `TOP (3)` tables, 8 suggestions et 15 index par
  table au plus, `TOP (70)` final.
- Aucune colonne `decimal` ni `numeric` dans le résultat : `sqlq` les rend en chaînes JSON.
- Dates du résultat en `varchar(19)` par `CONVERT(..., 126)` : `sqlq` suffixe les `datetime`
  d'un `Z` faux.
- `database_id = DB_ID()` sur `sys.dm_db_missing_index_details` et sur
  `sys.dm_db_index_usage_stats`.
- Seuil de `collection_capped` : 500 groupes.
- Le résultat ne justifie aucune suppression d'index ; le skill l'interdit.
- Le dépôt est public : aucun nom de client, d'instance, de profil, de base réelle ni de chemin
  réel, ni dans le code, ni dans le document de validation, ni dans les commits. Les bases de
  validation s'appellent `mi_val_*`.
- Écrire sur l'instance de test demande l'accord explicite de l'utilisateur **sur les
  instructions exactes** montrées, à chaque appel. Ne jamais passer `-allow-write` sans cet
  accord.
- Commandes shell préfixées par `rtk` (AGENTS.md).
- Messages de commit terminés par `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Review Focus

1. **Une table avec plus de 8 suggestions ou plus de 15 index** → blocs plafonnés, totaux
   exacts, aucune autre table amputée, aucune troncature par `sqlq`. Cas M3b de la tâche 3.
2. **Table partitionnée** → une seule ligne par index, taille sommée, compteurs non multipliés.
   Cas M7b de la tâche 3.
3. **Suggestions dans une autre base de l'instance** → aucune ligne étrangère, mais comptées
   dans `suggestion_groups_on_instance`. Cas M5 de la tâche 3.
4. **Login sans `VIEW DEFINITION`** → erreur franche, ou suggestions comptées comme
   introuvables ; jamais une table qui disparaît sans trace. Cas M9 de la tâche 3.
5. **Nom de colonne contenant `&` et `<`** → restitué intact, dans `key_columns` comme dans
   `equality_columns`. Cas M4 de la tâche 3.

---

## Fichiers

| fichier | rôle |
|---|---|
| `plugins/sqlserver-toolkit/skills/live-query/queries/missing-indexes.sql` (créé) | la requête et ses règles de lecture |
| `tools/internal/sqlq/missing_indexes_query_test.go` (créé) | fige le contrat textuel de la requête : bornes, filtres de base, absence de `decimal` |
| `plugins/sqlserver-toolkit/skills/live-query/references/missing-index-reading.md` (créé) | le protocole de lecture |
| `plugins/sqlserver-toolkit/skills/live-query/SKILL.md` (modifié) | une ligne dans l'arbre de décision, quatre interdits |
| `docs/validation/2026-10-01-missing-indexes.md` (créé) | résultats de la validation sur instance, sans nom réel |

---

### Task 1 : la requête `missing-indexes.sql` et son test de contrat

**Files :**
- Create : `tools/internal/sqlq/missing_indexes_query_test.go`
- Create : `plugins/sqlserver-toolkit/skills/live-query/queries/missing-indexes.sql`

**Interfaces :**
- Consumes : `Sanitize(string) string` de `tools/internal/sqlq/guard.go` (blanchit commentaires,
  littéraux et identifiants entre crochets) ; la constante `bundledQueriesDir` et la fonction
  `refusals` de `tools/internal/sqlq/bundled_queries_test.go` (même paquet `sqlq`).
- Produces : le fichier `queries/missing-indexes.sql` et les noms de colonnes du §4 de la spec,
  dans cet ordre : `row_kind, table_rank, schema_name, table_name, table_suggestion_count,
  table_index_count, memory_optimized, index_name, index_type, is_unique, is_primary_key,
  is_disabled, has_filter, filter_definition, key_columns, equality_columns,
  inequality_columns, included_columns, user_seeks, user_scans, user_lookups, user_updates,
  last_used, used_mb, usage_not_tracked, avg_user_impact, score, unique_compiles,
  instance_start_time, instance_uptime_days, is_auto_close_on, database_in_ag,
  suggestion_groups_on_instance, collection_capped, tables_with_suggestions,
  suggestions_on_system_objects, suggestions_on_unresolved_objects, hypothetical_indexes`.
  Les tâches 2 et 3 s'y réfèrent par ces noms.

Le test ne prouve pas que la requête est juste : il empêche qu'une retouche ultérieure retire
en silence une borne ou un filtre que la spec exige. La justesse se prouve en tâche 3.

- [ ] **Step 1 : écrire le test de contrat**

`tools/internal/sqlq/missing_indexes_query_test.go` :

```go
package sqlq

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The missing-indexes query promises bounds and filters that nothing else
// checks before a live run: a later edit that drops one still passes the
// guard and still returns plausible rows. This pins the text that carries
// each promise. Correctness is proven on an instance, not here.
func TestMissingIndexesQueryContract(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(bundledQueriesDir, "missing-indexes.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if r := refusals(string(body)); len(r) > 0 {
		t.Fatalf("sqlq would refuse the query: %s", strings.Join(r, "; "))
	}
	code := strings.ToUpper(Sanitize(string(body)))
	norm := regexp.MustCompile(`\s+`).ReplaceAllString(code, " ")

	mustContain := map[string]string{
		"TOP (70)":                 "the final bound equal to -maxrows 70",
		"TOP (3)":                  "three ranked tables",
		"MISS_SEQ <= 8":            "eight suggestions per table",
		"IDX_SEQ <= 15":            "fifteen indexes per table",
		"D.DATABASE_ID = DB_ID()":  "suggestions restricted to the current database",
		"US.DATABASE_ID = DB_ID()": "usage restricted to the current database",
		">= 500":                   "the collection cap threshold",
		"OPTION (RECOMPILE, MAXDOP 1)": "the query hints of the source scripts",
	}
	for needle, why := range mustContain {
		if !strings.Contains(norm, needle) {
			t.Errorf("missing %q (%s)", needle, why)
		}
	}

	forbidden := []string{"DECIMAL", "NUMERIC", "STRING_AGG", "DECLARE", "OBJECT_NAME("}
	for _, word := range forbidden {
		if strings.Contains(norm, word) {
			t.Errorf("the query uses %s, which the spec rules out", word)
		}
	}
}
```

`Sanitize` blanchit les commentaires : l'en-tête peut donc citer `decimal`, `DECLARE` ou
`OBJECT_NAME()` sans faire échouer le test. Il blanchit aussi les identifiants entre crochets,
d'où l'absence de crochets dans les aiguilles.

- [ ] **Step 2 : lancer le test, vérifier qu'il échoue**

Run : `cd tools && rtk go test ./internal/sqlq/ -run TestMissingIndexesQueryContract -v`
Expected : FAIL dès `os.ReadFile`, message d'ouverture du fichier
`missing-indexes.sql` introuvable (`The system cannot find the file specified` sous Windows,
`no such file or directory` ailleurs).

- [ ] **Step 3 : écrire la requête**

`plugins/sqlserver-toolkit/skills/live-query/queries/missing-indexes.sql`, contenu complet :

```sql
/*  Missing indexes in the current database, beside the indexes that already
    exist on the same tables and how much they are used.

    No parameter. Read-only. Run it with -database <db> -maxrows 70.

    READ THIS BEFORE TRUSTING THE RESULT.

    One result set, three kinds of row, grouped by table:
      context   - exactly one, always first (table_rank 0). Whether any
                  conclusion is possible at all.
      missing   - what the optimizer asked for: at most 8 per table, best
                  score first.
      existing  - what the table already has, heap or clustered included:
                  at most 15 per table, by index_id.
    Only the 3 tables with the highest SUM of suggestion scores are shown.
    The sum is a ranking device, not a gain: near-identical suggestions each
    count. Every table row carries table_suggestion_count and
    table_index_count, so you know what is not shown. The result can never
    exceed 70 rows, so -maxrows 70 never cuts a table block.

    Read the context row first.
      - instance_uptime_days < 1, or collection_capped = 1: no conclusion is
        possible, in either direction. Say so and stop.
      - instance_uptime_days is an UPPER BOUND on what the counters cover. A
        restore, taking the database offline, AUTO_CLOSE (is_auto_close_on)
        or an availability group failover empties this database's counters
        and suggestions without moving the instance start time.
      - collection_capped: Microsoft documents a cap of 600 suggestion
        groups for the whole instance, after which nothing more is
        collected. Older versions are reported to cap at 500, which the
        documentation does not confirm, so the flag trips at 500. A 0 does
        not prove nothing was lost: a cap reached, then freed by later
        clean-ups, leaves no trace.
      - suggestions_on_unresolved_objects > 0: suggestions on objects this
        login cannot see, or that no longer exist. Not shown, not ranked.
      - suggestions_on_system_objects: suggestions on is_ms_shipped objects
        (msdb tables, cdc.*, replication tables) or on objects that are not
        user tables. Excluded on purpose.
      - database_in_ag = 1: counters and suggestions belong to THIS replica.
        An index used only by reports on a readable secondary shows 0 reads
        here.

    What the absence of a suggestion does not prove. Suggestions for a table
    are deleted when its metadata changes (a column added or dropped, an
    index created) and when ALTER INDEX runs on any of its indexes - nightly
    index maintenance included, so a table rebuilt every night is
    under-represented in the ranking. None are made for trivial plans, and
    an eager index spool suppresses the request it stands for.

    What a suggestion is not. The order of equality_columns means nothing:
    compare them as a set. included_columns carries no size analysis.
    avg_user_impact is an estimate of an improvement to an estimate. The
    feature never suggests a unique, filtered, clustered or columnstore
    index. For a memory-optimized table (memory_optimized = 1), ignore
    included_columns: every column is in every memory-optimized index.

    What the existing rows do not say.
      - key_columns and included_columns are the DECLARED columns. Every
        nonclustered index also carries the clustered key (read it on the
        clustered row of the same table), and an undeclared partitioning
        column appears in neither list.
      - filter_definition is NULL for an unfiltered index AND when the login
        may not read it: trust has_filter.
      - user_seeks .. user_updates count operations since the database's
        counters were last emptied (see above), on this replica only. An
        index with no usage row shows 0. usage_not_tracked = 1 (spatial
        index, memory-optimized table) shows NULL: unknown, not unused. A
        rebuild is reported to reset these counters on some 2012 and 2014
        builds; Microsoft does not document it.

    THIS RESULT DOES NOT JUSTIFY DROPPING ANY INDEX.

    No DDL column, on purpose: never paste a suggestion as CREATE INDEX.
    Widen an existing index where one fits; a unique index or primary key is
    widened through INCLUDE only. The reading protocol is
    references/missing-index-reading.md.

    Excluded on purpose: is_ms_shipped objects, hypothetical indexes
    (counted in hypothetical_indexes), other databases.

    Permissions. Missing-index DMVs and sys.dm_db_index_usage_stats: VIEW
    SERVER STATE, or VIEW SERVER PERFORMANCE STATE from SQL Server 2022.
    sys.dm_db_partition_stats: VIEW DATABASE STATE and VIEW DEFINITION on the
    database, or VIEW DATABASE PERFORMANCE STATE and VIEW SECURITY DEFINITION
    from 2022. What happens without them is recorded in
    docs/validation/2026-10-01-missing-indexes.md.

    Written for SQL Server 2012 and later and Azure SQL Managed Instance.
    Tested on SQL Server 2019 only. Azure SQL Database: not tested.

    Dates are returned as text without a time zone, in the server's local
    time: sqlq renders a datetime with a false Z suffix.
*/
WITH
suggestion AS (
    -- Every suggestion of the current database. The grain is the group:
    -- nothing documents that an index_handle belongs to one group only.
    SELECT
        g.index_group_handle,
        d.index_handle,
        d.object_id,
        d.equality_columns,
        d.inequality_columns,
        d.included_columns,
        s.user_seeks,
        s.user_scans,
        s.last_user_seek,
        s.last_user_scan,
        s.avg_user_impact,
        s.unique_compiles,
        CAST(s.avg_total_user_cost AS float)
            * CAST(s.avg_user_impact AS float)
            * CAST(s.user_seeks + s.user_scans AS float) AS score,
        CASE
            WHEN o.object_id IS NULL THEN 'unresolved'
            WHEN o.is_ms_shipped = 1 OR o.type <> 'U' THEN 'system'
            ELSE 'user'
        END AS resolution
    FROM sys.dm_db_missing_index_details AS d
    JOIN sys.dm_db_missing_index_groups AS g
      ON g.index_handle = d.index_handle
    JOIN sys.dm_db_missing_index_group_stats AS s
      ON s.group_handle = g.index_group_handle
    LEFT JOIN sys.objects AS o
      ON o.object_id = d.object_id
    WHERE d.database_id = DB_ID()
),
suggested_table AS (
    -- Ranking and totals over ALL suggestions, before any cap.
    SELECT
        object_id,
        SUM(score) AS table_score,
        COUNT(*)   AS table_suggestion_count
    FROM suggestion
    WHERE resolution = 'user'
    GROUP BY object_id
),
ranked AS (
    SELECT TOP (3)
        object_id,
        table_suggestion_count,
        ROW_NUMBER() OVER (ORDER BY table_score DESC, object_id) AS table_rank
    FROM suggested_table
    ORDER BY table_score DESC, object_id
),
index_count AS (
    SELECT i.object_id, COUNT(*) AS table_index_count
    FROM sys.indexes AS i
    JOIN ranked AS r
      ON r.object_id = i.object_id
    WHERE i.is_hypothetical = 0
    GROUP BY i.object_id
),
missing AS (
    SELECT
        sg.*,
        ROW_NUMBER() OVER (PARTITION BY sg.object_id
                           ORDER BY sg.score DESC, sg.index_group_handle, sg.index_handle)
            AS miss_seq
    FROM suggestion AS sg
    JOIN ranked AS r
      ON r.object_id = sg.object_id
),
existing AS (
    SELECT
        i.object_id,
        i.index_id,
        i.name,
        i.type,
        i.type_desc,
        i.is_unique,
        i.is_primary_key,
        i.is_disabled,
        i.has_filter,
        i.filter_definition,
        ROW_NUMBER() OVER (PARTITION BY i.object_id ORDER BY i.index_id) AS idx_seq
    FROM sys.indexes AS i
    JOIN ranked AS r
      ON r.object_id = i.object_id
    WHERE i.is_hypothetical = 0
),
index_size AS (
    -- Aggregated per index BEFORE the join: one row per partition would
    -- otherwise duplicate the index row.
    SELECT ps.object_id, ps.index_id, SUM(ps.used_page_count) AS used_pages
    FROM sys.dm_db_partition_stats AS ps
    JOIN ranked AS r
      ON r.object_id = ps.object_id
    GROUP BY ps.object_id, ps.index_id
),
context_counts AS (
    SELECT
        (SELECT COUNT(*) FROM sys.dm_db_missing_index_groups)              AS groups_on_instance,
        (SELECT COUNT(*) FROM suggested_table)                             AS tables_with_suggestions,
        (SELECT COUNT(*) FROM suggestion WHERE resolution = 'system')      AS on_system,
        (SELECT COUNT(*) FROM suggestion WHERE resolution = 'unresolved')  AS on_unresolved,
        (SELECT COUNT(*) FROM sys.indexes WHERE is_hypothetical = 1)       AS hypothetical
)
SELECT TOP (70)
    row_kind, table_rank, schema_name, table_name,
    table_suggestion_count, table_index_count, memory_optimized,
    index_name, index_type, is_unique, is_primary_key, is_disabled, has_filter,
    filter_definition, key_columns,
    equality_columns, inequality_columns, included_columns,
    user_seeks, user_scans, user_lookups, user_updates, last_used,
    used_mb, usage_not_tracked,
    avg_user_impact, score, unique_compiles,
    instance_start_time, instance_uptime_days, is_auto_close_on, database_in_ag,
    suggestion_groups_on_instance, collection_capped, tables_with_suggestions,
    suggestions_on_system_objects, suggestions_on_unresolved_objects,
    hypothetical_indexes
FROM (
    SELECT
        CAST('context' AS varchar(8))       AS row_kind,
        CAST(0 AS int)                      AS table_rank,
        CAST(NULL AS sysname)               AS schema_name,
        CAST(NULL AS sysname)               AS table_name,
        CAST(NULL AS int)                   AS table_suggestion_count,
        CAST(NULL AS int)                   AS table_index_count,
        CAST(NULL AS bit)                   AS memory_optimized,
        CAST(NULL AS sysname)               AS index_name,
        CAST(NULL AS nvarchar(60))          AS index_type,
        CAST(NULL AS bit)                   AS is_unique,
        CAST(NULL AS bit)                   AS is_primary_key,
        CAST(NULL AS bit)                   AS is_disabled,
        CAST(NULL AS bit)                   AS has_filter,
        CAST(NULL AS nvarchar(max))         AS filter_definition,
        CAST(NULL AS nvarchar(max))         AS key_columns,
        CAST(NULL AS nvarchar(4000))        AS equality_columns,
        CAST(NULL AS nvarchar(4000))        AS inequality_columns,
        CAST(NULL AS nvarchar(max))         AS included_columns,
        CAST(NULL AS bigint)                AS user_seeks,
        CAST(NULL AS bigint)                AS user_scans,
        CAST(NULL AS bigint)                AS user_lookups,
        CAST(NULL AS bigint)                AS user_updates,
        CAST(NULL AS varchar(19))           AS last_used,
        CAST(NULL AS float)                 AS used_mb,
        CAST(NULL AS bit)                   AS usage_not_tracked,
        CAST(NULL AS float)                 AS avg_user_impact,
        CAST(NULL AS float)                 AS score,
        CAST(NULL AS bigint)                AS unique_compiles,
        CONVERT(varchar(19), si.sqlserver_start_time, 126)
                                            AS instance_start_time,
        ROUND(CAST(DATEDIFF(minute, si.sqlserver_start_time, GETDATE()) AS float) / 1440.0, 1)
                                            AS instance_uptime_days,
        CAST(db.is_auto_close_on AS bit)    AS is_auto_close_on,
        CAST(CASE WHEN db.replica_id IS NULL THEN 0 ELSE 1 END AS bit)
                                            AS database_in_ag,
        CAST(cc.groups_on_instance AS int)  AS suggestion_groups_on_instance,
        CAST(CASE WHEN cc.groups_on_instance >= 500 THEN 1 ELSE 0 END AS bit)
                                            AS collection_capped,
        CAST(cc.tables_with_suggestions AS int) AS tables_with_suggestions,
        CAST(cc.on_system AS int)           AS suggestions_on_system_objects,
        CAST(cc.on_unresolved AS int)       AS suggestions_on_unresolved_objects,
        CAST(cc.hypothetical AS int)        AS hypothetical_indexes,
        0                                   AS kind_order,
        CAST(0 AS bigint)                   AS seq
    FROM sys.dm_os_sys_info AS si
    CROSS JOIN context_counts AS cc
    CROSS JOIN sys.databases AS db
    WHERE db.database_id = DB_ID()

    UNION ALL

    SELECT
        CAST('missing' AS varchar(8)),
        CAST(r.table_rank AS int),
        CAST(sch.name AS sysname),
        CAST(o.name AS sysname),
        CAST(r.table_suggestion_count AS int),
        CAST(ic.table_index_count AS int),
        CAST(CASE WHEN OBJECTPROPERTY(m.object_id, 'TableIsMemoryOptimized') = 1
                  THEN 1 ELSE 0 END AS bit),
        CAST(NULL AS sysname),
        CAST(NULL AS nvarchar(60)),
        CAST(NULL AS bit),
        CAST(NULL AS bit),
        CAST(NULL AS bit),
        CAST(NULL AS bit),
        CAST(NULL AS nvarchar(max)),
        CAST(NULL AS nvarchar(max)),
        CAST(m.equality_columns AS nvarchar(4000)),
        CAST(m.inequality_columns AS nvarchar(4000)),
        CAST(m.included_columns AS nvarchar(max)),
        CAST(m.user_seeks AS bigint),
        CAST(m.user_scans AS bigint),
        CAST(NULL AS bigint),
        CAST(NULL AS bigint),
        CONVERT(varchar(19),
                CASE WHEN m.last_user_seek >= m.last_user_scan OR m.last_user_scan IS NULL
                     THEN m.last_user_seek ELSE m.last_user_scan END, 126),
        CAST(NULL AS float),
        CAST(NULL AS bit),
        CAST(m.avg_user_impact AS float),
        m.score,
        CAST(m.unique_compiles AS bigint),
        CAST(NULL AS varchar(19)),
        CAST(NULL AS float),
        CAST(NULL AS bit),
        CAST(NULL AS bit),
        CAST(NULL AS int),
        CAST(NULL AS bit),
        CAST(NULL AS int),
        CAST(NULL AS int),
        CAST(NULL AS int),
        CAST(NULL AS int),
        1,
        CAST(m.miss_seq AS bigint)
    FROM missing AS m
    JOIN ranked AS r
      ON r.object_id = m.object_id
    JOIN index_count AS ic
      ON ic.object_id = m.object_id
    JOIN sys.objects AS o
      ON o.object_id = m.object_id
    JOIN sys.schemas AS sch
      ON sch.schema_id = o.schema_id
    WHERE m.miss_seq <= 8

    UNION ALL

    SELECT
        CAST('existing' AS varchar(8)),
        CAST(r.table_rank AS int),
        CAST(sch.name AS sysname),
        CAST(o.name AS sysname),
        CAST(r.table_suggestion_count AS int),
        CAST(ic.table_index_count AS int),
        CAST(f.memory_optimized AS bit),
        CAST(e.name AS sysname),
        CAST(e.type_desc AS nvarchar(60)),
        CAST(e.is_unique AS bit),
        CAST(e.is_primary_key AS bit),
        CAST(e.is_disabled AS bit),
        CAST(e.has_filter AS bit),
        CAST(e.filter_definition AS nvarchar(max)),
        CAST(STUFF((
            SELECT N', ' + QUOTENAME(c.name)
                   + CASE WHEN xc.is_descending_key = 1 THEN N' DESC' ELSE N'' END
            FROM sys.index_columns AS xc
            JOIN sys.columns AS c
              ON c.object_id = xc.object_id
             AND c.column_id = xc.column_id
            WHERE xc.object_id = e.object_id
              AND xc.index_id = e.index_id
              AND xc.key_ordinal > 0
            ORDER BY xc.key_ordinal
            FOR XML PATH(''), TYPE).value('.', 'nvarchar(max)'), 1, 2, N'') AS nvarchar(max)),
        CAST(NULL AS nvarchar(4000)),
        CAST(NULL AS nvarchar(4000)),
        CAST(STUFF((
            SELECT N', ' + QUOTENAME(c.name)
            FROM sys.index_columns AS xc
            JOIN sys.columns AS c
              ON c.object_id = xc.object_id
             AND c.column_id = xc.column_id
            WHERE xc.object_id = e.object_id
              AND xc.index_id = e.index_id
              AND xc.is_included_column = 1
            ORDER BY xc.index_column_id
            FOR XML PATH(''), TYPE).value('.', 'nvarchar(max)'), 1, 2, N'') AS nvarchar(max)),
        CAST(CASE WHEN f.untracked = 1 THEN NULL ELSE COALESCE(us.user_seeks, 0) END AS bigint),
        CAST(CASE WHEN f.untracked = 1 THEN NULL ELSE COALESCE(us.user_scans, 0) END AS bigint),
        CAST(CASE WHEN f.untracked = 1 THEN NULL ELSE COALESCE(us.user_lookups, 0) END AS bigint),
        CAST(CASE WHEN f.untracked = 1 THEN NULL ELSE COALESCE(us.user_updates, 0) END AS bigint),
        CASE WHEN f.untracked = 1 THEN NULL ELSE
            CONVERT(varchar(19), (SELECT MAX(v)
                                  FROM (VALUES (us.last_user_seek),
                                               (us.last_user_scan),
                                               (us.last_user_lookup)) AS t(v)), 126)
        END,
        CASE WHEN f.memory_optimized = 1 THEN NULL
             ELSE ROUND(CAST(sz.used_pages AS float) * 8.0 / 1024.0, 2) END,
        CAST(f.untracked AS bit),
        CAST(NULL AS float),
        CAST(NULL AS float),
        CAST(NULL AS bigint),
        CAST(NULL AS varchar(19)),
        CAST(NULL AS float),
        CAST(NULL AS bit),
        CAST(NULL AS bit),
        CAST(NULL AS int),
        CAST(NULL AS bit),
        CAST(NULL AS int),
        CAST(NULL AS int),
        CAST(NULL AS int),
        CAST(NULL AS int),
        2,
        CAST(e.index_id AS bigint)
    FROM existing AS e
    JOIN ranked AS r
      ON r.object_id = e.object_id
    JOIN index_count AS ic
      ON ic.object_id = e.object_id
    JOIN sys.objects AS o
      ON o.object_id = e.object_id
    JOIN sys.schemas AS sch
      ON sch.schema_id = o.schema_id
    CROSS APPLY (
        SELECT
            CASE WHEN OBJECTPROPERTY(e.object_id, 'TableIsMemoryOptimized') = 1
                 THEN 1 ELSE 0 END AS memory_optimized,
            CASE WHEN OBJECTPROPERTY(e.object_id, 'TableIsMemoryOptimized') = 1
                   OR e.type = 4
                 THEN 1 ELSE 0 END AS untracked
    ) AS f
    LEFT JOIN sys.dm_db_index_usage_stats AS us
      ON us.database_id = DB_ID()
     AND us.object_id = e.object_id
     AND us.index_id = e.index_id
    LEFT JOIN index_size AS sz
      ON sz.object_id = e.object_id
     AND sz.index_id = e.index_id
    WHERE e.idx_seq <= 15
) AS result
ORDER BY table_rank, kind_order, seq
OPTION (RECOMPILE, MAXDOP 1);
```

Notes pour l'implémenteur, à ne pas « corriger » :

- `o.type <> 'U'` range dans `system` une suggestion portée par un objet qui n'est pas une
  table utilisateur (vue indexée). L'en-tête le dit. C'est une décision, pas un oubli.
- `ORDER BY table_rank, kind_order, seq` référence deux colonnes absentes de la liste du
  `SELECT` : c'est permis parce que l'union est dans la table dérivée, pas au niveau du
  `SELECT TOP`. `seq` est unique dans chaque couple `(table_rank, kind_order)` : `miss_seq` est
  un `ROW_NUMBER` par table, `index_id` est unique par table. L'ordre est donc total.
- Le `TOP (3)` de `ranked` et son `ROW_NUMBER` utilisent le même `ORDER BY` : le rang 1 est
  bien la table de plus forte somme.
- `GETDATE()` et `sqlserver_start_time` sont tous deux en heure locale du serveur.

- [ ] **Step 4 : lancer les tests, vérifier qu'ils passent**

Run : `cd tools && rtk go test ./internal/sqlq/ -run "TestMissingIndexesQueryContract|TestBundledQueriesPassTheReadOnlyGuard" -v`
Expected : PASS pour `TestMissingIndexesQueryContract` et pour le sous-test
`TestBundledQueriesPassTheReadOnlyGuard/missing-indexes.sql`.

Si `TestMissingIndexesQueryContract` échoue sur une aiguille alors que le texte est présent,
vérifier ce que `Sanitize` fait de la ligne (un crochet ou un littéral blanchi) avant de
toucher au test.

- [ ] **Step 5 : vérifier que le test attrape un retrait**

Retirer temporairement la ligne `WHERE d.database_id = DB_ID()` de la requête, relancer le
test de contrat.
Expected : FAIL, `missing "D.DATABASE_ID = DB_ID()"`. Remettre la ligne, relancer : PASS.
`rtk git diff --stat` ne doit plus montrer de différence sur la requête par rapport au step 3.

- [ ] **Step 6 : suite complète**

Run : `cd tools && rtk go test ./... && rtk go vet ./...`
Expected : PASS, aucun avertissement de `vet`.

- [ ] **Step 7 : commit**

```bash
rtk git add tools/internal/sqlq/missing_indexes_query_test.go plugins/sqlserver-toolkit/skills/live-query/queries/missing-indexes.sql
rtk git commit -m "feat(live-query): a bundled query for missing indexes beside the existing ones

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2 : le skill et son protocole de lecture

**Files :**
- Create : `plugins/sqlserver-toolkit/skills/live-query/references/missing-index-reading.md`
- Modify : `plugins/sqlserver-toolkit/skills/live-query/SKILL.md` (tableau « Decision tree »,
  et la section « What not to do » ou son équivalent)

**Interfaces :**
- Consumes : le nom du fichier `queries/missing-indexes.sql` et les noms de colonnes produits
  par la tâche 1.

- [ ] **Step 1 : écrire le protocole de lecture**

`plugins/sqlserver-toolkit/skills/live-query/references/missing-index-reading.md`, contenu
complet :

```markdown
# Reading missing-indexes.sql

The result answers one question: which indexes to propose on the three tables that ask for
them most. It does not answer which indexes to drop. Read the header of
`queries/missing-indexes.sql` before the rows.

## 1. The context row first

Stop, and say why, if `instance_uptime_days < 1` or `collection_capped = 1`: no conclusion is
possible in either direction.

Say, whenever it holds:

- `is_auto_close_on = 1` or `database_in_ag = 1`: the counters may cover far less than
  `instance_uptime_days`, and on an availability group they describe this replica only;
- `suggestions_on_unresolved_objects > 0`: some suggestions are on objects this login cannot
  see, or that no longer exist;
- `tables_with_suggestions > 3`: only the top three are shown.

## 2. Is the table complete?

For each table, count the `missing` and `existing` rows shown and compare them with
`table_suggestion_count` and `table_index_count`. If indexes are not shown, do not propose a
new index on that table until the missing ones have been listed by another query
(`sys.indexes` for that object).

## 3. Group, then compare

1. Group the table's suggestions with each other: suggestions that share their equality
   columns are usually one index.
2. For each group, look for an existing index whose **leading key columns cover the set** of
   equality columns, in any order: the DMV's order means nothing. Remember the clustered key
   (on the clustered row of the same table): every nonclustered index carries it.
3. Prefer widening that index to creating a new one, with these limits:
   - a unique index or a primary key (`is_unique`, `is_primary_key`) is widened through
     `INCLUDE` only. Adding a key column changes what is unique;
   - a filtered index (`has_filter = 1`) covers only the rows of its filter; if
     `filter_definition` is NULL, the filter is unreadable and coverage is unknown;
   - a disabled index (`is_disabled = 1`) covers nothing.
4. Memory-optimized table (`memory_optimized = 1`): ignore the suggestion's
   `included_columns`.

## 4. Weigh the writes

Before adding an index to a table, compare the clustered or heap row's `user_updates` with the
reads (`user_seeks + user_scans + user_lookups`) of its indexes. These counters cover this
replica only, over a window of at most `instance_uptime_days`.

## 5. Write the proposal

One consolidated proposal per table: which index to widen and how, or which index to create,
and which suggestions it serves. The order of the equality columns is for someone who knows
their selectivity to choose; the result does not carry it. Quote `instance_uptime_days` as an
upper bound, and say that index maintenance clears a table's suggestions.

## Never

- paste a suggestion as `CREATE INDEX`;
- conclude "no index is missing" from an absence of rows;
- recommend dropping an index from this result;
- add a key column to a unique index or a primary key.
```

- [ ] **Step 2 : ajouter la ligne de l'arbre de décision**

Dans le tableau « Decision tree » de `SKILL.md`, juste avant la ligne
`| Why is this query slow | ...`, insérer :

```markdown
| Which indexes are missing in this database | `-file queries/missing-indexes.sql -database <db> -maxrows 70`, then follow `references/missing-index-reading.md`. Read the file's header first. Never paste a suggestion as `CREATE INDEX`, never recommend dropping an index from this result |
```

- [ ] **Step 3 : relire le tableau**

Lire les lignes autour de l'insertion : deux colonnes, même nombre de `|` que les voisines,
aucune ligne vide qui couperait le tableau.

- [ ] **Step 3b : les quatre interdits dans « What not to do »**

Dans la section `## What not to do` de `SKILL.md`, après la puce
`- **Do not invent results.** ...` et avant `- **Do not assume the version.** ...`, insérer :

```markdown
- **Do not turn a missing-index suggestion into DDL.** Never paste a suggestion
  as `CREATE INDEX`; never conclude "no index is missing" from an empty result;
  never recommend dropping an index from `missing-indexes.sql`, whose counters
  cover one replica and a window shorter than the instance uptime; never add a
  key column to a unique index or a primary key. See
  `references/missing-index-reading.md`.
```

- [ ] **Step 4 : vérifier les renvois**

Run : `rtk grep -n "missing-index-reading.md\|missing-indexes.sql" plugins/sqlserver-toolkit/skills/live-query`
Expected : le `SKILL.md` cite les deux fichiers ; l'en-tête de la requête cite
`references/missing-index-reading.md` ; les deux fichiers existent.

- [ ] **Step 5 : commit**

```bash
rtk git add plugins/sqlserver-toolkit/skills/live-query/SKILL.md plugins/sqlserver-toolkit/skills/live-query/references/missing-index-reading.md
rtk git commit -m "docs(live-query): route the missing-index question and say how to read it

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3 : validation sur l'instance de test

Cette tâche ne se délègue pas : chaque écriture demande l'accord de l'utilisateur sur les
instructions exactes. Elle a besoin d'un profil de test fourni par l'utilisateur, en mode
`readwrite`, pointant vers une instance de développement ou de recette (SQL Server 2019
Developer pour la validation précédente). Si le profil est marqué `prod`, s'arrêter.

**Files :**
- Create : `docs/validation/2026-10-01-missing-indexes.md`

Notation : `<dev>` le profil fourni ; `Q` le chemin de la requête.

```bash
Q=plugins/sqlserver-toolkit/skills/live-query/queries/missing-indexes.sql
```

#### Règles communes à tous les cas

- **Une écriture = un appel `sqlq -allow-write`**, précédé du texte exact montré à
  l'utilisateur et de son accord. Un appel peut contenir plusieurs instructions d'un même cas
  (un seul batch, sans `GO`).
- **Une base par groupe de cas**, pour que le classement d'un cas ne dépende pas des tables
  d'un autre : `mi_val_a` (M1, M2, M11), `mi_val_b` (M3, M4), `mi_val_c` (M3b), `mi_val_d`
  (M7, M7b), `mi_val_e` (M6, M6b), `mi_val_other` (M5), `mi_val_f` (M10), `mi_val_g` (M9).
- **Registre** : chaque base et chaque login créés sont notés dès leur création. Le nettoyage
  porte sur ce registre, jamais sur un préfixe.
- **Générer une suggestion** : lancer en lecture (sans `-allow-write`) une requête qu'aucun
  index ne sert, sur une table d'au moins 20 000 lignes pour que le plan ne soit pas trivial.
  Chaque exécution incrémente `user_seeks` ou `user_scans` du groupe.
- **Relire la précondition** avant chaque observation, et consigner l'appel de la requête et
  les colonnes qui portent l'assertion.
- **Arrêt** : un écart entre attendu et observé arrête la série. Corriger la requête (tâche 1),
  relancer les tests Go, puis rejouer **tous** les cas déjà passés. Une correction qui change
  une règle de la spec remonte à l'utilisateur avant d'être faite.
- **Nettoyage après erreur** : si un cas échoue, est refusé ou interrompu, le step 8 s'exécute
  quand même avant de rendre la main.

Table type, utilisée par plusieurs cas (`<t>` son nom, `<n>` le nombre de lignes) :

```sql
CREATE TABLE dbo.<t> (
    id int IDENTITY(1, 1) NOT NULL CONSTRAINT pk_<t> PRIMARY KEY CLUSTERED,
    a int NOT NULL, b int NOT NULL, c int NOT NULL,
    f int NOT NULL, g int NOT NULL, h int NOT NULL, e int NOT NULL,
    d varchar(50) NOT NULL
);
INSERT dbo.<t> (a, b, c, f, g, h, e, d)
SELECT TOP (<n>)
    ABS(CHECKSUM(NEWID())) % 1000, ABS(CHECKSUM(NEWID())) % 100, ABS(CHECKSUM(NEWID())) % 10,
    ABS(CHECKSUM(NEWID())) % 1000, ABS(CHECKSUM(NEWID())) % 1000, ABS(CHECKSUM(NEWID())) % 1000,
    ABS(CHECKSUM(NEWID())) % 1000, 'x'
FROM sys.all_columns AS x CROSS JOIN sys.all_columns AS y;
```

- [ ] **Step 1 : relevé initial et V0 (lecture seule)**

```bash
rtk sqlq -profile <dev> -query "SELECT @@VERSION AS v, CAST(SERVERPROPERTY('EngineEdition') AS int) AS edition, CAST(SERVERPROPERTY('IsHadrEnabled') AS int) AS hadr"
rtk sqlq -profile <dev> -query "SELECT TOP (20) name FROM sys.databases WHERE name LIKE N'mi[_]val[_]%' ORDER BY name"
rtk sqlq -profile <dev> -query "SELECT COUNT(*) AS groups FROM sys.dm_db_missing_index_groups"
rtk sqlq -profile <dev> -file "$Q" -maxrows 70
```

Conditions d'arrêt avant toute écriture : une base `mi_val_*` préexistante (collision,
demander) ; un nombre de groupes déjà à 500 ou plus (les cas de classement deviennent
ininterprétables, le dire).

Le dernier appel est **V0**, lancé dans la base par défaut du profil : aucune erreur ; le champ
`columns` donne les 38 colonnes dans l'ordre de la tâche 1, avec les types `VARCHAR` (row_kind,
last_used, instance_start_time), `INT`, `NVARCHAR`, `BIT`, `BIGINT`, `FLOAT` attendus ; une
ligne `context` en tête ; dans `rows`, aucune valeur numérique entre guillemets.

- [ ] **Step 2 : M1, M2, M11 dans `mi_val_a`**

Écritures (après accord) : `CREATE DATABASE mi_val_a;` puis, avec `-database mi_val_a`, la
table type pour `t1`, `t2`, `t3`, `t4` à 20 000 lignes chacune.

| cas | action | attendu |
|---|---|---|
| M1 | aucune requête sur les tables ; lancer `Q` avec `-database mi_val_a` | une seule ligne, `context` ; `tables_with_suggestions = 0` ; `suggestions_on_system_objects` et `suggestions_on_unresolved_objects` à 0 |
| M2 | lancer en lecture `SELECT TOP (10) id, d FROM dbo.tN WHERE a = 5 AND b = 3` 8 fois sur `t1`, 4 fois sur `t2`, 2 fois sur `t3`, 1 fois sur `t4` ; puis `Q` deux fois | `tables_with_suggestions = 4` ; exactement 3 tables, `t1` rang 1, `t2` rang 2, `t3` rang 3 ; chaque table une ligne `missing` (`equality_columns` contenant `[a]` et `[b]`, `included_columns` contenant `[d]`) et une ligne `existing` (`pk_tN`, `CLUSTERED`, `key_columns = [id]`) ; les deux exécutions rendent des lignes identiques dans le même ordre |
| M11 | `ALTER DATABASE mi_val_a SET AUTO_CLOSE ON;` puis `Q` ; puis `ALTER DATABASE mi_val_a SET AUTO_CLOSE OFF;` | `is_auto_close_on = 1` sur la ligne `context` |

Si M2 classe les tables autrement que par nombre d'exécutions, relever `score`,
`avg_total_user_cost` (requête directe sur `sys.dm_db_missing_index_group_stats`) et consigner :
le classement suit la somme des scores, pas le nombre d'exécutions. Le cas passe si le rang
suit la somme des scores relevée.

- [ ] **Step 3 : M3, M4 dans `mi_val_b` ; M3b dans `mi_val_c`**

`mi_val_b` : table type `m3` (20 000 lignes), puis, dans un même appel :

```sql
CREATE INDEX ix_m3_unused ON dbo.m3 (e);
CREATE INDEX ix_m3_disabled ON dbo.m3 (c);
ALTER INDEX ix_m3_disabled ON dbo.m3 DISABLE;
CREATE INDEX ix_m3_filtered ON dbo.m3 (a) WHERE c = 1;
CREATE INDEX ix_m3_desc ON dbo.m3 (a DESC, b) INCLUDE (d);
CREATE TABLE dbo.[m4&<x] (
    id int IDENTITY(1, 1) NOT NULL CONSTRAINT [pk_m4&<x] PRIMARY KEY CLUSTERED,
    [k&<y] int NOT NULL, [p&<q] int NOT NULL, d varchar(50) NOT NULL
);
INSERT dbo.[m4&<x] ([k&<y], [p&<q], d)
SELECT TOP (20000) ABS(CHECKSUM(NEWID())) % 1000, ABS(CHECKSUM(NEWID())) % 1000, 'x'
FROM sys.all_columns AS x CROSS JOIN sys.all_columns AS y;
CREATE INDEX [ix_m4&<z] ON dbo.[m4&<x] ([k&<y]);
```

Les index sont créés **avant** les requêtes qui génèrent les suggestions : une création
d'index efface les suggestions de la table.

Lectures : sur `m3`, chacune 3 fois : `SELECT TOP (10) id, d FROM dbo.m3 WHERE f = 1`,
`SELECT TOP (10) id, d FROM dbo.m3 WHERE g = 2 AND h > 3`, `SELECT TOP (10) id, a FROM dbo.m3 WHERE h = 4`.
Sur `[m4&<x]`, 3 fois : `SELECT TOP (10) id, d FROM dbo.[m4&<x] WHERE [p&<q] = 7`.

| cas | attendu |
|---|---|
| M3 | table `m3` : `table_suggestion_count = 3`, `table_index_count = 5` ; 3 lignes `missing` ; 5 lignes `existing` dans l'ordre des `index_id` : `pk_m3` puis `ix_m3_unused`, `ix_m3_disabled`, `ix_m3_filtered`, `ix_m3_desc` ; `ix_m3_unused` avec les quatre compteurs à 0 ; `ix_m3_disabled` `is_disabled = 1` ; `ix_m3_filtered` `has_filter = 1` et `filter_definition` contenant `[c]=(1)` ; `ix_m3_desc` `key_columns = [a] DESC, [b]` et `included_columns = [d]` |
| M4 | table `m4&<x` : `table_name = m4&<x` ; ligne `missing` dont `equality_columns` contient `[p&<q]` ; ligne `existing` `ix_m4&<z` avec `key_columns = [k&<y]` ; aucune entité `&amp;` ni `&lt;` dans la sortie |

`mi_val_c` : table `m3b` avec la clé `id` et dix colonnes `c01` à `c10` et seize colonnes
`k01` à `k16`, toutes `int NOT NULL` plus `d varchar(50)`, 20 000 lignes ; seize index
`ix_m3b_k01` … `ix_m3b_k16` sur `kNN` ; puis dix lectures, une par colonne :
`SELECT TOP (10) id, d FROM dbo.m3b WHERE cNN = 1`. Le texte exact des instructions est
écrit à ce moment, sur ce modèle, et montré à l'utilisateur avant exécution.

| cas | attendu |
|---|---|
| M3b | `table_suggestion_count = 10`, `table_index_count = 17` ; 8 lignes `missing`, 15 lignes `existing` (`index_id` 1 à 15) ; `truncated = false` ; `rowcount = 24` |

- [ ] **Step 4 : M5 dans `mi_val_other` ; M6, M6b dans `mi_val_e`**

`mi_val_other` : table type `o1`, 20 000 lignes ; lecture 3 fois :
`SELECT TOP (10) id, d FROM dbo.o1 WHERE a = 5 AND b = 3`. Relever
`suggestion_groups_on_instance` avant et après (avec `-database mi_val_a`).

| cas | attendu |
|---|---|
| M5 | `Q -database mi_val_a` : aucune ligne nommant `o1` ; `suggestion_groups_on_instance` augmenté d'au moins 1 par rapport au relevé précédent |

`mi_val_e` : tables type `r1` et `r2`, 20 000 lignes ; sur chacune, 3 fois :
`SELECT TOP (10) id, d FROM dbo.rN WHERE a = 5 AND b = 3`. Lancer `Q`, relever les lignes
`missing` des deux tables. Puis, un appel par instruction :

| cas | écriture | attendu |
|---|---|---|
| M6 | `ALTER INDEX pk_r1 ON dbo.r1 REBUILD;` | `Q` : plus aucune ligne `missing` pour `r1` ; `r1` n'est plus classée |
| M6b | `ALTER INDEX pk_r2 ON dbo.r2 REORGANIZE;` | observé et consigné tel quel : les suggestions de `r2` restent ou disparaissent. L'en-tête de la requête est complété du constat, et la requête re-testée (tâche 1, step 4) |

- [ ] **Step 5 : M7, M7b dans `mi_val_d`**

```sql
CREATE TABLE dbo.h1 (a int NOT NULL, b int NOT NULL, d varchar(50) NOT NULL);
INSERT dbo.h1 (a, b, d)
SELECT TOP (20000) ABS(CHECKSUM(NEWID())) % 1000, ABS(CHECKSUM(NEWID())) % 100, 'x'
FROM sys.all_columns AS x CROSS JOIN sys.all_columns AS y;
CREATE PARTITION FUNCTION pf_mi (int) AS RANGE RIGHT FOR VALUES (5000, 10000, 15000);
CREATE PARTITION SCHEME ps_mi AS PARTITION pf_mi ALL TO ([PRIMARY]);
CREATE TABLE dbo.p1 (
    id int NOT NULL, a int NOT NULL, b int NOT NULL, d varchar(50) NOT NULL,
    CONSTRAINT pk_p1 PRIMARY KEY CLUSTERED (id) ON ps_mi (id)
);
INSERT dbo.p1 (id, a, b, d)
SELECT TOP (20000) ROW_NUMBER() OVER (ORDER BY (SELECT NULL)),
       ABS(CHECKSUM(NEWID())) % 1000, ABS(CHECKSUM(NEWID())) % 100, 'x'
FROM sys.all_columns AS x CROSS JOIN sys.all_columns AS y;
```

Lectures, 3 fois chacune : `SELECT TOP (10) d FROM dbo.h1 WHERE a = 5 AND b = 3`,
`SELECT TOP (10) d FROM dbo.p1 WHERE a = 5 AND b = 3`.
Relever aussi, en lecture :
`SELECT TOP (10) index_id, COUNT(*) AS partitions, SUM(used_page_count) AS pages FROM sys.dm_db_partition_stats WHERE object_id = OBJECT_ID(N'dbo.p1') GROUP BY index_id ORDER BY index_id`.

| cas | attendu |
|---|---|
| M7 | table `h1` : une ligne `existing` avec `index_type = HEAP`, `index_name` NULL, `key_columns` NULL |
| M7b | table `p1` : une seule ligne `existing` pour `pk_p1` ; `used_mb` = pages relevées × 8 / 1024, à 0,01 près ; `table_index_count = 1` |

- [ ] **Step 6 : M8, M9 (droits)**

Ces cas demandent deux profils de test au nom de logins non sysadmin, créés par l'utilisateur
ou avec son accord. Le mot de passe est choisi par l'utilisateur et placé par lui dans une
variable d'environnement nommée par le profil (`passwordEnv`) : il n'apparaît ni dans la
conversation ni dans le dépôt. Sans ces profils : « non exécuté », et le document de
validation dit quelle garantie n'est pas validée.

Instructions à montrer, mot de passe laissé à l'utilisateur :

```sql
-- base master
CREATE LOGIN mi_val_nostate WITH PASSWORD = N'<choisi par l''utilisateur>', CHECK_POLICY = ON;
CREATE LOGIN mi_val_nodef WITH PASSWORD = N'<choisi par l''utilisateur>', CHECK_POLICY = ON;
GRANT VIEW SERVER STATE TO mi_val_nodef;
-- base mi_val_a
CREATE USER mi_val_nostate FOR LOGIN mi_val_nostate;
ALTER ROLE db_datareader ADD MEMBER mi_val_nostate;
```

Base `mi_val_g` : tables type `v1` et `v2`, 20 000 lignes ; lectures 3 fois chacune
`SELECT TOP (10) id, d FROM dbo.vN WHERE a = 5 AND b = 3` (par `<dev>`) ; puis
`CREATE USER mi_val_nodef FOR LOGIN mi_val_nodef; GRANT SELECT ON dbo.v1 TO mi_val_nodef;`.

| cas | appel | attendu |
|---|---|---|
| M8 | `Q -database mi_val_a` avec le profil de `mi_val_nostate` | erreur (numéro relevé, 300 ou 297 attendu), `sqlq` code 2 ; pas de `rows` |
| M9 | `Q -database mi_val_g` avec le profil de `mi_val_nodef` | consigné tel quel. Acceptable : une erreur franche ; ou `v2` absente des lignes **et** comptée (`suggestions_on_unresolved_objects >= 1`). Inacceptable : `v2` absente sans être comptée, ou des lignes de `v1` aux compteurs ou tailles silencieusement vides |

Si M9 rend des lignes, consigner pour `v1` : `filter_definition`, `used_mb`,
`key_columns`, et comparer aux valeurs relevées par `<dev>`.

- [ ] **Step 7 : M10 dans `mi_val_f` (si l'instance le permet)**

Relever en lecture le répertoire des fichiers de données :
`SELECT TOP (1) physical_name FROM sys.master_files WHERE database_id = DB_ID(N'mi_val_f') AND type = 0`.
Puis, avec `<dir>` ce répertoire :

```sql
ALTER DATABASE mi_val_f ADD FILEGROUP mi_mo CONTAINS MEMORY_OPTIMIZED_DATA;
ALTER DATABASE mi_val_f ADD FILE (NAME = N'mi_mo', FILENAME = N'<dir>\mi_val_f_mo') TO FILEGROUP mi_mo;
```

Puis, avec `-database mi_val_f` :

```sql
CREATE TABLE dbo.mo1 (
    id int NOT NULL PRIMARY KEY NONCLUSTERED,
    a int NOT NULL, b int NOT NULL, d varchar(50) NOT NULL
) WITH (MEMORY_OPTIMIZED = ON, DURABILITY = SCHEMA_AND_DATA);
INSERT dbo.mo1 (id, a, b, d)
SELECT TOP (20000) ROW_NUMBER() OVER (ORDER BY (SELECT NULL)),
       ABS(CHECKSUM(NEWID())) % 1000, ABS(CHECKSUM(NEWID())) % 100, 'x'
FROM sys.all_columns AS x CROSS JOIN sys.all_columns AS y;
```

Lecture 3 fois : `SELECT TOP (10) id, d FROM dbo.mo1 WHERE a = 5 AND b = 3`.

| cas | attendu |
|---|---|
| M10 | si une suggestion apparaît : `memory_optimized = 1` sur les lignes `missing` et `existing` ; `usage_not_tracked = 1`, compteurs, `last_used` et `used_mb` NULL. Si aucune suggestion n'apparaît : consigné, et `mo1` est vérifiée par une requête directe sur `sys.indexes` (non classée, donc absente du résultat) |

- [ ] **Step 8 : nettoyage**

Un appel par instruction, sous accord, dans cet ordre, pour chaque entrée du registre :
`DROP USER` dans les bases encore présentes ; `DROP LOGIN mi_val_nostate;`,
`DROP LOGIN mi_val_nodef;` ; `ALTER DATABASE <base> SET SINGLE_USER WITH ROLLBACK IMMEDIATE;`
puis `DROP DATABASE <base>;` pour chaque base du registre. Puis relancer les requêtes du
step 1 et comparer : aucune base `mi_val_*`, aucun login `mi_val_*`
(`SELECT TOP (10) name FROM sys.server_principals WHERE name LIKE N'mi[_]val[_]%'`).
Les profils de test et leurs variables d'environnement sont à retirer par l'utilisateur ; le
document de validation le rappelle.

- [ ] **Step 9 : document de validation**

`docs/validation/2026-10-01-missing-indexes.md` : version et édition du moteur (sans nom de
serveur) ; un tableau `cas | précondition relue | attendu | observé | verdict` ; le constat
M6b et la phrase reprise dans l'en-tête ; l'observation M8 et M9 (numéros d'erreur, ou
comptage) ; la taille JSON du plus grand résultat observé, en caractères, et son nombre de
lignes ; les cas **non exécutés** avec leur raison (`collection_capped`, `database_in_ag = 1`,
M10 si refusé) ; les versions et moteurs non testés. Aucun nom réel.

- [ ] **Step 10 : contrôle final et commit**

Run : `cd tools && rtk go test ./... && rtk go vet ./...` — Expected : PASS.

```bash
rtk git add docs/validation/2026-10-01-missing-indexes.md plugins/sqlserver-toolkit/skills/live-query/queries/missing-indexes.sql
rtk git commit -m "test(live-query): validate the missing-indexes query on a test instance

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
