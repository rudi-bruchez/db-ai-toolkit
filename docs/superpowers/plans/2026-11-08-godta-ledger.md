# goDTA : registre de la relecture du plan

Relecture du plan `2026-11-08-godta.md` (commit `f467a2b`) par le panel de cinq lecteurs, le
8 novembre 2026 : agy avec le prompt directif et avec le neutre, codex avec les deux, un
sous-agent Claude avec le neutre. Tous disposaient de `sql2022` (2022 CU26) et ont exécuté les
artefacts du plan. Ce registre donne l'union de ce qu'ils ont trouvé, la décision prise pour
chaque point, et ce que le contrôleur a mesuré lui-même avant de corriger.

Sources : AD et AN (agy directif, neutre), CD et CN (codex directif, neutre), CL (Claude).

## Vérifié par le contrôleur

| Point | Mesure du contrôleur sur `sql2022` | Résultat |
|---|---|---|
| Ordre du contrôle de référence (AD) | base neuve du jeu de l'annexe A ; `SELECT c FROM dbo.godta_t WHERE a = 0 AND b = 1` sous `AUTOPILOT`, puis `SHOWPLAN_XML`, puis `AUTOPILOT` | 421 lignes estimées, coût 22,1859, aucune statistique créée ; puis `_WA_Sys_00000003` créée et 25 046 lignes, 22,2175 ; puis 25 046 sous `AUTOPILOT`. Confirmé |
| Procédure temporaire et Query Store (CL, AN) | trois `EXEC #p` et trois `sp_executesql` du même texte, `QUERY_CAPTURE_MODE = ALL`, `sp_query_store_flush_db` | seule la forme `sp_executesql` est captée (`(@a int,@b int)SELECT …`, 3 exécutions, `object_id = 0`). Confirmé |
| `dm_db_stats_properties` depuis `master` (AN) | mêmes statistiques lues par noms en trois parties depuis `master` et depuis la base | valeurs identiques ; le `NULL` rapporté vient de la statistique de `pk_godta_t`, créée sur une table vide et jamais calculée, `NULL` aussi depuis la base. Rejeté |
| `DBCC SHOW_STATISTICS` (CL, AD) | `(N'[dbo].[godta_t]', [godta_filt])` et `(N'[dbo].[godta_t]', N'godta_filt')` | les deux formes passent ; retenue : chaîne pour la table, identifiant pour la statistique |
| Format du dossier sqlferret (AN) | lecture de `docs/execution-plans.md` de sqlferret | `plans/profile/run_<id>/`, `profile` fixe ; `query-store-import` écrit sous `plans/`. Confirmé |

Les autres mesures retenues l'ont été sur la foi des relecteurs quand au moins deux d'entre eux
les avaient faites indépendamment (plans étroits : AD, AN, CL) ou quand le relecteur donnait le
XML ou le message d'erreur exact.

## Retenu et corrigé dans le plan

