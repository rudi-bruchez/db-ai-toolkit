# Design : catalogue de requêtes `sqlq`, avec tsql-scripts comme source

Date : 2026-10-04
Statut : brainstorming terminé, en attente de relecture avant plan d'implémentation.
Remplace : `2026-09-02-query-catalog-design.md`, jamais implémenté. Ses décisions sont
reprises ici quand elles tiennent, et modifiées là où ce document le dit (§12).
Prérequis : `2026-09-02-live-query-design.md`, implémenté.

## 1. Le problème

`sqlq` sait exécuter une requête, il ne sait rien retenir. Chaque question posée deux fois
fait réécrire le même SQL par le modèle : même coût, et surtout pas la même requête, donc
pas une réponse comparable d'une semaine sur l'autre.

Il existe par ailleurs un dépôt de requêtes de diagnostic déjà écrites, éprouvées en SSMS
et maintenues par l'utilisateur : `tsql-scripts` (github.com, 344 fichiers `.sql`, SQL
Server 2014 et suivants, Azure SQL). L'objectif est que l'agent puisse les appeler par un
nom court, au lieu de réécrire ce qu'elles font déjà.

Ce qui est visé, dans l'ordre :

1. La stabilité : la même question donne la même requête.
2. La qualité cumulée : une requête corrigée une fois reste corrigée, et une requête
   écrite à la main par un DBA vaut mieux qu'une requête improvisée par un modèle.
3. Le coût : le SQL n'entre plus dans le contexte (§11).

L'appelant est l'agent, à travers le skill `live-query`. La sortie reste le JSON actuel de
`sqlq` ; aucun mode d'affichage pour un humain n'est prévu.

## 2. Décisions prises au brainstorming

| Question | Décision |
|---|---|
| Qui appelle ? | L'agent, via `live-query`. |
| Où vit la référence d'un script tsql-scripts ? | Dans tsql-scripts, lu sur place dans un clone local. Pas de copie dans le plugin, pas de téléchargement. |
| Quels scripts entrent au catalogue ? | Ceux qui portent une ligne marqueur dans leur en-tête, et seulement eux. |
| Comment l'agent règle un paramètre d'un script tsql-scripts ? | Par surcharge des `DECLARE @x type = défaut` nommés dans le marqueur. Le script reste exécutable tel quel dans SSMS. |
| Périmètre de la v1 | Tout le design du 2 septembre (sources plugin et personnelle, `-list-queries`, `-saved`, `-save-query`), plus la source tsql-scripts. |
| Nom d'un script tsql-scripts | Déclaré dans le marqueur. Pas de priorité entre sources : une collision est refusée. |
| Trace d'exécution réussie | Un registre personnel par empreinte de contenu, pour les trois sources. Remplace la ligne `Verified:` écrite dans le fichier. |
| Chemin du clone | `-tsql-scripts <dir>`, sinon `$DB_AI_TOOLKIT_TSQL_SCRIPTS`. Absent : la source est vide. |

## 3. Ce que contient tsql-scripts

Mesuré le 2026-10-04 en passant les 344 fichiers dans le vrai garde-fou de `sqlq`
(`FindWrites`, `FindBatchSeparators`, `FindContextChanges`) :

| Résultat | Fichiers |
|---|---|
| passent tels quels | 214 |
| refusés pour `GO` | 98 |
| `CREATE` (tables temporaires, installations) | 73 |
| `USE` | 38 |
| `EXEC` ou `EXECUTE` | 38 |
| `ALTER` | 29 |
| `INSERT` | 19 |
| `INTO` (`SELECT … INTO #t`) | 14 |

Un fichier peut cumuler plusieurs motifs. Parmi les 214 qui passent : 160 n'ont ni
`DECLARE` ni marqueur `<…>`, 45 déclarent des variables en tête avec une valeur par défaut,
13 portent un marqueur à éditer (`<TABLE NAME>`).

Trois autres faits pèsent sur le design :

