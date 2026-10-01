# Contrôle de la trace `blocked_process_report` — plan d'implémentation

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal :** livrer une requête en lecture seule, `blocked-processes-check.sql`, lancée par
`sqlq -file`, qui dit si tout ce qu'il faut pour qu'une instance écrive les rapports de
processus bloqués dans un fichier est en place, en marche, et le restera après un redémarrage.

**Architecture :** un seul fichier SQL dans la bibliothèque de requêtes du skill `live-query`
(un batch : deux `DECLARE`, puis un `IF` dont une seule branche émet le résultat), une ligne
dans l'arbre de décision du skill, un durcissement du test Go qui valide les requêtes livrées,
et une validation sur une instance de dev consignée dans un document versionné. Aucun
changement de `sqlq` lui-même.

**Tech Stack :** T-SQL (SQL Server 2012+ et Azure SQL Managed Instance), Go (`go test`),
Markdown.

**Spec :** `docs/superpowers/specs/2026-10-01-blocked-processes-check-design.md` — à lire en
entier avant la première tâche. Relectures : `…-design-codex.md` (spec) et
`docs/superpowers/plans/2026-10-01-blocked-processes-check-codex.md` (première version de ce
plan) ; leur traitement est en §9 de la spec.

## Global Constraints

- Un seul batch : pas de `GO`, pas de `USE`, pas d'`EXEC`, pas de SQL dynamique.
- Pas de `STRING_AGG` : agrégation par `FOR XML PATH('')` + `, TYPE).value('.', 'nvarchar(max)')`.
- Aucun paramètre. Un seul jeu de résultats ; au plus 20 lignes de session.
- États : exactement `OK`, `NOT_OK`, `UNKNOWN`. Raisons séparées par `; `.
- Événement identifié par `package = 'sqlserver'` et `name = 'blocked_process_report'` ; cible
  exigée `package0.event_file`, définie et active.
- Ne jamais lire `sys.dm_xe_session_targets` : sa lecture force une vidange vers le disque.
- Noms de session comparés en `COLLATE Latin1_General_BIN2` des deux côtés.
- Sans le droit requis : une ligne unique `UNKNOWN`, produite par une branche qui ne référence
  aucune vue protégée.
- Le dépôt est public : aucun nom de client, d'instance, de profil, de base ni de chemin réel,
  ni dans le code, ni dans le document de validation, ni dans les commits.
- Écrire sur l'instance de dev demande l'accord explicite de l'utilisateur **sur l'instruction
  exacte**, à chaque fois. Ne jamais passer `-allow-write` sans cet accord.
- Commandes shell préfixées par `rtk` (AGENTS.md).
- Messages de commit terminés par `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Review Focus

1. **Login sans le droit requis** → une ligne `UNKNOWN`, sans erreur, jamais `no session`.
   Cas P1 de la tâche 4.
2. **Plus de 20 candidates, la conforme triée en dernier par nom** → `instance_state = OK`,
   `details_incomplete = 1`. Cas S8 de la tâche 4.
3. **Deux sessions ne différant que par la casse**, sur serveur sensible à la casse, contrôle
   lancé depuis une base insensible → aucune confusion de runtime. Cas C1 de la tâche 4.
4. **Seuil modifié sans `RECONFIGURE`, dans les deux sens** → `NOT_OK` « pending ». Cas T2 et
   T6 de la tâche 4, précondition vérifiée avant mesure.
5. **Nom de session avec `&`, `<` et un caractère non ASCII** → nom intact. Cas S6.

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
l'instance.

**Files :**
- Modify : `tools/internal/sqlq/bundled_queries_test.go`

**Interfaces :**
- Consumes : `FindWrites(sql string) []WriteViolation`, `FindBatchSeparators(sql string) []int`,
  `FindContextChanges(sql string) []WriteViolation` (`tools/internal/sqlq/guard.go`).
- Produces : la tâche 2 doit passer ce test.

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

Run : `cd tools && rtk go test ./internal/sqlq/ -run TestRefusalsCatchesWhatSqlqRefuses`
Expected : FAIL, `undefined: refusals`.

- [ ] **Step 3 : écrire `refusals` et y brancher le test existant**

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

Ajouter `"fmt"` et `"strings"` aux imports. Dans `TestBundledQueriesPassTheReadOnlyGuard`,
remplacer le bloc `if v := FindWrites(...) { ... }` par :

```go
			if r := refusals(string(body)); len(r) > 0 {
				t.Errorf("sqlq would refuse this bundled query: %s", strings.Join(r, "; "))
			}