| # | Point | Sources | Correction |
|---|---|---|---|
| 1 | La référence `AUTOPILOT` compilée avant le `SHOWPLAN_XML` ne voit pas les statistiques automatiques que ce dernier crée : faux écarts sur toute colonne sans statistique | AD, contrôleur | tâche 10, étape 3 de `Run` : `SHOWPLAN_XML` d'abord ; tests `TestReferenceOrderMatters` et casse volontaire de l'ordre |
| 2 | La charge de test ne parvient pas au Query Store : procédure `#` jamais captée, mode `AUTO` qui en perd | CL, AN, AD, contrôleur | tâche 1 : `QUERY_CAPTURE_MODE = ALL`, `ittest.RunWorkload` par `sp_executesql` ; annexe A bis |
| 3 | Plans d'écriture étroits : un seul opérateur cluster liste tous les index maintenus, la maintenance tombait à 0 ou au coût entier | AD, AN, CL | tâche 4 : `Write.Objs` ; tâche 10 : écart d'`OwnCost` avec et sans le candidat ; tests sur `INSERT` d'une ligne |
| 4 | Le motif `godta[_]%` attrape les statistiques d'index (que `DROP STATISTICS` refuse) et les objets du jeu de test | CL, CN | `tsql.RunPattern()`, forme exacte de l'identifiant d'exécution ; exclusion des statistiques d'index ; `TestCleanupIgnoresDataset` |
| 5 | Valeur `datetime` du plan rejouée sous `SET DATEFORMAT dmy` : erreur 8114 ou autre jour en silence | CL | tâche 6, règle 7 : réécriture au format ISO avec `T` ; cas de test |
| 6 | Empreinte de schéma prise dans `collect.json` : `DROP STATISTICS` de la phase `clone` déplace `modify_date` | CL | tâche 9 : `ReadFingerprints`, lecture vivante au `--dry-run` et à l'exécution ; la spec §7.1 est tenue telle quelle |
| 7 | `DBCC SHOW_STATISTICS` avec un nom en deux parties entre crochets : erreur 102 ; nom de statistique entre crochets dans une chaîne : erreur 2767 | CL, AD | tâche 2 : forme fixée ; `TestTemplatesRunOnLab` envoie chaque gabarit au moteur |
| 8 | Une vue indexée substituée cache sa table de base, que `DeclareReal` ne déclarait pas | CL, AN | tâche 4 : `Access.ViewBase` ; tâche 10 : tables d'une requête = accès plus `ViewBase` ; jointure servie par la vue dans le test |
| 9 | Rapatriement par morceaux : chaque morceau relit le fichier ; `backupset` ne donne pas la taille du fichier ; 1 Mio = un seul morceau sur le jeu de test | CL, AD, CD | tâche 17 : défaut 256 Mio, effet de bord dans le plan, taille par `DATALENGTH`, test à 64 Kio |
| 10 | `collect.Databases` écartait `godta_%`, donc les bases de test | CL | filtre par `IsClone` et par la forme exacte ; `TestCollectSkipsClone` |
| 11 | Règle 1 du rejeu contraire au cas `(@s int)DECLARE …` ; cas 9 en T-SQL invalide ; `;WITH` rejeté | CL, AD, CD, CN | tâche 6 : déclaration détachée d'abord, mots-clés lus par instruction du corps, `DECLARE` et `SET @` admis, cas corrigés et ajoutés |
| 12 | Instructions de procédure sans préfixe, paramètres pris au plan | AD | tâche 6, règle 1 : déclaration depuis les `Param` du plan |
| 13 | `ParameterList` dans l'ordre inverse de la déclaration ; celle d'une fonction table lue à tort | AD, AN | tâche 4 : ordre du XML dans le test, appariement par nom, `ParameterList` de `QueryPlan` seule |
| 14 | Cas 4 de la table de vérité faux (parcours `BACKWARD` sans tri) | AD, CD | `covered` ; cas ajouté où une seule clé est inversée |
| 15 | Cas 11 inatteignable sur une table cluster avec les clés effectives ; « pas covered » n'est pas une décision | CL, AD | cas 11 sur un tas, et son pendant cluster en `new` ; décisions exactes partout ; `TestWidenHidesExisting` remplace `TestReplaceHidesReplaced` |
| 16 | `Candidate` sans frontière des égalités | AD | `EqCount` |
| 17 | `CheckCloneDB` avant le `RESTORE` est impossible | CL, AN, CN, CD | appelée juste après le `RESTORE` et avant chaque `evaluate` (spec §7 : « pour `evaluate` ») |
| 18 | Contexte de base jamais fixé ; pool `database/sql` sans `USE` durable | CL, AN | contrainte globale : pool ouvert avec la base dans le DSN ; signatures de `conn.Open`, `collect.Database`, `NewSession`, `Restore` |
| 19 | Exigences de la spec sans tâche : tables temporaires et autres bases, DMV d'index manquants et d'usage, statistiques auto-créées | CL, AD | tâche 6 règle 4 ; tâche 5 `IndexUsage` et `MissingIndexDMV` ; tâche 10 `AutoCreatedStats` ; annexe B |
| 20 | sqlferret : le dernier `run_N` seul perd des ingestions | CL | tous les `run_N`, dédoublonnés |
| 21 | Mesure combinée après la suppression des hypothétiques | AN | tâche 10 : création de tous, mesures seules, combinée, puis suppression ; score `made redundant by` |
| 22 | Configurations de base de type `bit` rejouées en `= 0` : erreur 102 | AN | `tsql.ScopedConfig` typé, `ON`/`OFF` ; quatre configurations de l'optimiseur seulement |
| 23 | Recherche et lookup non reliés ; filtres des parcours dans `Predicate` | AN | `Access.LookupOf`, `PredEq`, `PredRange` ; tests de génération ; le test de bout en bout attend un `widen` de `nix_h_x` |
| 24 | Crochet du labo : flux indexé par nom de statistique que le candidat ignore | AD, AN, CD | `lab.StreamKey(table, clés)` ; `lab.StatsHook` ; `Hooks` rend des notes |
| 25 | Candidats de l'IA et de sqlferret sans requêtes ; requêtes de l'index remplacé absentes | AN | `BindQueries` ; `ConfrontAll` ajoute les requêtes de l'existant visé |
| 26 | Suggestions arrivées après le clone sans traitement | AN, CN | note `unreliable estimate`, jamais recommandée ; `--suggestions` sur `evaluate` ; voie de transfert à la demande non faite, dite dans l'annexe B |
| 27 | Paramètres de `ForClone` absents du `--dry-run` | AN | `--backup-dir` et `--max-bytes` au `--dry-run` ; `InstanceDefaultBackupPath` lu par `Identify` |
| 28 | Types partagés déclarés dans le paquet d'un autre | CD, AD | tous dans `model` dès la tâche 5 |
| 29 | `dotenv.Load` de sylloge rend `error`, pas `(int, error)` | CD, CL, AD, AN | API de sylloge gardée |
| 30 | `go run` d'un répertoire dont le fichier porte la balise `ignore` | CD, CN, CL | chemin du fichier |
| 31 | Comptes de tests faux (filtre qui attrape un test unitaire, test sauté compté) | CD, CN, AD | portes par noms et lignes `--- PASS`, `SKIP` refusé sauf test facultatif ; `TestPagesFormula` renommé |
| 32 | Erreurs de connexion pouvant contenir le mot de passe | CN | `conn.Redact`, tests unitaire et de sortie JSON |
| 33 | Base nommée `..` : chemin hors de l'espace de travail | CN | `SafeName` ; `TestDBDirStaysUnderRoot` |
| 34 | Erreur 8601 sous `AUTOPILOT` sans remise à zéro préalable | AD | `NewSession` envoie `AutopilotReset` |
| 35 | Session sans pool pour remplacer une connexion abandonnée | AN | `NewSession` prend le pool de la base |
| 36 | Test de la tâche 10 qui dépend de `cleanup` (tâche 13) | AN | reprise par `cleanup` testée à la tâche 13 |
| 37 | `DELETE`, `INSERT`, `MERGE` « de la tâche 4 » qui n'existent pas | AD, AN | instructions écrites dans le test |
| 38 | `xp_delete_file` sur un fichier absent lève une erreur | AD | `xp_fileexist` d'abord |
| 39 | Types SQL de `SERVERPROPERTY` et `value_in_use` | AD | `CAST` dans la requête |
| 40 | Compression de sauvegarde aussi en Standard | AN | `CanCompress` pour `EngineEdition` 2 et 3 |
| 41 | `SummaryRow` et `Reason` non définis ; statistiques auto-créées sans source | AD | dans `model` ; `report.Write` reçoit la liste |

