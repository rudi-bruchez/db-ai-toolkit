# Design de goDTA, conseiller d'index en ligne de commande

Date : 2026-11-08
Statut : brouillon issu du brainstorming, en attente de la vérification « constructeur » puis de
la relecture du panel externe.

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
ce conteneur.

## 3. Objectifs

1. Lire la charge de chaque base : Query Store sur une fenêtre configurable (24 h par défaut),
   requêtes les plus exécutées et les plus coûteuses.
2. Repli sur le cache de plans (`sys.dm_exec_query_stats`) quand le Query Store est éteint ou
   vide sur la fenêtre.
3. Accepter en source additionnelle un projet sqlferret (charge capturée par Extended Events),
   lu directement dans son dossier de plans (§8.2).
4. Déduire des index candidats des plans, les regrouper entre requêtes, les confronter aux index
   existants.
5. Mesurer chaque candidat par index hypothétique : coût estimé de chaque requête concernée avant
   et après.
6. Mettre en balance ce gain et le coût de maintenance de l'index (écritures sur la table,
   taille estimée).
7. Proposer aussi de modifier ou de consolider l'existant : élargir un index plutôt qu'en créer
   un voisin, remplacer un index existant par un candidat qui le couvre. Les redondances entre
   index existants eux-mêmes (sans candidat en jeu) sont signalées dans le rapport, sans gain
   chiffré et sans `DROP` dans le script (§8.7).
8. Deux modes de travail : sur le serveur (après validation), ou sur un clone restauré dans le
   conteneur local.
9. Laisser Claude soumettre ses propres candidats, que goDTA mesure comme les siens.

## 4. Non-objectifs

- Appliquer les index. goDTA produit un script ; c'est un humain qui le passe.
- Recommander la suppression d'un index au seul motif qu'il n'est pas utilisé. Les compteurs
  d'usage ne couvrent ni toute la vie de la base ni les autres réplicas (même raisonnement que
  `2026-10-01-missing-indexes-design.md`, §1 et §3). Un `DROP` n'est proposé que comme partie
  d'une consolidation (l'index est remplacé par un autre qui le couvre).
- Index columnstore, index filtrés, vues indexées, partitionnement. Rowstore non cluster seulement
  dans cette version.
