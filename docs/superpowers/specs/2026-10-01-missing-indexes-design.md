# Design — index manquants et index existants de la table (requête livrée)

Date : 2026-10-01
Statut : design validé en séance, en attente de relecture avant plan d'implémentation.
Tâche : Todoist 6hg6X2JVxh8mw77G, itération 1. L'itération 2 (plusieurs bases) et l'indicateur
de recouvrement sont hors périmètre (§9).

## 1. Le problème

« Quels index manquent dans cette base ? » est une question d'audit fréquente, et celle où un
agent se trompe le plus volontiers : il lit `sys.dm_db_missing_index_details`, recopie le
`CREATE INDEX` suggéré, et ignore les index déjà présents sur la table. Microsoft le dit
lui-même : la suggestion ne fixe pas l'ordre des colonnes d'égalité, ne pèse pas la taille des
colonnes incluses, ignore les index existants, et propose des variantes voisines d'une requête
à l'autre qu'il faut regrouper (*Tune nonclustered indexes with missing index suggestions*,
section *Limitations*). Le skill `query-plan-analysis` met en garde contre le même réflexe.

La bonne réponse se lit par table : les suggestions de la table entre elles, face à ses index
existants et à leur usage. Dans une bonne part des cas, elle consiste à élargir un index
existant plutôt qu'à en créer un nouveau. Aucune requête de `live-query/queries/` ne lit
aujourd'hui `sys.dm_db_missing_index_*` ni `sys.dm_db_index_usage_stats`.

## 2. Décisions

| Question | Décision |
|---|---|
| Option CLI ou requête livrée ? | Requête livrée, lancée par `sqlq -file ... -database <base>`. Aucune surface CLI nouvelle, aucun Go. |
| Point d'entrée | Vue d'ensemble de la base courante, sans paramètre. `sqlq` n'a pas de paramètre optionnel : une variable référencée sans `-param` fait échouer l'appel. |
| Grain | Un seul jeu de résultats, trois sortes de lignes (`context`, `missing`, `existing`), regroupées par table. Les compteurs restent numériques, rien n'est répété d'une suggestion à l'autre. |
| Tables retenues | Les 5 tables dont la somme des scores de suggestion est la plus haute, avec **toutes** leurs suggestions et **tous** leurs index. Valeur fixe dans le fichier. |
| Score | Celui de la documentation Microsoft : `avg_total_user_cost * avg_user_impact * (user_seeks + user_scans)`. |
| DDL | Aucun. Une instruction prête à copier pousse à la coller telle quelle. |
| Objets système | Exclus (`is_ms_shipped = 1`). Conséquence assumée : les tables de `msdb` comme `backupset` sont `is_ms_shipped`, un audit de `msdb` ne rend donc pas leurs suggestions. |
| Moteurs | SQL Server 2012 et suivants, Azure SQL Managed Instance. Pas de `STRING_AGG`, pas de colonne de catalogue apparue après 2012. Azure SQL Database : DMV présentes, requête non testée, et l'en-tête le dit. |

## 3. Ce que la documentation établit, et ce qu'elle n'établit pas

Vérifié sur Microsoft Learn le 2026-10-01.

- **Plafond.** Les suggestions sont collectées pour 600 groupes au plus ; au-delà, plus rien
  n'est collecté. `sys.dm_db_missing_index_details` est limitée à 600 lignes. Une requête
  chaude apparue après le plafond n'a jamais de suggestion.
- **Effacement.** Les suggestions disparaissent au redémarrage, au basculement, à la mise hors
  ligne de la base. Elles disparaissent aussi **pour une table** quand ses métadonnées changent
  (colonne ajoutée ou supprimée, index créé) et quand un `ALTER INDEX` porte sur un de ses
  index. Une maintenance nocturne efface donc les suggestions des tables qu'elle touche : la
  fenêtre d'observation est par table, et aucune vue ne donne sa date de début.
- **Ce qui n'est jamais suggéré.** Index unique, filtré, clustered, columnstore. Pas de
  suggestion pour un plan trivial. Coût moins fiable quand il n'y a que des prédicats
  d'inégalité.
- **Usage des index existants.** `sys.dm_db_index_usage_stats` est vidée au démarrage, et les
  lignes d'une base disparaissent quand elle est détachée ou fermée (`AUTO_CLOSE`). Une ligne
  n'existe qu'à partir de la première utilisation de l'index. Les index en mémoire et les index
  spatiaux n'y figurent pas. `user_updates` compte des opérations, pas des lignes.
- **Droits.** `VIEW SERVER STATE` jusqu'à 2019, `VIEW SERVER PERFORMANCE STATE` à partir de
  2022, pour les deux familles de vues.
