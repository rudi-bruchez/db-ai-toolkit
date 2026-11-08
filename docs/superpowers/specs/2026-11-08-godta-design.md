# Design de goDTA, conseiller d'index en ligne de commande

Date : 2026-11-08
Statut : révisé après la relecture du panel (agy et codex, prompts directif et neutre, plus un
sous-agent Claude), mesures faites sur SQL Server 2022 CU26. Le traitement de chaque constat est
en §14. Prochaine étape : le spike de §11, puis le plan d'implémentation.

## 1. Le besoin

Chez un client qui exploite des dizaines de bases (cas d'origine : Neoen, toutes les bases
australiennes), trouver les index qui manquent ou qui sont mal conçus se fait aujourd'hui à la
main, base par base. Le Database Engine Tuning Advisor de Microsoft ne convient pas : interface
graphique ou `dta.exe` sous Windows, une charge à fournir à la main, et des index hypothétiques
qu'il oublie souvent derrière lui en production.

goDTA est un binaire Go du dépôt, déterministe, utilisable de deux façons :

- en ligne de commande par Rudi, qui le lance sur un serveur et lit les rapports ;
- piloté par Claude, à travers un skill du plugin `sqlserver-toolkit`.

Il cible un serveur, analyse toutes ses bases utilisateur (ou celles qu'on choisit), et produit
pour chaque base un rapport technique destiné à la validation par le client et un script
`CREATE INDEX` / `DROP INDEX` prêt à passer.

## 2. Le contexte d'exploitation

Rudi est `sysadmin` sur les serveurs du client, mais rien ne se crée en production sans
validation préalable. Il dispose d'un conteneur podman local qui tourne la même version de
SQL Server que la production. Il veut pouvoir travailler soit sur le serveur lui-même, soit sur
ce conteneur. La sortie des données du client vers sa machine (histogrammes, textes du Query
Store) est couverte par ses contrats : aucun avertissement ni purge n'est demandé.

## 3. Objectifs

1. Lire la charge de chaque base : Query Store sur une fenêtre configurable (24 h par défaut),
   requêtes les plus exécutées et les plus coûteuses.
2. Repli sur le cache de plans (`sys.dm_exec_query_stats`) quand le Query Store est éteint ou
   vide sur la fenêtre.
3. Accepter en source additionnelle un projet sqlferret, lu dans son dossier de plans, comme
   source de candidats seulement (§8.2).
4. Déduire des index candidats des plans, les regrouper entre requêtes, les confronter aux index
   existants selon un modèle de couverture explicite (§8.3).
5. Mesurer chaque candidat par index hypothétique : coût estimé de chaque requête concernée avant
   et après.
6. Mettre en balance ce gain et le coût de maintenance de l'index.
7. Proposer aussi de modifier ou de consolider l'existant : élargir un index plutôt qu'en créer
   un voisin, remplacer un index existant par un candidat qui le couvre, chaque fois avec une
   mesure (§8.5). Les redondances entre index existants eux-mêmes sont signalées dans le
   rapport, sans `DROP` dans le script (§8.7).
8. Deux modes de travail : sur le serveur (après validation), ou sur un clone restauré dans le
   conteneur local.
9. Laisser Claude soumettre ses propres candidats, que goDTA mesure comme les siens.

## 4. Non-objectifs

- Appliquer les index. goDTA produit un script ; c'est un humain qui le passe.
- Recommander la suppression d'un index au seul motif qu'il n'est pas utilisé. Les compteurs
  d'usage ne couvrent ni toute la vie de la base ni les autres réplicas (même raisonnement que
  `2026-10-01-missing-indexes-design.md`, §1 et §3).
- Proposer de nouveaux index columnstore, filtrés, des vues indexées, du partitionnement.
  Rowstore non cluster seulement. Les index filtrés existants sont en revanche pris en compte
  dans la confrontation (§8.3).
- Azure SQL Database (pas de `DBCC CLONEDATABASE`). SQL Server 2016 et suivants ; Azure SQL
  Managed Instance si le spike (§11) le confirme.
- `sys.dm_exec_requests` : un instantané, que le Query Store et le cache de plans remplacent.
- Lecture du fichier DuckDB de sqlferret (cgo, binaire unique perdu), et toute modification de
  sqlferret.

## 5. Anti-objectifs

Ce qui ferait de goDTA un échec même s'il fonctionne :

- Proposer un index qui double un index existant, au sens du modèle de couverture de §8.3. C'est
  la crainte que Rudi a nommée en premier.
- Écrire en production sans l'avoir demandé explicitement, ou écrire autre chose que ce que le
  `--dry-run` a montré.
- Laisser un index hypothétique, une statistique `godta_*`, une base clone ou un fichier de
  sauvegarde sur le serveur après un arrêt brutal sans moyen de le retrouver et de le nettoyer.
- Exécuter pour de bon une requête de la charge alors qu'on voulait seulement son plan (§8.5).
- Annoncer un gain qui n'a pas été mesuré. Un candidat non évalué (verrou refusé, requête non
  rejouable, erreur) apparaît comme tel dans le rapport, sans chiffre.
- Annoncer comme « sur 24 h » un chiffre qui ne l'est pas (cache de plans, compteurs des DMV).
- Exécuter tel quel un texte SQL fourni par l'IA.

## 6. Connexion

goDTA importe le paquet `tools/internal/sqlq` : mêmes profils (`-profile`, `$MSSQL_PROFILES`,
`~/.config/db-ai-toolkit/mssql-profiles.json`), même résolution des secrets (`NewResolver`,
`Resolve`, `passwordEnv`, `passwordDpapi`), même construction de DSN (`Profile.DSN`). Il
n'appelle pas le binaire `sqlq`, dont la garde refuse `DBCC`, `CREATE`, `BACKUP`, `OPENROWSET`.
Le nom d'application de la connexion est `db-ai-toolkit/godta`, pas celui de `sqlq` : il faut
pouvoir distinguer les deux dans `sys.dm_exec_sessions` (petite option à ajouter à `DSN`, ou
réécriture du paramètre après coup ; au choix du constructeur).

En plus des profils :

- Un fichier `.env` est chargé au démarrage (`--env`, défaut `./.env`, absent sans erreur), avec
  la règle habituelle : une variable déjà présente dans l'environnement gagne. Loader copié dans
  `tools/internal/dotenv` depuis `~/Sources/Repos/atelier/outils/sylloge/internal/dotenv/`.