- Azure SQL Database (pas de `DBCC CLONEDATABASE`, pas d'`AUTOPILOT` connu). SQL Server 2016 et
  suivants, et Azure SQL Managed Instance si le spike (§11) le confirme.
- `sys.dm_exec_requests` : cité au départ, écarté. Il ne donne qu'un instantané ; le Query Store
  et le cache de plans couvrent la fenêtre. À rouvrir si un besoin précis apparaît.
- Lecture du fichier DuckDB de sqlferret : DuckDB en Go impose cgo et casserait le binaire
  unique multiplateforme. Aucune modification de sqlferret non plus : goDTA lit les fichiers
  que sqlferret écrit déjà (§8.2).
- La sortie de données du client vers la machine de Rudi en mode labo (histogrammes, textes du
  Query Store) : décision de Rudi, ses contrats la couvrent, aucun avertissement ni purge.

## 5. Anti-objectifs

Ce qui ferait de goDTA un échec même s'il fonctionne :

- Proposer un index qui double un index existant. C'est la crainte que Rudi a nommée en premier.
  Une recommandation dont les clés et les colonnes incluses sont déjà couvertes par un index
  existant ne doit jamais sortir ; si le besoin est presque couvert, la recommandation est
  « élargir l'index X », jamais « créer un index Y ».
- Écrire en production sans l'avoir demandé explicitement.
- Laisser un index hypothétique, une base clone ou un fichier de sauvegarde sur le serveur après
  un arrêt brutal sans moyen de le retrouver et de le nettoyer.
- Annoncer un gain qui n'a pas été mesuré. Un candidat non évalué (verrou refusé, erreur) apparaît
  comme tel dans le rapport, sans chiffre.
- Exécuter tel quel un texte SQL fourni par l'IA.

## 6. Connexion

goDTA réutilise le paquet `tools/internal/sqlq` : mêmes profils (`-profile`, `$MSSQL_PROFILES`,
`~/.config/db-ai-toolkit/mssql-profiles.json`), même résolution des secrets (`passwordEnv`,
`passwordDpapi`), même construction de DSN. Il n'appelle pas le binaire `sqlq` : celui-ci refuse
`DBCC`, `OPENROWSET` et toute écriture sur un profil `readonly`, ce dont goDTA a besoin pour ses
phases d'écriture.

En plus des profils :

- Un fichier `.env` est chargé au démarrage (`--env`, défaut `./.env`, absent sans erreur), avec
  la règle habituelle : une variable déjà présente dans l'environnement gagne. Loader copié dans
  `tools/internal/dotenv` depuis `~/Sources/Repos/atelier/outils/sylloge/internal/dotenv/`. Il
  sert à fournir les `passwordEnv` des profils.
- Sans profil, une connexion directe par variables (`GODTA_SERVER`, `GODTA_AUTH`, `GODTA_USER`,
  `GODTA_PASSWORD`, `GODTA_ENVIRONMENT`), transformées en `sqlq.Profile` pour suivre le même
  chemin. Noms des variables : laissés au constructeur, à documenter dans `.env.example`.

Deux connexions au plus par exécution : la source (le serveur analysé) et le labo (le conteneur,
lui aussi décrit par un profil).

## 7. Lecture et écriture

Règle centrale : toutes les lectures sont permises ; toute écriture est une phase séparée,
demandée explicitement, et refusée sur un profil `readonly`.

| Phase | Écrit sur la source ? | Condition |
|---|---|---|
| `collect` | non | aucun |
| `clone` | oui : base clone, fichier de sauvegarde | profil `readwrite` et `--allow-write` |
| `evaluate --target server` | oui : index hypothétiques | profil `readwrite` et `--allow-write` |
| `evaluate --target lab` | non (écrit sur le labo) | profil du labo `readwrite` |
| `cleanup` | oui : suppression des restes | profil `readwrite` et `--allow-write` |

Mon choix, à valider : Rudi crée pour goDTA un profil `readwrite` distinct du profil `readonly`
de `sqlq` sur le même serveur. Le drapeau seul ne suffit pas, le profil seul non plus.

Chaque phase d'écriture accepte `--dry-run`, qui imprime les instructions exactes sans rien
exécuter : c'est ce qui se montre au client pour obtenir la validation.

Toutes les instructions d'écriture viennent de gabarits compilés dans le binaire. Les noms
d'objets y entrent par `QUOTENAME` ou un équivalent Go testé, jamais par concaténation brute.
Le nom de tout objet créé par goDTA commence par un préfixe fixe (par exemple `godta_`) et un identifiant
d'exécution, pour que `cleanup` les retrouve sans ambiguïté et ne touche à rien d'autre.

## 8. Le déroulement

### 8.1 Un espace de travail par exécution

Comme sqlferret, goDTA écrit dans un répertoire de projet (`--project`) : un sous-répertoire par
base, des fichiers JSON par phase. Chaque phase lit ce que la précédente a écrit. C'est ce qui
permet de reprendre, de rejouer `evaluate` sur le labo sans relire la production, et à Claude de
lire le matériau entre deux phases.

### 8.2 `collect` (lecture seule)

Pour chaque base retenue (toutes les bases utilisateur, filtrées par `--include` et `--exclude`,
motifs à jokers sur le nom ; les bases système, hors ligne, en lecture seule ou secondaires
non lisibles sont listées comme écartées avec la raison) :

- version, édition, niveau de compatibilité, état du Query Store ;
- charge : top N requêtes (N configurable) par nombre d'exécutions et par coût (CPU, lectures
  logiques, durée) sur la fenêtre, avec `query_id`, texte, plan estimé ou dernier plan connu ;
  la fenêtre a une borne de fin explicite (`--until`, défaut : maintenant), enregistrée dans la
  sortie, pour que deux exécutions sur le même clone donnent le même résultat ;
