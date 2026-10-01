# Contrôle de la trace `blocked_process_report` — plan d'implémentation

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal :** livrer une requête en lecture seule, `blocked-processes-check.sql`, lancée par
`sqlq -file`, qui dit si une instance collecte les rapports de processus bloqués dans un fichier
et continuera après un redémarrage.

**Architecture :** un seul fichier SQL dans la bibliothèque de requêtes du skill `live-query`,
une ligne dans l'arbre de décision du skill, un durcissement du test Go qui valide les requêtes
livrées, et une validation sur une instance de dev consignée dans un document versionné.
Aucun changement de `sqlq` lui-même.

**Tech Stack :** T-SQL (SQL Server 2012+ et Azure SQL Managed Instance), Go 1.x (`go test`),
Markdown.

**Spec :** `docs/superpowers/specs/2026-10-01-blocked-processes-check-design.md` — à lire en
entier avant la première tâche. Relecture adversariale :
`docs/superpowers/specs/2026-10-01-blocked-processes-check-design-codex.md`.

## Global Constraints

- Un seul batch : pas de `GO`, pas de `USE`, pas d'`EXEC`, pas de SQL dynamique.
- Pas de `STRING_AGG` : agrégation par `FOR XML PATH('')` + `, TYPE).value('.', 'nvarchar(max)')`.
- Aucun paramètre.
- Un seul jeu de résultats ; au plus 20 lignes de session (`TOP (20)`).
- États : exactement `OK`, `NOT_OK`, `UNKNOWN`. Raisons séparées par `; `.
- Événement identifié par `package = 'sqlserver'` et `name = 'blocked_process_report'` ; cible
  exigée `package0.event_file`.
- Le dépôt est public : aucun nom de client, d'instance, de profil, de base ni de chemin réel,
  ni dans le code, ni dans les tests, ni dans le document de validation, ni dans les commits.
- Écrire sur l'instance de dev (poser les états de test) demande l'accord explicite de
  l'utilisateur **sur l'instruction exacte**, à chaque fois. Ne jamais passer `-allow-write`
  sans cet accord.