- 59 noms de fichiers ne respectent pas la règle de nommage du catalogue
  (`dm_io_virtual_file_stats.sql`, `LOB-usage.sql`), et six noms normalisés existent en
  double dans le dépôt. `missing-indexes` existe trois fois : `index-management/`,
  `diagnostics/query-store/`, et la requête livrée dans le plugin.
- L'en-tête suit le gabarit du `CLAUDE.md` de tsql-scripts : des lignes `--` entre deux
  lignes de tirets, pas un bloc `/* … */`.
- La plupart des scripts posent `SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED` eux-mêmes.

Ce document ne cherche pas à rendre exécutables les 130 fichiers refusés. Le garde-fou
reste ce qu'il est ; un script refusé reste dans SSMS.

## 4. La sauvegarde d'une exécution

Repris du 2 septembre, et c'est ce qui tient le reste : on ne sauvegarde pas une requête,
on sauvegarde une exécution réussie.

```bash
sqlq -profile prod-erp -query "<sql>" -save-query orders-late
```

La requête tourne d'abord. Le fichier n'est écrit que si le run se termine en code 0.
Aucun fichier de la couche personnelle ne peut donc exister sans avoir tourné au moins une
fois, et l'attestation d'exécution est établie par le binaire, jamais affirmée par le modèle.

Pour les sources que `sqlq` n'écrit pas (plugin, tsql-scripts), l'équivalent est le
registre du §8 : il ne dit « vérifié » que pour un contenu qui a réellement tourné.

## 5. Répartition des responsabilités

| Ce qui doit être vrai : `sqlq` (Go, testé) | Ce qui relève du jugement : skill `live-query` |
|---|---|
| lister le catalogue, résoudre un nom | reconnaître qu'aucune requête stockée ne répond |
| valider noms, paramètres, portée, collisions | proposer une sauvegarde, et sous quel nom |
| réécrire les `DECLARE` surchargés | lire l'en-tête d'une requête avant de la lancer |
| refuser un script marqué mais inutilisable | présenter le résultat, dire ce qui n'est pas vérifié |
| tenir le registre des exécutions | demander un oui avant une requête `heavy` en prod |
| détecter `dirty_reads` | ne pas tirer de conclusion de justesse d'un résultat `dirty_reads` |

Aucune règle de sécurité, de portée ou de nommage ne dépend du respect d'une consigne en
langage naturel.

## 6. Les sources

| Source | Emplacement | Nom | Portée |
|---|---|---|---|
| `bundled` | `<plugin>/skills/live-query/queries/*.sql` | nom du fichier | `generic` |
| `personal` | `~/.config/db-ai-toolkit/queries/<profil>/*.sql` | nom du fichier | `<profil>` |
| `personal` | `~/.config/db-ai-toolkit/queries/_generic/*.sql` | nom du fichier | `generic` |
| `tsql-scripts` | clone local, parcours récursif, `.git/` exclu | `name=` du marqueur | `generic` |

### Localisation

- `bundled` : relativement à l'exécutable, `<dir(os.Executable())>/../skills/live-query/queries/`.
  `-queries <dir>` force le chemin (tests, développement hors plugin).
- `personal` : `~/.config/db-ai-toolkit/queries/`. Absent : la source est vide.
- `tsql-scripts` : `-tsql-scripts <dir>`, sinon `$DB_AI_TOOLKIT_TSQL_SCRIPTS`. Aucun défaut
  deviné. Non configuré ou introuvable : la source est vide, et `-list-queries` ajoute un
  message dans `messages` (`tsql-scripts source not configured`, ou `not found`) pour que
  l'agent ne conclue pas que le dépôt ne contient rien. Pas de fichier de configuration
  propre à `sqlq` en v1 : le drapeau et la variable suffisent.

### Portée

- Une requête de portée `<profil>` n'est exécutable que sur ce profil. Ailleurs : refus,
  code 1, message nommant le fichier et sa portée.
- `generic` : exécutable partout. C'est la promesse qu'elle ne lit que des vues système et
  des DMV.
- Élargir une portée est un geste humain : déplacer le fichier dans `_generic/`, ou le
  commiter dans le plugin ou dans tsql-scripts. Pas de drapeau `-widen` (question 3 du
  2 septembre, tranchée).