```

- [ ] **Step 4 : lancer le module entier**

Run : `cd tools && rtk go test ./... && rtk go vet ./...`
Expected : PASS. Les cinq requêtes livrées existantes passent toujours.

- [ ] **Step 5 : commit**

```bash
rtk git add tools/internal/sqlq/bundled_queries_test.go
rtk git commit -m "test(sqlq): a bundled query must pass every check sqlq runs, not just the write guard"
```

---

### Task 2 : la requête `blocked-processes-check.sql`

**Files :**
- Create : `plugins/sqlserver-toolkit/skills/live-query/queries/blocked-processes-check.sql`
- Test : `tools/internal/sqlq/bundled_queries_test.go` (inchangé, couvre le nouveau fichier)

**Interfaces :**
- Consumes : le test de la tâche 1.
- Produces : les colonnes, dans cet ordre, identiques dans les deux branches, que la tâche 3
  documente et que la tâche 4 vérifie : `instance_state, server_name, required_permission,
  has_permission, threshold_value, threshold_value_in_use, candidate_count, details_incomplete,
  is_hadr_enabled, ag_replicas, session_name, session_state, reasons, startup_state, is_running,
  event_in_running_session, event_predicate, file_target_defined, file_target_running, targets`.

- [ ] **Step 1 : vérifier que le test voit le nouveau fichier**

Créer le fichier avec seulement `SELECT 1 AS probe;`, lancer
`cd tools && rtk go test ./internal/sqlq/ -run TestBundledQueriesPassTheReadOnlyGuard -v` et
vérifier qu'un sous-test `blocked-processes-check.sql` apparaît et passe.

- [ ] **Step 2 : écrire la requête**

Remplacer le contenu par :

```sql
/*  Is everything in place for this instance to record blocked process reports?

    No parameter. Read-only. One row per event session that captures
    sqlserver.blocked_process_report (at most 20), or one row with NULL
    session columns when there is none or when the login cannot see them.

    READ instance_state, NOT THE ROWS.
      OK      - at the time of the check: the threshold is in use, a session
                captures the event without a filter, is running, starts with
                the instance, and has an event_file target defined and active.
      NOT_OK  - something on that list is missing. A row with a session_name is
                a session to repair, never a reason to add a second session.
      UNKNOWN - neither yes nor no: a permission is missing, the threshold
                could not be read, or the event carries a predicate that only
                a human can judge. Say so; do not round it to yes or no.

    What OK does NOT say:
      - that a report was ever produced or written. No file is read; the
        target's disk space and folder permissions are not checked.
      - that every block will be reported. The threshold is a minimum duration,
        and the monitor runs about every five seconds, best effort.
      - that a session with MAX_DURATION (SQL Server 2025, Managed Instance)
        will still run tomorrow. Not checked.
      - anything about another instance. In an availability group every
        replica needs its own session. Coverage is measured against the list
        of replicas the user confirms: complete when each of them has come back
        as the server_name of an OK check. ag_replicas helps build that list
        but does not prove it complete - a node that lost the cluster only sees
        itself, and without VIEW ANY DEFINITION the list is empty. Two profiles
        returning the same server_name are one instance.

    The views are read one after the other, not as one snapshot. If sessions
    are being created or stopped during the check, run it again.

    details_incomplete = 1 means more than 20 sessions matched; instance_state
    was still computed over all of them.

    sys.dm_xe_session_targets is deliberately not read: reading it flushes the
    collected data to disk. The active target is found in
    sys.dm_xe_session_object_columns instead.

    Permissions: VIEW SERVER PERFORMANCE STATE where it exists (2022+,
    Managed Instance; VIEW SERVER STATE implies it), VIEW SERVER STATE before.
    Without it, the first branch answers UNKNOWN without touching any
    protected view. Azure SQL Database has no server-scoped sessions: this
    query fails there with an error, which is the intended outcome.

    Runs on SQL Server 2012 and later. FOR XML PATH rather than STRING_AGG.
*/
DECLARE @perf int = HAS_PERMS_BY_NAME(NULL, NULL, N'VIEW SERVER PERFORMANCE STATE');
DECLARE @required_permission nvarchar(40) =
    CASE WHEN @perf IS NULL THEN N'VIEW SERVER STATE'
         ELSE N'VIEW SERVER PERFORMANCE STATE' END;