- repli sur le cache de plans si le Query Store est éteint ou vide sur la fenêtre, avec la
  mention dans le rapport (pas de `query_id` mais `query_hash`, couverture limitée à ce qui n'a
  pas été évincé) ;
- index existants des tables touchées : clés dans l'ordre, colonnes incluses, unicité, filtre,
  taille, `sys.dm_db_index_usage_stats` et `sys.dm_db_index_operational_stats` ;
- suggestions d'index manquants (`sys.dm_db_missing_index_*`) et éléments `MissingIndex` des
  plans ;
- nombre de lignes et largeur des colonnes des tables concernées.

`collect` s'exécute toujours sur la source, même en mode labo : sur un clone, les DMV d'usage,
d'opérations et d'index manquants sont vides et le cache de plans est froid. Le clone ne sert
qu'à `evaluate`, qui reprend la liste des requêtes établie par `collect` ; le Query Store du
clone n'est pas relu pour choisir les requêtes.

Source sqlferret (`--sqlferret <projet>`) : goDTA lit `plans/profile/run_N/index.json` et les
fichiers `.sqlplan` qu'il référence. Ces plans réels fournissent des colonnes pour générer des
candidats et des nombres d'exécutions. Une requête dont le texte a été caviardé à l'import
(niveau `literals`) ne peut pas être rejouée pour un plan estimé : elle contribue aux candidats
mais pas à la mesure du gain, et le rapport le dit. Le format exact de `index.json` est à lire
dans le dépôt sqlferret (`~/Sources/Repos/sqlferret`) au moment du plan.

### 8.3 Génération des candidats

À partir des plans (le paquet `tools/internal/sqlplan` lit déjà le XML) : colonnes des prédicats
de recherche et des prédicats résiduels des opérateurs d'accès, colonnes de sortie, groupes
`MissingIndex`. Règles de base :

- clés : égalités d'abord, puis une inégalité ; colonnes de sortie en `INCLUDE` ;
- regroupement des candidats d'une même table entre requêtes quand l'un est préfixe de l'autre ;
- confrontation à l'existant, dans cet ordre : doublon (même clé, inclus couverts) : écarté ;
  candidat couvert par les clés d'un existant à qui manquent seulement des inclus : transformé en
  « élargir l'existant » ; existant dont les clés sont préfixe du candidat : « remplacer l'existant
  par le candidat », avec `DROP` de l'ancien dans le script ;
- tri et départage entièrement déterministes (par table, puis par clé textuelle).

Le détail de l'heuristique (largeur maximale des inclus, nombre maximal de clés, quand préférer
l'ordre du `MissingIndex` à celui déduit des prédicats) est laissé au constructeur, à condition
que chaque règle soit nommée et apparaisse dans le rapport à côté du candidat qu'elle a produit
ou écarté.

### 8.4 `clone` (écriture validée sur la source, puis labo)

1. `DBCC CLONEDATABASE` de la base vers `godta_<base>_<run>`, Query Store compris (le comportement
   par défaut ; à vérifier par version).
2. `BACKUP DATABASE` du clone, compressé, dans le répertoire de sauvegarde par défaut de
   l'instance ou dans `--backup-dir`.
3. Rapatriement du fichier à travers la connexion SQL, par `OPENROWSET(BULK …, SINGLE_BLOB)`,
   sans partage réseau. Un clone sans données pèse quelques dizaines de Mo ; une limite de taille
   configurable protège le cas contraire.
4. Suppression de la base clone et du fichier sur la source.
5. Dépôt du fichier dans un répertoire de l'hôte monté dans le conteneur (chemin hôte et chemin
   vu par le conteneur en configuration), `RESTORE` sur le labo, passage en `READ_WRITE`.

goDTA ne gère pas le conteneur lui-même (ni démarrage ni arrêt) : il se connecte au profil du
labo. Mon choix ; le démarrage du conteneur reste à Rudi ou au skill.

### 8.5 `evaluate`

Sur la cible (`server` ou `lab`), pour chaque candidat et chaque requête qu'il concerne :

