# Design de goDTA, conseiller d'index en ligne de commande

Date : 2026-11-08
Statut : révisé après la relecture du panel (agy et codex, prompts directif et neutre, plus un
sous-agent Claude), puis après le spike de §11, mené sur SQL Server 2016 SP3, 2017 CU31, 2019 CU32,
2022 CU26 et 2025 CU7. Le traitement de chaque constat du panel est en §14, les résultats du spike en §15.
Approuvé par Rudi le 2026-11-08 ; 2016 et 2017 ajoutés au spike à sa demande le même jour.
Prochaine étape : le plan d'implémentation.

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
suppriment par la connexion SQL avec `master.dbo.xp_delete_file 0, <chemin>` (non documentée,
mesurée sur 2016 à 2025, Linux et Windows ; elle ne supprime qu'un fichier de sauvegarde, ce qui
suffit). `xp_delete_files` n'existe pas sur 2016 (erreur 2812) et n'est pas utilisée ; un fichier dont le chemin n'a pas été journalisé avant le crash est signalé, pas cherché.

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
   colonne sans statistique, clé d'un candidat ou colonne utilisée par une requête retenue
   (prédicat, jointure, regroupement, tri), une statistique d'une colonne, puisque le labo n'en
   créera pas. La statistique multicolonne seule ne suffit pas : sans les histogrammes d'une
   colonne que la source crée d'elle-même à la compilation, le clone estime 633 lignes là où la
   source en estime 24 858, 9 et 143 (mesuré sur 2025, §15). La statistique multicolonne ne
   reste pas non plus telle quelle sur le labo (étape 8). Échantillonnage par défaut du
   moteur. C'est une lecture échantillonnée de la table (183 ms pour 7,8 millions de lignes) et
   une écriture de métadonnées sous verrou `Sch-S`, sans recompilation des plans en cache
   (mesuré), listée dans le plan d'exécution.
2. `DBCC CLONEDATABASE (<base>, godta_<run>_<base>) WITH NO_QUERYSTORE` (option disponible
   depuis 2016 SP1). Le clone copie les statistiques existantes, dont celles de l'étape 1. Le
   Query Store n'est pas copié : `collect` l'a déjà lu sur la source, et le copier grossirait le
   fichier et exporterait des textes pour rien. `DBCC CLONEDATABASE` exige `sysadmin`.
3. Suppression des statistiques de l'étape 1 sur la source. `DROP STATISTICS` prend `Sch-M` sur
   la table et fait recompiler ses plans en cache à leur prochaine exécution (mesuré :
   `plan_generation_num` passe de 1 à 2) : même délai de verrou court, mêmes tentatives et même
   journalisation pour `cleanup` que le `DROP INDEX` de §8.5.
4. `BACKUP DATABASE` du clone, compressé quand l'édition le permet (Express refuse
   `COMPRESSION`), dans le répertoire de sauvegarde par défaut de l'instance ou dans
   `--backup-dir`, chemin journalisé avant.
5. Rapatriement du fichier par la connexion SQL, `OPENROWSET(BULK …, SINGLE_BLOB)`, sans partage
   réseau (mesuré : lecture complète juste après le `BACKUP`, sans verrou ; 3,6 Mo pour la base
   de démonstration avec son Query Store). Le résultat est un `varbinary(max)`, plafonné à 2 Go :
   au-delà, ou au-delà d'une limite configurable plus basse, la phase s'arrête et le dit.