- Sans profil, une connexion directe par variables (`GODTA_SERVER`, `GODTA_AUTH`, `GODTA_USER`,
  `GODTA_PASSWORD`, `GODTA_ENVIRONMENT`), transformées en `sqlq.Profile` avec
  `PasswordEnv: "GODTA_PASSWORD"` (le paquet refuse un mot de passe en ligne). Documentées dans
  `.env.example`.

Deux connexions au plus par exécution : la source (le serveur analysé) et le labo.

Toute séquence qui dépend d'un état de session (`SET LOCK_TIMEOUT`, `DBCC AUTOPILOT`,
`SET AUTOPILOT`) s'exécute sur une seule connexion épinglée (`*sql.Conn`), jamais sur le pool de
`database/sql`, qui donne à chaque appel une connexion quelconque.

## 7. Lecture et écriture

Règle centrale : toute écriture est une phase séparée, demandée explicitement, montrée d'abord
par `--dry-run`, et refusée sur un profil `readonly`.

| Phase | Écrit sur la source ? | Condition |
|---|---|---|
| `collect` | non | sur un profil `prod`, la confirmation d'`AGENTS.md` avant la première requête |
| `clone` | oui : statistiques `godta_*`, base clone, fichier de sauvegarde | profil `readwrite`, `--allow-write`, plan validé |
| `evaluate --target server` | oui : index hypothétiques, statistiques auto-créées | profil `readwrite`, `--allow-write`, plan validé |
| `evaluate --target lab` | non (écrit sur le labo) | profil du labo `readwrite`, `--allow-write`, gardes du labo |
| `cleanup` | oui : suppression des restes | profil `readwrite`, `--allow-write` |

Rudi crée pour goDTA un profil `readwrite` distinct du profil `readonly` de `sqlq` sur le même
serveur. Le drapeau seul ne suffit pas, le profil seul non plus.

Gardes du labo, toutes obligatoires, vérifiées avant la première instruction sur le labo (le
`RESTORE` compris) :

- le profil du labo déclare `"environment": "lab"` ; une valeur absente ou autre est refusée
  (liste d'autorisation, pas d'exclusion : le champ est facultatif dans les profils) ;
- l'instance du labo n'est pas celle de la source : `@@SERVERNAME` et
  `SERVERPROPERTY('MachineName')` diffèrent sur les deux connexions ;
- pour `evaluate`, la base visée a `DATABASEPROPERTYEX(<base>, 'IsClone') = 1` (la propriété
  survit à la sauvegarde et à la restauration, mesuré) et son nom commence par le préfixe de
  goDTA.

### 7.1 Le `--dry-run` et le plan d'exécution

`--dry-run` ne peut pas imprimer à l'avance des identifiants connus seulement à l'exécution
(`index_id` d'un index hypothétique). Il écrit donc un fichier de plan d'exécution dans l'espace
de travail : l'identifiant d'exécution (fixé à ce moment), la liste ordonnée des instructions
avec leurs noms d'objets définitifs, et, pour chaque valeur résolue à l'exécution, la requête de
catalogue qui la résoudra. C'est ce fichier qui se montre au client. La phase réelle prend ce
fichier en entrée (`--plan <fichier>`) et n'exécute que lui ; elle refuse un plan dont la
somme de contrôle a changé, dont le serveur et la base ne correspondent pas, ou dont l'empreinte
de schéma a bougé : le plan enregistre, pour chaque table concernée, ses index (définition) et
son `modify_date`. Un index ajouté entre la validation et l'exécution ne serait pas déclaré à
`AUTOPILOT` (§8.5) et fausserait la mesure.

Le plan d'exécution dit aussi les effets de bord (§8.4, §8.5) : le verrou `S` sur la table
pendant la création de l'index hypothétique, qui bloque les écrivains le temps de la lecture
échantillonnée ; le `Sch-M` à la suppression, qui fait attendre toute requête arrivant derrière ;
la remise à zéro des suggestions d'index manquants de la table ; les statistiques auto-créées ;
la création et la suppression des statistiques `godta_*` et leurs effets possibles sur les
recompilations (à mesurer, §11) ; les procédures temporaires créées dans `tempdb` pour le rejeu.

### 7.2 Gabarits et noms

Toutes les instructions d'écriture viennent de gabarits compilés dans le binaire. Les noms
d'objets y entrent par une fonction d'échappement testée (équivalent de `QUOTENAME`), jamais par
concaténation brute. Le nom de tout objet créé par goDTA commence par `godta_` suivi de
l'identifiant d'exécution. `cleanup` le recherche avec un motif `LIKE` dont les `_` sont
échappés (`godta[_]…`), sinon `_` est un joker et le motif attrape d'autres noms (constaté par
un relecteur sur ses propres objets).

Avant toute création, l'identité de chaque objet (base, table, nom, chemin du fichier de
sauvegarde) est écrite dans l'espace de travail : c'est ce journal, et non une recherche par
motif, qui guide d'abord `cleanup`. Le motif sert de filet. Les fichiers de sauvegarde se
suppriment par la connexion SQL avec `master.sys.xp_delete_files` (non documentée, mesurée sur
Linux 2022 seulement ; sur les versions qui ne l'ont pas, `xp_delete_file`, à mesurer en §11) ; un fichier dont le chemin n'a pas été journalisé avant le crash est signalé, pas cherché.

## 8. Le déroulement

### 8.1 Un espace de travail par exécution

Comme sqlferret, goDTA écrit dans un répertoire de projet (`--project`) : un sous-répertoire par
base, des fichiers JSON par phase. Chaque phase lit ce que la précédente a écrit. C'est ce qui
permet de reprendre, de rejouer `evaluate` sur le labo sans relire la production, et à Claude de
lire le matériau entre deux phases.

Ordre des phases : `collect` (source) → candidats (hors connexion) → `clone` ou
`evaluate --target server` → `evaluate --target lab` le cas échéant → `report`.

### 8.2 `collect` (lecture seule, toujours sur la source)

Pour chaque base retenue (toutes les bases utilisateur, filtrées par `--include` et `--exclude`,
motifs à jokers sur le nom ; les bases système, hors ligne, en lecture seule ou secondaires
non lisibles sont listées comme écartées avec la raison) :

- version, édition, niveau de compatibilité, état du Query Store ;
- charge : top N requêtes (N configurable) par nombre d'exécutions et par coût (CPU, lectures
  logiques, durée) sur la fenêtre, avec `query_id`, `query_hash`, `context_settings_id` et ses
  options `SET` (`sys.query_context_settings`), texte,
  dernier plan connu et ses `ParameterCompiledValue` ; la fenêtre a une borne de fin explicite
  (`--until`, défaut : maintenant), enregistrée dans la sortie ;