- `sqlq` n'écrit jamais dans le plugin ni dans tsql-scripts. `-save-query` écrit toujours
  sous `personal/<profil>/`.

### Unicité des noms

Aucune source n'a priorité sur une autre : un masquage silencieux n'est pas possible.

- L'ensemble des noms visibles depuis un profil P est : `bundled`, `personal/_generic`,
  `tsql-scripts`, `personal/P`. Un nom présent deux fois dans cet ensemble rend les deux
  entrées `rejected` (`name "x" also defined by <source>:<path>`).
- Le même nom dans deux répertoires de profil différents est permis : ils ne sont jamais
  visibles ensemble.
- Deux scripts tsql-scripts marqués du même nom : les deux sont `rejected`.
- `-save-query` refuse un nom déjà visible depuis le profil, quelle que soit sa source.

### Nommage

`^[a-z][a-z0-9-]{1,48}$`, validé avant toute construction de chemin.
`-save-query` écrit à un chemin dérivé d'un nom proposé par un modèle :
`../../../.ssh/authorized_keys` doit être refusé par une règle.

## 7. Format des en-têtes

Deux analyseurs, un par format, produisant le même modèle d'entrée.

### Sources `bundled` et `personal` : bloc `/* … */`

Format des requêtes déjà livrées, inchangé, sauf la ligne `Verified:` qui disparaît (§8) :

```sql
/*  Largest user tables in the current database, by space reserved.

    Parameters: none.

    Why this shape rather than the obvious one: ...
*/
SELECT ...
```

1. Le fichier ouvre par un bloc `/* … */`. Sinon : `rejected`.
2. La première ligne non vide du bloc est le résumé.
3. `Parameter:` ou `Parameters:` déclare les paramètres (`Parameter: @name - …`, ou
   `Parameters: none.`).
4. Le reste est de la prose pour le lecteur humain.

`-save-query` écrit ce format : un bloc contenant le résumé et la ligne `Parameters:`,
fournis par l'agent par `-summary "<texte>"`, puis le SQL exécuté. Le paramétrage d'une
requête sauvée suit la règle des requêtes livrées : un `@x` référencé et non déclaré.

### Source `tsql-scripts` : en-tête `--` et ligne marqueur

```sql
-----------------------------------------------------------------
-- Wait statistics since the last restart, cumulative
--
-- sqlq: name=waits-statistics params=database_name,top_n
-- rudi@babaluga.com, go ahead license
-----------------------------------------------------------------
```

- L'en-tête est le bloc de lignes `--` contigu qui ouvre le fichier (lignes vides en tête
  ignorées).
- La ligne marqueur commence par `-- sqlq:`, à l'intérieur de cet en-tête. Ailleurs dans le
  fichier, elle est ignorée : un marqueur dans un commentaire au milieu du code n'est pas un
  marqueur. Un en-tête sans marqueur : le fichier n'entre pas au catalogue, et n'apparaît
  pas du tout (pas même en `rejected`).
- Clés, séparées par des espaces : `name=` (obligatoire), `params=` (liste séparée par des
  virgules, facultative), `heavy` (sans valeur, facultative). Une clé inconnue rend l'entrée
  `rejected` : une faute de frappe dans `heavy` ne doit pas passer pour son absence.
- Le résumé est la première ligne de l'en-tête dont le contenu, après `--`, est non vide et
  n'est pas fait que de tirets, et qui n'est pas la ligne marqueur.

### Le champ `rejected`

Un script marqué (tsql-scripts) ou un fichier d'une source `bundled` ou `personal` qui ne
peut pas tourner apparaît quand même dans `-list-queries`, avec la raison :

- refusé par le garde-fou : `batch separator GO at line 41`, `write keyword EXEC`,
  `USE is refused` ;
- nom invalide, ou nom en collision (§6) ;
- en-tête illisible, clé de marqueur inconnue, résumé absent ;
- l'un des refus de surcharge du §9.