- Messages de commit terminés par `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Review Focus

1. **Login sans le droit requis** → une ligne `UNKNOWN`, jamais `no session`. Couvert par le
   cas V15 de la tâche 4.
2. **Plus de 20 candidates, la conforme triée en dernier par nom** → `instance_state = OK`,
   `details_incomplete = 1`. Couvert par le cas V14 de la tâche 4.
3. **Conflit de collation entre `sys.dm_xe_sessions.name` et
   `sys.server_event_sessions.name`** sur une instance à collation serveur non standard → la
   jointure est écrite avec `COLLATE DATABASE_DEFAULT` des deux côtés (tâche 2) ; le cas V0
   l'exécute.
4. **Seuil modifié sans `RECONFIGURE`, dans les deux sens** → `NOT_OK` avec la raison
   « pending ». Cas V2 et V6 de la tâche 4, préconditions vérifiées avant mesure.
5. **Nom de session avec `&`, `<` et un caractère non ASCII** → nom et `targets` intacts. Cas
   V12 de la tâche 4.

---

## Fichiers

| fichier | rôle |
|---|---|
| `tools/internal/sqlq/bundled_queries_test.go` (modifié) | refuse aussi une requête livrée contenant `GO` ou `USE`, que `sqlq` refuse à l'exécution |
| `plugins/sqlserver-toolkit/skills/live-query/queries/blocked-processes-check.sql` (créé) | la requête et ses règles de lecture |
| `plugins/sqlserver-toolkit/skills/live-query/SKILL.md` (modifié) | une ligne dans l'arbre de décision |
| `docs/validation/2026-10-01-blocked-processes-check.md` (créé) | résultats de la validation sur instance, sans nom réel |

---

### Task 1 : le test des requêtes livrées refuse aussi `GO` et `USE`

`sqlq` refuse à l'exécution un batch contenant `GO` (`FindBatchSeparators`) ou `USE`
(`FindContextChanges`), mais le test des requêtes livrées ne vérifie que `FindWrites`. Une
requête livrée qui contiendrait l'un ou l'autre passerait le test et serait refusée devant
l'instance. Même raison d'être que le test existant.

**Files :**
- Modify : `tools/internal/sqlq/bundled_queries_test.go`

**Interfaces :**
- Consumes : `FindWrites(sql string) []WriteViolation`, `FindBatchSeparators(sql string) []int`,
  `FindContextChanges(sql string) []WriteViolation` (dans `tools/internal/sqlq/guard.go`).
- Produces : rien pour les tâches suivantes, sinon que la tâche 2 doit passer ce test.

- [ ] **Step 1 : écrire le test qui échoue**

Ajouter dans `bundled_queries_test.go` un test d'une fonction `refusals` qui n'existe pas
encore :

```go
func TestRefusalsCatchesWhatSqlqRefuses(t *testing.T) {
	cases := map[string]string{
		"write":     "DELETE FROM dbo.T;",
		"use":       "USE master;\nSELECT 1;",
		"separator": "SELECT 1;\nGO\nSELECT 2;",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if len(refusals(body)) == 0 {
				t.Errorf("refusals(%q) found nothing; sqlq would refuse it", body)
			}
		})
	}
	if r := refusals("SELECT TOP (1) name FROM sys.objects;"); len(r) > 0 {
		t.Errorf("a plain SELECT was refused: %v", r)
	}
}
```

- [ ] **Step 2 : le lancer, il doit échouer à la compilation**

Run : `cd tools && go test ./internal/sqlq/ -run TestRefusalsCatchesWhatSqlqRefuses`
Expected : FAIL, `undefined: refusals`.

- [ ] **Step 3 : écrire `refusals` et y brancher le test existant**

Dans le même fichier :

```go
// refusals lists why sqlq would refuse to run this text, in the order sqlq
// checks: writes, then context changes, then batch separators.
func refusals(body string) []string {
	var out []string
	if v := FindWrites(body); len(v) > 0 {
		out = append(out, fmt.Sprintf("statement %q uses %s", v[0].Statement, v[0].Keyword))
	}
	if c := FindContextChanges(body); len(c) > 0 {
		out = append(out, fmt.Sprintf("statement %q uses %s", c[0].Statement, c[0].Keyword))
	}
	if lines := FindBatchSeparators(body); len(lines) > 0 {
		out = append(out, fmt.Sprintf("GO batch separator on line %d", lines[0]))
	}
	return out
}
```

Ajouter `"fmt"` aux imports. Puis, dans `TestBundledQueriesPassTheReadOnlyGuard`, remplacer le
bloc `if v := FindWrites(...)` par :

```go
			if r := refusals(string(body)); len(r) > 0 {
				t.Errorf("sqlq would refuse this bundled query: %s", strings.Join(r, "; "))
			}
