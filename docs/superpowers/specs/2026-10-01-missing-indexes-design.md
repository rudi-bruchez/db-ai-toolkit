# Design — index manquants et index existants de la table (requête livrée)

Date : 2026-10-01
Statut : révisé après la relecture du panel (agy et codex, consignes dirigée et neutre, plus un
sous-agent Claude). Le traitement de chaque constat est en §10. En attente de relecture avant
plan d'implémentation.
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

Ce résultat sert à **proposer** des index. Il ne sert pas à en **supprimer** : ses compteurs
d'usage ne couvrent ni toute la vie de la base, ni les autres réplicas (§3). Le protocole de
lecture l'interdit (§6).

## 2. Décisions

| Question | Décision |
|---|---|
| Option CLI ou requête livrée ? | Requête livrée, lancée par `sqlq -file ... -database <base>`. Aucune surface CLI nouvelle, aucun Go. |
| Point d'entrée | Vue d'ensemble de la base courante, sans paramètre. `sqlq` n'a pas de paramètre optionnel : une variable référencée sans `-param` fait échouer l'appel. |
| Grain | Un seul jeu de résultats, trois sortes de lignes (`context`, `missing`, `existing`), regroupées par table. Les compteurs restent numériques. |
| Tables retenues | Les **3** tables dont la somme des scores de suggestion est la plus haute. Valeur fixe dans le fichier. La somme est un critère de classement, pas une estimation de gain : des suggestions voisines y comptent chacune. |
| Lignes par table | Au plus **8** suggestions (les meilleurs scores) et **15** index (par `index_id`, donc heap ou clustered en premier). Chaque ligne porte les totaux de sa table, pour que l'agent sache ce qui n'est pas montré. |
| Borne finale | `TOP (70)` = 1 + 3 × (8 + 15). Le skill passe `-maxrows 70` : la requête ne peut pas produire plus de lignes que `sqlq` n'en garde, donc `truncated` ne coupe jamais un bloc de table. |
| Score | Celui de la documentation Microsoft : `avg_total_user_cost * avg_user_impact * (user_seeks + user_scans)`. |
| DDL | Aucun. Une instruction prête à copier pousse à la coller telle quelle. |
| Objets système | Exclus (`is_ms_shipped = 1`), et leurs suggestions comptées à part. Conséquence assumée : les tables de `msdb`, de CDC (`cdc.*`) et de réplication sont `is_ms_shipped`, leurs suggestions n'apparaissent pas en lignes. |
| Moteurs | Écrit pour SQL Server 2012 et suivants et Azure SQL Managed Instance : pas de `STRING_AGG`, pas de colonne de catalogue apparue après 2012. **Testé sur SQL Server 2019 seulement** ; l'en-tête le dit. Azure SQL Database : DMV présentes, requête non testée. |

## 3. Ce que la documentation établit, et ce qu'elle n'établit pas

Vérifié sur Microsoft Learn le 2026-10-01, et recoupé par le panel.

- **Plafond.** Learn : les suggestions sont collectées pour 600 groupes au plus, après quoi plus
  rien n'est collecté ; les trois DMV sont limitées à 600 lignes. Un relecteur affirme 500 sur
  les versions antérieures à 2017 ; Learn ne documente que 600 et ne date pas la valeur. La
  requête lève donc l'alerte à 500 (§4). Un compteur sous le seuil ne prouve pas qu'aucune
  suggestion n'a été perdue : un plafond atteint puis libéré ne laisse pas de trace.
- **Grain des groupes.** « An index group contains only one index » ; les deux colonnes de
  `sys.dm_db_missing_index_groups` forment ensemble la clé. Rien ne garantit qu'un
  `index_handle` n'appartient qu'à un groupe : le grain de la requête est donc le groupe.
- **Effacement des suggestions.** Au redémarrage, au basculement, à la mise hors ligne de la
  base. Et **pour une table** quand ses métadonnées changent (colonne ajoutée ou supprimée,
  index créé) ou qu'un `ALTER INDEX` porte sur un de ses index. La fenêtre d'observation est
  donc par table, et aucune vue ne donne sa date de début.