6. Suppression de la base clone et du fichier sur la source (`xp_delete_file`, §7.2).
7. Dépôt du fichier dans un répertoire de l'hôte monté dans le conteneur (chemin hôte et chemin
   vu par le conteneur en configuration), `RESTORE FILELISTONLY`, puis `RESTORE … WITH MOVE` de
   chaque fichier logique vers le répertoire de données du labo (les chemins de la source, souvent
   Windows, n'existent pas sur le conteneur), puis `ALTER DATABASE … SET READ_WRITE` (un clone est
   en lecture seule ; sans ce passage, la création d'un index hypothétique échoue, erreur 3906),
   puis `AUTO_CREATE_STATISTICS OFF` et `AUTO_UPDATE_STATISTICS OFF` sur le clone, avant toute
   compilation. Le clone copie les compteurs de modification des statistiques : une statistique
   dont le compteur dépasse le seuil est recalculée à la première compilation à partir de la
   table vide, et perd son histogramme (mesuré : 1 000 lignes avant, `NULL` après, estimation
   retombée à 31,6). Une statistique auto-créée sur le clone est vide et n'apporte rien.
   Ensuite, les configurations à l'échelle de la base de la source
   (`sys.database_scoped_configurations`, lues par `collect`) sont reportées sur le clone, que
   `DBCC CLONEDATABASE` ne copie pas : un `MAXDOP` de 2 sur la source et de 0 sur le clone
   donnait 23,50 contre 22,66 pour le même balayage parallèle, et l'égalité revient une fois la
   valeur reportée (mesuré). Le `MAXDOP` effectif de la source (configuration de la base, sinon
   de l'instance, borné par son nombre de processeurs) est reporté tel quel ; si le labo a moins
   de processeurs que ce `MAXDOP`, les plans parallèles diffèrent et le rapport le dit.
   Microsoft ne prend en charge un clone que comme copie en lecture seule pour le diagnostic :
   l'usage en écriture qu'en fait goDTA sort de ce cadre, c'est assumé et limité au labo.
8. Sur le labo, pour chaque statistique multicolonne `godta_*` de l'étape 1 : lecture de son flux
   (`DBCC SHOW_STATISTICS … WITH STATS_STREAM`, sur le clone lui-même, qui l'a copiée), gardé dans
   l'espace de travail, puis `DROP STATISTICS` sur le labo. Le flux est posé sur l'index
   hypothétique de mêmes clés au moment de l'évaluer (§8.5, étape 2). Laisser la statistique
   multicolonne sur le labo fausse la référence là où la source n'en a pas : sur 2016, 630 lignes
   estimées sur le labo contre 24 725 sur la source, alors que 2025 l'ignorait (mesuré). Les
   statistiques d'une colonne restent : ce sont celles que la source crée d'elle-même à la
   première compilation. Ainsi le labo a pour la référence les statistiques de la source, et
   pour le candidat celles qu'aurait l'index hypothétique sur la source : estimations et coûts
   identiques sur 2016, 2019 et 2025 (§15).

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
   sur lequel goDTA pose aussitôt le flux de la statistique multicolonne de mêmes clés gardé à
   l'étape 8 de §8.4 (`UPDATE STATISTICS <table>(<index>) WITH STATS_STREAM = …`, accepté pour un
   index aux mêmes clés avec des inclus, mesuré). Les statistiques d'un index hypothétique non
   déclaré n'influencent pas la référence (mesuré sur 2016). Sur la source, les statistiques de l'index hypothétique sont
   échantillonnées, alors que celles d'un index réel sont calculées sur toute la table à sa
   création : les estimations changent après la création réelle (25 992 lignes estimées avec
   l'hypothétique, 10 963 avec le réel, mesuré). Le rapport le dit à côté des gains ;
3. `DBCC AUTOPILOT(0, <db>, <table>, <index_id>, <pages>)` pour l'index hypothétique, avec un
   nombre de pages de données estimé : lignes × (octets moyens des clés et des inclus + clé de
   l'index cluster + 8 octets d'en-tête de ligne + 2 octets d'emplacement) / 8 096 octets, les
   largeurs moyennes venant des statistiques. Mesuré : 3 308 pages estimées contre 3 314 réelles,
   coût égal à 0,1 % près. Sans ce paramètre (ou avec 0), l'index hypothétique compte comme
   presque vide et une lecture d'intervalle paraît gratuite (0,015 contre 0,041, mesuré) ; non
   déclaré, il est invisible ;
4. `DBCC AUTOPILOT(0, <db>, <table>, <index_id>, 0)` pour chaque index réel non cluster de chaque
   table que la requête lit (lues dans son plan, jointures comprises), filtrés compris. Avec
   `pages` à 0, le moteur prend la taille réelle de l'index (coût identique à celui d'un plan
   ordinaire, mesuré) ; une valeur explicite remplace la taille réelle et doit alors être le
   nombre de pages de données (`in_row_data_page_count`), pas de pages utilisées (2 pages
   déclarées au lieu d'1 faisaient passer un coût de 0,0037 à 0,0044, mesuré). Les vues indexées
   se déclarent par le type 6 : `DBCC AUTOPILOT(6, <db>, <vue>, 1, 0)` ; déclarées par le type 0,
   elles restent invisibles (mesuré de 2017 à 2025). Seules les éditions Enterprise et Developer
   substituent d'elles-mêmes une vue indexée à ses tables ; ailleurs (Standard, Express), une vue
   indexée ne sert que par `NOEXPAND`, et elle est alors visible sous `AUTOPILOT` sans
   déclaration, au même coût (mesuré sur 2016 Express). Le rapport donne l'édition de la source. Un index réel non déclaré est caché
   à l'optimiseur sous `AUTOPILOT` (un seek sur l'index réel à 0,0037 devient un balayage à
   0,436). C'est aussi ce qui permet de mesurer un `DROP` : pour évaluer un remplacement, on ne
   déclare pas l'index remplacé. L'index cluster et le tas sont visibles sans déclaration
   (mesuré). Les index columnstore ne sont pas pris en charge : toute requête sur une table à
   index columnstore cluster échoue sous `AUTOPILOT` (erreur 2510, « Invalid DTA command »),
   déclaré ou non ; un index columnstore non cluster déclaré provoque la même erreur, non
   déclaré il est caché et le contrôle de l'étape 8 écarte la mesure. Ces requêtes sont listées
   « non évaluables (columnstore) », sans tentative ;
5. `SET AUTOPILOT ON`, seul dans son lot ; le résultat de ce lot est vérifié avant de continuer.
   S'il a échoué, la requête suivante s'exécuterait pour de bon : goDTA s'arrête ;
6. la requête rejouée ; le résultat doit être un document `ShowPlanXML`, sinon goDTA abandonne la
   connexion et marque l'erreur ;
7. `SET AUTOPILOT OFF` ;
8. la même séquence sans l'index candidat donne le coût de référence, dans les mêmes conditions.
   Contrôle obligatoire : ce coût de référence et la forme du plan doivent être identiques à ceux
   d'un simple `SET SHOWPLAN_XML ON` de la même requête sans `AUTOPILOT`. Un écart signifie qu'une
   déclaration manque ; la mesure est alors écartée, jamais publiée. Mesuré sur toutes les
   requêtes rejouables de la base de démonstration (1 913 sur 2022, 1 892 sur 2025) : identité
   exacte sur les tables utilisateur, sauf trois `INSERT … WITH (TABLOCK)` dans un tas, plan
   parallèle en `SHOWPLAN_XML` et série sous `AUTOPILOT` (coût doublé). Le contrôle les écarte.
   Le `SHOWPLAN_XML` passe avant la référence : une compilation sous `AUTOPILOT` ne crée pas
   les statistiques automatiques, un `SHOWPLAN_XML` les crée, et dans l'ordre inverse toute
   requête sur une colonne sans statistique sort en faux écart (mesuré sur 2022 à la relecture
   du plan : 421 lignes estimées contre 25 046). La base de démonstration avait déjà ses
   statistiques, ce qui explique l'identité exacte ci-dessus ;
9. `DROP INDEX` de l'index hypothétique.

Avant le rejeu, la session reçoit les options `SET` du `context_settings_id` de la requête
(`ANSI_NULLS`, `QUOTED_IDENTIFIER`, `ARITHABORT`, etc.) : les valeurs par défaut d'une connexion
Go ne sont pas celles des applications, et un index filtré ou une vue indexée n'est utilisé que
sous les options requises. Une procédure temporaire de rejeu est créée sous ces mêmes options,
puisqu'elle fige `ANSI_NULLS` et `QUOTED_IDENTIFIER` à sa création.

Rejeu des requêtes. Le texte du Query Store `(@P1 int)SELECT …` tel quel échoue (erreurs 1050
et 137) ; `EXEC sp_executesql` sous `AUTOPILOT` ne rend aucun plan de l'instruction interne ; un
appel paramétré de `go-mssqldb` (RPC) ne rend rien ; `DECLARE @P1 … = valeur` fait perdre la
valeur reniflée ; `OPTIMIZE FOR` refuse les types `MAX` (erreur 4132). goDTA enveloppe donc
chaque requête paramétrée dans une procédure temporaire (`#godta_…`) dont les paramètres
reprennent sa déclaration, et l'appelle avec les `ParameterCompiledValue` du plan, nommées
(`@p = valeur`). Mesuré sur 2019, 2022 et 2025 : le plan rendu est celui de l'instruction
interne, compilée avec ces valeurs reniflées ; 122 des 139 requêtes paramétrées de la base de
démonstration qui lisent des tables de la base sans table temporaire ni columnstore se rejouent
avec un plan identique au `SHOWPLAN_XML` (88 %, §15). Règles tirées des échecs :

- la clause `OPTION (PLAN PER VALUE(…))` que le Query Store ajoute aux variantes d'une requête
  optimisée par le PSP (2022 et suivantes) est retirée du texte avant le rejeu (erreur 102
  sinon) ;
- un texte préfixé dont les variables n'ont pas de `ParameterCompiledValue` est une instruction
  d'un lot ad hoc qui lit des variables locales (paramétrage `None`) : il se rejoue en lot, les
  variables déclarées sans valeur (`DECLARE @n int; …`), pour garder l'estimation sans valeur
  reniflée de l'original, et non par la procédure, qui reniflerait `NULL`. Un texte qui déclare
  lui-même la variable de son préfixe (erreur 134) se rejoue tel quel, sans le préfixe ;
- les paramètres et littéraux d'une procédure temporaire prennent le classement de `tempdb` :
  quand le classement de la base diffère de celui de l'instance, une expression sans colonne
  peut échouer (erreurs 457 et 468, deux requêtes) ; elle est listée « non rejouable
  (classement) » ;
- seuls se rejouent les textes dont le premier mot-clé, après le préfixe de paramètres, les
  commentaires et les parenthèses, est `SELECT`, `WITH`, `INSERT`, `UPDATE`, `DELETE` ou
  `MERGE` ; tout le reste est écarté sans être envoyé. Le type d'instruction du plan ne suffit
  pas : le Query Store garde aussi le plan de la lecture interne d'un `CREATE INDEX`, d'un
  `CREATE STATISTICS` ou d'un `RESTORE`, avec `StatementType="SELECT"` ou `"INSERT"`. Rejoués
  sous `AUTOPILOT` pendant le spike, ces textes n'ont été que compilés (aucun objet créé, base non
  restaurée, vérifié sur 2017), mais rien ne garantit qu'il en soit ainsi pour toute instruction
  d'administration ;
- table temporaire, variable table, objet d'une autre base ou disparu : « non rejouable », avec
  la raison.

Une requête non paramétrée se rejoue telle quelle. Le plan rejoué diffère souvent du dernier plan
du Query Store (statistiques, données ou niveau de compatibilité ont changé depuis) : c'est le
plan rejoué qui sert de référence, jamais celui du Query Store.

Les instructions `INSERT`, `UPDATE`, `DELETE`, `MERGE` et `BULK INSERT` ne sont que compilées
sous `AUTOPILOT` (mesuré : 232 instructions d'écriture rejouées sur 2022, 212 sur 2025, les
quatre formes sur 2019, aucune ligne écrite selon `sys.dm_db_index_operational_stats`, détecteur
vérifié par un vrai `UPDATE`). Elles sont évaluées comme les lectures : elles peuvent profiter
d'un candidat pour trouver leurs lignes, et leur plan sous `AUTOPILOT` contient la maintenance
de l'index hypothétique déclaré (opérateurs `Index Insert`, `Index Update`, `Index Delete` sur
lui, mesuré sur 2019 et 2022), qui donne le coût de maintenance de §8.6.

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
- les plans en cache des tables touchées ne sont pas invalidés par l'index hypothétique (mesuré :
  même `plan_handle`, `plan_generation_num` inchangé) ; `CREATE STATISTICS` ne les invalide pas
  non plus, `DROP STATISTICS` les fait recompiler (§8.4) ;
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
- coût de maintenance, sur la même fenêtre : pour chaque requête d'écriture du Query Store sur la
  table (§8.2), rejouée sous `AUTOPILOT` avec le candidat déclaré, le coût estimé de l'opérateur
  qui maintient l'index hypothétique (`Index Insert`, `Index Update`, `Index Delete`, coût de
  l'opérateur sans ses enfants) × exécutions. Le moteur décide lui-même quels `UPDATE` touchent
  l'index (une colonne de l'index ou la clé cluster ; un index non cluster n'est pas maintenu
  quand l'`UPDATE` ne touche aucune de ses colonnes, mesuré). Le coût total de l'instruction
  d'écriture ne sert pas : elle peut utiliser le candidat pour trouver ses lignes et devenir
  moins chère (23,48 sans le candidat, 3,96 avec, maintenance comprise, mesuré), ce qui entre
  dans le gain. Pour une écriture non rejouable, la maintenance retombe sur les lignes écrites
  (exécutions × `avg_rowcount`), marquées comme estimation grossière. Les compteurs des DMV
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
`sql2025` du labo, et si possible une instance 2016 ou 2019 (version plancher). Mené le
2026-11-08 sur 2019 CU32, 2022 CU26 et 2025 CU7 ; résultats en §15, qui disent aussi ce qui reste
ouvert. La liste ci-dessous est gardée telle qu'elle a été posée.

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
- statistiques créées avant le clone pour que le clone les copie ; sur le labo, statistiques
  multicolonnes posées sur l'index hypothétique par `STATS_STREAM` puis supprimées (§8.4, étape 8) ;