```

Ajouter `"strings"` aux imports.

- [ ] **Step 4 : lancer tout le paquet**

Run : `cd tools && go test ./internal/sqlq/ && go vet ./...`
Expected : PASS. Les cinq requêtes livrées existantes passent toujours.

- [ ] **Step 5 : commit**

```bash
git add tools/internal/sqlq/bundled_queries_test.go
git commit -m "test(sqlq): a bundled query must pass every check sqlq runs, not just the write guard"
```

---

### Task 2 : la requête `blocked-processes-check.sql`

**Files :**
- Create : `plugins/sqlserver-toolkit/skills/live-query/queries/blocked-processes-check.sql`
- Test : `tools/internal/sqlq/bundled_queries_test.go` (inchangé, couvre le nouveau fichier)

**Interfaces :**
- Consumes : le test de la tâche 1.
- Produces : les colonnes, dans cet ordre, que la tâche 3 documente et que la tâche 4 vérifie :
  `instance_state, server_name, required_permission, has_permission, threshold_value,
  threshold_value_in_use, candidate_count, details_incomplete, is_hadr_enabled, ag_replicas,
  session_name, session_state, reasons, startup_state, is_running, event_in_running_session,
  event_predicate, file_target_defined, file_target_running, targets`.

- [ ] **Step 1 : vérifier que le test voit le nouveau fichier**

Créer le fichier avec seulement `SELECT 1 AS probe;`, lancer
`cd tools && go test ./internal/sqlq/ -run TestBundledQueriesPassTheReadOnlyGuard -v` et
vérifier qu'un sous-test `blocked-processes-check.sql` apparaît et passe.

- [ ] **Step 2 : écrire la requête**

Remplacer le contenu par :

```sql
/*  Is the blocked process report actually being collected on this instance?

    No parameter. Read-only. One row per event session that captures
    sqlserver.blocked_process_report (at most 20), or one row with NULL
    session columns when there is none or when the login cannot see them.

    READ instance_state, NOT THE ROWS.
      OK      - at the time of the check, this instance writes blocked process
                reports to an event_file target, and will again after a
                restart.
      NOT_OK  - it does not. A row with a session_name is a session to repair,
                never a reason to add a second session.
      UNKNOWN - neither yes nor no: a permission is missing, the threshold
                could not be read, or the event carries a predicate that only
                a human can judge. Say so; do not round it to yes or no.

    What OK does NOT say:
      - that every block will be reported. The threshold is a minimum duration,
        and the monitor runs about every five seconds, best effort.
      - that a session with MAX_DURATION (SQL Server 2025, Managed Instance)
        will still run tomorrow. Not checked.
      - anything about another instance. In an availability group every
        replica needs its own session: run this on every replica, and coverage
        is complete only when each name in ag_replicas has come back as the
        server_name of an OK check. Two profiles returning the same
        server_name are one instance. is_hadr_enabled = 1 with ag_replicas
        NULL means coverage is unknown, not complete.

    details_incomplete = 1 means more than 20 sessions matched; instance_state
    was still computed over all of them.

    Permissions: VIEW SERVER PERFORMANCE STATE where it exists (2022+,
    Managed Instance; VIEW SERVER STATE implies it), VIEW SERVER STATE before.
    Azure SQL Database has no server-scoped sessions: this query fails there
    with an error, which is the intended outcome.

    Runs on SQL Server 2012 and later. FOR XML PATH rather than STRING_AGG.
*/
WITH perm AS (
    SELECT
        CASE WHEN HAS_PERMS_BY_NAME(NULL, NULL, N'VIEW SERVER PERFORMANCE STATE') IS NULL
             THEN N'VIEW SERVER STATE'
             ELSE N'VIEW SERVER PERFORMANCE STATE' END              AS required_permission,
        COALESCE(HAS_PERMS_BY_NAME(NULL, NULL, N'VIEW SERVER PERFORMANCE STATE'),
                 HAS_PERMS_BY_NAME(NULL, NULL, N'VIEW SERVER STATE'),
                 0)                                                 AS has_permission
),
cfg AS (
    -- An aggregate without GROUP BY always returns one row, NULLs if the
    -- setting is missing.
    SELECT MAX(CAST(c.value        AS int)) AS threshold_value,
           MAX(CAST(c.value_in_use AS int)) AS threshold_value_in_use
    FROM sys.configurations AS c
    WHERE c.name = N'blocked process threshold (s)'
),
thr AS (
    SELECT cfg.threshold_value,
           cfg.threshold_value_in_use,
           CASE WHEN cfg.threshold_value IS NULL OR cfg.threshold_value_in_use IS NULL
                     THEN N'UNKNOWN'
                WHEN cfg.threshold_value_in_use = 0 OR cfg.threshold_value = 0
                     THEN N'NOT_OK'
                ELSE N'OK' END                                      AS threshold_state,
           CASE WHEN cfg.threshold_value IS NULL OR cfg.threshold_value_in_use IS NULL
                     THEN N'threshold unknown'
                WHEN cfg.threshold_value_in_use = 0 AND cfg.threshold_value = 0
                     THEN N'threshold=0'
                WHEN cfg.threshold_value_in_use = 0
                     THEN N'threshold set but not in use (RECONFIGURE pending)'
                WHEN cfg.threshold_value = 0
                     THEN N'threshold disable pending (next RECONFIGURE turns reports off)'
           END                                                      AS threshold_reason
    FROM cfg
),
cand AS (
    -- One row per session, chosen by EXISTS so that several targets or
    -- events never multiply it. Nothing is read when the permission is
    -- missing: the verdict must not rest on a view that may come back empty.
    SELECT s.event_session_id,
           s.name          AS session_name,
           s.startup_state
    FROM sys.server_event_sessions AS s
    CROSS JOIN perm
    WHERE perm.has_permission = 1
      AND EXISTS (SELECT 1
                  FROM sys.server_event_session_events AS e
                  WHERE e.event_session_id = s.event_session_id
                    AND e.package = N'sqlserver'
                    AND e.name    = N'blocked_process_report')
),
detail AS (
    SELECT c.session_name,
           c.startup_state,
           CASE WHEN xs.address IS NULL THEN 0 ELSE 1 END           AS is_running,
           CASE WHEN EXISTS (SELECT 1
                             FROM sys.dm_xe_session_events AS xe
                             JOIN sys.dm_xe_packages AS p
                               ON p.guid = xe.event_package_guid
                             WHERE xe.event_session_address = xs.address
                               AND xe.event_name = N'blocked_process_report'
                               AND p.name        = N'sqlserver')
                THEN 1 ELSE 0 END                                   AS event_in_running_session,
           (SELECT TOP (1) e.predicate
              FROM sys.server_event_session_events AS e
             WHERE e.event_session_id = c.event_session_id
               AND e.package = N'sqlserver'
               AND e.name    = N'blocked_process_report'
             ORDER BY e.event_id)                                   AS event_predicate,
           CASE WHEN EXISTS (SELECT 1
                             FROM sys.server_event_session_targets AS t
                             WHERE t.event_session_id = c.event_session_id
                               AND t.package = N'package0'
                               AND t.name    = N'event_file')
                THEN 1 ELSE 0 END                                   AS file_target_defined,
           CASE WHEN EXISTS (SELECT 1
                             FROM sys.dm_xe_session_targets AS xt
                             WHERE xt.event_session_address = xs.address
                               AND xt.target_name = N'event_file')
                THEN 1 ELSE 0 END                                   AS file_target_running,
           STUFF((SELECT N', ' + t.name
                    FROM sys.server_event_session_targets AS t
                   WHERE t.event_session_id = c.event_session_id
                   ORDER BY t.name
                     FOR XML PATH(''), TYPE).value('.', 'nvarchar(max)'), 1, 2, N'')
                                                                    AS targets
    FROM cand AS c
    LEFT JOIN sys.dm_xe_sessions AS xs
      ON xs.name COLLATE DATABASE_DEFAULT = c.session_name COLLATE DATABASE_DEFAULT
),
judged AS (
    SELECT d.*,
           CASE WHEN thr.threshold_state = N'NOT_OK'
                  OR d.startup_state = 0
                  OR d.is_running = 0
                  OR (d.is_running = 1 AND d.event_in_running_session = 0)
                  OR d.file_target_defined = 0
                  OR (d.is_running = 1 AND d.file_target_defined = 1 AND d.file_target_running = 0)
                     THEN N'NOT_OK'
                -- NULL never reaches OK: every unknown is named here.
                WHEN thr.threshold_state = N'UNKNOWN'
                  OR d.startup_state IS NULL
                  OR d.event_predicate IS NOT NULL
                     THEN N'UNKNOWN'
                ELSE N'OK' END                                      AS session_state,
           -- STUFF on an empty string returns NULL: reasons is NULL when OK.
           STUFF(COALESCE(N'; ' + thr.threshold_reason, N'')
               + CASE WHEN d.startup_state = 0 THEN N'; startup_state=OFF' ELSE N'' END
               + CASE WHEN d.is_running = 0 THEN N'; not running' ELSE N'' END
               + CASE WHEN d.is_running = 1 AND d.event_in_running_session = 0
                      THEN N'; event not in running session' ELSE N'' END
               + CASE WHEN d.file_target_defined = 0 THEN N'; no file target' ELSE N'' END
               + CASE WHEN d.is_running = 1 AND d.file_target_defined = 1 AND d.file_target_running = 0
                      THEN N'; file target not running' ELSE N'' END
               + CASE WHEN d.event_predicate IS NOT NULL
                      THEN N'; event filtered by predicate' ELSE N'' END,
                 1, 2, N'')                                         AS reasons
    FROM detail AS d
    CROSS JOIN thr
),
inst AS (
    SELECT perm.required_permission,
           perm.has_permission,
           thr.threshold_value,
           thr.threshold_value_in_use,
           thr.threshold_state,
           thr.threshold_reason,
           agg.candidate_count,
           CASE WHEN perm.has_permission <> 1           THEN N'UNKNOWN'
                WHEN agg.any_ok = 1                     THEN N'OK'
                WHEN agg.any_unknown = 1
                  OR thr.threshold_state = N'UNKNOWN'   THEN N'UNKNOWN'
                ELSE N'NOT_OK' END                                  AS instance_state,
           CASE WHEN agg.candidate_count > 20 THEN 1 ELSE 0 END     AS details_incomplete,
           CAST(SERVERPROPERTY('IsHadrEnabled') AS int)             AS is_hadr_enabled,
           CASE WHEN CAST(SERVERPROPERTY('IsHadrEnabled') AS int) = 1
                THEN STUFF((SELECT N', ' + ar.replica_server_name
                              FROM sys.availability_replicas AS ar
                             GROUP BY ar.replica_server_name
                             ORDER BY ar.replica_server_name
                               FOR XML PATH(''), TYPE).value('.', 'nvarchar(max)'), 1, 2, N'')
           END                                                      AS ag_replicas
    FROM perm
    CROSS JOIN thr
    CROSS JOIN (SELECT COUNT(*)                                                     AS candidate_count,
                       MAX(CASE WHEN session_state = N'OK'      THEN 1 ELSE 0 END) AS any_ok,
                       MAX(CASE WHEN session_state = N'UNKNOWN' THEN 1 ELSE 0 END) AS any_unknown
                  FROM judged) AS agg
),
shown AS (
    SELECT TOP (20)
           j.session_name, j.session_state, j.reasons, j.startup_state, j.is_running,
           j.event_in_running_session, j.event_predicate, j.file_target_defined,
           j.file_target_running, j.targets,
           CASE j.session_state WHEN N'OK' THEN 0 WHEN N'UNKNOWN' THEN 1 ELSE 2 END AS sort_state
    FROM judged AS j
    ORDER BY sort_state, j.session_name
),
sentinel AS (
    -- The single row when there is no candidate to show. Session columns are
    -- not applicable, so only "no session" (or the missing permission) is
    -- reported - no invented session defects.
    SELECT CAST(NULL AS sysname)                                    AS session_name,
           CASE WHEN inst.has_permission <> 1 THEN N'UNKNOWN' ELSE N'NOT_OK' END
                                                                    AS session_state,
           CASE WHEN inst.has_permission <> 1
                THEN N'missing ' + inst.required_permission
                ELSE COALESCE(inst.threshold_reason + N'; ', N'') + N'no session' END
                                                                    AS reasons,
           CAST(NULL AS bit)                                        AS startup_state,
           CAST(NULL AS int)                                        AS is_running,
           CAST(NULL AS int)                                        AS event_in_running_session,
           CAST(NULL AS nvarchar(3000))                             AS event_predicate,
           CAST(NULL AS int)                                        AS file_target_defined,
           CAST(NULL AS int)                                        AS file_target_running,
           CAST(NULL AS nvarchar(max))                              AS targets,
           3                                                        AS sort_state
    FROM inst
    WHERE inst.candidate_count = 0
)
SELECT TOP (21)
       inst.instance_state,
       @@SERVERNAME                                                 AS server_name,
       inst.required_permission,
       inst.has_permission,
       inst.threshold_value,
       inst.threshold_value_in_use,
       inst.candidate_count,
       inst.details_incomplete,
       inst.is_hadr_enabled,
       inst.ag_replicas,
       r.session_name,
       r.session_state,
       r.reasons,
       r.startup_state,
       r.is_running,
       r.event_in_running_session,
       r.event_predicate,
       r.file_target_defined,
       r.file_target_running,
       r.targets