DECLARE @has_permission int =
    COALESCE(@perf, HAS_PERMS_BY_NAME(NULL, NULL, N'VIEW SERVER STATE'), 0);

IF @has_permission <> 1
    -- No protected view in this branch: the answer cannot depend on a view
    -- that might come back empty for lack of permission.
    SELECT CAST(N'UNKNOWN' AS nvarchar(10))                          AS instance_state,
           @@SERVERNAME                                              AS server_name,
           @required_permission                                      AS required_permission,
           @has_permission                                           AS has_permission,
           CAST(NULL AS int)                                         AS threshold_value,
           CAST(NULL AS int)                                         AS threshold_value_in_use,
           CAST(NULL AS int)                                         AS candidate_count,
           CAST(NULL AS int)                                         AS details_incomplete,
           CAST(SERVERPROPERTY('IsHadrEnabled') AS int)              AS is_hadr_enabled,
           CAST(NULL AS nvarchar(max))                               AS ag_replicas,
           CAST(NULL AS sysname)                                     AS session_name,
           CAST(N'UNKNOWN' AS nvarchar(10))                          AS session_state,
           CAST(N'missing ' + @required_permission AS nvarchar(max)) AS reasons,
           CAST(NULL AS bit)                                         AS startup_state,
           CAST(NULL AS int)                                         AS is_running,
           CAST(NULL AS int)                                         AS event_in_running_session,
           CAST(NULL AS nvarchar(3000))                              AS event_predicate,
           CAST(NULL AS int)                                         AS file_target_defined,
           CAST(NULL AS int)                                         AS file_target_running,
           CAST(NULL AS nvarchar(max))                               AS targets;