1. plan estimé de référence, sans index hypothétique ;
2. création de l'index hypothétique (`CREATE INDEX … WITH STATISTICS_ONLY`), sous un
   `SET LOCK_TIMEOUT` court (configurable, quelques secondes) ; si le verrou ne vient pas, le
   candidat est marqué non évalué et on passe au suivant ;
3. plan estimé avec l'index visible par `DBCC AUTOPILOT` / `SET AUTOPILOT ON` ;
4. coût estimé avant et après, index effectivement choisi ou non dans le plan ;
5. suppression de l'index hypothétique.

Sur le serveur, chaque `CREATE` et `DROP` de l'index hypothétique invalide les plans en cache des
tables touchées, et la prise du verrou `Sch-M` peut faire attendre les requêtes qui arrivent
derrière. Le rapport de `--dry-run` le dit au client. Ces risques n'existent pas sur le labo.

Les candidats sont aussi évalués en combinaison par table (tous les retenus d'une table ensemble)
pour détecter qu'un index en rend un autre inutile. Le détail (combinaisons testées, plafond) est
laissé au constructeur.

### 8.6 Score

Pour chaque index recommandé :

- gain : somme sur les requêtes concernées de (coût estimé avant − coût estimé après) × nombre
  d'exécutions sur la fenêtre ;
- coût de maintenance : volume d'écritures sur la table (`leaf_insert_count`,
  `leaf_update_count`, `leaf_delete_count` de l'index cluster ou du tas, et `user_updates`),
  rapporté à la même fenêtre quand c'est possible, et taille estimée de l'index (lignes ×
  largeur des clés et des inclus) ;
- une décision `recommandé` / `à discuter` / `écarté` selon des seuils configurables.

La formule exacte qui combine gain et maintenance est laissée au constructeur ; elle doit être
écrite dans le rapport et rester la même d'une exécution à l'autre. Le rapport affiche toujours
les deux grandeurs séparément, jamais le seul score combiné.

### 8.7 `report`

Par base :

- `rapport.md` : contexte (version, source de charge, fenêtre, ce qui manque), puis un bloc par
  recommandation : définition, origine (algorithme ou suggestion IA), requêtes servies
  (`query_id`, extrait du texte, exécutions, coût avant/après), coût de maintenance, index
  existants touchés (élargi, remplacé), règles appliquées ; ensuite les candidats écartés avec la
  raison, et les candidats non évalués ;
- `indexes.sql` : le script, un bloc commenté par recommandation, `DROP` d'un index remplacé
  placé après la création de son remplaçant ;
- `result.json` : tout ce qui précède, pour Claude.

Une section « redondances de l'existant » liste les index existants en doublon exact ou dont
les clés sont préfixe d'un autre, comme observations, sans gain chiffré (on ne sait pas mesurer
un `DROP` par index hypothétique) et sans instruction dans `indexes.sql`. Jamais de `DROP`, même
dans une consolidation, d'un index cluster, unique, de clé primaire ou appuyant une contrainte.

Par serveur : `synthese.md`, une ligne par base (recommandations, gain total, avertissements).

Mon choix : Markdown pour les rapports, faciles à relire, à convertir et à joindre.

## 9. Les suggestions de l'IA

goDTA ne demande rien à un modèle. Le va-et-vient passe par des fichiers :

1. après `collect` (et éventuellement un premier `evaluate`), Claude lit `result.json` ;
2. il écrit ses candidats dans un fichier JSON structuré (table, clés ordonnées, inclus, raison) ;
3. `evaluate --suggestions <fichier>` les évalue exactement comme les candidats de l'algorithme,
   après la même confrontation à l'existant.

Un candidat est une structure, jamais un texte SQL exécuté tel quel : goDTA régénère le DDL à
partir de la structure, après avoir vérifié que la table et les colonnes existent.

Le rapport distingue l'origine de chaque recommandation. Une suggestion qui ne fait baisser aucun
coût estimé est écartée avec ses chiffres.

## 10. Intégration au dépôt

- Binaire : `tools/cmd/godta`, code dans `tools/internal/godta` (et `tools/internal/dotenv`).
  Construit par `scripts/build-tools.*` vers `plugins/sqlserver-toolkit/bin/` comme les autres.