## Rejeté, avec la raison

| Point | Sources | Raison |
|---|---|---|
| `sys.dm_db_stats_properties` rend `NULL` depuis `master` | AN | mesuré faux par le contrôleur (tableau plus haut) ; le contexte de base reste fixé, pour les instructions de base qui n'acceptent pas trois parties |
| `evaluate --target lab` devrait avoir un plan d'exécution | CN | la spec §7 dit le contraire dans son tableau des phases (« gardes du labo »), approuvée par Rudi ; le plan suit la spec. À rouvrir avec Rudi s'il le souhaite |
| Les tests du labo ne peuvent pas tourner sur un seul conteneur | AD, CD | le plan exige deux conteneurs (`GODTA_TEST_LAB_PROFILE`) ; c'est la contrainte du relecteur, pas un défaut ; le plan le dit maintenant explicitement à la tâche 18 |
| `ParameterDataType` sans longueur | AN | non mesuré par le relecteur ; un type incomplet ferait échouer la compilation, que `Run` rapporte en `not evaluated: error <n>` sans fausse mesure ; à surveiller à la revue de la partie 1 sur la copie de `PachaDataFormation` |
| Ajouter un plan d'exécution à `ShowStatisticsStream` côté labo, ou un `USE` dans les gabarits | AN | remplacé par la contrainte globale du DSN par base, plus simple et sans état de session |