- **Non établi par la documentation.** La remise à zéro de `sys.dm_db_index_usage_stats` par un
  rebuild sur certaines versions 2012 et 2014 vient de billets de support, pas de Learn.
  L'en-tête la cite comme connue et non vérifiée, sans numéro de version. L'effet d'un
  `ALTER INDEX ... REORGANIZE` sur les suggestions n'est pas distingué de celui d'un rebuild :
  il est observé en validation (§8, M6b) et l'en-tête dit ce qui a été vu.

## 4. La requête

Fichier : `plugins/sqlserver-toolkit/skills/live-query/queries/missing-indexes.sql`.
Aucun paramètre. Un seul batch, sans `GO`, `USE`, `EXEC`, `INTO` ni `DECLARE`.
`OPTION (RECOMPILE, MAXDOP 1)`, comme les scripts sources.

### Forme du résultat

| `row_kind` | nombre | rôle |
|---|---|---|
| `context` | exactement une, toujours, en tête | dit si une conclusion est possible |
| `missing` | toutes les suggestions des tables retenues | ce que l'optimiseur a demandé |
| `existing` | tous les index des tables retenues, heap et clustered compris | ce qui existe et sert déjà |

Sans la ligne `context`, une base sans suggestion rend zéro ligne, et l'agent ne peut pas
distinguer « rien à suggérer », « instance redémarrée il y a une heure » et « collecte
plafonnée ».

### Colonnes, dans cet ordre

| colonne | `context` | `missing` | `existing` |
|---|---|---|---|
| `row_kind` | `context` | `missing` | `existing` |
| `table_rank` | 0 | 1 à 5 | 1 à 5 |
| `schema_name`, `table_name` | NULL | table | table |
| `index_name` | NULL | NULL | nom, NULL pour un heap |
| `index_type` | NULL | NULL | `type_desc` de `sys.indexes` |
| `is_unique`, `is_primary_key`, `is_disabled` | NULL | NULL | `sys.indexes` |
| `filter_definition` | NULL | NULL | `sys.indexes`, NULL si non filtré |
| `key_columns` | NULL | NULL | clés par `key_ordinal`, suffixe ` DESC` si `is_descending_key` |
| `equality_columns`, `inequality_columns` | NULL | DMV, telles quelles | NULL |
| `included_columns` | NULL | DMV, telle quelle | colonnes incluses par `index_column_id` |
| `user_seeks`, `user_scans` | NULL | `group_stats` | `index_usage_stats`, 0 si pas de ligne |
| `user_lookups`, `user_updates` | NULL | NULL | `index_usage_stats`, 0 si pas de ligne |
| `last_used` | NULL | plus récente de `last_user_seek`, `last_user_scan` | plus récente de seek, scan, lookup |
| `used_mb` | NULL | NULL | somme de `used_page_count` sur les partitions, LOB compris |
| `usage_not_tracked` | NULL | NULL | 1 pour un index spatial ou une table en mémoire, sinon 0 |
| `avg_total_user_cost`, `avg_user_impact`, `score` | NULL | `group_stats`, score calculé | NULL |
| `unique_compiles` | NULL | `group_stats` | NULL |
| `instance_start_time` | `sqlserver_start_time` | NULL | NULL |
| `observed_days` | jours depuis le démarrage, une décimale | NULL | NULL |
| `suggestion_groups_on_instance` | nombre de groupes, toutes bases | NULL | NULL |
| `collection_capped` | 1 si ce nombre atteint 600 | NULL | NULL |
| `tables_with_suggestions` | tables de la base courante ayant au moins une suggestion | NULL | NULL |
| `suggestions_on_hidden_objects` | suggestions de la base courante dont l'objet n'est pas visible ou n'existe plus | NULL | NULL |
| `hypothetical_indexes` | index `is_hypothetical` de la base courante | NULL | NULL |

Quand `usage_not_tracked = 1`, les quatre compteurs et `last_used` sont NULL, pas 0 : un 0
voudrait dire « jamais utilisé », un NULL veut dire « on ne sait pas ».

### Ordre

`table_rank`, puis `missing` avant `existing`. Les `missing` par `score` décroissant puis
`index_handle` ; les `existing` par `index_id`. Entre tables, une égalité de somme de scores se
départage par `object_id`. Un même état rend le même ordre.

### Invariants d'implémentation

- **Base courante, partout.** `database_id = DB_ID()` sur `sys.dm_db_missing_index_details` et
  sur `sys.dm_db_index_usage_stats`. Sans ce filtre, un `object_id` d'une autre base peut
  désigner une table de la base courante, et la ligne est fausse sans erreur.