ELSE
BEGIN
    WITH cfg AS (
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
                    ELSE N'OK' END                                  AS threshold_state,
               CASE WHEN cfg.threshold_value IS NULL OR cfg.threshold_value_in_use IS NULL
                         THEN N'threshold unknown'
                    WHEN cfg.threshold_value_in_use = 0 AND cfg.threshold_value = 0
                         THEN N'threshold=0'
                    WHEN cfg.threshold_value_in_use = 0
                         THEN N'threshold set but not in use (RECONFIGURE pending)'
                    WHEN cfg.threshold_value = 0
                         THEN N'threshold disable pending (next RECONFIGURE turns reports off)'
               END                                                  AS threshold_reason
        FROM cfg
    ),
    cand AS (
        -- One row per session, chosen by EXISTS so that several targets or
        -- events never multiply it.
        SELECT s.event_session_id,
               s.name          AS session_name,
               s.startup_state
        FROM sys.server_event_sessions AS s
        WHERE EXISTS (SELECT 1
                      FROM sys.server_event_session_events AS e
                      WHERE e.event_session_id = s.event_session_id
                        AND e.package = N'sqlserver'
                        AND e.name    = N'blocked_process_report')
    ),
    detail AS (
        SELECT c.session_name,
               c.startup_state,
               CASE WHEN xs.address IS NULL THEN 0 ELSE 1 END       AS is_running,
               CASE WHEN EXISTS (SELECT 1
                                 FROM sys.dm_xe_session_events AS xe
                                 JOIN sys.dm_xe_packages AS p
                                   ON p.guid = xe.event_package_guid
                                 WHERE xe.event_session_address = xs.address
                                   AND xe.event_name = N'blocked_process_report'
                                   AND p.name        = N'sqlserver')
                    THEN 1 ELSE 0 END                               AS event_in_running_session,
               (SELECT TOP (1) e.predicate
                  FROM sys.server_event_session_events AS e
                 WHERE e.event_session_id = c.event_session_id
                   AND e.package = N'sqlserver'
                   AND e.name    = N'blocked_process_report'
                 ORDER BY e.event_id)                               AS event_predicate,
               CASE WHEN EXISTS (SELECT 1
                                 FROM sys.server_event_session_targets AS t
                                 WHERE t.event_session_id = c.event_session_id
                                   AND t.package = N'package0'
                                   AND t.name    = N'event_file')
                    THEN 1 ELSE 0 END                               AS file_target_defined,
               -- Not sys.dm_xe_session_targets: reading it flushes to disk.
               CASE WHEN EXISTS (SELECT 1
                                 FROM sys.dm_xe_session_object_columns AS oc
                                 JOIN sys.dm_xe_packages AS p
                                   ON p.guid = oc.object_package_guid
                                 WHERE oc.event_session_address = xs.address
                                   AND oc.object_type = N'target'
                                   AND oc.object_name = N'event_file'
                                   AND p.name         = N'package0')
                    THEN 1 ELSE 0 END                               AS file_target_running,
               STUFF((SELECT N', ' + t.name
                        FROM sys.server_event_session_targets AS t
                       WHERE t.event_session_id = c.event_session_id
                       ORDER BY t.name
                         FOR XML PATH(''), TYPE).value('.', 'nvarchar(max)'), 1, 2, N'')
                                                                    AS targets
        FROM cand AS c
        -- BIN2 on both sides: an exact match that does not depend on the
        -- database the profile opened.
        LEFT JOIN sys.dm_xe_sessions AS xs
          ON xs.name COLLATE Latin1_General_BIN2 = c.session_name COLLATE Latin1_General_BIN2
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
                    ELSE N'OK' END                                  AS session_state,
               -- STUFF on an empty string returns NULL: reasons is NULL when OK.
               STUFF(COALESCE(N'; ' + thr.threshold_reason, N'')
                   + CASE WHEN d.startup_state = 0 THEN N'; startup_state=OFF' ELSE N'' END
                   + CASE WHEN d.is_running = 0 THEN N'; not running' ELSE N'' END
                   + CASE WHEN d.is_running = 1 AND d.event_in_running_session = 0
                          THEN N'; event not in running session' ELSE N'' END
                   + CASE WHEN d.file_target_defined = 0 THEN N'; no file target' ELSE N'' END
                   + CASE WHEN d.is_running = 1 AND d.file_target_defined = 1
                               AND d.file_target_running = 0
                          THEN N'; file target not running' ELSE N'' END
                   + CASE WHEN d.event_predicate IS NOT NULL
                          THEN N'; event filtered by predicate' ELSE N'' END,
                     1, 2, N'')                                     AS reasons
        FROM detail AS d
        CROSS JOIN thr
    ),
    inst AS (
        SELECT thr.threshold_value,
               thr.threshold_value_in_use,
               thr.threshold_reason,
               agg.candidate_count,
               CASE WHEN agg.any_ok = 1                     THEN N'OK'
                    WHEN agg.any_unknown = 1
                      OR thr.threshold_state = N'UNKNOWN'   THEN N'UNKNOWN'
                    ELSE N'NOT_OK' END                              AS instance_state,
               CASE WHEN agg.candidate_count > 20 THEN 1 ELSE 0 END AS details_incomplete,
               CAST(SERVERPROPERTY('IsHadrEnabled') AS int)         AS is_hadr_enabled,
               CASE WHEN CAST(SERVERPROPERTY('IsHadrEnabled') AS int) = 1
                    THEN STUFF((SELECT N', ' + ar.replica_server_name
                                  FROM sys.availability_replicas AS ar
                                 GROUP BY ar.replica_server_name
                                 ORDER BY ar.replica_server_name
                                   FOR XML PATH(''), TYPE).value('.', 'nvarchar(max)'), 1, 2, N'')
               END                                                  AS ag_replicas
        FROM thr
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
               CASE j.session_state WHEN N'OK' THEN 0 WHEN N'UNKNOWN' THEN 1 ELSE 2 END
                                                                    AS sort_state
        FROM judged AS j
        ORDER BY sort_state, j.session_name
    ),
    sentinel AS (
        -- The single row when no session captures the event. Session columns
        -- are not applicable, so only "no session" is reported - no invented
        -- session defects.
        SELECT CAST(NULL AS sysname)                                AS session_name,
               N'NOT_OK'                                            AS session_state,
               COALESCE(inst.threshold_reason + N'; ', N'') + N'no session'
                                                                    AS reasons,
               CAST(NULL AS bit)                                    AS startup_state,
               CAST(NULL AS int)                                    AS is_running,
               CAST(NULL AS int)                                    AS event_in_running_session,
               CAST(NULL AS nvarchar(3000))                         AS event_predicate,
               CAST(NULL AS int)                                    AS file_target_defined,
               CAST(NULL AS int)                                    AS file_target_running,
               CAST(NULL AS nvarchar(max))                          AS targets,
               3                                                    AS sort_state
        FROM inst
        WHERE inst.candidate_count = 0
    )
    SELECT TOP (21)
           CAST(inst.instance_state AS nvarchar(10))                AS instance_state,
           @@SERVERNAME                                             AS server_name,
           @required_permission                                     AS required_permission,
           @has_permission                                          AS has_permission,
           inst.threshold_value,
           inst.threshold_value_in_use,
           inst.candidate_count,
           inst.details_incomplete,
           inst.is_hadr_enabled,
           inst.ag_replicas,
           r.session_name,
           CAST(r.session_state AS nvarchar(10))                    AS session_state,
           CAST(r.reasons AS nvarchar(max))                         AS reasons,
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
END;
```

Notes pour l'implémenteur :
- `WITH` suit directement `BEGIN`. Si le moteur répond par l'erreur 319 (instruction précédente
  non terminée par un point-virgule), écrire `;WITH` — et le noter dans le commit.
- `file target not running` n'est émis que si une cible `event_file` est **définie** : sans
  elle, `no file target` dit déjà tout. C'est la seule précision apportée à la table de règles
  de la spec.
- Le `TOP (21)` final est une borne de forme exigée par les conventions. Les lectures n'étant
  pas atomiques, `shown` et `sentinel` peuvent en théorie coexister (20 + 1 lignes) : la borne
  le tolère.
- `event_predicate` est `nvarchar(3000)` d'après la documentation de
  `sys.server_event_session_events` ; la tâche 4 le vérifie dans le champ `columns` du JSON de
  `sqlq`, pas en attendant une erreur.

- [ ] **Step 3 : lancer le test des requêtes livrées et le module**

Run : `cd tools && rtk go test ./...`
Expected : PASS, dont `TestBundledQueriesPassTheReadOnlyGuard/blocked-processes-check.sql`.
Si le garde refuse un mot de l'en-tête (il retire normalement les commentaires), reformuler le
commentaire, jamais le garde.

- [ ] **Step 4 : commit**

```bash
rtk git add plugins/sqlserver-toolkit/skills/live-query/queries/blocked-processes-check.sql
rtk git commit -m "feat(live-query): a bundled check for the blocked process trace"
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