- **Effacement des compteurs d'usage.** `sys.dm_db_index_usage_stats` est vidée au démarrage,
  et les lignes d'une base disparaissent quand elle est détachée ou fermée (`AUTO_CLOSE`). Une
  ligne n'existe qu'à partir de la première utilisation de l'index. **La durée de vie de
  l'instance n'est donc qu'une borne haute** de la fenêtre des compteurs d'une base.
- **Réplicas.** Les compteurs d'usage et les suggestions sont locaux au réplica interrogé :
  Microsoft conseille de surveiller l'usage des index sur un secondaire lisible en interrogeant
  la DMV sur ce secondaire. Un index qui ne sert qu'aux rapports d'un secondaire a des lectures
  à 0 sur le primaire.
- **Ce qui n'est jamais suggéré.** Index unique, filtré, clustered, columnstore. Pas de
  suggestion pour un plan trivial. Coût moins fiable quand il n'y a que des prédicats
  d'inégalité.
- **Colonnes implicites.** `sys.index_columns` ne liste pas les colonnes de la clé clustered
  qu'un index non clustered porte implicitement.
- **Tables en mémoire.** Pour un index en mémoire, ignorer `included_columns` de la
  suggestion : toutes les colonnes y sont. `sys.dm_db_index_usage_stats` ne couvre ni les index
  en mémoire, ni les index spatiaux.
- **`filter_definition`** vaut NULL pour un heap, un index non filtré, **ou faute de droits**.
- **Droits.** DMV de suggestions et `sys.dm_db_index_usage_stats` : `VIEW SERVER STATE`
  jusqu'à 2019, `VIEW SERVER PERFORMANCE STATE` à partir de 2022.
  `sys.dm_db_partition_stats` : `VIEW DATABASE STATE` et `VIEW DEFINITION` sur la base
  jusqu'à 2019, `VIEW DATABASE PERFORMANCE STATE` et `VIEW SECURITY DEFINITION` à partir de
  2022.
- **Non établi par la documentation.** La remise à zéro de `sys.dm_db_index_usage_stats` par un
  rebuild sur certaines versions 2012 et 2014 vient de billets de support ; l'en-tête la cite
  comme connue et non vérifiée. L'effet d'un `ALTER INDEX ... REORGANIZE` sur les suggestions
  n'est pas distingué de celui d'un rebuild : il est observé en validation (§8, M6b).

## 4. La requête

Fichier : `plugins/sqlserver-toolkit/skills/live-query/queries/missing-indexes.sql`.
Aucun paramètre. Un seul batch, sans `GO`, `USE`, `EXEC`, `INTO` ni `DECLARE`.
`OPTION (RECOMPILE, MAXDOP 1)`, comme les scripts sources.

### Forme du résultat

| `row_kind` | nombre | rôle |
|---|---|---|
| `context` | exactement une, toujours, en tête | dit si une conclusion est possible |
| `missing` | au plus 8 par table retenue | ce que l'optimiseur a demandé |
| `existing` | au plus 15 par table retenue, heap ou clustered toujours compris | ce qui existe déjà |

Sans la ligne `context`, une base sans suggestion rend zéro ligne, et l'agent ne peut pas
distinguer « rien à suggérer », « instance redémarrée il y a une heure » et « collecte
plafonnée ».

### Colonnes, dans cet ordre, avec leur type SQL

Le type est imposé par un `CAST` explicite. Il compte : `sqlq` rend `decimal` et `numeric` en
chaînes JSON (`normalise`, `tools/cmd/sqlq/main.go`), et une chaîne `"0.4"` ne se compare pas à
1. Aucune colonne n'est donc `decimal`. Un `bit` sort en JSON `true` / `false` (`go-mssqldb`) :
dans ce document, « `= 1` » pour un `bit` se lit `true`.

Colonnes communes :

| colonne | type | `context` | `missing` / `existing` |
|---|---|---|---|
| `row_kind` | `varchar(8)` | `context` | `missing` / `existing` |
| `table_rank` | `int` | 0 | 1 à 3 |
| `schema_name`, `table_name` | `sysname` | NULL | la table |
| `table_suggestion_count` | `int` | NULL | nombre total de suggestions de la table |
| `table_index_count` | `int` | NULL | nombre total d'index de la table, heap compris |
| `memory_optimized` | `bit` | NULL | 1 si la table est en mémoire |