- les requêtes d'écriture (`INSERT`, `UPDATE`, `DELETE`, `MERGE`) de la fenêtre, avec leurs
  exécutions, leur nombre moyen de lignes (`avg_rowcount`) et leur plan. Le Query Store ne dit ni
  la table écrite ni les colonnes modifiées : on les lit dans le plan (opérateurs de mise à jour,
  leurs objets cibles, `SetPredicate`), qui montre aussi les écritures en cascade des clés
  étrangères et la maintenance des vues indexées. Les instructions des déclencheurs sont des
  requêtes à part, rattachées au déclencheur. C'est la source principale du coût de maintenance
  (§8.6). En mode de capture `AUTO`, les écritures rares et peu coûteuses manquent : le rapport
  indique le mode de capture ;
- repli sur le cache de plans si le Query Store est éteint ou vide sur la fenêtre. Ses compteurs
  sont cumulés depuis la compilation de chaque plan, pas sur la fenêtre : le rapport le dit et
  n'affiche jamais ces chiffres comme « sur 24 h ». La base d'une requête ad hoc se lit par
  `sys.dm_exec_plan_attributes` (`dbid`), `sys.dm_exec_sql_text` rendant `NULL` pour elles ;
- index existants des tables touchées, réels seulement (`is_hypothetical = 0`, les restes du DTA
  ou de goDTA sont listés à part et proposés au nettoyage) : clés dans l'ordre avec leur sens
  (`is_descending_key`), colonnes incluses, unicité, contrainte appuyée (clé primaire, unique),
  filtre et sa définition, état désactivé, compression, facteur de remplissage, groupe de
  fichiers ou schéma de partition, taille, clé de l'index cluster ;
  `sys.dm_db_index_usage_stats` et `sys.dm_db_index_operational_stats`, à titre indicatif ;
- références nommées aux index existants : indications `INDEX(...)` dans les textes du Query
  Store et dans les définitions des modules (`sys.sql_modules`, qui couvrent le code non exécuté
  pendant la fenêtre), repères de plan (`sys.plan_guides`), plans forcés du Query Store. Le code
  envoyé par les applications et non exécuté pendant la fenêtre reste invisible : le rapport le
  rappelle à côté de tout `DROP` ;
- statistiques existantes par colonne (pour savoir lesquelles manqueront au clone) ;
- suggestions d'index manquants (`sys.dm_db_missing_index_*`) et éléments `MissingIndex` des
  plans ;
- nombre de lignes, de pages et largeur des colonnes des tables concernées.

`collect` s'exécute toujours sur la source, même en mode labo : sur un clone, les DMV d'usage,
d'opérations et d'index manquants sont vides et le cache de plans est froid. Le clone ne sert
qu'à `evaluate`, qui reprend la liste des requêtes établie par `collect`.

Le Query Store identifie une requête par `query_id`, qui dépend du texte, du contexte
(`context_settings_id`) et de l'objet parent ; un même `query_hash` correspond souvent à
plusieurs `query_id` (huit pour un même hash sur la base de démonstration). Le rapport cite les
`query_id` ; une correspondance par `query_hash` (cache de plans, sqlferret) est présentée comme
telle, avec tous les `query_id` qu'elle recouvre.

Source sqlferret (`--sqlferret <projet>`) : goDTA lit `plans/profile/run_N/index.json` et les
`.sqlplan` qu'il référence. Ce dossier ne contient ni le texte complet des requêtes (le
`StatementText` du plan est tronqué par le moteur), ni la politique de caviardage de l'import,
ni le nom de la base hors des références de colonnes du plan, et ses compteurs sont par plan et
par événement capturé, pas par exécution d'instruction. Les plans sqlferret servent donc
uniquement à proposer des candidats, rattachés à leur base par les noms à trois parties des
`ColumnReference`. Leur gain n'est mesuré qu'à travers les requêtes du Query Store ou du cache
que le candidat améliore ; un candidat issu de sqlferret seul qui n'améliore aucune d'elles est
listé « non mesuré ». Les plans écrits par `sqlferret query-store-import` ne sont pas lus (ils
viennent du Query Store, déjà lu directement).

### 8.3 Génération des candidats et modèle de couverture

Il faut écrire un lecteur de plans XML : `tools/internal/sqlplan` ne fait que décoder le fichier
(BOM, UTF-16) et l'afficher, il n'expose aucune structure. Le nouveau lecteur extrait
`StatementSubTreeCost`, les opérateurs d'accès, les prédicats de recherche et résiduels, les
colonnes de sortie, les tris demandés, `ParameterList` et les groupes `MissingIndex`. Il réutilise
`sqlplan.Decode` pour l'encodage.

Règles de génération : clés d'égalité d'abord ; puis, soit une inégalité, soit les colonnes d'un
tri demandé avec leur sens, mais pas les deux, sauf si la colonne de l'inégalité est elle-même la
première colonne du tri (après un intervalle sur `b`, les lignes d'un index `(a, b, c)` ne sont
plus triées sur `c`, la colonne de tri ne ferait qu'élargir la clé) ; colonnes de sortie en
`INCLUDE`, moins celles déjà en clé et moins la clé de l'index cluster (présente d'office dans
tout index non cluster ; la mettre en `INCLUDE` en plus d'une clé provoque l'erreur 1910).
Avant la confrontation, les colonnes d'égalité d'un candidat sont rangées dans l'ordre où elles
apparaissent dans l'index existant le plus proche, s'il y en a un, sinon dans un ordre fixe.

La confrontation compare des clés effectives, pas les clés déclarées de `sys.index_columns` :

- index non cluster non unique sur une table cluster : clés déclarées suivies des colonnes de la
  clé cluster qui n'y sont pas déjà (la documentation du guide de conception des index le dit :
  elles sont ajoutées à la clé et servent au traitement des requêtes) ;
- index non cluster unique : clés déclarées ; la clé cluster est au niveau feuille, comme un
  inclus ;
- index cluster : clés déclarées, et il contient toutes les colonnes. Il n'est jamais la cible
  d'un « élargir » ni d'un « remplacer » ;