- [ ] **Step 2 : relire le tableau**

Lire les lignes du tableau autour de l'insertion : deux colonnes, même nombre de `|` que les
voisines, aucune ligne vide qui couperait le tableau.

- [ ] **Step 3 : commit**

```bash
rtk git add plugins/sqlserver-toolkit/skills/live-query/SKILL.md
rtk git commit -m "docs(live-query): route the blocked process trace question to its bundled check"
```

---

### Task 4 : validation sur l'instance de dev

Cette tâche ne se délègue pas : chaque écriture demande l'accord de l'utilisateur sur
l'instruction exacte. Elle a besoin du profil de dev, fourni par l'utilisateur, en mode
`readwrite`, et de son accord avant la première requête si le profil est marqué `prod`.

**Files :**
- Create : `docs/validation/2026-10-01-blocked-processes-check.md`

Notation : `<dev>` le profil fourni ; `Q` le chemin de la requête.

```bash
Q=plugins/sqlserver-toolkit/skills/live-query/queries/blocked-processes-check.sql
```

#### Règles communes à tous les cas

- **Une écriture = un appel `sqlq -allow-write`**, précédé de l'instruction exacte montrée à
  l'utilisateur et de son accord.
- **Registre** : chaque objet créé (session, login, droit) est noté dès sa création dans une
  liste de travail. Le nettoyage porte sur cette liste, jamais sur un préfixe.