- rapatriement du clone par `OPENROWSET(BULK)`, suppression par `xp_delete_file` ;
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

Par Claude, après le spike (§15) :

- requêtes sur une table à index columnstore listées « non évaluables », sans contournement ;
- coût de maintenance lu dans les plans d'écriture rejoués sous `AUTOPILOT`, `avg_rowcount` en
  repli seulement ;
- index réels déclarés avec `pages` à 0, vues indexées par le type 6 ;
- configurations à l'échelle de la base reportées sur le clone ;
- statistiques d'une colonne pour chaque clé de candidat sans statistique, en plus de la
  multicolonne ;
- 2016 SP3 et 2017 pris en charge, comme demandé par Rudi, après mesure (§15) : rejeu limité aux
  textes dont le premier mot-clé est sur une liste fermée, `xp_delete_file` partout, compression
  de la sauvegarde selon l'édition.

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

## 15. Résultats du spike

Mené le 2026-11-08 sur cinq instances : `sqltop-test-2019` (2019 CU32), `sql2022`
(2022 CU26), `sql2025` (2025 CU7), un conteneur jetable 2017 CU31 (Linux, supprimé ensuite) et
une instance nommée `GODTA2016` (2016 SP3 Express, Windows Server 2019) installée sur la VM SQL02
du labo de ryokan, après un instantané libvirt `avant-godta2016-2026-11-08` ; l'instance est
gardée, arrêtée, en démarrage manuel. Bases : une copie restaurée de `PachaDataFormation` (2022
et 2025) et une base construite pour l'occasion (2016, 2017, 2019), jamais la base de
référence. Sur 2016, l'instance n'écoutant pas le réseau, les scripts ont tourné par `sqlcmd`
dans la VM et les plans ont été lus après coup. Outil : un petit
programme Go sur `go-mssqldb` v1.11.0, une connexion épinglée (`*sql.Conn`), chaque instruction
dans son lot, ce que goDTA fera. Scripts et sorties dans le répertoire de travail de la session,
non versionnés. Tout a été supprimé et les trois instances arrêtées à la fin.