Lignes `existing` :

| colonne | type | contenu |
|---|---|---|
| `index_name` | `sysname` | NULL pour un heap |
| `index_type` | `nvarchar(60)` | `sys.indexes.type_desc` |
| `is_unique`, `is_primary_key`, `is_disabled`, `has_filter` | `bit` | `sys.indexes` |
| `filter_definition` | `nvarchar(max)` | NULL si non filtré **ou** illisible : lire `has_filter` |
| `key_columns` | `nvarchar(max)` | clés par `key_ordinal`, chacune par `QUOTENAME`, suffixe ` DESC` si `is_descending_key` |

Colonnes partagées par `missing` et `existing` (NULL sur `context`) :

| colonne | type | `missing` | `existing` |
|---|---|---|---|
| `equality_columns`, `inequality_columns` | `nvarchar(4000)` | DMV, telles quelles (noms entre crochets) | NULL |
| `included_columns` | `nvarchar(max)` | DMV, telle quelle | colonnes incluses par `index_column_id`, par `QUOTENAME` |
| `user_seeks`, `user_scans` | `bigint` | `group_stats` | `index_usage_stats`, 0 si pas de ligne |
| `user_lookups`, `user_updates` | `bigint` | NULL | `index_usage_stats`, 0 si pas de ligne |
| `last_used` | `varchar(19)` | plus récente de `last_user_seek`, `last_user_scan` | plus récente de seek, scan, lookup |
| `used_mb` | `float` | NULL | `used_page_count` sommé sur les partitions, LOB compris ; NULL pour une table en mémoire |
| `usage_not_tracked` | `bit` | NULL | 1 pour un index spatial ou une table en mémoire |
| `avg_user_impact`, `score` | `float` | `group_stats`, score calculé | NULL |
| `unique_compiles` | `bigint` | `group_stats` | NULL |

Quand `usage_not_tracked = 1`, les quatre compteurs et `last_used` sont NULL, pas 0 : un 0
voudrait dire « jamais utilisé », un NULL veut dire « on ne sait pas ».

Ligne `context` (NULL ailleurs) :

| colonne | type | contenu |
|---|---|---|
| `instance_start_time` | `varchar(19)` | `sqlserver_start_time` |
| `instance_uptime_days` | `float` | jours depuis le démarrage, **non arrondi** (un arrondi ferait passer 23 h 20 pour 1,0), borne haute de la fenêtre d'observation |
| `is_auto_close_on` | `bit` | `sys.databases`, base courante |
| `database_in_ag` | `bit` | `sys.databases.replica_id IS NOT NULL` |
| `suggestion_groups_on_instance` | `int` | groupes de suggestions, toutes bases |
| `collection_capped` | `bit` | 1 si ce nombre atteint 500 (§3) |
| `tables_with_suggestions` | `int` | tables utilisateur visibles de la base ayant au moins une suggestion |
| `suggestions_on_system_objects` | `int` | suggestions écartées parce que l'objet est `is_ms_shipped` |
| `suggestions_on_unresolved_objects` | `int` | suggestions dont l'objet est introuvable dans `sys.objects` : supprimé, ou invisible au login |
| `hypothetical_indexes` | `int` | index `is_hypothetical` de la base |

### Ordre

`table_rank`, puis `missing` avant `existing`. Les `missing` par `score` décroissant puis
`index_group_handle` puis `index_handle` ; les `existing` par `index_id`. Entre tables, une
égalité de somme de scores se départage par `object_id`. Un même état rend le même ordre.

### Invariants d'implémentation

- **Base courante, partout.** `database_id = DB_ID()` sur `sys.dm_db_missing_index_details` et
  sur `sys.dm_db_index_usage_stats`.
- **Noms par jointure** sur `sys.objects`, jamais par `OBJECT_NAME()` ni par la colonne
  `statement`. Trois issues, comptées séparément : objet utilisateur visible (`type = 'U'`,
  `is_ms_shipped = 0`), objet système (compté dans `suggestions_on_system_objects`), objet
  introuvable (compté dans `suggestions_on_unresolved_objects`).