- **État de base** des cas `S*` : seuil 10/10, aucune session candidate hors celles du cas.
  Chaque cas supprime ses sessions avant le suivant, sauf transitions annoncées.
- **Assertions séparées** : `instance_state`, et pour chaque ligne `session_name`,
  `session_state`, `reasons` ; plus `rowcount` et l'ordre des lignes.
- **Arrêt** : un écart entre attendu et observé arrête la série. Corriger la requête (tâche 2),
  puis rejouer **tous** les cas déjà passés. Une correction qui change une règle de la spec
  remonte à l'utilisateur avant d'être faite.
- **Nettoyage après erreur** : si un cas échoue, est refusé ou interrompu, le step 6
  (restauration) s'exécute quand même avant de rendre la main.
- **Avant chaque `RECONFIGURE`** : relire `sys.configurations WHERE value <> value_in_use`. Toute
  option en attente autre que les deux options testées arrête la série : `RECONFIGURE`
  l'appliquerait aussi, et la vérification du step 1 ne couvre pas une option préparée pendant
  les tests (revue des risques, H2).

Instructions de pose de référence (une par appel) :

```sql
EXEC sys.sp_configure N'show advanced options', 1; RECONFIGURE;
EXEC sys.sp_configure N'blocked process threshold (s)', 10; RECONFIGURE;
CREATE EVENT SESSION [bpr_test_a] ON SERVER
    ADD EVENT sqlserver.blocked_process_report
    ADD TARGET package0.event_file (SET filename = N'bpr_test_a', max_file_size = (5), max_rollover_files = (2))
    WITH (STARTUP_STATE = OFF);
ALTER EVENT SESSION [bpr_test_a] ON SERVER STATE = START;
ALTER EVENT SESSION [bpr_test_a] ON SERVER WITH (STARTUP_STATE = ON);
ALTER EVENT SESSION [bpr_test_a] ON SERVER STATE = STOP;
DROP EVENT SESSION [bpr_test_a] ON SERVER;
```

`filename` relatif : le fichier `.xel` est écrit dans le répertoire du journal d'erreurs de
l'instance. Supprimer la session ne supprime pas ses fichiers ; leur suppression est laissée à
l'utilisateur, et le document de validation le dit.

- [ ] **Step 1 : relevé de l'état initial (lecture seule)**

```bash
rtk sqlq -profile <dev> -query "SELECT @@VERSION AS v, SERVERPROPERTY('EngineEdition') AS edition, SERVERPROPERTY('Collation') AS server_collation, CAST(SERVERPROPERTY('IsHadrEnabled') AS int) AS hadr"
rtk sqlq -profile <dev> -query "SELECT TOP (10) name, CAST(value AS int) AS value, CAST(value_in_use AS int) AS value_in_use FROM sys.configurations WHERE name IN (N'show advanced options', N'blocked process threshold (s)') ORDER BY name"
rtk sqlq -profile <dev> -query "SELECT TOP (50) name, CAST(value AS int) AS value, CAST(value_in_use AS int) AS value_in_use FROM sys.configurations WHERE value <> value_in_use ORDER BY name"
rtk sqlq -profile <dev> -query "SELECT TOP (50) name FROM sys.server_event_sessions WHERE name LIKE N'bpr[_]test[_]%' ORDER BY name"
rtk sqlq -profile <dev> -file "$Q"
```

Noter : version, édition, collation serveur, HADR, les deux options (valeur et en vigueur).