FROM (SELECT * FROM shown UNION ALL SELECT * FROM sentinel) AS r
CROSS JOIN inst
ORDER BY r.sort_state, r.session_name;
```

Notes pour l'implémenteur :
- `sentinel` existe seulement quand `candidate_count = 0` ; sans le droit, `cand` est vide, donc
  la ligne unique est `UNKNOWN` / `missing <droit>`, sans raison de seuil.
- `file target not running` n'est émis que si une cible `event_file` est **définie** : sans
  elle, `no file target` dit déjà tout, et répéter le défaut sous deux libellés brouillerait la
  lecture. C'est la seule précision apportée à la table de règles de la spec.
- Le `TOP (21)` final est une borne de forme exigée par les conventions ; il ne peut jamais
  couper (20 lignes de session au plus, ou une sentinelle seule).
- Si `sys.server_event_session_events.predicate` n'est pas `nvarchar(3000)` sur la version
  testée, aligner le `CAST` de la sentinelle sur le type réel (la tâche 4 le révèle par une
  erreur de conversion ou une colonne tronquée).

- [ ] **Step 3 : lancer le test des requêtes livrées**

Run : `cd tools && go test ./internal/sqlq/ -run TestBundledQueriesPassTheReadOnlyGuard -v`
Expected : PASS pour `blocked-processes-check.sql`. Si le garde refuse un mot de l'en-tête
(il retire normalement les commentaires), reformuler le commentaire, jamais le garde.

- [ ] **Step 4 : commit**

```bash
git add plugins/sqlserver-toolkit/skills/live-query/queries/blocked-processes-check.sql
git commit -m "feat(live-query): a bundled check for the blocked process trace"
```

La requête n'est pas déclarée fonctionnelle à ce stade : seule la tâche 4 l'établit.

---

### Task 3 : le skill renvoie vers la requête

**Files :**
- Modify : `plugins/sqlserver-toolkit/skills/live-query/SKILL.md` (tableau « Decision tree »)

**Interfaces :**
- Consumes : le nom du fichier et la colonne `instance_state` de la tâche 2.

- [ ] **Step 1 : ajouter la ligne**

Dans le tableau « Decision tree », juste avant la ligne `| Why is this query slow | ...`,
insérer :

```markdown
| Is the blocked process trace in place, will blocking be captured | `-file queries/blocked-processes-check.sql`. Answer from `instance_state`, not from the rows, and read the file's header first: what OK does not promise, and how to cover an availability group |
```

- [ ] **Step 2 : vérifier le rendu**

Run : `grep -n "blocked-processes-check" plugins/sqlserver-toolkit/skills/live-query/SKILL.md`
Expected : une ligne, dans le tableau (le nombre de `|` est celui des lignes voisines).

- [ ] **Step 3 : commit**

```bash
git add plugins/sqlserver-toolkit/skills/live-query/SKILL.md
git commit -m "docs(live-query): route the blocked process trace question to its bundled check"
```

---

### Task 4 : validation sur l'instance de dev

Cette tâche ne se délègue pas à un sous-agent : chaque écriture demande l'accord de
l'utilisateur sur l'instruction exacte. Elle a besoin du nom du profil de dev, fourni par
l'utilisateur, en mode `readwrite`, et de son accord avant la première requête si le profil
est marqué `prod`.

**Files :**
- Create : `docs/validation/2026-10-01-blocked-processes-check.md`

Commandes, avec `<dev>` le profil fourni et `Q` le chemin de la requête :

```bash
Q=plugins/sqlserver-toolkit/skills/live-query/queries/blocked-processes-check.sql
sqlq -profile <dev> -query "SELECT @@VERSION AS v, SERVERPROPERTY('EngineEdition') AS e"
sqlq -profile <dev> -file "$Q"
```

Pour chaque cas : (a) noter l'état initial avec la requête ; (b) montrer à l'utilisateur
l'instruction exacte qui pose l'état, attendre son accord, l'exécuter avec `-allow-write` ;
(c) vérifier la précondition ; (d) relancer la requête ; (e) consigner attendu / observé.
Les sessions de test s'appellent `bpr_test_*`, et chacune est supprimée en fin de tâche, avec
le même accord. Le seuil est remis à sa valeur initiale.

Instructions de pose de référence (une par appel `sqlq`, `sp_configure` + `RECONFIGURE`
tiennent dans un batch) :

```sql
EXEC sys.sp_configure N'show advanced options', 1; RECONFIGURE;
EXEC sys.sp_configure N'blocked process threshold (s)', 10; RECONFIGURE;
CREATE EVENT SESSION [bpr_test_a] ON SERVER
    ADD EVENT sqlserver.blocked_process_report
    ADD TARGET package0.event_file (SET filename = N'bpr_test_a', max_file_size = (5), max_rollover_files = (2))
    WITH (STARTUP_STATE = OFF);