| Point | Résultat | Versions |
|---|---|---|
| 1. Rejeu par procédure temporaire | marche : plan de l'instruction interne, valeurs reniflées reprises. 122 des 139 requêtes paramétrées dans le périmètre rejouées avec un plan identique au `SHOWPLAN_XML` (88 %) ; 1 716 requêtes ad hoc aussi. Échecs : `OPTION (PLAN PER VALUE)` du PSP (5), variable déclarée par le texte (6), classement de `tempdb` (2), divers (4). Hors périmètre : tables temporaires et variables table (968), autres bases ou objets disparus (141), catalogue système (72), columnstore (54) | 2019, 2022, 2025 |
| 2. Statistiques sur le labo | le coût d'un index hypothétique sur le clone est identique à celui de la source, y compris sur une colonne sans statistique avant le clone et sur des colonnes corrélées, à quatre conditions : statistiques `godta_*` d'une colonne en plus de la multicolonne, multicolonne posée sur l'index hypothétique puis supprimée du labo, `MAXDOP` de la base reporté, statistiques automatiques coupées avant toute compilation. Sans `godta_*` : 1 000 lignes estimées au lieu de 24 ; multicolonne seule : 633 au lieu de 24 858, 9 et 143 ; multicolonne laissée sur le labo en 2016 : 630 au lieu de 24 725 pour la référence. Avec les quatre conditions : identique sur 2016, 2019 et 2025. Sur 2017 et 2022, mesuré seulement avec la multicolonne laissée sur le labo, ce qui y donnait aussi l'identité | 2016, 2017, 2019 (clone sauvegardé et restauré), 2022, 2025 |
| 3. Écritures sous `AUTOPILOT` | compilées seulement : `INSERT`, `UPDATE`, `DELETE`, `MERGE`, `BULK INSERT`, aucune ligne écrite ; leur plan contient la maintenance de l'index hypothétique déclaré | 2019, 2022, 2025 |
| 4. Version plancher | `STATISTICS_ONLY = -1`, `AUTOPILOT` (types 0, 5, 6), procédure temporaire, `ParameterCompiledValue` et `ParameterDataType` dans les plans du Query Store, écritures compilées seulement, clone `NO_QUERYSTORE`, `OPENROWSET`, `IsClone` après restauration : identiques sur 2016, 2017 et 2019. Sur 2016 : pas de `xp_delete_files`, la statistique multicolonne du clone fausse la référence (étape 8 de §8.4), Express sans compression de sauvegarde ni substitution automatique des vues indexées. Managed Instance non mesurée | 2016 SP3 Express (Windows), 2017 CU31 (Linux), 2019 |
| 5. Pages | estimation 3 308 contre 3 314 réelles, coût égal à 0,1 % ; index réel à `pages` 0 : taille réelle ; valeur explicite : pages de données, pas pages utilisées ; hypothétique à 0 : sous-estimé | 2022 |
| 6. Statistiques automatiques, objet vide, `STATS_STREAM` | l'optimiseur se sert des `godta_*` copiées malgré l'objet vide de l'index hypothétique ; `STATS_STREAM` posé sur l'hypothétique donne le même résultat sur 2022 et 2025 et devient nécessaire sur 2016 une fois la multicolonne retirée du labo ; les statistiques d'un hypothétique non déclaré n'influencent pas la référence ; une statistique auto-créée sur le clone est vide ; la mise à jour automatique efface une statistique copiée dont le compteur de modifications dépasse le seuil | 2022, 2025 |
| 7. Jointures, tas, vues indexées, contrôle | jointures et tas sans difficulté ; vue indexée visible seulement par le type 6, ou par `NOEXPAND` sans déclaration (Express) ; index filtré déclaré normalement ; columnstore : erreur 2510 ; contrôle `AUTOPILOT` contre `SHOWPLAN_XML` identique sur les tables utilisateur sauf `INSERT … TABLOCK` parallèle (3 requêtes) | 2019, 2022, 2025 |
| 8. `CREATE` et `DROP STATISTICS` | création sous `Sch-S`, sans recompilation, 183 ms pour 7,8 millions de lignes échantillonnées ; suppression sous `Sch-M`, recompilation des plans de la table | 2022 |
| 9. `xp_delete_files`, `xp_delete_file` | `xp_delete_file 0` supprime un fichier de sauvegarde partout ; `xp_delete_files` aussi, sauf sur 2016 où elle n'existe pas (erreur 2812) | 2016 (Windows), 2017, 2019, 2022, 2025 (Linux et Windows) |