## Restent ouverts

- L'égalité source et labo entre deux instances (et non une seule) n'est mesurée par personne :
  c'est l'objet de `TestLabEqualsServer`, à lancer sur 2017, 2022 et si possible 2016.
- La voie « transfert des seules statistiques nouvelles » de la spec §8.4 n'a pas de tâche :
  goDTA propose l'évaluation sur le serveur ou un nouveau `clone`. À ajouter si l'usage le
  demande.

## Relecture adverse des corrections

Un sous-agent Claude neuf a relu les corrections ci-dessus (diff `f467a2b..6a237a8`) en
exécutant les annexes A et A bis sur `sql2022`. Quinze points, dont plusieurs dans des lignes
écrites pour corriger le panel ; tous retenus sauf le dixième, laissé à Rudi.

| # | Point | Correction |
|---|---|---|
| 1 | Le test de bout en bout ne pouvait pas obtenir sa recommandation : la jointure de l'annexe A bis lit aussi `nix_h_x`, entre dans `Queries` et ramène le gain à 0,5 % du coût cumulé, sous le seuil de 10 % | jointure retirée de la charge ; la raison écrite dans la tâche 15 |
| 2 | La règle « clés effectives des deux côtés » rendait les cas 4 `new` : la clé cluster implicite, croissante des deux côtés, entrait dans la règle du sens | clé cluster implicite hors de la condition 3 ; parcours `BACKWARD` sans tri mesuré sur `godta_t` |
| 3 | L'`INSERT` étroit a un second `Write`, maintenance de la vue indexée | test sur le `Write` de `godta_t` |
| 4 | `AUTOPILOT` ne rafraîchit pas une statistique périmée : la fixture « étroite » sortait large | capture précédée d'un `SHOWPLAN_XML` ; raison de l'ordre complétée ; écritures passées par l'étape 3 |
| 5 | « Pas de nom en trois parties » faux pour `CREATE INDEX`, `DROP INDEX`, `DBCC SHOW_STATISTICS` | contrainte réécrite avec ses vraies raisons |
| 6 | `date_format` est un `smallint` ; `datetime2` tronqué par la réécriture | table de correspondance ; réécriture limitée à `datetime` et `smalldatetime` |
| 7 | Filtre de la tâche 5 qui n'attrape pas les tests unitaires attendus | filtre élargi |
| 8 | `--lock-timeout` sur la phase réelle alors que `SET LOCK_TIMEOUT` est une étape scellée | déplacé au `--dry-run`, paramètre de `ForEvaluate` |
| 9 | Crochet du labo sans moyen d'écrire ni nom de table ; tests unitaires sans session factice | `Session.Exec`, interface interne et `NewRecordingSession` ; `StatsHook(p, w)` |
| 10 | `replace` ne se déclenche jamais sur une table cluster avec les clés effectives | fidèle à la spec §8.3, laissé en l'état ; décision demandée à Rudi |
| 11 | `cleanup` sans moyen d'ouvrir un pool par base | paramètre `open` |
| 12 | État du labo non transmis entre deux invocations | `lab.State`, `SaveState`, `LoadState` |
| 13 | Tâche 17 qui disait 64 Mio et 256 Mio | 256 Mio |
| 14 | `CanCompress` vrai pour Web (`EngineEdition` 2) | Web exclue par `Edition` |
| 15 | `scan_param` « par `sp_executesql` » sous `AUTOPILOT`, qui ne rend pas le plan interne | par le rejeu `proc` |

Le relecteur a aussi levé un doute du registre : `ParameterDataType` porte bien la longueur
(`varchar(20)`, `datetime2(7)`).