- **Noms par jointure**, sur `sys.objects` (`type = 'U'`, `is_ms_shipped = 0`), jamais par
  `OBJECT_NAME()` ni par la colonne `statement`. Une suggestion dont l'objet n'est pas visible
  est écartée des lignes et comptée dans `suggestions_on_hidden_objects` : un login sans
  `VIEW DEFINITION` voit moins d'objets, et ce compteur rend l'écart visible au lieu de le
  taire.
- **Index existants complets.** Point de départ `sys.indexes` (hors `is_hypothetical`),
  jointure **externe** vers `sys.dm_db_index_usage_stats`. Un index jamais utilisé depuis le
  démarrage n'a pas de ligne dans la DMV ; il apparaît avec des compteurs à 0.
- **Colonnes d'index.** Clés : `key_ordinal > 0`, triées par `key_ordinal`. Incluses :
  `is_included_column = 1`, triées par `index_column_id`. Une colonne de partitionnement non
  déclarée (`key_ordinal = 0`, non incluse) n'apparaît dans aucune liste.
- **Agrégations de texte** par `FOR XML PATH(''), TYPE).value('.', 'nvarchar(max)')`, pour
  qu'un nom contenant `&` ou `<` ressorte intact.
- **Score en `float`**, arrondi à l'affichage. Un produit en `decimal` peut déborder.
- **Classement avant présentation.** La somme des scores par table et `tables_with_suggestions`
  sont calculés sur toutes les suggestions de la base avant le `TOP (5)`.
- **Tables en mémoire** repérées par `OBJECTPROPERTY(object_id, 'TableIsMemoryOptimized')`,
  qui rend NULL sur 2012 au lieu d'échouer à la compilation comme le ferait
  `sys.tables.is_memory_optimized`. Index spatiaux : `sys.indexes.type = 4`.
- **Dates en texte.** `instance_start_time` et `last_used` sont des `datetime` en heure locale
  du serveur ; `sqlq` les rend aujourd'hui suffixées `Z`, donc fausses du décalage horaire
  (tâche Todoist 6hfvCXxP39vff2vG, §1). La requête les convertit en texte ISO sans fuseau
  (`CONVERT(varchar(19), x, 126)`), et `observed_days` est calculé côté serveur.
- **Types fixes** sur chaque sorte de ligne : les NULL portent le type de leur colonne par
  `CAST`, pour que l'union garde des types stables.

## 5. En-tête du fichier

Sur le modèle de `blocked-processes-check.sql`, sous « READ THIS BEFORE TRUSTING THE RESULT » :

- lire la ligne `context` d'abord. `observed_days` inférieur à un jour, ou
  `collection_capped = 1`, interdit toute conclusion dans un sens comme dans l'autre ;
  `suggestions_on_hidden_objects > 0` veut dire que le login ne voit pas tout ;
- une absence de suggestion ne prouve rien : effacement par `ALTER INDEX`, création d'index ou
  changement de colonnes, plan trivial, spool eager (qui supprime la suggestion) ;
- une suggestion n'est pas une prescription : ordre des colonnes d'égalité non significatif,
  colonnes incluses sans analyse de taille, `avg_user_impact` est une estimation sur une
  estimation, jamais d'index unique, filtré, clustered ni columnstore ;
- la fenêtre d'observation est par table : une table souvent reconstruite est sous-représentée
  dans le classement ;
- les compteurs `existing` valent depuis le démarrage ou la mise en ligne de la base. Un index
  à 0 n'est candidat à la suppression qu'au vu de `observed_days` et d'un cycle d'activité
  complet (fin de mois, clôture). Remise à zéro par rebuild sur 2012 et 2014 : connue, non
  vérifiée ;
- limites assumées : objets `is_ms_shipped` exclus, base courante seulement, tables en mémoire
  et index spatiaux sans compteurs (`usage_not_tracked`) ;
- droits : `VIEW SERVER STATE`, ou `VIEW SERVER PERFORMANCE STATE` à partir de 2022. Sans eux,
  l'erreur est franche (`sqlq` code 2), jamais un résultat vide.

## 6. Le skill

`plugins/sqlserver-toolkit/skills/live-query/SKILL.md` :

- une ligne dans l'arbre de décision : « Which indexes are missing » →
  `-file queries/missing-indexes.sql -database <db> -maxrows 150`, puis le protocole de
  lecture ; renvoi à l'en-tête du fichier ;
- trois interdits en clair : recopier une suggestion en `CREATE INDEX` ; conclure « aucun
  index manquant » d'une absence de lignes ; recommander la suppression d'un index sur des
  compteurs à 0 sans citer `observed_days`.

`plugins/sqlserver-toolkit/skills/live-query/references/missing-index-reading.md`, chargé à la
demande, pour garder le corps du skill court :

1. lire `context` ; s'arrêter si aucune conclusion n'est possible, et le dire ;
2. travailler table par table ; regrouper les suggestions de la table entre elles ;
3. pour chaque groupe, chercher un index existant dont la clé commence par les mêmes colonnes,
   et proposer de l'élargir plutôt que de créer ;