`-saved` sur une entrée `rejected` : code 1, avec la même raison. Sans ce champ, un
marqueur posé sur un script contenant `GO` le ferait disparaître sans explication.

## 8. Le registre des exécutions

`~/.config/db-ai-toolkit/verified.json`, écrit par `sqlq` seul.

```json
{"tsql-scripts:diagnostics/wait-statistics/waits-statistics.sql":
   {"sha256":"9f2c…","date":"2026-10-04","profile":"prod-erp"}}
```

- Clé : source et chemin relatif à la racine de la source (pour `personal` :
  `personal:<profil>/<fichier>`). Le nom n'entre pas dans la clé : renommer un marqueur ne
  fait pas perdre la vérification d'un contenu inchangé.
- Après chaque run `-saved` terminé en code 0, et après chaque `-save-query` réussi, l'entrée
  est écrite ou remplacée : c'est la dernière vérification (question 2 du 2 septembre,
  tranchée).
- L'empreinte est celle du fichier sur disque, pas du texte réécrit par la surcharge : une
  vérification couvre le script quelles que soient les valeurs passées.
- Le catalogue affiche `verified` seulement si l'empreinte actuelle du fichier égale celle
  du registre. Un fichier modifié redevient `null`, sans que personne ait à y penser.
- Le registre enregistre le nom du profil, pas le serveur ni la base. `-list-queries` est
  lu à chaque session et part chez le fournisseur du modèle ; il obéit à la même règle que
  `-list-profiles`. Le design du 2 septembre imprimait `on SRV01/ERP` : corrigé ici.
- Écriture atomique : fichier temporaire dans le même répertoire, puis `rename`.
- Registre illisible ou JSON invalide : traité comme vide, un message dans `messages`,
  réécrit en entier au prochain succès.
- Le registre est propre à la machine. Un même script peut être vérifié ici et non vérifié
  sur un autre poste.

`verified` atteste d'une exécution réussie, pas d'une réponse juste.

## 9. La surcharge des `DECLARE`

Ne concerne que la source `tsql-scripts`.

### Le mécanisme

Pour `sqlq -profile p -saved waits-statistics -param database_name=ERP` :

```sql
-- dans le fichier
DECLARE @database_name sysname = '%';
-- texte envoyé au serveur
DECLARE @database_name sysname = @sqlq_database_name;
```

- La valeur part en `sql.Named("sqlq_database_name", "ERP")`, par le chemin existant de
  `namedArgs`. Jamais de concaténation.
- La conversion vers le type déclaré est faite par le serveur. Une valeur invalide pour un
  `bit` ou un `int` revient en erreur SQL, code 2, avec son numéro.
- Un paramètre non passé garde le défaut du fichier ; sa ligne n'est pas touchée.
- Le préfixe `sqlq_` évite le conflit avec la variable elle-même, qu'on ne peut pas
  déclarer deux fois. Un script qui utilise déjà un identifiant `@sqlq_…` est `rejected`.

### Repérage

L'analyse porte sur le texte passé par `Sanitize`, sans commentaires ni littéraux, et dont
les positions et sauts de ligne sont préservés. La réécriture s'applique au texte d'origine
aux mêmes positions. `Sanitize` travaille en runes : la réécriture aussi, pour qu'un
caractère accentué dans un commentaire ne décale pas l'offset.

Pour un paramètre `p` du marqueur, `sqlq` cherche la déclaration `DECLARE @p <type> =`.
La portion remplacée commence après le `=` et finit au premier `;` de la même ligne, ou à
la fin de la ligne.

### Refus au moment de construire le catalogue

| Cas | Raison du refus |
|---|---|
| paramètre du marqueur sans ligne `DECLARE @p <type> = …` | l'en-tête promet un réglage qui n'existe pas |
| `@p` déclaré plus d'une fois | on ne sait pas laquelle réécrire |
| la ligne de `@p` déclare plusieurs variables (`DECLARE @a int = 1, @b int = 2`) | le découpage de l'initialiseur devient ambigu |
| parenthèses déséquilibrées dans la portion remplacée | initialiseur sur plusieurs lignes, la réécriture casserait le SQL |
| `SET @p =`, `SELECT @p =`, ou `@p` en cible d'un `FETCH … INTO` ailleurs dans le script | la valeur passée serait écrasée sans bruit |