Découvertes hors liste, reportées dans le texte :

- le clone garde le nombre de lignes et de pages des tables (`sys.dm_db_partition_stats`), donc
  les pages des index réels n'ont pas à être transportées ;
- le clone copie les compteurs de modification des statistiques (§8.4) ;
- `DBCC CLONEDATABASE` ne copie pas les configurations à l'échelle de la base (§8.4) ;
- les statistiques d'un index hypothétique sont échantillonnées, celles d'un index réel non
  (§8.5) ;
- le coût de maintenance se lit directement dans les plans d'écriture (§8.6).

Restent ouverts, à traiter dans le plan ou avant de déclarer la prise en charge :

- Managed Instance ;
- 2017 et 2022 avec la statistique multicolonne posée sur l'hypothétique puis retirée du labo
  (correction mesurée sur 2016, 2019 et 2025) : à couvrir par le test d'intégration du plan ;
- l'écart entre source et labo dû au matériel : nombre de processeurs inférieur au `MAXDOP`
  effectif, mémoire disponible pour les allocations (`EstimatedAvailableMemoryGrant`), non
  mesuré entre deux instances différentes (toutes les mesures source contre clone l'ont été sur
  la même instance, sauf 2019 où le labo était une base restaurée sur la même instance) ;
- les deux règles de rejeu sans mesure directe : variables locales d'un lot ad hoc rejouées en
  lot, texte qui déclare sa propre variable rejoué sans préfixe ; elles reçoivent un test dans
  le plan.