4. peser `user_updates` contre `user_seeks + user_scans + user_lookups` avant d'ajouter un
   index à une table très écrite ;
5. rédiger une proposition consolidée, en disant que l'ordre des colonnes d'égalité reste à
   choisir selon la sélectivité ;
6. citer `observed_days` et la limite de fenêtre par table dans la réponse.

Les phrases du skill sont en anglais, comme le reste de `live-query`.

## 7. Taille du résultat

Avec 5 tables, la sortie tient le plus souvent sous 100 lignes ; le skill demande
`-maxrows 150`. Si `truncated` est vrai, la dernière table est incomplète, et l'agent doit le
dire. La taille réelle en caractères est mesurée en validation et consignée ; une sortie
au-delà de 40 000 caractères sur la base de test fait revenir la question de la forme.

## 8. Tests et validation

**Statique.** `TestBundledQueriesPassTheReadOnlyGuard` prend le fichier automatiquement. Aucun
test Go nouveau.

**En direct**, rapport dans `docs/validation/2026-10-01-missing-indexes.md`, sur l'instance de
test de la validation précédente (SQL Server 2019 Developer). Base jetable `mi_validation`,
plus `mi_validation_other` pour M5. La mise en place écrit : chaque instruction est montrée à
l'utilisateur avant exécution. Chaque cas relit sa précondition avant l'observation.

| cas | mise en place | attendu |
|---|---|---|
| V0 | aucune | pas d'erreur ; colonnes dans l'ordre et les types du §4 ; une ligne `context` |
| M1 | base neuve, aucune requête | la seule ligne `context`, `tables_with_suggestions = 0` |
| M2 | requêtes sans index adapté sur 6 tables, d'intensités différentes | exactement 5 tables ; rangs conformes aux sommes de scores ; ordre identique sur deux exécutions |
| M3 | une table avec 3 suggestions et 4 index : un jamais utilisé, un désactivé, un filtré, une clé `DESC` avec `INCLUDE` | les 3 `missing`, les 4 `existing` et le clustered ; l'index jamais utilisé présent à 0 ; clés dans l'ordre avec `DESC` ; filtre restitué |
| M4 | colonne dont le nom contient `&` et `<` | nom restitué tel quel |
| M5 | suggestions dans `mi_validation_other` | aucune ligne de l'autre base en lisant `mi_validation` ; `suggestion_groups_on_instance` les compte |
| M6 | `ALTER INDEX ... REBUILD` sur une table à suggestions | ses suggestions disparaissent |
| M6b | `ALTER INDEX ... REORGANIZE` sur une autre table | observé et consigné, l'en-tête reprend le constat |
| M7 | heap avec suggestions | ligne `existing` présente, `index_type = HEAP`, `index_name` NULL |
| M8 | login sans `VIEW SERVER STATE` | erreur, `sqlq` code 2, pas de résultat vide |
| M9 | login sans `VIEW DEFINITION` ni droit sur une des tables | la table absente des lignes ; `suggestions_on_hidden_objects > 0` |
| M10 | table en mémoire, si l'instance le permet | `usage_not_tracked = 1`, compteurs NULL |

Non reproduit : le plafond de 600 groupes (`collection_capped`), vérifié par lecture du code.
Le rapport le dit.

**Nettoyage.** Les deux bases et les logins temporaires sont supprimés, et le rapport le
constate.

**Revues** (route FULL) : `external-design-review` sur cette spec et le plan ;
`external-code-review` (agy et codex, opencode n'est pas installé) ; revue des risques
adversariale. `adversarial-spec-review` et `adversarial-harm-review` ne sont pas installés sur
ce poste : la revue de design et un sous-agent au prompt adversarial en tiennent lieu.

## 9. Hors périmètre

- **Plusieurs bases.** Les DMV de suggestions couvrent l'instance, `sys.indexes` la base
  courante, et `USE` est refusé. Deux voies : une requête d'instance qui compte les suggestions
  par base, puis une boucle `-database` ; ou une option `-each-database` dans `sqlq`, à trancher
  avec la tâche sur les différentielles (6hg66wpj37CCGhXG). Suivi Todoist.
- **Indicateur de recouvrement** calculé en SQL (colonnes d'égalité formant le début d'une clé
  existante). L'ordre des colonnes d'égalité n'est pas significatif dans la DMV, l'indicateur
  ne peut donc pas conclure. Suivi Todoist.
- **Variante ciblée sur une table** (`@table` obligatoire), si le besoin revient.
- **Persistance par le Query Store** (plans contenant `<MissingIndexes>`), qui survit aux
  redémarrages : autre requête, autre tâche.