Conditions d'arrêt, **avant toute écriture** :
- une configuration en attente (`value <> value_in_use`), quelle qu'elle soit : un
  `RECONFIGURE` l'appliquerait. Montrer la liste à l'utilisateur et attendre sa décision ;
- une session `bpr_test_*` préexistante : collision de noms, demander ;
- `candidate_count > 0` : une trace existe déjà. Ne jamais l'arrêter ni la modifier : la
  restauration ne porte que sur les objets créés et ne la relancerait pas (revue des risques,
  H5). Les cas à assertion d'instance ne sont alors pas exécutables ; ne valider que les
  assertions de session et le consigner, ou changer d'instance.

Ce premier `-file` est aussi le cas **V0** : aucune erreur, colonnes et types conformes à la
tâche 2 (champ `columns` du JSON), `event_predicate` de type `NVARCHAR` de taille 3000.

- [ ] **Step 2 : seuil (T1 à T6)**

| cas | état posé | attendu |
|---|---|---|
| T1 | seuil 0/0, aucune candidate | 1 ligne ; `instance_state = NOT_OK` ; `session_name` NULL ; `session_state = NOT_OK` ; `reasons = threshold=0; no session` ; `candidate_count = 0` |
| T2 | `value` 10, `value_in_use` 0 (`sp_configure` sans `RECONFIGURE`, précondition relue) | 1 ligne ; `NOT_OK` ; `reasons = threshold set but not in use (RECONFIGURE pending); no session` |
| T3 | seuil 10/10 ; `bpr_test_a` (event_file), OFF, arrêtée | 1 ligne ; instance `NOT_OK` ; session `NOT_OK` ; `reasons = startup_state=OFF; not running` |
| T4 | T3, session démarrée | instance `NOT_OK` ; `reasons = startup_state=OFF` |
| T5 | T4, `STARTUP_STATE = ON` | instance `OK` ; session `OK` ; `reasons` NULL ; `file_target_defined = 1` ; `file_target_running = 1` ; `event_in_running_session = 1` |
| T6 | T5, puis `value` 0 sans `RECONFIGURE` (`value_in_use` 10 relu) | instance `NOT_OK` ; session `NOT_OK` ; `reasons = threshold disable pending (next RECONFIGURE turns reports off)` |

T3 → T6 sont des transitions sur la même session. Fin : seuil remis à 10/10 avec
`RECONFIGURE`, `bpr_test_a` arrêtée et supprimée.

- [ ] **Step 3 : forme des sessions (S1 à S8)**, seuil 10/10, chaque cas seul