- **Index existants complets.** Point de départ `sys.indexes` (hors `is_hypothetical`).
  Usage par jointure **externe** vers `sys.dm_db_index_usage_stats` sur `object_id` et
  `index_id`. Taille par jointure **externe** vers un agrégat de `sys.dm_db_partition_stats`
  par `(object_id, index_id)`, jamais vers ses lignes brutes : une partition ne doit pas
  dupliquer un index.
- **Colonnes d'index.** Clés : `key_ordinal > 0`, triées par `key_ordinal`. Incluses :
  `is_included_column = 1`, triées par `index_column_id`. Une colonne de partitionnement non
  déclarée et les colonnes implicites de la clé clustered n'apparaissent dans aucune liste ; le
  protocole de lecture le compense (§6).
- **Agrégations de texte** par `FOR XML PATH(''), TYPE).value('.', 'nvarchar(max)')`, pour
  qu'un nom contenant `&` ou `<` ressorte intact.
- **Score en `float`.** Un produit en `decimal` peut déborder, et sortirait en chaîne.
- **Classement et totaux avant présentation.** Somme des scores par table, totaux par table et
  compteurs de la ligne `context` sont calculés sur toutes les suggestions de la base, avant
  le `TOP (3)` des tables et avant les plafonds par table.
- **Tables en mémoire** repérées par
  `CASE WHEN OBJECTPROPERTY(object_id, 'TableIsMemoryOptimized') = 1 THEN 1 ELSE 0 END` :
  la propriété rend NULL sur 2012, que le `CASE` ramène à 0. `sys.tables.is_memory_optimized`
  échouerait à la compilation sur 2012. Index spatiaux : `sys.indexes.type = 4`.
- **Dates en texte.** `instance_start_time` et `last_used` sont des `datetime` en heure locale
  du serveur ; `sqlq` les rend aujourd'hui suffixées `Z`, donc fausses du décalage horaire
  (vérifié dans `normalise` et par le panel ; tâche Todoist 6hfvCXxP39vff2vG, §1). La requête
  les convertit en texte ISO sans fuseau (`CONVERT(varchar(19), x, 126)`).
- **Types fixes** sur chaque sorte de ligne : les NULL portent le type de leur colonne par
  `CAST`, pour que l'union garde les types du tableau ci-dessus.

## 5. En-tête du fichier

Sur le modèle de `blocked-processes-check.sql`, sous « READ THIS BEFORE TRUSTING THE RESULT » :

- lire la ligne `context` d'abord. `instance_uptime_days` inférieur à un jour, ou
  `collection_capped = 1`, interdit toute conclusion dans un sens comme dans l'autre ;
  `suggestions_on_unresolved_objects > 0` veut dire que des suggestions portent sur des objets
  que le login ne voit pas ou qui n'existent plus ;
- `instance_uptime_days` est une borne haute : une restauration, une mise hors ligne,
  `AUTO_CLOSE` ou un basculement raccourcissent la fenêtre réelle de la base sans la changer ;
- une absence de suggestion ne prouve rien : effacement par `ALTER INDEX`, création d'index ou
  changement de colonnes, plan trivial, spool eager (qui supprime la suggestion) ;
- une suggestion n'est pas une prescription : ordre des colonnes d'égalité non significatif,
  colonnes incluses sans analyse de taille, `avg_user_impact` est une estimation sur une
  estimation, jamais d'index unique, filtré, clustered ni columnstore ;
- la fenêtre d'observation est par table : une table souvent reconstruite est sous-représentée
  dans le classement ; et le classement additionne des suggestions voisines ;
- les compteurs et les suggestions ne valent que pour le réplica interrogé
  (`database_in_ag = 1`) ;
- **ce résultat ne justifie aucune suppression d'index** ;
- les listes de colonnes sont les colonnes déclarées : les colonnes de la clé clustered sont
  portées implicitement par chaque index non clustered ;
- limites assumées : objets `is_ms_shipped` exclus, base courante seulement, tables en mémoire
  et index spatiaux sans compteurs (`usage_not_tracked`), 3 tables, 8 suggestions et 15 index
  par table au plus (les totaux disent ce qui manque) ;
- droits : ceux du §3. Le comportement sans eux est celui observé en validation (M8, M9) ;
- versions : écrit pour 2012 et suivants, testé sur 2019.