Le dernier cas est le plus important des cinq. C'est le seul où tout semble avoir marché
alors que la réponse est fausse : l'agent rapporterait un résultat pour ERP calculé sur une
autre base.

### Refus à l'exécution, avant toute connexion (code 1)

- Un `-param` absent du marqueur. Sans ce refus, `-param databse_name=ERP` (faute de
  frappe) ferait tourner le défaut `'%'` sur toutes les bases.
- Pour `bundled` et `personal` : la règle du 2 septembre, inchangée. Un paramètre est un
  `@x` référencé et non déclaré ; un paramètre requis absent est refusé
  (`parameter "name" required`), un `-param` inconnu aussi.

Le garde-fou s'applique au texte réécrit, en filet, en plus du texte d'origine.

## 10. Contrat CLI

Trois sources de SQL, mutuellement exclusives : `-query`, `-file`, `-saved`.

| Drapeau | Effet |
|---|---|
| `-list-queries` | imprime le catalogue en JSON et sort. N'exige pas de profil. |
| `-saved <nom>` | exécute l'entrée du catalogue de ce nom (question 1 du 2 septembre : le nom est gardé) |
| `-save-query <nom>` | après un run `-query` ou `-file` réussi, écrit la requête sous `personal/<profil>/` |
| `-summary <texte>` | résumé écrit dans l'en-tête par `-save-query`, obligatoire avec lui |
| `-queries <dir>` | force le répertoire de la source `bundled` |
| `-tsql-scripts <dir>` | chemin du clone tsql-scripts |

`-save-query` refuse toute requête que `FindWrites` signale, y compris sur un profil
`readwrite` et y compris avec `-allow-write`. Le catalogue ne contient que des lectures :
sinon il fabriquerait des instructions destructrices appelables par un nom court.

### Catalogue

```json
{"queries":[
  {"name":"tables-largest","summary":"Largest user tables in the current database, by space reserved.",
   "params":[],"scope":"generic","source":"bundled","path":"tables-largest.sql",
   "verified":null,"dirty_reads":false,"heavy":false},
  {"name":"waits-statistics","summary":"Wait statistics since the last restart, cumulative",
   "params":["database_name","top_n"],"scope":"generic","source":"tsql-scripts",
   "path":"diagnostics/wait-statistics/waits-statistics.sql",
   "verified":{"date":"2026-10-04","profile":"prod-erp"},"dirty_reads":true,"heavy":false},
  {"name":"io-virtual-file-stats","source":"tsql-scripts",
   "path":"diagnostics/IO/dm_io_virtual_file_stats.sql",
   "rejected":"batch separator GO at line 41"}
],
 "messages":[]}
```

- `path` est relatif à la racine de sa source. Le chemin absolu du clone ou du répertoire
  personnel n'est jamais imprimé.
- `dirty_reads` : `true` si le texte nettoyé contient `READ UNCOMMITTED` ou `NOLOCK`.
  Calculé, donc impossible à oublier.
- `heavy` : déclaré dans le marqueur (tsql-scripts) ou par une ligne `Heavy: yes` dans le
  bloc (`bundled`, `personal`). Relève du jugement : une analyse lexicale ne sait pas ce que
  coûte un scan.

### Résultat d'un run `-saved`

Le JSON habituel gagne un champ `saved`, pour que l'agent ne puisse pas se tromper sur ce
qui a tourné :

```json
"saved":{"name":"waits-statistics","source":"tsql-scripts",
         "path":"diagnostics/wait-statistics/waits-statistics.sql",
         "params":{"database_name":"ERP","top_n":"default"},"verified":null}
```

`verified` y est l'état avant le run.

Codes de sortie inchangés : `3` reste réservé au garde-fou. Les nouveaux refus (nom
inconnu, entrée `rejected`, portée incompatible, paramètre manquant ou inconnu, collision
à la sauvegarde) sont des erreurs d'usage : code 1, JSON habituel sur stdout.