| cas | sessions posées (toutes ON et démarrées sauf mention) | attendu |
|---|---|---|
| S1 | `bpr_test_rb` : cible `ring_buffer` seule | instance `NOT_OK` ; `reasons = no file target` ; `targets = ring_buffer` ; `file_target_running = 0` |
| S2 | `bpr_test_nt` : aucune cible | instance `NOT_OK` ; `reasons = no file target` ; `targets` NULL |
| S3 | `bpr_test_pr` : event_file, `WHERE (sqlserver.database_id = 1)` | instance `UNKNOWN` ; session `UNKNOWN` ; `reasons = event filtered by predicate` ; `event_predicate` non NULL |
| S4 | `bpr_test_mt` : `event_file` et `ring_buffer` | 1 ligne ; `OK` ; `targets = event_file, ring_buffer` |
| S5 | `bpr_test_ok` (conforme) et `bpr_test_ko` (OFF, arrêtée) | 2 lignes ; instance `OK` ; ligne 1 `bpr_test_ok` `OK`, ligne 2 `bpr_test_ko` `NOT_OK` |
| S6 | `[bpr_test_&<é]` (conforme) | `session_name = bpr_test_&<é` intact ; `OK` |
| S7 | `bpr_test_a` créée et démarrée, puis `ALTER EVENT SESSION … DROP EVENT sqlserver.blocked_process_report` | 0 candidate : 1 ligne `no session` (la session n'est plus une candidate) |
| S8 | `bpr_test_00` … `bpr_test_20` (21 sessions, toutes capturant l'événement, toutes OFF et arrêtées) et `bpr_test_zz` conforme | avant mesure, compter 22 candidates ; puis `candidate_count = 22` ; `details_incomplete = 1` ; `rowcount = 20` ; ligne 1 `bpr_test_zz` `OK` ; noms uniques ; instance `OK` |

- [ ] **Step 4 : collation (C1)**

Seulement si la collation serveur relevée au step 1 est sensible à la casse (`_CS_` ou `_BIN`) ;
sinon « non exécuté : serveur insensible à la casse ».

| cas | état posé | attendu |
|---|---|---|
| C1 | `[bpr_test_A]` OFF arrêtée, `[bpr_test_a]` conforme ; requête lancée deux fois, avec `-database` sur une base insensible à la casse puis sur une base sensible | les deux fois : 2 lignes ; `bpr_test_a` `OK` ; `bpr_test_A` `NOT_OK` `startup_state=OFF; not running` ; instance `OK` |

- [ ] **Step 5 : droits (P1 à P3) et AG (A1)**

`EXECUTE AS` est refusé par le garde (`EXECUTE`) : ces cas demandent **un profil de test** au
nom d'un login non sysadmin (un sysadmin ignore les `DENY`), créé par l'utilisateur ou avec son
accord, et ajouté au registre. Sans lui : « non exécuté », garantie non validée.

| cas | droits du login de test | attendu |
|---|---|---|
| P1 | aucun des deux droits | 1 ligne, **sans erreur** ; `instance_state = UNKNOWN` ; `reasons = missing <required_permission>` ; `required_permission` cohérent avec la version |
| P2 | 2022+ : `VIEW SERVER PERFORMANCE STATE` seul | verdict normal (le même qu'avec le profil de dev sur le même état) |
| P3 | 2022+ : `VIEW SERVER STATE` accordé, `DENY VIEW SERVER PERFORMANCE STATE` | `UNKNOWN`, `missing VIEW SERVER PERFORMANCE STATE` |

Si P1 rend une erreur au lieu de la ligne, c'est un écart au contrat de la spec §5 : arrêt et
correction.

| cas | état | attendu |
|---|---|---|
| A1 | instance membre d'un AG | `ag_replicas` liste les réplicas connus de l'utilisateur ; avec le login P1, `ag_replicas` NULL. Instance hors AG : « non exécuté » |

Managed Instance : « non exécuté » sauf si l'utilisateur en fournit une, avec une recette de
cible propre (stockage Azure) qu'il fournit aussi. SQL Server 2012 : « non testé » sauf
instance fournie.

- [ ] **Step 6 : restauration**

Dans cet ordre, chaque instruction sous accord : arrêter et supprimer chaque session du
registre ; supprimer logins et droits du registre ; remettre le seuil puis
`show advanced options` à leurs valeurs du step 1, avec `RECONFIGURE` après la relecture des
options en attente exigée par les règles communes. Si la valeur courante du seuil n'est plus
celle qu'a posée le dernier cas, quelqu'un d'autre l'a changée : ne pas l'écraser, demander. Puis relancer
les requêtes du step 1 et comparer **valeur par valeur** à l'état initial : options, sessions
`bpr_test_*` (aucune), verdict de la requête.

- [ ] **Step 7 : document de validation**

`docs/validation/2026-10-01-blocked-processes-check.md` : version et édition du moteur,
collation serveur (sans nom de serveur) ; un tableau `cas | précondition relue | attendu |
observé | verdict` ; les cas **non exécutés** avec leur raison ; les cas **exclus par la spec**
(`event not in running session`, `MAX_DURATION`) ; les versions et moteurs non testés ; les
fichiers `.xel` laissés à supprimer par l'utilisateur, sans chemin. Aucun nom réel.

- [ ] **Step 8 : contrôle final et commit**

Run : `cd tools && rtk go test ./... && rtk go vet ./...` — Expected : PASS.

```bash
rtk git add docs/validation/2026-10-01-blocked-processes-check.md plugins/sqlserver-toolkit/skills/live-query/queries/blocked-processes-check.sql
rtk git commit -m "test(live-query): validate the blocked process check on a dev instance"
```