## 6. Le skill

`plugins/sqlserver-toolkit/skills/live-query/SKILL.md` :

- une ligne dans l'arbre de décision : « Which indexes are missing » →
  `-file queries/missing-indexes.sql -database <db> -maxrows 70`, puis le protocole de
  lecture ; renvoi à l'en-tête du fichier ;
- quatre interdits en clair : recopier une suggestion en `CREATE INDEX` ; conclure « aucun
  index manquant » d'une absence de lignes ; recommander la suppression d'un index à partir de
  ce résultat ; ajouter une colonne à la clé d'un index unique ou d'une clé primaire.

`plugins/sqlserver-toolkit/skills/live-query/references/missing-index-reading.md`, chargé à la
demande, pour garder le corps du skill court :

1. Lire `context`. S'arrêter et le dire si `instance_uptime_days < 1` ou
   `collection_capped = 1`. Signaler `is_auto_close_on`, `database_in_ag`,
   `suggestions_on_unresolved_objects > 0`.
2. Pour chaque table, comparer les lignes montrées aux totaux `table_suggestion_count` et
   `table_index_count`. Si des index existants ne sont pas montrés, ne pas proposer de nouvel
   index sur cette table sans les avoir listés par une autre requête.
3. Regrouper les suggestions de la table entre elles.
4. Pour chaque groupe, chercher un index existant dont les **premières colonnes de clé**
   couvrent l'**ensemble** des colonnes d'égalité, dans n'importe quel ordre : l'ordre de la
   DMV n'est pas significatif. Tenir compte des colonnes de la clé clustered (ligne clustered de
   la même table), portées implicitement par chaque index non clustered.
5. Élargir plutôt que créer, avec trois réserves. Un index unique ou une clé primaire ne
   s'élargit que par `INCLUDE`. Un index filtré (`has_filter = 1`) ne couvre que les lignes de
   son filtre, et un filtre illisible (`filter_definition` NULL) rend la couverture inconnue.
   Un index désactivé ne couvre rien.
6. Table en mémoire (`memory_optimized = 1`) : ignorer `included_columns` de la suggestion.
7. Peser `user_updates` contre `user_seeks + user_scans + user_lookups` avant d'ajouter un
   index à une table très écrite, en se souvenant que ces compteurs ne couvrent que ce réplica
   et une fenêtre au plus égale à `instance_uptime_days`.
8. Rédiger une proposition consolidée. L'ordre des colonnes d'égalité reste à choisir par
   quelqu'un qui connaît leur sélectivité : le résultat ne la donne pas.
9. Citer dans la réponse `instance_uptime_days` comme borne haute, et la limite de fenêtre par
   table.

Les phrases du skill sont en anglais, comme le reste de `live-query`.

## 7. Taille du résultat

`sqlq` écrit chaque ligne comme une table de toutes les colonnes, NULL compris (`Row`,
`tools/internal/sqlq/result.go`). Avec les colonnes du §4, une ligne fait de l'ordre de 800
caractères. Le maximum de 70 lignes fait donc environ 55 000 caractères ; un cas courant tourne
autour de 25 lignes. La taille réelle est mesurée en validation et consignée.

## 8. Tests et validation

**Statique.** `TestBundledQueriesPassTheReadOnlyGuard` prend le fichier automatiquement. Aucun
test Go nouveau.

**En direct**, rapport dans `docs/validation/2026-10-01-missing-indexes.md`, sur l'instance de
test de la validation précédente (SQL Server 2019 Developer). Base jetable `mi_validation`,
plus `mi_validation_other` pour M5. La mise en place écrit : chaque instruction est montrée à
l'utilisateur avant exécution. Chaque cas relit sa précondition avant l'observation.