## 11. Le coût en tokens

| | Coût |
|---|---|
| Catalogue de 40 entrées, une fois par session | ~1 500 tokens |
| Réécrire une requête comme `tables-largest` | ~900 tokens de sortie, plus 1 à 3 allers-retours d'erreur : 2 000 à 4 000 |
| Rejouer par son nom | ~15 tokens, plus la lecture de l'en-tête à la première exécution de la session |

Le poste principal reste les lignes de résultat. `TOP (n)` et `-maxrows` gardent toute leur
importance. Les chiffres du catalogue sont des estimations à remplacer par une mesure sur le
premier lot de marqueurs.

## 12. Changements au skill `live-query`

- La table de décision disparaît. Règle unique : appeler `-list-queries` avant d'écrire du
  SQL, et préférer une entrée du catalogue à une requête ad hoc.
- Les avertissements propres à une requête, qui vivent aujourd'hui dans cette table
  (« lire l'en-tête d'abord », `-maxrows 70` pour `missing-indexes`), passent dans l'en-tête
  du fichier. Le skill dit : avant la première exécution d'une requête dans une session,
  lire son en-tête. Pour une entrée tsql-scripts, l'agent lit le fichier par son `path`
  sous le clone ; le skill dit où le trouver.
- `verified: null` : le dire avant de lancer (« jamais exécutée avec `sqlq` ici »).
- `rejected` : ne pas lancer, ne pas contourner en réécrivant le script en ad hoc, rapporter
  la raison. La correction revient à l'utilisateur, dans le fichier source.
- `dirty_reads: true` : ne jamais s'appuyer sur le résultat pour répondre à une question sur
  la justesse des données. Sans conséquence pour une requête sur des DMV ; pertinent pour
  `number-of-NULL-in-table.sql`, qui lit des tables utilisateur.
- `heavy: true` sur un profil prod : annoncer le coût et attendre un oui, même si la session
  a déjà été confirmée.
- Après une requête ad hoc réussie et utile : proposer la sauvegarde en une ligne, avec un
  nom et un résumé. Jamais sans accord explicite. Pas de proposition pour une requête dont
  la réponse n'a servi à rien.
- La règle prod (« nommer le profil et attendre un oui ») est inchangée.

Écarts au design du 2 septembre :

- la ligne `Verified:` dans le fichier est remplacée par le registre (§8) ;
- `verified` n'imprime plus le serveur ni la base ;
- `-summary` est ajouté, faute de quoi `-save-query` ne saurait pas écrire le résumé ;
- un fichier `bundled` ou `personal` à l'en-tête invalide est `rejected` au lieu d'être
  absent, comme pour tsql-scripts.

## 13. Côté tsql-scripts

- Documenter la ligne marqueur dans son `CLAUDE.md`, à côté du gabarit d'en-tête : clés,
  règle de nommage, condition sur les `DECLARE` surchargés, et le fait qu'un script à `GO`
  ou `EXEC` ne sera pas accepté.
- Poser un premier lot de marqueurs, choisi par l'utilisateur. Point de départ proposé : la
  table « Script for a need » de son `AGENTS.md`, restreinte aux scripts qui passent le
  garde-fou.
- Convertir à la main en `DECLARE` avec défaut les scripts à marqueur `<…>` qu'on veut au
  catalogue. La conversion sert aussi dans SSMS.

Aucune modification de tsql-scripts n'est nécessaire pour que `sqlq` fonctionne : sans
marqueur, la source est simplement vide.

## 14. Tests

Sur un mini-dépôt dans `testdata/`, jamais sur le vrai clone. Chaque test supprime une
classe de défaut.