- Skill : `plugins/sqlserver-toolkit/skills/index-advisor/` (nom au choix du constructeur), qui
  décrit à Claude les phases, la règle de validation avant toute écriture sur un profil `prod`
  (même règle que dans `AGENTS.md`), et le format du fichier de suggestions.
- `AGENTS.md` : une section goDTA, dont l'exception explicite à « tout passe par `sqlq` ».
- Sortie standard : un objet JSON par commande, succès comme échec, à la manière de `sqlq`.
  Codes de sortie dans la même logique (`0`, `1` usage, `2` échec après connexion, `3` écriture
  refusée, `4` connexion impossible).

## 11. Risques à lever par un spike avant le plan

Le spike passe avant le plan d'implémentation : le plan ne s'écrit qu'avec ses résultats.
Ces points conditionnent l'architecture et ne se tranchent qu'en mesurant, sur `sql2022` et
`sql2025` du labo :

1. Syntaxe et comportement exacts des index hypothétiques : valeur de `STATISTICS_ONLY`
   utilisée par le DTA, droits nécessaires, forme du plan rendu sous `SET AUTOPILOT ON`, prise en
   compte réelle de l'index par l'optimiseur, compatibilité de `AUTOPILOT` avec les plans de
   requêtes paramétrées du Query Store. Tout ceci est non documenté.
2. Les statistiques d'un index hypothétique sur un clone. Le clone copie les statistiques
   existantes mais ne contient aucune ligne : les statistiques calculées pour un nouvel index
   hypothétique seraient vides, et l'estimation fausse. Pistes : s'appuyer sur les statistiques de
   colonnes déjà présentes, ou lire les flux de statistiques en production
   (`DBCC SHOW_STATISTICS … WITH STATS_STREAM`, lecture) et les poser sur le clone
   (`UPDATE STATISTICS … WITH STATS_STREAM`, non documenté), ce que fait le DTA avec un serveur de
   test. Le spike doit mesurer si les coûts estimés sur clone et sur base pleine concordent.
3. Rejouer une requête du Query Store pour obtenir son plan estimé : texte paramétré
   (`@P1 int, …`), valeurs des paramètres reniflés à reprendre du plan, requêtes à table
   temporaire ou dans une procédure. Ce qui ne peut pas être rejoué est listé comme tel.
4. `OPENROWSET(BULK …, SINGLE_BLOB)` sur le fichier de sauvegarde : droits, taille maximale, le
   fichier encore verrouillé juste après le `BACKUP`.
5. Présence du Query Store dans le clone selon la version.

Si le point 2 échoue, le mode labo ne donne que des estimations dégradées ; le document devra
alors être révisé avant le plan.

## 12. Ce qui est laissé au constructeur

- les noms des sous-commandes et des drapeaux, dans l'esprit de §7 et §8 ;
- l'heuristique fine des candidats (§8.3) et la formule du score (§8.6), sous les contraintes
  données ;
- le format exact des fichiers JSON de l'espace de travail ;
- les seuils par défaut (top N, délai de verrou, taille maximale du fichier rapatrié).

## 13. Décisions prises pendant le brainstorming

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

Par moi, à vérifier à la lecture :

- importer `tools/internal/sqlq` plutôt qu'appeler `sqlq` ;
- un profil `readwrite` distinct en plus de `--allow-write` ;
- `--dry-run` sur toute phase d'écriture ;
- rapatriement du clone par `OPENROWSET(BULK)` plutôt que par partage réseau ;
- goDTA ne pilote pas podman ;
- sqlferret pas lu par DuckDB ;
- `collect` toujours sur la source, le clone ne sert qu'à `evaluate` ;
- redondances entre index existants signalées sans `DROP` ni gain ;
- jamais de `DROP` d'un index cluster, unique, de clé primaire ou de contrainte ;
- spike avant le plan, pas de plan à branches ;
- `sys.dm_exec_requests` écarté ;
- pas de suppression d'index hors consolidation ;
- rowstore non cluster seulement ;
- rapports en Markdown ;
- un spike de mesure (§11) avant le plan.
