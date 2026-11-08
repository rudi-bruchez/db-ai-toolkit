# goDTA : registre d'exécution

Décisions prises seules par le contrôleur pendant l'exécution du plan
`docs/superpowers/plans/2026-11-08-godta.md`, branche `feat/godta`, avec la raison et le coût
si elles sont fausses.

| Décision | Raison | Coût si fausse |
|---|---|---|
| Sous-agents par l'outil Agent seulement, modèle par défaut de la session | consigne de Rudi du 8 novembre 2026 : abonnement, jamais l'API ; tâches à jugement, pas mécaniques | aucun |
| Tâches 1 et 3 en parallèle dans le même arbre | fichiers disjoints, commits par chemins explicites | un conflit d'index git, rattrapé par une nouvelle tentative |
| Profil de test `godta-src` en `readwrite`, base `master`, mot de passe par `passwordEnv`, fichier hors du dépôt (scratchpad) | le profil de Rudi est `readonly` sur `PachaDataFormation` ; les tests créent leurs bases | aucun : rien n'est écrit dans le dépôt |
| Tâche 3 acceptée sans relecteur (`16b4faa`) ; tests relancés par le contrôleur, 7 PASS | paquet pur de 340 lignes, 16 mutations rapportées toutes tombées | un défaut du journal se verrait à la tâche 13 |
| Laissé pour la revue de la partie 1 : deux bases `Sales` et `sales` d'une instance sensible à la casse partagent un répertoire sur un système de fichiers insensible (Windows) ; noms réservés Windows (`CON`) | hors brief, rare ; signalé par l'implémenteur | rapports de deux bases mêlés sur Windows |
| Identité d'un objet du journal : (Server, Database, Kind, Schema, Table, Name, Path), sans RunID | choix de l'implémenteur : un `cleanup` ultérieur journalise sous son propre RunID | à respecter par la tâche 13 (dit dans son brief) |
| Tâche 1 acceptée sans relecteur (`c89c98d`) ; 15 tests relancés par le contrôleur avec l'environnement, `go vet` propre | toutes les mutations tombées ; `AUTO` confirmé à 2 exécutions sur 3 | faible |
| `CheckSupported` refuse aussi les `EngineEdition` hors 1 à 4 (Synapse, Edge) | choix de l'implémenteur, cohérent avec « refus de ce qui n'est pas mesuré » | un client sur une plateforme non mesurée est refusé, ce qui est voulu |
| Tâches 2 et 4 en parallèle | paquets disjoints (`tsql`, `showplan`) | conflit d'index git |
| Tâches 2 (`9092b67`) et 4 (`590df9c`) acceptées sans relecteur de tâche ; gates relancés par le contrôleur | 13 et 19 mutations rapportées tombées, gabarits hors test envoyés une fois au moteur ; le lecteur de plans aura la revue de branche de la partie 1 | un défaut d'arithmétique du lecteur faussera les mesures de la tâche 10, où les tests d'intégration le verraient |
| `IndexDef.OnTarget` et `Filter` sont des fragments T-SQL bruts | une cible de partition `[ps]([c])` n'est pas un seul nom ; dit aux tâches 8 et 12 : `OnTarget` bâti par `QuoteName` | injection si un appelant y met un nom brut |
| `Plan.Statements` contient l'`EXEC` externe sans plan (`Root == nil`) avant l'instruction de la procédure | lu dans le XML ; dit aux tâches 6 et 10 : prendre la dernière instruction qui a un plan | mauvaise instruction prise pour la mesure |
| Tâche 5 acceptée (`fb71cc9`), 12 PASS relancés par le contrôleur ; balises JSON de `conn.Instance` et `tsql.ScopedConfig` ajoutées par le contrôleur | collecte non exécutée sur 2016 : seules des colonnes du Query Store de 2016 sont lues, à vérifier à la revue | une colonne absente sur 2016 casserait `collect` sur la version plancher |
| La collecte écarte du Query Store tout ce qui n'est pas une instruction DML ou `SELECT`, et ses propres requêtes (marqueur `/* godta:collect */`) | choix de l'implémenteur, mesuré : un commentaire final n'est pas gardé | aucun |
| Tâche 7 acceptée (`9efc8b4`), 13 PASS relancés | 16 mutations tombées ; un test de vue qui ne vérifiait rien, trouvé et réparé par l'implémenteur | faible |
| Colonnes calculées toutes hors des clés, persistées comprises | `model.Column` n'a pas `is_persisted` ; écart à la spec §8.3 mineur, à reprendre si un client en a besoin | un index utile sur une colonne calculée persistée n'est pas proposé |
| Tâche 6 acceptée (`f1830ac`) ; tests relancés, rejeu compilé sur le labo | 23 mutations sur 25 tombées, la 24e sans effet attendu (arguments nommés) | faible |
| `INSERT … EXEC` refusé (`statement kind: EXEC`) ; un `SET` sans point-virgule après une autre instruction n'est pas détecté | prudence de l'implémenteur ; le Query Store garde les instructions une à une, ce texte n'y apparaît pas | un `INSERT … EXEC` n'est pas évalué |
| Table de `date_format` mesurée sur 2022 (mdy 1 … dym 6) ; Microsoft Learn ne la donne pas | lacune de la doc, candidate à `corriger-doc-microsoft`, signalée à Rudi | aucun pour goDTA |
| Tâche 8 (`b1187c7`) : condition 3 sur les clés déclarées du candidat contre les clés effectives de l'existant ; clés effectives des deux côtés pour le seul préfixe de `replace` | lecture littérale de la spec §8.3 ; la formule « des deux côtés » du plan était fausse (cas 10 et 4 la contredisent, mutation M15) | aucun : c'est la spec |
| `ConfrontAll` reforme le candidat `widen` (clés de l'existant, union des inclus) et recalcule son `ID` | la mesure doit porter sur la forme élargie (spec §8.3 et §8.6) | un ID change entre `candidates.json` et l'évaluation : les tâches suivantes lisent les candidats après `ConfrontAll` |
| Mutation M16 survivante (appariement par nom d'index non gardé) : renvoyée à l'implémenteur pour un test | un garde sans test ne vaut rien | aucun |
| Tâche 9 acceptée (`c0c82f2`), 9 PASS relancés | mutations tombées, mesure de `modify_date` reconfirmée | faible |
| Délai de verrou fixe des `DROP` : 1 000 ms (`runplan.DropLockTimeoutMS`) | ni la spec ni le plan ne le fixent ; court pour ne pas faire attendre les requêtes derrière le `Sch-M` | un `DROP` qui échoue plus souvent, rattrapé par `cleanup` |
| Le plan n'a pas d'étape pour les candidats `covered` ni ceux d'une table columnstore (listés dans `Skipped`) | spec §8.3 et §8.5 ; dit à la tâche 10 : même filtre dans `Run` | `ErrNotInPlan` si `Run` diffère |
| `ForCleanup` laisse `MachineName` vide, à poser par la tâche 13 avant `Seal` | `Verify` refuse sinon | dit à la tâche 13 |
| Tâche 11 (`99a852a`) : maintenance comptée deux fois pour une écriture (son `After` la contient déjà) ; renvoyée pour correction : gain d'une écriture = `Before − (After − Maintenance)` | relevé par l'implémenteur lui-même | une sous-estimation systématique des candidats sur tables écrites |
| `UsedHypothetical` et `Combined` contiennent des ID de candidats, pas des noms d'index | le score n'a pas le plan pour rapprocher les noms ; dit à la tâche 10 | aucun |
| `BindQueries` ignore les écritures qui écrivent la table sans la lire (`INSERT … VALUES`) : confié à la tâche 10, premier consommateur | règle « corriger là où on le trouve » | maintenance d'un `INSERT` jamais comptée |
| Tâche 14 acceptée (`adbc737`) | 12 mutations tombées, une réparée (filtre de base) | faible |
| Suggestions de l'IA avec `EqCount = 0` : leurs clés sont comparées dans l'ordre | le format JSON ne dit pas quelles clés sont des égalités ; un champ `equality` y suffirait | une suggestion couverte par une permutation n'est pas reconnue couverte, elle est mesurée et rejetée par le score |
| Tâche 11 close (`bed0f51`) : double compte corrigé, test cassé par l'ancienne règle | aucun | aucun |
| Tâche 12 acceptée (`f87c4a4`) ; script exécuté dans une transaction annulée sur le labo | mutations tombées | faible |
| `DROP` d'un remplacé gardé par l'existence du remplaçant ; noms d'index `IX_<table>_<clés>_<6 car. de l'ID>` | un `CREATE` raté ne laisse pas la table sans index ; noms stables, jamais de la forme `RunPattern` | aucun |
| Schéma de partition : `ON [<data space>]` repris pour élargir ou remplacer, avec un commentaire quand ce n'est pas `PRIMARY` ; un nouvel index sans `ON` | le modèle ne distingue pas groupe de fichiers et schéma de partition ; à collecter (`data_spaces.type`, colonne) plus tard | erreur 2726 à l'exécution du script sur une table partitionnée, visible par le DBA |
| Total du gain dans `synthese.md` (spec §8.7) absent faute de champ : confié à la tâche 15 (`SummaryRow.Gain`) | premier consommateur | aucun |
| Tâche 13 acceptée (`d2751b8`), 8 PASS relancés sans cache | mutations tombées ; `Run` relit chaque objet avant de l'envoyer et refuse un index réel ou une base non clone sous un nom journalisé | faible |
| Conteneur de labo `godta-lab2022` (port 11434, nom d'hôte `godtalab`, 3 Go) créé depuis l'image exacte de `sql2022` (`90488d58`, 2022 CU26) ; partage hôte `labshare` monté sur `/var/opt/mssql/godta` | l'étiquette `2022-latest` donne désormais CU27 : même version que la source pour que `TestLabEqualsServer` ne mesure pas un écart de CU ; à supprimer en fin d'exécution | aucun |
| Tâche 16 acceptée (`d17e929`), 18 PASS relancés ; `CheckSameVersion` rend `(warning, error)` | un avertissement ne tient pas dans un `error` | dit aux tâches 17 à 19 |

## Tâche 10 (evaluate), commit f37207b

- Porte relancée par le contrôleur, `-count=1`, une seule invocation : 15 noms attendus plus `TestPagesFormula`, `TestRunRefusesUnplannedWrite`, `TestBindQueriesIncludesPureWrites`, tous `--- PASS`, aucun SKIP. Pas de pied de page d'attribution.
- Contradiction du brief retenue : inverser l'ordre de la référence ne faisait tomber aucune des six requêtes (toutes avaient déjà leurs statistiques après la tâche 4). Ajout d'une septième requête `no_stats`, qui tombe sous la casse (22,217 contre 22,1859). Même piège que celui consigné dans LESSONS.md avant l'exécution.
- Contradiction retenue : `TestWidenHidesExisting` ne vérifiait rien avec `x = 42`. Requête remplacée par `x < 3000`, qui tombe quand l'index n'est pas masqué.
- Décision : `BindQueries` ajoute les écritures de la table à tous les candidats, pas seulement à ceux sans requête. Sinon la maintenance des candidats de l'algorithme n'est jamais mesurée (spec §8.6). Coût si c'est faux : des compilations d'écritures en trop, sans erreur de score.
- `collect.ReadCatalog` est exporté pour les tests. Accepté.

## Tâche 17 (clone), commit f387839

- Porte relancée par le contrôleur, `-count=1` : 5 noms `--- PASS`. Pas de pied de page.
- Accepté : `runplan.CloneStats`/`StatSpec` à cause du cycle d'import, avec un alias dans `lab` ; `Plan.MaxBytes` et `ChunkBytes` en omitempty ; un fichier hôte en 0644 (podman sans root : `mssql` ne lirait pas un fichier en 0600) ; une statistique non supprimée après trois essais reste journalisée et le clone continue avec `ErrLeftForCleanup`.
- Défaut retenu, corrigé à part : la spec (§8.4 étape 1) demande une statistique d'une colonne pour les colonnes de jointure et de regroupement, et le lecteur de plans ne les rend pas. Sans elles, le clone estime faux sur une jointure (même mécanisme que les 633 lignes contre 24 858 de §15). Un agent de correction étend `showplan`.
- Reporté à la tâche 19 : l'estimation de taille par `msdb.dbo.backupset` dans le plan du `--dry-run`, qui demande une connexion que `ForClone` n'a pas. C'est `main` qui la lit.

## Correctif colonnes de jointure et de regroupement, commit 7c5f58e

- Porte relancée : `TestJoinColumns`, `TestGroupColumns`, `TestCloneStatsJoinAndGroupColumns` passent. Pas de pied de page.
- Mesuré par l'agent : sur 2022, un Hash Match d'agrégat n'a pas de `GroupBy` et liste ses colonnes sous `HashKeysBuild` ; le LogicalOp décide. Dix casses sur treize tombent ; les trois autres (résiduel de merge redondant, remise à zéro de profondeur, `ProbeResidual` et `AdaptiveJoin` sans fixture) sont des chemins sans fixture, acceptés et signalés pour la relecture de branche.

## Tâche 15 (binaire), commit 91c06ba

- Porte relancée : 7 tests unitaires et `TestEndToEndServer` passent. Pas de pied de page.
- Brief faux, corrigé par l'agent : `MachineName` du plan de nettoyage vient de `inst.MachineName` (comparé à `SERVERPROPERTY('MachineName')` par `Verify`), pas de `os.Hostname`. L'erreur était dans mes notes de contrôleur.
- Piège de test : le chargement du jeu de données entre dans la fenêtre du Query Store ; `QUERY_STORE CLEAR` après chargement.
- Défaut de produit trouvé : la maintenance d'une écriture non rejouable était en lignes, ajoutée à un coût en unités de l'optimiseur, puis comparée au gain. Un chargement massif capturé rejetait tout candidat de la table. Décision du contrôleur : les lignes écrites vont dans un champ à part, et un candidat qui en a ne peut pas être « recommandé », au mieux « à discuter ». Coût si c'est faux : des candidats rétrogradés à discuter sur les tables chargées en masse, jamais un index recommandé à tort. Correctif confié à un agent Sonnet. À valider par Rudi, c'est une lecture de la spec (ligne 542).
- Choix acceptés : `candidates-evaluate.json` figé au `--dry-run`, `--suggestions` refusé hors `--dry-run`, plan refusé par `Verify` en code 3.

## Tâche 18 (restauration), commit 41a2199

- Porte relancée : 3 noms passent ; `go vet` et `go test ./...` verts (538 tests).
- Mesuré sur 2022 entre deux instances : source, labo avec flux et labo sans flux identiques sur les trois requêtes. Le flux ne change rien sur 2022, conforme au spike.
- Accepté : vérification `Identify(server) == labi` ajoutée dans `Restore` ; `containerPath` est un répertoire.
- 2017 et 2016 : aucun conteneur 2017 sur patrocle ; la tâche 19 est gatée sur 2022, le contrôleur décide ensuite des autres versions.

## Correctif lignes écrites, commit f1c2c60

- Trois tests relancés, passent. Lacune : aucune ligne de rapport ne vérifie `RowsWritten` (pour la relecture de branche).

## Tâche 19 (labo), commit e59db33

- Porte relancée sur 2022 (deux instances) : `TestLabEqualsServer`, `TestLateSuggestionUnreliable`, `TestCloneSizeEstimate` passent. Pas de pied de page.
- Défaut réel trouvé par la mesure et corrigé : sur le labo, les plans nomment la base clone, donc `tablesOf` écartait leurs tables et les index réels n'étaient pas déclarés. La vérification par la référence l'a attrapé (« reference mismatch »), ce qui confirme que ce garde fonctionne.
- Résultat de casse, pas un échec : sur 2022, garder la multicolonne sur le labo ou couper le flux ne change aucun coût. Seul le test de la note garde le crochet. Il faut 2016 pour garder l'étape de suppression.
- Accepté : `--chunk`, `--backup-dir`, `--max-bytes` passent au `--dry-run` (ils sont sous la somme de contrôle) ; estimation de taille par la dernière sauvegarde complète de la source, présentée comme telle ; `report --target` ; la base clone restaurée reste sur le labo, à la charge du DBA.
- 2017 : le contrôleur crée deux conteneurs 2017 jetables pour lancer la porte ; 2016 seulement par le labo Windows, non tenté.

## 2017, commit 86bae04 (contrôleur)

- Deux conteneurs 2017 CU31 jetables (source et labo, même image), créés puis arrêtés et supprimés par le contrôleur.
- Deux défauts trouvés avant toute mesure, corrigés : certificat auto-signé à numéro de série négatif refusé par Go depuis 1.23 (directive `go:debug x509negativeserial=1` dans `cmd/godta`) ; `InstanceDefaultBackupPath` NULL avant 2019, repli sur la valeur `BackupDirectory` du registre de l'instance, lue seulement sur le chemin du clone pour que `Identify` n'envoie que des SELECT (le garde du `--dry-run` a refusé la première version, qui la lisait dans `Identify`).
- Ensuite `TestLabEqualsServer` et `TestLateSuggestionUnreliable` passent sur 2017 : coûts et estimations identiques entre source et labo, sur les cinq requêtes.
- 2016 non mesuré : aucun conteneur 2016 n'existe (pas d'image Linux), et le labo Windows n'a pas d'instance 2016 connue.

## Essai réel sur une copie de PachaDataFormation (contrôleur)

- Copie `pdfcopy_controle` restaurée sur sql2022 depuis une sauvegarde COPY_ONLY ; l'original n'est pas modifié. Fichier .bak supprimé.
- Premier passage : 103 requêtes, 0 table, « 0 candidates » en succès silencieux. Les plans du Query Store gardent le nom de compilation (`[PachaDataFormation]`, 5 411 références, 0 pour le nom courant). Corrigé par 5778d43 : nom de compilation détecté comme alias quand il est le seul nom non système, avertissement, et avertissement dès que des requêtes ne résolvent aucune table. Porte relancée par le contrôleur.
- Second passage, de bout en bout : 21 tables, 42 candidats, 10 couverts par l'existant, plan de 86 étapes, exécution en 3,9 s, rapport et script écrits, `cleanup --dry-run` à 0 étape, aucun index hypothétique ni statistique `godta_*` restant. 524 statistiques auto-créées dans la base (les `_WA_Sys_*` de l'original compris, non comptées à part).
- Défaut d'affichage relevé : un candidat que le score recommande mais que la confrontation met « à discuter » (spec ligne 298) apparaît sous « Recommended indexes », avec « not scripted ». Il devrait être dans « To discuss ». À corriger avec les retours de la relecture.

## Relecture de branche (agent neuf, rapport dans scratchpad/branch-review/review.md)

- Incident de règle signalé par le relecteur : `cleanup` sur sql2022 a lu le catalogue de PachaDataFormation (lecture seule, aucune écriture). C'est le défaut 12 : `cleanup.Find` se connecte à toutes les bases avant d'appliquer `--include`.
- Retenus et corrigés, en lots par fichiers :
  - lot A (candidate, score) : 1 recherche de clé (Key Lookup) qui met la clé cluster en tête des égalités ; 6 écriture qui ne fait que lire la table liée comme écriture ; 14 maintenance des mesures combinées à vérifier ;
  - lot B (sûreté d'exécution) : 2 `ok: true` avec des index hypothétiques restés (échec de suppression, SIGINT) ; 3 plan rejoué après plantage (erreur 1913, rien supprimé, succès) et plan exécuté deux fois ; message peu clair pour un plan remplacé ;
  - lot C (rapport, collecte, nettoyage) : 4 rapport qui mêle une évaluation ancienne et une collecte récente ; 5 nettoyage `--include` qui oublie clone et sauvegarde (base `master`) ; 8 index créé entre `collect` et `--dry-run` ; 9 base SINGLE_USER collectée ; 10 identifiant de requête inventé pour le cache de plans ; 11 seuils non configurables (spec §8.6) ; 12 ; 15 noms de répertoires sur système de fichiers insensible à la casse et noms réservés de Windows ; plus le classement « à discuter » de la confrontation et l'étiquette « no query » avant évaluation ;
  - lot D (labo) : 7 reprise quand le clone restauré a disparu mais que le fichier est là.
- Non corrigé, pour Rudi : 13, sur une table cluster le « replace » ne se déclenche presque jamais (clés effectives), d'où un candidat `new` à côté d'un index dont il est le prolongement, sans note de redondance. C'est la règle que Rudi a choisi de garder ; le relecteur la constate contraire au premier anti-objectif.
- Écarté : la capture AUTO avec seulement des écritures sans repli sur le cache (rare, et le mode de capture est signalé) ; l'ETL non rejouable qui met la table « à discuter » (voulu par la spec et par f1c2c60).

## Lot A, commits b98e2bb, 6f3c680, 2db1d26

- Tests relancés, passent ; pas de pied de page.
- 1 Key Lookup reproduit et corrigé : candidat `(CustomerID, Status) INCLUDE (OrderDate, Amount)`, coût 0,131299 → 0,003326 (avant : clé cluster en tête, index ignoré, 0,131299). L'élargissement sort maintenant de `(CustomerID)`.
- 6 corrigé dans `score` (là était l'erreur) avec `candidate.WritesTable`.
- 14 réfuté par lecture et garde : le score ne lit jamais les coûts d'une mesure combinée.

## Lot B, commits ba4c630, f0fc829, 87d50ce

- `cmd/godta` complet relancé seul avec l'environnement : ok ; les 9 nouveaux tests passent ; pas de pied de page ; arbre propre.
- Restes : tout objet du passage resté « created » met `ok: false`, code 2, et nomme les objets. SIGTERM n'était pas capté (seul SIGINT l'était) ; une création coupée par l'annulation est recherchée et supprimée.
- Un plan déjà exécuté, ou remplacé par un `--dry-run` plus récent, est refusé en code 3 avant toute connexion.
- Limites dites par l'agent : un plan écrasé sur place par un nouveau `--dry-run` n'est pas détectable ; un plan de clone remplacé passe encore (pas de fichier de candidats).
- Instabilité vue en parallèle : `cleanup --dry-run` lit toutes les bases, y compris celles que d'autres tests suppriment au même moment (défaut 12) ; traité par le lot C.

## Lot D, commit 83652bf

- Quatre nouveaux tests relancés, passent ; pas de pied de page.
- Clone disparu du labo : restauré de nouveau depuis le fichier hôte si taille et SHA-256 correspondent, refus en code 3 sinon.
- Clones remplacés : la base du labo est supprimée à la préparation suivante et listée ; le fichier hôte est seulement signalé, avec la commande `rm` (disque de l'utilisateur). Choix de l'agent, accepté.
- Laissé tel quel : `cleanup` sur un profil de labo propose de supprimer aussi le clone courant (par motif). Sans dommage depuis ce correctif, puisque la restauration repart du fichier.

## Lot C, commits bbd5c1c à 418dd0b ; suites de lot C par le contrôleur, 2263578

- Suite complète `-p 1` avec l'environnement : aucun FAIL ni SKIP (lancée par le contrôleur après 2263578).
- 2263578 : `collect_id` dans l'évaluation du labo (cassé exprès, `TestLabEqualsServer` tombe) ; contrôle de dérive au `clone --dry-run` (sans test de casse dédié).
- F15 change les répertoires des noms en casse mixte : un projet créé avec un binaire antérieur garde ses anciens répertoires. Branche non publiée, accepté.

## Régression vue au dernier essai réel

- La lecture d'index de `godta` (contrôle de dérive) est capturée par le Query Store de la copie ; son plan nomme la base courante, la règle d'alias (« nom courant absent ») ne s'applique plus, 0 table. Le nouvel avertissement l'a signalé, donc ce n'était plus silencieux.
- Deux défauts confiés à un agent : les instructions de `godta` entrent dans la charge collectée (marqueur commun et exclusion) ; règle d'alias réécrite sur les seules références d'objets utilisateur avec contrôle d'existence, valable aussi quand des plans récents nomment la base courante (base renommée qui continue de tourner).
- C'est la troisième fois que le même mécanisme (nom de base dans les plans) produit un défaut : clone (tâche 19), copie restaurée (5778d43), et ici. Un correctif fait sous pression (5778d43) avait une condition trop étroite.

## Commits c37ea09 et 302369b, dernier essai réel

- Marqueur commun `/* godta:internal */` sur toutes les instructions que `godta` envoie, exclues de la collecte ; un test d'analyse du code refuse une requête littérale sans marqueur. La requête capturée venait de `runplan.ReadFingerprints`, pas de `readIndexes`.
- Règle d'alias réécrite : seules comptent les références d'objets utilisateur, et toutes les tables vues sous l'ancien nom doivent exister dans la base courante. Risque accepté : une base qui interroge une autre base de même schéma serait lue comme renommée, avec l'avertissement.
- Essai réel final sur `pdfcopy_controle` : alias reconnu sur 42 requêtes, 42 candidats, exécution, plan rejoué refusé en code 3, rapport, `cleanup --dry-run` vide.
- `TestKilledPlanNotReplayed`, signalé une fois instable, passe trois fois de suite.

## Reste ouvert, pour Rudi

- Défaut 13 de la relecture : « replace » presque jamais applicable sur une table cluster (règle des clés effectives choisie par Rudi).
- 2016 non mesuré (pas d'instance).
- Le contrôle de dérive au `clone --dry-run` n'a pas de test de casse dédié.

## Relecture externe du code (codex, sous-agent Claude, agy), après 1455c9a

- codex, périmètre conformité : 3 trouvailles. Retenues : le Query Store compte des intervalles entiers qui débordent de la fenêtre demandée (décision : garder les intervalles entiers, que le Query Store ne sait pas couper, mais enregistrer et afficher les bornes réelles mesurées) ; le schéma de partition rendu sans sa colonne (erreur 2726 sur un script d'élargissement). Écartée : l'index filtré jamais « couvrant ». La spec demande une preuve d'implication « quand c'est possible » ; refuser toute preuve est la lecture prudente, et la mesure tranche ensuite. Coût : une évaluation de trop.
- Sous-agent Claude, périmètre cassure : 26 cassures, 14 mordent. Les 12 qui laissent la suite verte deviennent des tests (Verify aux points d'appel, garde d'index réel du nettoyage, délai de verrou du nettoyage, essais de suppression, entrée « planned » avant CREATE, gardes du labo), avec une correction : `cleanup.Run` doit refuser une étape qui ne vise pas un objet du motif de passage.
- agy : question ciblée sur la liste des instructions SQL envoyées, sans dépôt.
- agy, question ciblée sur les instructions SQL (sans serveur, 11 points, tous annoncés « confiance élevée ») : retenus et confiés à un agent, après vérification sur le moteur : la recherche d'indications `INDEX` qui rate les minuscules sous un classement sensible à la casse ; les secondaires de groupe de disponibilité en lecture seule par intention (valeur 1) et le rôle NULL ; trois textes d'effets de bord trop affirmatifs (file d'attente des verrous, mise à jour synchrone des statistiques à la compilation, invalidation des plans) ; les scripts sans `ONLINE = ON` sur les éditions qui le permettent. Rejetés après lecture du code : la lecture répétée du fichier par `OPENROWSET` (annoncée dans les effets de bord, taille plafonnée) ; le délai de verrou absent du plan de nettoyage (posé par `cleanup` à chaque suppression) ; l'injection par le filtre (un candidat n'a pas de filtre, le filtre vient de `filter_definition`) ; la colonne de partition prise pour une clé (ignorée par `catalog.go`) ; le conflit de classement avec `msdb` (un paramètre a la coercibilité par défaut) ; le nom du clone trop long (il ne contient pas le nom de la base source) ; l'édition Web (déjà traitée).
- Lot F (03abb2e à 2d28baf) : les 11 cassures restées vertes sont toutes attrapées par un test, plus 5 cassures du nouveau code. Le défaut de `cleanup.Run` était réel (un plan édité et rescellé supprimait un vrai index) : chaque étape est maintenant contrôlée contre son entrée de journal et le gabarit de `ForCleanup`. Le contrôleur a rejoué la cassure « `checkStep` neutralisé » : `TestCleanupRefusesEditedPlan` tombe. `ErrWrongDatabase` était une tautologie dans `evaluateOne` ; retiré.
- Lot E (b3aac00, 3bb3b97) : bornes réelles des intervalles du Query Store enregistrées et affichées ; colonne de partition rendue dans les scripts, script exécuté dans une transaction annulée.
- Lot G (dd6aded, 6e0509f, b5a6dff, 07172a3) : indications `INDEX` cherchées avec un classement insensible à la casse (mesuré : 0 module trouvé sous `Latin1_General_CS_AS` avant, 1 après) ; secondaires non lisibles écartés, `Updateability` lu, non mesuré sur un vrai groupe de disponibilité ; effets de bord réécrits sur la mesure (7 nouveaux écrivains sur 10 bloqués derrière une création en attente, par la file et le partitionnement des verrous) et la spec mise à jour en conséquence par le contrôleur ; scripts `ONLINE = ON` sur les éditions Enterprise, sinon un avertissement.
- Suite complète `-p 1` relancée par le contrôleur : verte. Essai réel final sur une nouvelle copie de PachaDataFormation : 42 candidats, période réellement mesurée affichée, scripts en ligne, `cleanup --dry-run` vide.