ALTER EVENT SESSION [bpr_test_a] ON SERVER STATE = START;
ALTER EVENT SESSION [bpr_test_a] ON SERVER WITH (STARTUP_STATE = ON);
DROP EVENT SESSION [bpr_test_a] ON SERVER;
```

- [ ] **Step 1 : V0 — exécution à vide**, état de l'instance tel quel. Attendu : pas d'erreur
  (en particulier pas d'erreur de collation), une forme de résultat conforme à la tâche 2.
- [ ] **Step 2 : cas V1 à V17**

| cas | état posé | attendu |
|---|---|---|
| V1 | seuil 0/0, aucune session candidate | `NOT_OK`, `threshold=0; no session` |
| V2 | `value` 10, `value_in_use` 0 (précondition : `sp_configure` sans `RECONFIGURE`) | `NOT_OK`, `threshold set but not in use (RECONFIGURE pending)` |
| V3 | seuil 10/10, `bpr_test_a` avec `event_file`, OFF, arrêtée | `NOT_OK`, `startup_state=OFF; not running` |
| V4 | V3 démarrée | `NOT_OK`, `startup_state=OFF` |
| V5 | V4 avec `STARTUP_STATE = ON` | `OK`, `reasons` NULL |
| V6 | `value` 0, `value_in_use` 10 (précondition vérifiée) | `NOT_OK`, `threshold disable pending…` |
| V7 | `bpr_test_rb` à `ring_buffer` seul, ON, démarrée | `NOT_OK`, `no file target` |
| V8 | `bpr_test_nt` sans cible, ON, démarrée | `NOT_OK`, `no file target` |
| V9 | `bpr_test_pr` avec `WHERE (sqlserver.database_id = 1)`, `event_file`, ON, démarrée | `UNKNOWN`, `event filtered by predicate`, prédicat affiché |
| V10 | `bpr_test_mt` avec `event_file` **et** `ring_buffer` | une seule ligne, `targets = event_file, ring_buffer` |
| V11 | session nommée sans rapport avec le script de pose | détectée |
| V12 | session nommée `bpr_test_&<é` | `session_name` intact, aucune erreur XML |
| V13 | deux sessions, une seule `OK` | `instance_state = OK`, ligne `OK` en premier |
| V14 | 21 sessions `bpr_test_00` … `bpr_test_20`, seule `bpr_test_zz` conforme (triée en dernier par nom) | `instance_state = OK`, `candidate_count = 22`, `details_incomplete = 1`, la ligne `OK` présente |
| V15 | login sans le droit requis (fourni par l'utilisateur, ou `EXECUTE AS` refusé : à défaut, cas non exécuté) | ligne unique `UNKNOWN`, `missing <droit>` |
| V16 | 2022+ : login avec `VIEW SERVER PERFORMANCE STATE` seul | verdict normal |
| V17 | login avec le droit et un `DENY` explicite | `UNKNOWN` |

V15 à V17 demandent un login de test : les poser seulement si l'utilisateur en fournit un ou
accepte sa création ; sinon, cas « non exécuté », avec la raison. Si l'instance n'est pas en
AG, le contrôle de `ag_replicas` est « non exécuté ».

- [ ] **Step 3 : nettoyage** — supprimer les sessions `bpr_test_*`, remettre le seuil initial,
  relancer la requête et constater l'état initial noté en V0.
- [ ] **Step 4 : écrire le document de validation**

`docs/validation/2026-10-01-blocked-processes-check.md`, avec : version et édition du moteur
(sans nom de serveur), puis un tableau `cas | précondition vérifiée | attendu | observé |
verdict`, puis la liste des cas non exécutés avec leur raison, puis les cas exclus par la spec
(`event not in running session`, `MAX_DURATION`). Aucun nom réel.

- [ ] **Step 5 : corriger si un cas échoue**

Un écart entre attendu et observé est une correction de la requête (tâche 2) et un nouveau
passage de **tous** les cas déjà validés, pas seulement du cas fautif. Une correction qui change
une règle de la spec remonte à l'utilisateur avant d'être faite.

- [ ] **Step 6 : commit**

```bash
git add docs/validation/2026-10-01-blocked-processes-check.md plugins/sqlserver-toolkit/skills/live-query/queries/blocked-processes-check.sql
git commit -m "test(live-query): validate the blocked process check on a dev instance"
```