- tas : pas de clé ajoutée (le localisateur de ligne n'est pas une colonne).

Le modèle de couverture décide si un besoin est satisfait par un index existant. Un index
existant E couvre un candidat C si toutes ces conditions tiennent :

1. E est réel, actif (`is_disabled = 0`), non hypothétique ;
2. E n'est pas filtré, ou son filtre est impliqué par les prédicats de toutes les requêtes qui
   ont produit C (en pratique : sans démonstration simple de l'implication, E ne couvre pas) ;
3. les colonnes d'égalité de C forment, dans un ordre quelconque et sans considération de sens,
   le début des clés effectives de E (les permutations d'égalités se valent, le sens d'une
   colonne d'égalité est indifférent), suivies dans l'ordre des autres clés de C ; pour ces
   dernières, le sens compte, avec le même sens ou le sens inverse sur toutes à la fois ;
4. chaque colonne incluse de C est présente dans E, en clé effective, en inclus, ou au niveau
   feuille (clé cluster d'un non cluster unique ; toute colonne pour un index cluster).

Confrontation, dans cet ordre, pour chaque candidat :

- couvert par un existant : écarté, avec le nom de l'existant ;
- un existant non cluster satisfait les conditions 1 à 3 mais pas 4 : « élargir l'existant »
  (même clé, inclus réunis, par `DROP_EXISTING = ON` en reprenant sa compression, son facteur de
  remplissage et son groupe de fichiers ou schéma de partition), sauf si l'existant appuie une
  contrainte. SQL Server refuse alors
  `DROP_EXISTING` (erreur 1907) et le `DROP INDEX` (erreur 3723) : le candidat passe en
  « à discuter », sans script, avec l'explication ;
- les clés effectives d'un existant sont un préfixe strict de celles du candidat (après la mise
  en ordre des égalités ci-dessus), et l'existant n'est ni cluster, ni unique, ni filtré, ni
  appuyé sur une contrainte, ni nommé dans une indication, un module, un repère de plan ou un
  plan forcé : « remplacer », le candidat recevant l'union des inclus des deux et les options
  physiques de l'existant ; dans tous les autres cas, le candidat est proposé à côté de
  l'existant ;
- sinon : nouveau candidat.

Deux index existants qui ensemble couvrent un candidat ne le couvrent pas : l'intersection
d'index est un choix de l'optimiseur, pas un chemin d'accès unique. La mesure (§8.5) tranche.

Tri et départage entièrement déterministes. Le détail fin (largeur maximale des inclus, nombre
maximal de clés, ordre du `MissingIndex` contre ordre déduit des prédicats) est laissé au
constructeur, à condition que chaque règle soit nommée et apparaisse dans le rapport à côté du
candidat qu'elle a produit ou écarté. Une batterie de tests de table de vérité couvre chaque cas
cité ici et en §14.

### 8.4 `clone` (écriture validée sur la source, puis labo)

Cette phase vient après la génération des candidats, parce qu'elle en dépend.

1. Statistiques : pour chaque candidat, `CREATE STATISTICS godta_<run>_<n>` sur ses colonnes de
   clé dans l'ordre (densités du préfixe et histogramme de la première colonne) ; pour chaque
   colonne sans statistique utilisée par une requête retenue (prédicat, jointure, regroupement,
   tri), une statistique d'une colonne, puisque le labo n'en créera pas. Échantillonnage par
   défaut du moteur. C'est une lecture échantillonnée de la table et une écriture de
   métadonnées, listée dans le plan d'exécution.
2. `DBCC CLONEDATABASE (<base>, godta_<run>_<base>) WITH NO_QUERYSTORE` (option disponible
   depuis 2016 SP1). Le clone copie les statistiques existantes, dont celles de l'étape 1. Le
   Query Store n'est pas copié : `collect` l'a déjà lu sur la source, et le copier grossirait le
   fichier et exporterait des textes pour rien. `DBCC CLONEDATABASE` exige `sysadmin`.
3. Suppression des statistiques de l'étape 1 sur la source.
4. `BACKUP DATABASE` du clone, compressé, dans le répertoire de sauvegarde par défaut de
   l'instance ou dans `--backup-dir`, chemin journalisé avant.
5. Rapatriement du fichier par la connexion SQL, `OPENROWSET(BULK …, SINGLE_BLOB)`, sans partage
   réseau (mesuré : lecture complète juste après le `BACKUP`, sans verrou ; 3,6 Mo pour la base
   de démonstration avec son Query Store). Le résultat est un `varbinary(max)`, plafonné à 2 Go :
   au-delà, ou au-delà d'une limite configurable plus basse, la phase s'arrête et le dit.
6. Suppression de la base clone et du fichier sur la source (`xp_delete_files`).
7. Dépôt du fichier dans un répertoire de l'hôte monté dans le conteneur (chemin hôte et chemin
   vu par le conteneur en configuration), `RESTORE FILELISTONLY`, puis `RESTORE … WITH MOVE` de
   chaque fichier logique vers le répertoire de données du labo (les chemins de la source, souvent
   Windows, n'existent pas sur le conteneur), puis `ALTER DATABASE … SET READ_WRITE` (un clone est
   en lecture seule ; sans ce passage, la création d'un index hypothétique échoue, erreur 3906),
   puis `AUTO_CREATE_STATISTICS OFF` et `AUTO_UPDATE_STATISTICS OFF` sur le clone : sinon chaque
   compilation sur le labo peut créer ou recalculer des statistiques à partir de tables vides
   (colonnes de jointure, de regroupement, de tri, suggestions de l'IA) et fausser le coût de
   référence comme le coût candidat. Microsoft ne prend en charge un clone que comme copie en
   lecture seule pour le diagnostic : l'usage en écriture qu'en fait goDTA sort de ce cadre,
   c'est assumé et limité au labo.

goDTA ne gère pas le conteneur lui-même (ni démarrage ni arrêt) : il se connecte au profil du
labo.

Les suggestions de l'IA arrivées après le clone (§9) n'ont pas leurs statistiques sur le labo.
Deux voies, au choix de l'utilisateur à ce moment : un nouveau passage de l'étape 1 suivi d'un
transfert des seules statistiques nouvelles par `DBCC SHOW_STATISTICS … WITH STATS_STREAM` sur la
source et `UPDATE STATISTICS … WITH STATS_STREAM` sur le labo (non supporté par Microsoft, mesuré
fonctionnel sur 2022 quand le flux vient d'une statistique de mêmes colonnes ; un flux d'une
colonne posé sur un objet de deux colonnes est refusé, erreur 9105), ou une évaluation sur le
serveur. Sans l'une des deux, ces candidats sont marqués « estimation non fiable » et ne peuvent
pas être recommandés.

### 8.5 `evaluate`

Mécanisme, sur une connexion épinglée, chaque instruction dans son propre lot :

1. `DBCC AUTOPILOT(5, <db>, 0, 0, 0, 0, 0)` : remise à zéro de la liste de la session (sans
   elle, un candidat évalué précédemment reste visible ; `SET AUTOPILOT OFF` ne la vide pas) ;
2. `CREATE INDEX godta_<run>_<n> ON … WITH STATISTICS_ONLY = -1`, sous `SET LOCK_TIMEOUT`
   court. La valeur `-1` est obligatoire : sans valeur, l'index est créé sans statistique et
   l'estimation est fausse même sur une base pleine (mesuré par quatre relecteurs). Sur le labo,
   la table est vide : l'index obtient un objet de statistiques vide (en-tête à `NULL`, mesuré),
   en concurrence avec la statistique `godta_*` copiée sur les mêmes colonnes. Rien ne garantit
   aujourd'hui que l'optimiseur choisisse la seconde ; c'est le point 2 du spike, qui compare
   aussi une autre voie : poser sur l'index hypothétique du labo le flux de la statistique
   `godta_*` (`UPDATE STATISTICS … WITH STATS_STREAM`, même liste de colonnes) ;
3. `DBCC AUTOPILOT(0, <db>, <table>, <index_id>, <pages>)` pour l'index hypothétique, avec un
   nombre de pages estimé (lignes × largeur de ligne de l'index / 8 060 octets environ) : sans ce
   paramètre, l'index est compté comme une seule page et toute lecture d'intervalle paraît
   gratuite ;
4. `DBCC AUTOPILOT(0, <db>, <table>, <index_id>, <pages>)` pour chaque index réel non cluster
   de chaque table que la requête lit (lues dans son plan, jointures comprises) et des vues
   indexées qu'elle peut utiliser, avec leur nombre de pages réel
   (`sys.dm_db_partition_stats`). Un index réel non déclaré est caché à l'optimiseur sous
   `AUTOPILOT` (vérifié par le contrôleur : un seek sur l'index réel à 0,0037 devient un balayage
   à 0,436). C'est aussi ce qui permet de mesurer un `DROP` : pour évaluer un remplacement, on ne
   déclare pas l'index remplacé. Le comportement de l'index cluster et des tas sous `AUTOPILOT`
   (visibles d'office, mesuré pour un index cluster) et des vues indexées est à confirmer (§11) ;
5. `SET AUTOPILOT ON`, seul dans son lot ; le résultat de ce lot est vérifié avant de continuer.
   S'il a échoué, la requête suivante s'exécuterait pour de bon : goDTA s'arrête ;
6. la requête rejouée ; le résultat doit être un document `ShowPlanXML`, sinon goDTA abandonne la
   connexion et marque l'erreur ;
7. `SET AUTOPILOT OFF` ;
8. la même séquence sans l'index candidat donne le coût de référence, dans les mêmes conditions.
   Contrôle obligatoire : ce coût de référence et la forme du plan doivent être identiques à ceux
   d'un simple `SET SHOWPLAN_XML ON` de la même requête sans `AUTOPILOT`. Un écart signifie qu'une
   déclaration manque ; la mesure est alors écartée, jamais publiée ;
9. `DROP INDEX` de l'index hypothétique.

Avant le rejeu, la session reçoit les options `SET` du `context_settings_id` de la requête
(`ANSI_NULLS`, `QUOTED_IDENTIFIER`, `ARITHABORT`, etc.) : les valeurs par défaut d'une connexion
Go ne sont pas celles des applications, et un index filtré ou une vue indexée n'est utilisé que
sous les options requises. Une procédure temporaire de rejeu est créée sous ces mêmes options,
puisqu'elle fige `ANSI_NULLS` et `QUOTED_IDENTIFIER` à sa création.

Rejeu des requêtes : c'est le point le moins sûr (§11). Mesuré : le texte du Query Store
`(@P1 int)SELECT …` tel quel échoue (erreurs 1050 et 137) ; `EXEC sp_executesql` sous
`AUTOPILOT` ne rend aucun plan de l'instruction interne ; un appel paramétré de `go-mssqldb`
(RPC) ne rend rien ; `DECLARE @P1 … = valeur` fait perdre la valeur reniflée ; `OPTIMIZE FOR`
refuse les types `MAX` (erreur 4132). En revanche, `EXEC` d'une procédure stockée sous
`AUTOPILOT` compile son corps avec les valeurs passées comme valeurs reniflées. La piste à
mesurer : envelopper chaque requête paramétrée dans une procédure temporaire (`#godta_…`) dont
les paramètres reprennent sa déclaration, et l'appeler avec les `ParameterCompiledValue` du
plan. Une requête non paramétrée se rejoue telle quelle. Une requête qui ne peut pas être rejouée
(table temporaire, variables locales d'une procédure absentes de `ParameterList`, texte incomplet)
est listée comme telle.

Les instructions `INSERT`, `UPDATE`, `DELETE`, `MERGE` ne sont évaluées que si le spike prouve,
sur chacune de ces formes, qu'elles ne sont que compilées sous `AUTOPILOT` (mesuré pour un
`UPDATE` : données inchangées). Sinon elles servent au seul coût de maintenance.

Effets mesurés sur le serveur, à dire dans le plan d'exécution :

- la création prend un verrou `S` sur la table (et `Sch-S`) : un écrivain en cours la bloque,
  un lecteur non ; elle lit la table par échantillonnage ;
- la création garde ce verrou `S` pendant la lecture échantillonnée : elle bloque les
  écrivains pendant ce temps ;
- la suppression prend `Sch-M` : un lecteur long la bloque, et pendant qu'elle attend, toute
  nouvelle requête sur la table attend derrière elle. Une suppression qui échoue sur le délai de
  verrou laisserait l'index derrière : goDTA la retente avec un délai court et fixe et une pause
  entre deux tentatives (jamais un délai qui s'allonge, qui allongerait d'autant la file
  d'attente), puis, après la dernière, la journalise pour `cleanup` ;
- les plans en cache des tables touchées ne sont pas invalidés (mesuré : même `plan_handle`,
  `plan_generation_num` inchangé) ;
- les suggestions d'index manquants de la table sont effacées par la création : `collect` doit
  donc passer avant, ce que l'ordre des phases garantit ;
- la compilation peut auto-créer des statistiques `_WA_Sys_*` sur des colonnes sans statistique.
  Ce sont celles que le moteur aurait créées à la première requête sur ces colonnes ; goDTA les
  liste dans le rapport mais ne les supprime pas (leur nom n'a pas son préfixe et elles peuvent
  servir à d'autres requêtes).

Les candidats d'une même table sont aussi évalués ensemble (tous les retenus déclarés à la fois)
pour détecter qu'un index en rend un autre inutile. Le plafond des combinaisons est laissé au
constructeur.

### 8.6 Score

Pour chaque index recommandé :

- gain : somme sur les requêtes concernées de (coût estimé avant − coût estimé après) × nombre
  d'exécutions sur la fenêtre (Query Store), ou depuis la compilation (cache de plans, signalé) ;
- coût de maintenance, sur la même fenêtre : lignes écrites dans la table par les requêtes
  d'écriture du Query Store (exécutions × `avg_rowcount`), tirées de leurs plans (§8.2). Pour un
  `UPDATE`, ne comptent que ceux qui modifient une colonne de l'index ou la clé cluster
  (modifier la clé cluster maintient tous les index non cluster) ; un index non cluster n'est
  pas maintenu quand l'`UPDATE` ne touche aucune de ses colonnes (mesuré). Les compteurs des DMV
  (`leaf_*_count` en lignes, `user_updates` en instructions, remis à zéro par une
  reconstruction ou l'éviction des métadonnées, sans horodatage) ne sont affichés qu'à titre
  indicatif, jamais rapportés à la fenêtre. En repli sur le cache de plans, la maintenance n'a
  pas de source fiable : aucun index n'y est `recommandé`, au mieux `à discuter` ;
- taille estimée de l'index (lignes × largeur des clés et des inclus) ;
- pour un remplacement, la mesure inclut les requêtes qui utilisaient l'index remplacé, évaluées
  sans lui ;
- une décision `recommandé` / `à discuter` / `écarté` selon des seuils configurables.

La formule exacte qui combine gain et maintenance est laissée au constructeur ; elle doit être
écrite dans le rapport et rester la même d'une exécution à l'autre. Le rapport affiche toujours
les grandeurs séparément, jamais le seul score combiné.

### 8.7 `report`

Par base :

- `rapport.md` : contexte (version, source de charge et sa période réelle, ce qui manque), puis
  un bloc par recommandation : définition, origine (algorithme ou suggestion IA), requêtes servies
  (`query_id`, extrait du texte, exécutions, coût avant/après), coût de maintenance, index
  existants touchés (élargi, remplacé), règles appliquées ; ensuite les candidats écartés avec la
  raison, les candidats « à discuter », les non évalués, les statistiques auto-créées pendant
  l'évaluation, les index hypothétiques préexistants trouvés ;
- une section « redondances de l'existant » : index existants en doublon exact ou dont les clés
  sont préfixe d'un autre, comme observations, sans gain chiffré et sans instruction dans le
  script ;
- `indexes.sql` : le script, un bloc commenté par recommandation, `DROP` d'un index remplacé
  placé après la création de son remplaçant. Jamais de `DROP` d'un index cluster, unique, de
  clé primaire, appuyant une contrainte, ou nommé dans une indication, un repère de plan ou un
  plan forcé ;
- `result.json` : tout ce qui précède, pour Claude.

Par serveur : `synthese.md`, une ligne par base (recommandations, gain total, avertissements).

Rapports en Markdown.

## 9. Les suggestions de l'IA

goDTA ne demande rien à un modèle. Le va-et-vient passe par des fichiers :

1. après `collect` (et éventuellement un premier `evaluate`), Claude lit `result.json` ;
2. il écrit ses candidats dans un fichier JSON structuré (table, clés ordonnées avec leur sens,
   inclus, raison) ;
3. `evaluate --suggestions <fichier>` les évalue exactement comme les candidats de l'algorithme,
   après la même confrontation à l'existant (§8.3), avec la réserve sur les statistiques en mode
   labo (§8.4).

Un candidat est une structure, jamais un texte SQL exécuté tel quel : goDTA régénère le DDL à
partir de la structure, après avoir vérifié que la table et les colonnes existent.

Le rapport distingue l'origine de chaque recommandation. Une suggestion qui ne fait baisser aucun
coût estimé est écartée avec ses chiffres.

## 10. Intégration au dépôt

- Binaire : `tools/cmd/godta`, code dans `tools/internal/godta` (et `tools/internal/dotenv`).
  Construit par `scripts/build-tools.*` vers `plugins/sqlserver-toolkit/bin/` comme les autres.
- Skill : `plugins/sqlserver-toolkit/skills/index-advisor/` (nom au choix du constructeur), qui
  reprend les règles d'`AGENTS.md` sans les affaiblir : sur un profil `prod`, nommer le profil et
  attendre un oui avant la première requête, `collect` compris ; avant toute phase d'écriture,
  montrer le plan d'exécution de `--dry-run` et attendre un oui à ce plan, sur tout profil ; le
  format du fichier de suggestions.
- `AGENTS.md` : une section goDTA, dont l'exception explicite à « tout passe par `sqlq` ».
- Sortie standard : un objet JSON par commande, succès comme échec, à la manière de `sqlq`.
  Codes de sortie dans la même logique (`0`, `1` usage, `2` échec après connexion, `3` écriture
  refusée, `4` connexion impossible). Une exécution sur plusieurs bases où certaines échouent
  rend `2` et dit lesquelles.

## 11. Le spike, avant le plan

Le plan d'implémentation ne s'écrit qu'avec les résultats de ce spike, mené sur `sql2022` et
`sql2025` du labo, et si possible une instance 2016 ou 2019 (version plancher).

Déjà établi par le panel sur 2022 CU26 (à reconfirmer sur 2025) : `STATISTICS_ONLY = -1`, la
syntaxe `DBCC AUTOPILOT (typeid [, dbid [, {maxQueryCost | tabid [, indid [, pages [, flag [,
rowcounts]]]]} ]])` (obtenue par `DBCC TRACEON(2588); DBCC HELP('AUTOPILOT')`), le masquage des
index non déclarés, la remise à zéro par le type 5, le paramètre `pages`, la copie du Query Store
par le clone, le rapatriement par `OPENROWSET`, `xp_delete_files`, la persistance d'`IsClone`, la
création d'index hypothétiques et `DBCC AUTOPILOT` sous un utilisateur `db_owner` sans `sysadmin`
(la phase `clone` exige en revanche `sysadmin`, pour `DBCC CLONEDATABASE`).

Reste à mesurer :

1. Le rejeu des requêtes paramétrées par procédure temporaire (§8.5) : plan de l'instruction
   interne, valeurs reniflées, types `MAX`, requêtes dans des procédures, requêtes à tables
   temporaires ; proportion des requêtes du Query Store de la base de démonstration effectivement
   rejouables.
2. Les statistiques sur le labo : un index hypothétique sans statistiques propres, sur un clone
   contenant des statistiques `godta_*` de mêmes colonnes, donne-t-il le même coût que sur la
   source ? Cas d'une colonne, de colonnes corrélées, de valeurs très asymétriques. C'est le
   point qui décide de la valeur du mode labo.
3. `INSERT`, `UPDATE`, `DELETE`, `MERGE` sous `AUTOPILOT` : compilés seulement, sur chaque forme.
4. Le comportement de `DBCC AUTOPILOT` et de `STATISTICS_ONLY = -1` sur 2016 (ou la version
   plancher retenue) et sur Managed Instance si elle reste dans le périmètre.
5. Le nombre de pages à donner à `AUTOPILOT` : l'estimation de §8.5 contre la taille d'un index
   réel de même définition ; et l'effet d'un index réel déclaré sans `pages`.
6. Les statistiques sur le labo, suite du point 2 : avec et sans statistiques automatiques,
   objet vide de l'index hypothétique contre statistique `godta_*` copiée, flux `STATS_STREAM`
   posé sur l'index hypothétique.
7. `AUTOPILOT` sur une requête à jointure (index déclarés de plusieurs tables), un tas, une vue
   indexée ; le contrôle de §8.5 (référence sous `AUTOPILOT` égale au `SHOWPLAN_XML` simple) sur
   toutes les requêtes rejouables de la base de démonstration.
8. Les effets de `CREATE STATISTICS` et `DROP STATISTICS` sur les plans en cache des tables
   (recompilations), et la durée de la lecture échantillonnée sur une grande table.
9. `xp_delete_files` et `xp_delete_file` sur la version plancher et sous Windows.

Si le point 1 échoue largement, goDTA ne mesure que les requêtes non paramétrées et les
procédures, et le document est révisé. Si le point 2 échoue, le mode labo est retiré.

## 12. Ce qui est laissé au constructeur

- les noms des sous-commandes et des drapeaux, dans l'esprit de §7 et §8 ;
- l'heuristique fine des candidats (§8.3) et la formule du score (§8.6), sous les contraintes
  données ;
- le format exact des fichiers JSON de l'espace de travail et du plan d'exécution ;
- les seuils par défaut (top N, délai de verrou, nombre de tentatives de `DROP`, taille maximale
  du fichier rapatrié, plafond des combinaisons) ;
- la façon de poser le nom d'application `db-ai-toolkit/godta`.

## 13. Décisions

Par Rudi :

- Go, binaire unique, déterministe ; CLI et pilotage par Claude.
- Connexion par `.env`, variables d'environnement, ou profils `sqlq`.
- Pas de création en production sans validation.
- Deux modes : sur le serveur et sur un clone restauré dans un conteneur podman local de même
  version, backup et restauration automatisés.
- Rapport technique détaillé pour validation et script `CREATE INDEX`.
- Gain estimé et coût de maintenance, tous deux.
- Toutes les bases d'un serveur, un rapport par base, filtres d'inclusion et d'exclusion.
- IA en deux modes : commentatrice du rapport, et source de suggestions.
- Repli sur le cache de plans quand le Query Store est absent.
- Aucun index en doublon de l'existant.
- Pas d'avertissement sur la sortie des données du client en mode labo : ses contrats la couvrent.
- sqlferret lu par son dossier de plans, sans modifier sqlferret.
- Mode labo gardé, au prix d'une écriture validée de statistiques `godta_*` en production avant
  le clone (décidé après la mesure du panel).

Par Claude, à vérifier à la lecture :

- importer `tools/internal/sqlq` plutôt qu'appeler `sqlq`, sous un nom d'application propre ;
- un profil `readwrite` distinct en plus de `--allow-write`, et `--allow-write` aussi pour le labo ;
- `--dry-run` qui écrit un plan d'exécution, seul exécutable ensuite ;
- statistiques créées avant le clone pour que le clone les copie, `STATS_STREAM` réservé aux
  suggestions arrivées après ;
- rapatriement du clone par `OPENROWSET(BULK)`, suppression par `xp_delete_files` ;
- goDTA ne pilote pas podman ;
- `collect` toujours sur la source ;
- coût de maintenance tiré des requêtes d'écriture du Query Store, les DMV à titre indicatif ;
- remplacement mesuré en masquant l'index remplacé sous `AUTOPILOT` ;
- clone sans Query Store, statistiques automatiques coupées sur le labo ;
- contrôle de chaque référence `AUTOPILOT` contre un `SHOWPLAN_XML` simple ;
- profil de labo déclaré `"environment": "lab"` et instance différente de la source ;
- modèle de couverture de §8.3, élargissement d'un index de contrainte en « à discuter » ;
- statistiques `_WA_Sys_*` auto-créées listées, pas supprimées ;
- sqlferret comme source de candidats seulement ;
- rowstore non cluster seulement, rapports en Markdown ;
- un spike de mesure (§11) avant le plan.

## 14. Relecture du panel

Panel du 2026-11-08 sur le commit `7cd6647` : agy (Gemini 4 Argon) directif et neutre, codex
(gpt-6-sol) directif et neutre, un sous-agent Claude neutre. Tous ont exécuté sur `sql2022`
(2022 CU26). Rapports dans le répertoire de travail de la session, non versionnés. Union des
constats, regroupés par mécanisme ; entre crochets, qui l'a trouvé (A = agy, C = codex,
Cl = Claude ; d = directif, n = neutre).

| Constat | Traitement |
|---|---|
| `STATISTICS_ONLY` sans valeur crée un index sans statistiques [Ad, An, Cd, Cn] | `= -1` imposé, §8.5 |
| `SET AUTOPILOT ON` doit être seul dans son lot, sur la même connexion ; en cas d'échec la requête s'exécute pour de bon [Cl, Cn, An] | connexion épinglée, vérification du lot et du résultat, §6 et §8.5 |
| Un index réel non déclaré à `AUTOPILOT` est caché [Ad], vérifié par le contrôleur | déclaration de tous les index réels ; sert aussi à mesurer un remplacement, §8.5 |
| Sans `pages`, l'index hypothétique compte une page [Ad] | estimation du nombre de pages, §8.5 et §11.5 |
| La liste `AUTOPILOT` persiste dans la session [An, Cl] | remise à zéro par le type 5, §8.5 |
| Le texte paramétré du Query Store ne se rejoue pas tel quel ; `sp_executesql`, RPC, `DECLARE`, `OPTIMIZE FOR` échouent [Ad, An] | piste de la procédure temporaire, point 1 du spike, §8.5 et §11 |
| Sur un clone, colonnes sans statistique et colonnes corrélées donnent des coûts faux, avant comme après [Ad, An, Cd, Cl] ; `STATS_STREAM` ne marche que d'un objet de mêmes colonnes [Ad, An, Cd] | statistiques `godta_*` créées en production avant le clone, décision de Rudi, §8.4 ; point 2 du spike |
| Verrous : `S` à la création, `Sch-M` à la suppression, pas `Sch-M` partout ; une suppression bloquée laisse l'index [Ad, An, Cd, Cl] | texte corrigé, suppression retentée et journalisée, §8.5 |
| Les plans en cache ne sont pas invalidés [Ad, An, Cd, Cl] | affirmation retirée, §8.5 |
| La création efface les suggestions d'index manquants de la table [An] | `collect` avant, effet dit dans le plan d'exécution, §8.5 |
| Statistiques `_WA_Sys_*` auto-créées en production pendant l'évaluation [Cl, An] | listées, non supprimées, §8.5 |
| Les index hypothétiques préexistants faussent la confrontation et sont copiés par le clone [Cl] | `is_hypothetical = 0`, restes listés, §8.2 |
| Règles de doublon : clé de l'index cluster implicite, filtres, index désactivés, sens des clés, permutations d'égalités, clés de l'existant couvrant les inclus, candidat préfixe d'un existant, inclus perdus au remplacement, index uniques, intersection [Ad, An, Cd, Cn, Cl] | modèle de couverture réécrit, §8.3 |
| Élargir un index de contrainte est impossible (1907, 3723) [An, Cl] | « à discuter » sans script, §8.3 |
| Un `DROP` de remplacement n'est pas mesuré, et ignore indications, repères de plan, plans forcés (erreur 308) [Cn, Cl] | mesure en masquant l'index, exclusion des index nommés, §8.2, §8.3, §8.6 |
| Les compteurs de maintenance ne se rapportent pas à une fenêtre, unités différentes, remis à zéro par une reconstruction ; un `UPDATE` hors colonnes ne maintient pas un index non cluster [Ad, An, Cd, Cn] | maintenance tirée des écritures du Query Store, DMV indicatives, §8.6 |
| Cache de plans : compteurs depuis la compilation, `query_hash` n'est pas `query_id` [Ad, Cd, Cn] ; `dbid` nul pour l'ad hoc [Ad, laissé de côté par lui] | période réelle affichée, correspondances par hash présentées comme telles, `dm_exec_plan_attributes`, §8.2 |
| `tools/internal/sqlplan` ne lit pas le XML [Ad, An, Cd, Cn, Cl] | nouveau lecteur à écrire, §8.3 |
| Le dossier de plans sqlferret n'a ni texte complet, ni politique de caviardage, ni base, ni compte par instruction [Ad, An, Cd, Cn, Cl] | sqlferret source de candidats seulement, §8.2 |
| `collect` sans la confirmation de production d'`AGENTS.md` [Cn, An] | confirmation avant la première requête, §7 et §10 |
| `--dry-run` ne peut pas montrer l'instruction exacte [Cl] | plan d'exécution écrit puis seul exécuté, §7.1 |
| `evaluate --target lab` écrit sans `--allow-write` ; un profil de production pris pour le labo écrirait en production [Ad, Cl] | `--allow-write`, refus d'un profil `prod`, contrôle `IsClone`, §7 |
| `RESTORE` sans `WITH MOVE` échoue sur le conteneur [An] | `FILELISTONLY` puis `WITH MOVE`, §8.4 |
| `cleanup` des fichiers de sauvegarde sans mécanisme nommé [An, Cl] ; motif `LIKE` dont `_` est un joker [Cl] | journal avant création, `xp_delete_files`, motif échappé, §7.2 |
| Nom d'application `sqlq` réutilisé [Cl] | `db-ai-toolkit/godta`, §6 |
| `--until` justifié par des rejeux « sur le même clone » alors que `collect` ne lit pas le clone [Cl] | phrase retirée, §8.2 |
| Un clone est en lecture seule (erreur 3906) [Ad, Cd] | `SET READ_WRITE` après restauration, §8.4 |

Constats rejetés : aucun. Les points vérifiés sans défaut (copie du Query Store par le clone,
`OPENROWSET` juste après le `BACKUP`, import de `tools/internal/sqlq` sans changer les garanties
du binaire `sqlq`, droits `db_owner` suffisants) sont repris en §11 comme acquis.

### 14.1 Relecture des corrections

Les corrections ci-dessus (commit `a35ebc2`) ont été relues à part, par un sous-agent Claude neuf
chargé de les attaquer, sans instance SQL Server. Traitement :

| Constat | Traitement |
|---|---|
| Le modèle de couverture ignore que la clé cluster fait partie de la clé d'un index non cluster non unique : un remplacement aurait recréé un index identique ; il traite mal l'index cluster ; le sens d'une égalité ; préfixe strict contre permutations ; options physiques perdues | clés effectives, égalités sans sens, égalités rangées avant la confrontation, index cluster jamais cible, options physiques reprises, §8.3 |
| Contradiction sur les statistiques de l'index hypothétique au labo ; statistiques automatiques du clone | objet vide reconnu, choix de l'optimiseur renvoyé au spike, statistiques automatiques coupées, §8.4, §8.5, §11 |
| `AUTOPILOT` limité aux index d'une table ; `pages` des index réels ; vues indexées, tas | toutes les tables lues par la requête, pages réelles, contrôle contre `SHOWPLAN_XML`, §8.5, §11 |
| Colonne de tri après une inégalité | règle de génération corrigée, §8.3 |
| Options `SET` du contexte ignorées au rejeu ; procédure temporaire non listée | options appliquées, procédure créée sous ces options et listée, §7.1, §8.5 |
| Retente du `DROP` à délai croissant ; la création bloque les écrivains | délai court et fixe avec pause, effet dit, §7.1, §8.5 |
| Le Query Store ne donne ni table ni colonnes écrites ; cascades, vues indexées, déclencheurs ; lignes contre exécutions ; clé cluster ; mode de capture ; repli sur le cache | lecture des plans d'écriture, `avg_rowcount`, clé cluster, mode de capture signalé, pas de `recommandé` en repli, §8.2, §8.6 |
| Les gardes du labo ne protègent pas le `RESTORE` ; champ `environment` facultatif | `environment` à `lab` exigé, instance différente de la source, vérifiées avant toute instruction, §7 |
| Query Store copié inutilement ; plafond de 2 Go ; `sysadmin` exigé par le clone ; `xp_delete_files` sur la version plancher | `NO_QUERYSTORE`, plafond dit, droits corrigés, point 9 du spike, §8.4, §11 |
| Indications dans les modules ; dérive de schéma entre validation et exécution ; effets des statistiques `godta_*` | `sys.sql_modules`, empreinte de schéma dans le plan d'exécution, point 8 du spike, §7.1, §8.2 |

Aucun constat rejeté.