| Test | Classe supprimée |
|---|---|
| `TestMarkedScriptRefusedByGuardIsListedAsRejected` | le marqueur posé qui fait disparaître le script sans explication |
| `TestUnmarkedScriptIsAbsent` | l'entrée accidentelle |
| `TestMarkerOutsideHeaderIsIgnored` | le marqueur trouvé dans un commentaire du code |
| `TestUnknownMarkerKeyIsRejected` | `heavy` mal orthographié pris pour son absence |
| `TestDuplicateNameAcrossSourcesRejectsBoth` | le masquage d'une source par une autre |
| `TestSameNameInTwoProfileDirsIsAllowed` | le refus à tort de deux portées disjointes |
| `TestDeclareOverrideBindsNotConcatenates` | l'injection par `-param` |
| `TestDeclareOverrideKeepsOffsetsWithAccents` | la réécriture décalée par un caractère multi-octet |
| `TestReassignedVariableIsRejected` | la valeur passée écrasée sans bruit |
| `TestMultiVariableDeclareIsRejected` | la réécriture qui casse le SQL |
| `TestUnbalancedInitializerIsRejected` | idem, initialiseur sur plusieurs lignes |
| `TestUnknownParamRefusedBeforeConnecting` | la faute de frappe qui lance le défaut `'%'` |
| `TestVerifiedDropsWhenFileChanges` | la vérification qui survit à une modification |
| `TestVerifiedSurvivesRename` | la vérification perdue par un simple changement de nom |
| `TestRegistryCorruptIsTreatedAsEmpty` | le catalogue rendu inutilisable par un fichier abîmé |
| `TestDirtyReadsDetected` | le script en `READ UNCOMMITTED` présenté comme fiable |
| `TestListQueriesPrintsNoAbsolutePathOrServer` | le chemin local ou le nom de serveur publié à chaque session |
| `TestMissingTsqlScriptsSourceIsAMessageNotAnError` | le catalogue cassé par un clone absent |
| `TestDeclaredParametersMatchTheSQL` | l'en-tête qui promet un paramètre que le SQL n'a pas |
| `TestCatalogListsExactlyTheFilesPresent` | l'entrée fantôme et la requête invisible |
| `TestBoundQueryRefusedOnAnotherProfile` | la bonne réponse à la mauvaise base |
| `TestQueryNameRejectsTraversalAndCase` | l'écriture hors du répertoire de requêtes |
| `TestNothingIsSavedWhenTheRunFailed` | du SQL jamais exécuté entré dans la bibliothèque |
| `TestSaveRefusesAVisibleName` | la sauvegarde qui en masque une autre |
| `TestSaveRefusesAWritingQuery` | l'instruction destructrice appelable par un nom court |
| `TestMissingParameterIsRefusedBeforeConnecting` | l'erreur serveur là où une erreur d'usage suffit |

S'y ajoutent les tests existants `TestBundledQueriesPassTheReadOnlyGuard` et
`TestEveryFlagIsDocumented`, qui couvrira les nouveaux drapeaux.

Contrôle sur le vrai clone, sauté si `$DB_AI_TOOLKIT_TSQL_SCRIPTS` est absent :
`TestRealCloneHasNoRejectedEntry`. Il sert à l'utilisateur après avoir posé des marqueurs,
et se lance depuis db-ai-toolkit, tsql-scripts n'ayant pas de suite de tests.

## 15. Limites connues

- `verified` atteste d'une exécution réussie, pas d'une réponse juste.
- Le registre et la couche personnelle ne sont pas synchronisés entre machines.
- Le garde-fou laisse hors catalogue 130 des 344 scripts tsql-scripts (`GO`, tables
  temporaires, `EXEC`). Ce design ne l'assouplit pas.
- La surcharge ne couvre que les `DECLARE` avec initialiseur sur une ligne. Les marqueurs
  `<…>` et les noms d'objets en dur ne sont pas réglables tant qu'on ne les a pas convertis.
- Les valeurs de `-param` arrivent en `nvarchar` ; la conversion est celle du serveur.
- Une requête de portée profil casse si le profil est renommé.
- Pas de recherche par mots-clés : l'index complet suffit jusqu'à une centaine d'entrées.
- Un clone tsql-scripts sur une branche ou un commit ancien fournit cette version-là. `sqlq`
  ne fait pas de `git pull`.