| cas | mise en place | attendu |
|---|---|---|
| V0 | aucune | pas d'erreur ; `columns` dans l'ordre et avec les types du §4 (le champ `type` de chaque colonne, pas l'ordre des clés JSON, que `sqlq` trie) ; une ligne `context` ; aucune valeur numérique rendue en chaîne |
| M1 | base neuve, aucune requête | la seule ligne `context`, `tables_with_suggestions = 0` |
| M2 | requêtes sans index adapté sur 4 tables, d'intensités différentes | exactement 3 tables ; rangs conformes aux sommes de scores ; ordre identique sur deux exécutions |
| M3 | une table avec 3 suggestions et 4 index : un jamais utilisé, un désactivé, un filtré, une clé `DESC` avec `INCLUDE` | les 3 `missing`, les 4 `existing` et le clustered ; l'index jamais utilisé présent à 0 ; clés dans l'ordre, entre crochets, avec `DESC` ; `has_filter = 1` et filtre restitué |
| M3b | une table avec 10 suggestions et 17 index | 8 `missing` et 15 `existing` montrés ; `table_suggestion_count = 10`, `table_index_count = 17` ; aucune autre table amputée |
| M4 | colonne dont le nom contient `&` et `<` | nom restitué tel quel |
| M5 | suggestions dans `mi_validation_other` | aucune ligne de l'autre base en lisant `mi_validation` ; `suggestion_groups_on_instance` les compte |
| M6 | `ALTER INDEX ... REBUILD` sur une table à suggestions | ses suggestions disparaissent |
| M6b | `ALTER INDEX ... REORGANIZE` sur une autre table | observé et consigné, l'en-tête reprend le constat |
| M7 | heap avec suggestions | ligne `existing` présente, `index_type = HEAP`, `index_name` NULL |
| M7b | table partitionnée sur plusieurs partitions | une seule ligne par index, `used_mb` sommé |
| M8 | login sans `VIEW SERVER STATE` | erreur, `sqlq` code 2, pas de résultat vide |
| M9 | login avec `VIEW SERVER STATE`, sans `VIEW DEFINITION` sur la base | observé et consigné. Acceptable : une erreur, ou des suggestions comptées dans `suggestions_on_unresolved_objects`. Inacceptable : une table à suggestions qui disparaît sans être comptée |
| M10 | table en mémoire, si l'instance le permet | `memory_optimized = 1`, `usage_not_tracked = 1`, compteurs et `used_mb` NULL |
| M11 | base `AUTO_CLOSE ON` | `is_auto_close_on = 1` |

Non reproduits, et le rapport le dit : le plafond de collecte (`collection_capped`), vérifié
par lecture du code ; `database_in_ag = 1` et le comportement sur un secondaire, faute d'AG sur
l'instance de test sauf si elle en porte un.

**Nettoyage.** Les deux bases et les logins temporaires sont supprimés, et le rapport le
constate.

**Revues** (route FULL) : panel de design sur cette spec (fait, §10) puis sur le plan ;
`external-code-review` (agy et codex, opencode n'est pas installé) ; revue des risques
adversariale.

## 9. Hors périmètre

- **Plusieurs bases.** Les DMV de suggestions couvrent l'instance, `sys.indexes` la base
  courante, et `USE` est refusé. Deux voies : une requête d'instance qui compte les suggestions
  par base, puis une boucle `-database` ; ou une option `-each-database` dans `sqlq`, à trancher
  avec la tâche sur les différentielles (6hg66wpj37CCGhXG). Suivi Todoist.
- **Indicateur de recouvrement** calculé en SQL. L'ordre des colonnes d'égalité n'est pas
  significatif dans la DMV, l'indicateur ne peut donc pas conclure. Suivi Todoist.
- **Index inutilisés et suppression.** Une autre question, qui exige une fenêtre d'observation
  établie par base et par réplica. Suivi Todoist.
- **Variante ciblée sur une table** (`@table` obligatoire), si le besoin revient.
- **Persistance par le Query Store** (plans contenant `<MissingIndexes>`), qui survit aux
  redémarrages : autre requête, autre tâche.
- **Rendu des `decimal` en chaînes et des `datetime` suffixés `Z` par `sqlq`** : contournés
  ici, corrigés par la tâche 6hfvCXxP39vff2vG.

## 10. Relecture du panel et traitement

Cinq lecteurs le 2026-10-01 : agy et codex, chacun avec la consigne dirigée et la consigne
neutre, et un sous-agent Claude neutre. Aucun n'avait accès à une instance : les constats sur
le moteur reposent sur la documentation, ceux sur `sqlq` sur le code. Les rapports sont dans
`docs/reviews/2026-10-01-missing-indexes-design-panel/`. Codex tournait en bac à sable
`read-only` (le mode `workspace-write` bloque toute commande sous Windows).

Les trois constats sur `sqlq` ont été vérifiés dans le code avant d'être retenus : troncature
qui garde le début (`RowSet.Add`), `decimal` rendu en chaîne (`normalise`), ligne écrite avec
toutes ses colonnes (`Row`).

| # | constat | relevé par | traitement |
|---|---|---|---|
| 1 | `observed_days` mesure l'instance ; restauration, `AUTO_CLOSE`, basculement raccourcissent la fenêtre de la base, et le protocole faisait citer ce nombre pour supprimer un index | les cinq | renommé `instance_uptime_days`, présenté comme borne haute ; `is_auto_close_on` et `database_in_ag` ajoutés ; suppression d'index interdite (§1, §6) |
| 2 | troncature par préfixe : les index existants disparaissent avant les suggestions, puis des tables entières | codex ×2, Claude | plafonds par table, totaux par table, `TOP (70)` égal à `-maxrows` (§2) |
| 3 | taille environ trois fois l'estimation | Claude | 3 tables au lieu de 5, arbitrage de l'utilisateur (§2, §7) |
| 4 | `decimal` rendu en chaîne JSON | Claude | types imposés, aucun `decimal` ; V0 contrôle les types (§4, §8) |
| 5 | élargir la clé d'un index unique change la contrainte | codex ×2 | interdit, `INCLUDE` seulement (§6) |
| 6 | la règle du préfixe suppose un ordre que la DMV ne donne pas | agy, codex | comparaison par ensemble (§6) |
| 7 | colonnes implicites de la clé clustered non listées | codex ×2 | dit dans l'en-tête, compensé par le protocole (§5, §6) |
| 8 | `filter_definition` NULL faute de droits | codex ×2 | `has_filter` ajouté (§4, §6) |
| 9 | tables en mémoire : `included_columns` à ignorer | codex, Claude | `memory_optimized` sur chaque ligne de table, branche du protocole (§4, §6) |
| 10 | droits de `sys.dm_db_partition_stats` omis ; M9 incohérent | codex ×2 | droits complétés (§3) ; M9 réécrit en observation avec critère d'acceptation (§8) |
| 11 | le compteur d'objets cachés mélange objets système et objets invisibles | agy, codex, Claude | deux compteurs (§4) |
| 12 | compteurs et suggestions locaux au réplica | Claude | `database_in_ag`, en-tête et protocole (§3, §5, §6) |
| 13 | jointure aux partitions brutes qui duplique les index | agy, codex | agrégat par `(object_id, index_id)` en jointure externe ; M7b (§4, §8) |
| 14 | un `index_handle` dans plusieurs groupes n'est pas exclu | agy, codex | grain = groupe, départage par `index_group_handle` (§3, §4) |
| 15 | plafond à 500 avant 2017 | agy | non vérifiable sur Learn ; seuil à 500 par prudence, un faux « impossible de conclure » valant mieux qu'un faux « complet » (§3) |
| 16 | la somme des scores compte plusieurs fois des suggestions voisines | codex, Claude | somme gardée (choix de l'utilisateur), nommée critère de classement (§2, §5) |
| 17 | `key_columns` sans crochets, DMV avec | Claude | `QUOTENAME` (§4) |
| 18 | en-tête qui annonce 2012 sans test hors 2019 | Claude | versions testées dites (§2, §5) |
| 19 | règle `TOP (n)` d'`AGENTS.md` | codex ×2 | couvert par le n° 2 |
| 20 | la sélectivité invoquée par le protocole n'est pas dans le résultat | codex | le protocole renvoie le choix à quelqu'un qui la connaît (§6) |

Aucun constat rejeté. Les points jugés sans problème par plusieurs lecteurs, et donc non
modifiés : le garde accepte les commentaires d'en-tête et `user_updates` (`Sanitize` blanchit
les commentaires) ; `OBJECTPROPERTY` ne casse pas 2012 ; le filtre `DB_ID()` suffit à séparer
les bases ; la formule du score est celle de Microsoft ; un brouillon de requête respectant
toutes les contraintes passe les trois contrôles de refus (sous-agent Claude).
