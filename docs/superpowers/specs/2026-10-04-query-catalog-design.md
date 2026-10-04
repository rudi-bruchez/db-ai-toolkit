# Design : catalogue de requêtes `sqlq`, avec tsql-scripts comme source

Date : 2026-10-04
Version : 2.1, après la relecture du panel de la spec (`docs/reviews/2026-10-04-query-catalog-design-panel/`)
et celle du plan (`docs/reviews/2026-10-04-query-catalog-plan-panel/`).
Statut : en attente de plan d'implémentation.
Remplace : `2026-09-02-query-catalog-design.md`, jamais implémenté. Ses décisions sont
reprises ici quand elles tiennent, et modifiées là où le §14 le dit.
Prérequis : `2026-09-02-live-query-design.md`, implémenté.

## 1. Le problème

`sqlq` sait exécuter une requête, il ne sait rien retenir. Chaque question posée deux fois
fait réécrire le même SQL par le modèle : même coût, et surtout pas la même requête, donc
pas une réponse comparable d'une semaine sur l'autre.

Il existe par ailleurs un dépôt de requêtes de diagnostic déjà écrites, éprouvées en SSMS
et maintenues par l'utilisateur : `tsql-scripts` (344 fichiers `.sql`, SQL Server 2014 et
suivants, Azure SQL). L'objectif est que l'agent puisse les appeler par un nom court, au
lieu de réécrire ce qu'elles font déjà.

Ce qui est visé, dans l'ordre :

1. La stabilité : la même question donne la même requête.
2. La qualité cumulée : une requête corrigée une fois reste corrigée, et une requête
   écrite à la main par un DBA vaut mieux qu'une requête improvisée par un modèle.
3. Le coût : le SQL n'entre plus dans le contexte (§13).

L'appelant est l'agent, à travers le skill `live-query`. La sortie reste le JSON de `sqlq`.

## 2. Décisions prises au brainstorming

| Question | Décision |
|---|---|
| Qui appelle ? | L'agent, via `live-query`. |
| Où vit la référence d'un script tsql-scripts ? | Dans tsql-scripts, lu sur place dans un clone local. Pas de copie dans le plugin, pas de téléchargement. |
| Quels scripts entrent au catalogue ? | Ceux qui portent une ligne marqueur dans leur en-tête, et seulement eux. |
| Comment l'agent règle un paramètre d'un script tsql-scripts ? | Par surcharge des `DECLARE @x type = défaut` nommés dans le marqueur. Le script reste exécutable tel quel dans SSMS. |
| Périmètre de la v1 | Tout le design du 2 septembre (sources plugin et personnelle, `-list-queries`, `-saved`, `-save-query`), plus la source tsql-scripts. |
| Nom d'un script tsql-scripts | Déclaré dans le marqueur. |
| Trace d'exécution réussie | Un registre personnel par empreinte de contenu, pour les trois sources. Remplace la ligne `Verified:` écrite dans le fichier. |

Décisions ajoutées après la relecture, prises par l'agent en autonomie sur délégation de
l'utilisateur :

| Question | Décision |
|---|---|
| Un script qui rend plusieurs jeux de résultats | `sqlq` les rend tous (§5). Sans cela, une partie du corpus donnerait une réponse partielle sans le dire. |
| Les messages serveur (`PRINT`, `RAISERROR` de sévérité 10 au plus) | Capturés dans `messages`, par le même changement (§5). |
| Collision entre le canon livré et une autre source | Le livré reste valide, l'autre entrée est `rejected` (§7). |
| Valeurs de `-param` pour une surcharge | Validées contre le type déclaré et liées avec un type Go, avant toute connexion (§11). |

## 3. Ce que contient tsql-scripts

Mesuré le 2026-10-04 en passant les 344 fichiers dans le vrai garde-fou de `sqlq`
(`FindWrites`, `FindBatchSeparators`, `FindContextChanges`), et recoupé par quatre
relecteurs :

| Résultat | Fichiers |
|---|---|
| passent le garde-fou | 214 |
| refusés pour `GO` | 98 |
| `CREATE` (tables temporaires, installations) | 73 |
| `USE` | 38 |
| `EXEC` ou `EXECUTE` | 38 |
| `ALTER` | 29 |
| `INSERT` | 19 |
| `INTO` (`SELECT … INTO #t`, `FETCH … INTO`) | 14 |

Un fichier peut cumuler plusieurs motifs. Passer le garde-fou ne veut pas dire donner une
réponse utilisable : les relecteurs comptent entre 8 et 69 fichiers à plusieurs `SELECT`
parmi les 214 selon la règle de comptage, 4 qui écrivent par `PRINT`, plusieurs dont les
en-têtes n'ont pas de ligne de description, et quelques-uns que le délai par défaut de
30 secondes ne suffira pas à couvrir. Le nombre exact de scripts utiles ne se connaîtra
qu'en posant les marqueurs.

Faits qui pèsent sur le design :

- 59 noms de fichiers ne respectent pas la règle de nommage du catalogue
  (`dm_io_virtual_file_stats.sql`, `LOB-usage.sql`), et six noms normalisés existent en
  double dans le dépôt. `missing-indexes` existe trois fois : `index-management/`,
  `diagnostics/query-store/`, et la requête livrée dans le plugin.
- L'en-tête suit le plus souvent le gabarit du `CLAUDE.md` de tsql-scripts (lignes `--`
  entre deux lignes de tirets), mais 45 en-têtes sont coupés par une ligne vide, certains
  ouvrent sur une URL de provenance, et quatre n'ont pas de ligne de description : la
  première ligne utile y est la licence.
- La plupart des scripts posent eux-mêmes `SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED`,
  et huit des scripts qui passent emploient l'indice `WITH (READUNCOMMITTED)`.
- Parmi les 214, 45 lignes `DECLARE @x <type> = <valeur>;` tiennent sur une ligne avec un
  type simple (chaîne, entier, `bit`, date). Ce sont les candidates naturelles à la
  surcharge.

Ce document ne rend pas exécutables les 130 fichiers refusés. Le garde-fou reste ce qu'il
est ; un script refusé reste dans SSMS.

## 4. La sauvegarde d'une exécution

Repris du 2 septembre, et c'est ce qui tient le reste : on ne sauvegarde pas une requête,
on sauvegarde une exécution réussie.

```bash
sqlq -profile prod-erp -query "<sql>" -save-query orders-late \
     -summary "Orders past their promised date, by customer."
```

La requête tourne d'abord. Le fichier n'est écrit que si le run se termine en code 0.
Aucun fichier de la couche personnelle ne peut donc exister sans avoir tourné au moins une
fois, et l'attestation d'exécution est établie par le binaire, jamais affirmée par le modèle.

Pour les sources que `sqlq` n'écrit pas (plugin, tsql-scripts), l'équivalent est le
registre du §10 : il ne dit « vérifié » que pour un contenu qui a réellement tourné.

## 5. Jeux de résultats multiples et messages

`collect()` (`tools/cmd/sqlq/main.go`) ne garde aujourd'hui que le premier jeu de
résultats et draine les suivants ; `Result.Messages` est déclaré mais rien ne l'alimente.
Un script de tsql-scripts qui rend quatre jeux (`server-information/cores-and-numa.sql`)
donnerait donc une réponse partielle sans le dire. C'est aussi les points 1 et 2 de la
tâche Todoist « sqlq : capturer les messages serveur et rendre les jeux de résultats
multiples », qui en décrit le mécanisme.

- L'exécution passe un `*sqlexp.ReturnMessage` à `QueryContext` et lit le flux de messages
  (`github.com/golang-sql/sqlexp`, déjà présent en dépendance indirecte de go-mssqldb, à
  promouvoir en `require` direct) : `MsgNotice` alimente `messages` dans l'ordre d'arrivée,
  `MsgNextResultSet` ouvre un nouveau jeu.
- Le premier jeu non showplan reste dans `columns`, `rows`, `rowcount`, `truncated`, comme
  aujourd'hui : rien ne change pour un appelant d'une seule requête.
- Chaque jeu suivant non showplan s'ajoute à `more_results`, tableau d'objets
  `{columns, rows, rowcount, truncated, incomplete}`. `-maxrows` s'applique à chaque jeu.
- Le showplan garde son traitement actuel (`plan`).
- Une erreur SQL survenue après un premier jeu rend `error`, avec les jeux déjà lus : le
  code de sortie reste `2`. Le jeu encore ouvert quand l'erreur arrive porte
  `incomplete: true` (le pilote le termine proprement, et ses lignes passeraient sinon pour
  le jeu entier) ; le premier jeu porte le même champ au niveau de l'objet. `error` garde la
  première erreur, les suivantes vont dans `messages` sous la forme `error <numéro>: <texte>`.
  Ajouté après la relecture de la tâche 2.
- Cela vaut pour toutes les sources de SQL, `-query` et `-file` compris. La règle « une
  requête, une réponse » reste une discipline du skill, pas une impossibilité de l'outil.

Hors périmètre : le drapeau `-statistics` et l'analyse des lignes `STATISTICS IO` de la même
tâche, et le découpage des scripts à `GO`.

## 6. Répartition des responsabilités

| Ce qui doit être vrai : `sqlq` (Go, testé) | Ce qui relève du jugement : skill `live-query` |
|---|---|
| lister le catalogue, résoudre un nom | reconnaître qu'aucune requête stockée ne répond |
| valider noms, paramètres, portée, collisions | proposer une sauvegarde, et sous quel nom |
| réécrire les `DECLARE` surchargés, valider leurs valeurs | lire l'en-tête d'une requête avant de la lancer |
| refuser un script marqué mais inutilisable | présenter le résultat, dire ce qui n'est pas vérifié |
| tenir le registre des exécutions | demander un oui avant une requête `heavy` en prod |
| détecter `dirty_reads` | ne pas tirer de conclusion de justesse d'un résultat `dirty_reads` |

Aucune règle de sécurité, de portée ou de nommage ne dépend du respect d'une consigne en
langage naturel.

## 7. Les sources

| Source | Emplacement | Nom | Portée |
|---|---|---|---|
| `bundled` | `<plugin>/skills/live-query/queries/*.sql` | nom du fichier | `generic` |
| `personal` | `~/.config/db-ai-toolkit/queries/_generic/*.sql` | nom du fichier | `generic` |
| `personal` | `~/.config/db-ai-toolkit/queries/profiles/<profil>/*.sql` | nom du fichier | `<profil>` |
| `tsql-scripts` | clone local, parcours récursif, `.git/` exclu | `name=` du marqueur | `generic` |

### Localisation

- `bundled` : relativement à l'exécutable, `<dir(os.Executable())>/../skills/live-query/queries/`.
  `-queries <dir>` force le chemin (tests, développement hors plugin). Répertoire introuvable :
  message `bundled queries not found`, pour qu'un binaire construit hors du plugin ne fasse
  pas croire à un canon vide.
- `personal` : `~/.config/db-ai-toolkit/queries/`. Absent : la source est vide.
- `tsql-scripts` : `-tsql-scripts <dir>`, sinon `$DB_AI_TOOLKIT_TSQL_SCRIPTS`. Aucun défaut
  deviné. Non configuré ou introuvable : la source est vide, et `-list-queries` ajoute un
  message dans `messages` (`tsql-scripts source not configured`, ou `not found`) pour que
  l'agent ne conclue pas que le dépôt ne contient rien. Pas de fichier de configuration
  propre à `sqlq` en v1.

### Lecture des répertoires

Règles ajoutées après la revue de code de la tâche 7.

- Un répertoire `bundled` ou `personal` se lit par `os.ReadDir`, jamais par un motif
  `*.sql` : un `[` dans le chemin du plugin faisait d'un motif glob une expression qui ne
  trouvait rien, et le canon disparaissait sans un mot. L'extension `.sql` se reconnaît sans
  tenir compte de la casse, dans les trois sources.
- Un répertoire qui existe mais ne se lit pas donne un message (`bundled queries directory
  unreadable: .`, `tsql-scripts directory unreadable: <chemin relatif>`), jamais le texte
  de l'erreur système, qui porte le chemin absolu. Un répertoire `personal` absent reste
  une source vide, sans message.
- Un `.sql` qui est un lien symbolique est `rejected` (`symbolic link`) et n'est pas suivi :
  sa cible peut être hors de la source, et c'est elle qu'on exécuterait. Un `.sql` qui n'est
  pas un fichier ordinaire est `rejected` (`not a regular file`).
- Chaque fichier est lu une seule fois, et au plus 1 Mio. Un fichier plus gros est
  `rejected` (`file too large`) ; dans tsql-scripts, seulement si ses 64 premiers Kio
  contiennent une tentative de marqueur, sinon il reste ignoré comme tout script non marqué.
- Un fichier illisible est `rejected` (`unreadable`), dans toutes les sources : on ne peut
  pas savoir s'il est marqué, et l'ignorer le ferait disparaître.
- Un fichier qui commence par un BOM UTF-16 (`FF FE` ou `FE FF`) ou contient un octet nul est
  `rejected` (`not UTF-8 text`), dans toutes les sources, qu'il soit marqué ou non : décodé
  comme UTF-8, il ne présente aucune ligne lisible, et un script marqué enregistré en UTF-16
  par SSMS disparaissait. Le corpus réel n'en contient aucun.

### Répertoire d'un profil

Les noms de profil générés par `registered-servers` contiennent des `/` (groupes SSMS). Le
répertoire personnel d'un profil est `profiles/` suivi du nom du profil passé en
minuscules, chaque `/` ouvrant un sous-répertoire. Chaque segment doit respecter
`^[a-z0-9][a-z0-9._-]*$` et ne pas valoir `.` ni `..` ; sinon la couche personnelle de ce
profil est désactivée, avec un message. Deux profils dont les noms ne diffèrent que par la
casse désactivent leur couche personnelle tous les deux : sur un système de fichiers
insensible à la casse, ils partageraient un répertoire et donc une portée. `_generic` vit
hors de `profiles/` et ne peut pas être atteint par un nom de profil.

### Portée

- Une requête de portée `<profil>` n'est exécutable que sur ce profil. Ailleurs : refus,
  code 1, message nommant le fichier et sa portée.
- `generic` : exécutable partout. C'est la promesse qu'elle ne lit que des vues système et
  des DMV.
- Élargir une portée est un geste humain : déplacer le fichier dans `_generic/`, ou le
  commiter dans le plugin ou dans tsql-scripts. Pas de drapeau `-widen` (question 3 du
  2 septembre, tranchée).
- `sqlq` n'écrit jamais dans le plugin ni dans tsql-scripts. `-save-query` écrit toujours
  sous `profiles/<profil>/`.

### Vue du catalogue et collisions

`-list-queries` accepte `-profile`, facultatif. La vue est :

- sans `-profile` : `bundled`, `personal/_generic`, `tsql-scripts` ;
- avec `-profile P` : la même chose, plus `profiles/P`.

Les collisions de noms se jugent dans la vue. `-saved` sur le profil P juge dans la vue de P.

- Le canon livré l'emporte : un nom présent dans `bundled` et dans une autre source laisse
  l'entrée livrée valide, et rend l'autre `rejected` (`name "x" is taken by bundled`). Le
  skill dépend des requêtes livrées ; une note posée par erreur dans un autre dépôt ne doit
  pas les désactiver. Ce n'est pas un masquage silencieux : l'entrée écartée est listée,
  avec sa raison.
- Entre deux sources non livrées, ou deux scripts tsql-scripts marqués du même nom, toutes
  les entrées du nom sont `rejected`.
- `-save-query` refuse un nom déjà présent dans la vue du profil, quelle que soit sa source.

Le skill appelle `-list-queries -profile <profil>` une fois le profil choisi : c'est la
seule façon de voir ses requêtes sauvées.

### Nommage

`^[a-z][a-z0-9-]{1,48}$`, validé avant toute construction de chemin.
`-save-query` écrit à un chemin dérivé d'un nom proposé par un modèle :
`../../../.ssh/authorized_keys` doit être refusé par une règle.

## 8. Format des en-têtes

Avant toute analyse, un BOM UTF-8 en tête de fichier est retiré. Les fins de ligne CRLF sont
acceptées et conservées. Un CR isolé (fins de ligne du Mac classique) coupe aussi une ligne
d'en-tête, pour que le fichier soit vu ; mais l'entrée est `rejected` (`lone CR line
endings`), parce que le garde-fou et l'analyse lexicale ne finissent une ligne que sur LF :
pour eux, `-- x<CR>DELETE …` serait un seul commentaire. L'empreinte reste calculée sur les
octets lus (§10), la ligne marqueur retirée selon le même découpage.

### Sources `bundled` et `personal` : bloc `/* … */`

Format des requêtes déjà livrées, inchangé :

```sql
/*  Largest user tables in the current database, by space reserved.

    Parameters: none.

    Why this shape rather than the obvious one: ...
*/
SELECT ...
```

1. Le fichier ouvre (après d'éventuelles lignes vides) par un bloc `/* … */`. Sinon :
   `rejected` (`no header comment`).
2. Le premier paragraphe du bloc (ses lignes non vides consécutives, jointes par une
   espace) est le résumé. `missing-indexes.sql` ouvre sur une phrase de deux lignes, qu'une
   règle « première ligne » couperait au milieu.
3. Les paramètres se déduisent du SQL (§9). Une ligne `Parameter:` ou `Parameters:` est
   facultative ; quand elle est présente, elle doit nommer exactement les paramètres
   déduits, sinon `rejected`. Les fichiers livrés qui disent `No parameter.` restent valides.
4. Une ligne `Heavy: yes` dans le bloc pose `heavy` (§12). Une ligne qui commence par
   `Heavy:` doit dire exactement `yes` ou `no` (casse et espaces autour indifférents) ;
   sinon `rejected` : `Heavy: yes.` ne doit pas passer pour l'absence de la ligne (revue
   de code de la tâche 7).
5. Le reste est de la prose pour le lecteur humain.

### Source `tsql-scripts` : en-tête `--` et ligne marqueur

Exemple, sur un fichier réel (`diagnostics/sessions/sessions-from-host.sql`, dont seule la
ligne marqueur serait ajoutée) :

```sql
-----------------------------------------------------------------
-- Get sessions from a specific host
-- sqlq: name=sessions-from-host params=hostname
--
-- rudi@babaluga.com, go ahead license
-----------------------------------------------------------------

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

DECLARE @hostname sysname = N'%';
```

- L'en-tête est la suite de lignes qui ouvre le fichier et dont chacune est vide ou commence
  par `--` (espaces en tête permis). Il s'arrête à la première autre ligne.
- Une tentative de marqueur est toute ligne du fichier qui commence par `--`, précédé et
  suivi d'espaces quelconques (Unicode compris, l'espace insécable par exemple), puis le mot
  `sqlq` (frontière de mot), deux-points ou non : `(?i)^[\s\p{Zs}]*--[\s\p{Zs}]*sqlq\b`.
  Si un fichier n'en a aucune, il n'entre pas au catalogue et n'apparaît pas du tout. S'il
  en a une dans l'en-tête, c'est le marqueur, qui doit être bien formé :
  `(?i)^[ \t]*--[ \t]*sqlq[ \t]*:`, sinon `rejected` (`malformed sqlq marker at line N`).
  Toute autre configuration rend l'entrée `rejected` : marqueur hors de l'en-tête (`marker
  outside the header`), ou plus d'un marqueur. Un marqueur mal placé ou mal orthographié se
  voit donc, au lieu de faire disparaître le script. La tentative a été élargie après la
  revue de code de la tâche 7 : `-- sqlq name=x` (sans deux-points) et `--` suivi d'une
  espace insécable faisaient disparaître le script. Sur le corpus réel, aucun fichier non
  marqué ne devient une entrée rejetée (un `TSQLQuery` dans une chaîne reste ignoré).
- Clés, séparées par des espaces : `name=` (obligatoire), `params=` (liste séparée par des
  virgules, facultative, sans doublon), `heavy` (sans valeur, facultative). Une clé inconnue
  rend l'entrée `rejected` : une faute de frappe dans `heavy` ne doit pas passer pour son
  absence. Un paramètre listé deux fois aussi. Un nom de `params=` doit être fait de lettres
  ASCII, de chiffres et de `_`, sinon `rejected` (`invalid parameter name at position N in
  params=`).
- Le résumé est la ligne de commentaire de l'en-tête la plus proche au-dessus du marqueur
  dont le contenu, après `--`, est non vide, n'est pas fait que de tirets et ne commence pas
  par `http://` ou `https://`. Il n'y en a pas : `rejected` (`no summary above the marker`).
  La règle place le résumé sous le contrôle de qui pose le marqueur : le poser juste sous la
  ligne de description. Une licence placée sous le marqueur ne peut pas devenir le résumé.

### Le champ `rejected`

Un script marqué (tsql-scripts) ou un fichier d'une source `bundled` ou `personal` qui ne
peut pas tourner apparaît quand même dans `-list-queries`, avec la raison :

- refusé par le garde-fou : `batch separator GO at line 9`, `write keyword EXEC at line 12`,
  `USE at line 1` ;
- nom invalide, ou nom en collision (§7) ;
- en-tête illisible, marqueur hors de l'en-tête ou en double, clé inconnue, résumé absent ;
- l'un des refus de surcharge du §11.

Une raison nomme un mot-clé, un nom de paramètre et une ligne, jamais un extrait du texte :
`-list-queries` part chez le fournisseur du modèle à chaque session (§12, « Ce qui est
publié »). Une raison ne cite jamais un jeton qui a échoué à la validation : elle donne sa
position (`unknown marker key at position 3`, `invalid parameter name at position 1 in
params=`, `invalid name`). Un nom de paramètre qui a passé la validation peut être cité.
Règle précisée après la revue de code de la tâche 7, où `name=bk C:\Users\…\secret.xel`
publiait le chemin dans la raison.

`-saved` sur une entrée `rejected` : code 1, avec la même raison.

## 9. Paramètres des sources `bundled` et `personal`

Repris du 2 septembre, précisé. Un paramètre est un `@x` référencé et non déclaré, calculé
sur le texte passé par `Sanitize`, comparé sans tenir compte de la casse.

- Les variables globales (`@@VERSION`) sont exclues.
- Une variable est déclarée si elle est la cible d'un `DECLARE` : le premier `@x` qui suit
  le mot `DECLARE`, puis le premier `@x` qui suit chaque virgule de profondeur de
  parenthèses nulle de la même instruction. Dans `DECLARE @local INT = @cutoff;`, seule
  `@local` est déclarée ; `@cutoff` est un paramètre.
- Un paramètre requis non passé : refus avant connexion (`parameter "cutoff" required`).
  Un `-param` qui ne correspond à aucun paramètre : refus aussi.
- Les valeurs sont liées en `nvarchar`, comme aujourd'hui par `namedArgs`.

## 10. Le registre des exécutions

`~/.config/db-ai-toolkit/verified.json`, écrit par `sqlq` seul.

```json
{"9f2c…":{"date":"2026-10-04","profile":"prod-erp"}}
```

- Clé : le SHA-256 du contenu. Pour tsql-scripts, le contenu est le fichier privé de sa
  ligne marqueur, si bien que renommer l'entrée, ajouter `heavy` ou changer `params=` ne
  fait pas perdre la vérification d'un SQL inchangé. Pour les autres sources, le fichier
  entier. Ni le nom ni le chemin n'entrent dans la clé : déplacer ou renommer un fichier
  garde sa vérification, et deux fichiers au contenu identique la partagent, ce qui est
  juste puisque c'est le même SQL.
- L'empreinte est calculée sur les octets lus pour l'exécution, pas sur une relecture du
  fichier après le run : un fichier modifié pendant une longue requête ne peut pas recevoir
  la vérification d'un SQL qui n'a pas tourné.
- Après chaque run `-saved` terminé en code 0, et après chaque `-save-query` réussi,
  l'entrée est écrite ou remplacée : c'est la dernière vérification (question 2 du
  2 septembre, tranchée).
- `verified` veut dire : ce contenu a été compilé et exécuté avec succès au moins une fois
  sur cette machine. Une surcharge ne change que la valeur d'initialisation d'une variable,
  pas la structure du batch ; une vérification faite avec des valeurs passées couvre donc
  aussi le défaut, et inversement. Elle n'atteste pas d'une réponse juste.
- Le catalogue affiche `verified` seulement si l'empreinte actuelle est dans le registre.
  Un fichier modifié redevient `null`.
- Le registre enregistre le nom du profil, pas le serveur ni la base, comme `-list-profiles`.
- Écriture : relire le registre, fusionner l'entrée, écrire un fichier temporaire dans le
  même répertoire, `rename`. Deux runs qui finissent au même instant peuvent encore perdre
  l'une des deux entrées : la conséquence est une requête affichée comme jamais vérifiée,
  donc du côté prudent, et cela ne justifie pas un verrou en v1.
- Registre illisible ou JSON invalide : traité comme vide, un message dans `messages`,
  réécrit en entier au prochain succès.
- Le registre est propre à la machine.

## 11. La surcharge des `DECLARE`

Ne concerne que la source `tsql-scripts`.

### Le mécanisme

Pour `sqlq -profile p -saved sessions-from-host -param hostname=SRV-APP01` :

```sql
-- dans le fichier
DECLARE @hostname sysname = N'%';
-- texte envoyé au serveur
DECLARE @hostname sysname = @sqlq_hostname;
```

- La valeur est validée contre le type déclaré, convertie en valeur Go typée, et liée par
  `sql.Named("sqlq_hostname", valeur)`. Jamais de concaténation.
- Un paramètre non passé garde le défaut du fichier ; sa ligne n'est pas touchée.
- Le préfixe `sqlq_` évite le conflit avec la variable elle-même. Un script qui contient
  déjà un identifiant commençant par `@sqlq_` est `rejected`.
- Les noms de paramètre se comparent sans tenir compte de la casse, comme SQL Server compare
  les noms de variables.

### La ligne de déclaration

Une surcharge n'est acceptée que sur une ligne qui, dans le texte passé par `Sanitize`
(commentaires et littéraux blanchis, positions et sauts de ligne conservés), est
entièrement de la forme :

```
DECLARE @p <type> = <expression> ;
```

- rien d'autre sur la ligne, avant ni après, hors espaces ; un commentaire de fin de ligne
  est déjà blanchi et ne compte pas ;
- le `;` final est obligatoire et sur la même ligne ;
- les parenthèses de `<expression>` s'équilibrent sans jamais passer sous zéro
  (`(1 + 2))` est refusé) ;
- `<expression>` est non vide dans le texte d'origine (dans le texte nettoyé, `''` n'est
  plus que des espaces), ne contient ni `;` ni virgule de profondeur de parenthèses
  nulle (ce qui exclut `DECLARE @a int = 1, @b int = 2;`), et ses parenthèses sont
  équilibrées ;
- `<type>` est l'un des types de la table ci-dessous, écrit nu ; `DECLARE @p AS <type> =`
  est la même déclaration et s'accepte. Un type entre crochets (`[int]`) ou entre
  guillemets est blanchi par `Sanitize` comme tout identifiant délimité : le refus le dit
  plutôt que de parler d'un type `=` ;
- `<expression>` forme une seule expression à sa profondeur de parenthèses : opérandes
  (littéral, variable, nom, groupe entre parenthèses) et opérateurs binaires (`+ - * / % &
  | ^`) alternent, avec `+ - ~` en unaires. Un nom collé à un groupe est un appel de
  fonction ; un nombre collé à un groupe ne l'est pas. `CASE` et `COLLATE` à cette
  profondeur sont refusés plutôt qu'analysés. Sans cette règle, `DECLARE @p int = 1 GOTO x;`,
  `= 1 (SELECT 2 AS two);`, `= 1 CHECKPOINT;`, `= 1 WAITFOR DELAY '00:00:01';`, `COMMIT`,
  `OPEN c` ou `BREAK` passaient, et la réécriture effaçait l'instruction qui suit le `1` :
  une liste de mots qui commencent une instruction ne sera jamais complète, la structure
  l'est ;
- aucun `IF`, `ELSE`, `WHILE`, `BEGIN`, `GOTO` ni étiquette (`x:`) ne précède la déclaration
  dans le texte nettoyé (un `ELSE` dans `CASE … END` ne compte pas). Après `IF 1 = 0`, la
  ligne `DECLARE @p int = 5;` ne s'exécute pas, `@p` existe et vaut `NULL`, quelle que soit
  la valeur liée.

La portion remplacée est `<expression>`, aux positions du texte nettoyé reportées sur le
texte d'origine. `Sanitize` travaille en runes, la réécriture aussi. Les remplacements
s'appliquent de la position la plus haute à la plus basse, quel que soit l'ordre de
`params=`, pour qu'un remplacement ne décale jamais le suivant.

Ces conditions écartent les cas trouvés par les relecteurs : un initialiseur sur plusieurs
lignes (`CASE` sur deux lignes, ou `N'%'` suivi d'une ligne `+ N'Orders%';`), une instruction
qui suit la déclaration sur la même ligne et qui aurait été effacée, un `;` contenu dans un
littéral.

### Types acceptés et validation des valeurs

Avant toute connexion, chaque valeur passée est vérifiée et convertie. Une valeur qui ne
convient pas : code 1, message nommant le paramètre et son type.

| Type déclaré | Valeur acceptée | Liée comme |
|---|---|---|
| `nvarchar(n)`, `nchar(n)`, `sysname` (n = 128) | au plus n unités UTF-16 (un emoji en compte deux) | `string` |
| `nvarchar(max)` | toute chaîne | `string` |
| `varchar(n)`, `char(n)` | au plus n caractères, ASCII seulement | `string` |
| `varchar(max)` | ASCII seulement | `string` |
| `tinyint`, `smallint`, `int`, `bigint` | entier décimal dans l'intervalle du type | `int64` |
| `bit` | `0`, `1`, `true`, `false` | `bool` |
| `date` | `AAAA-MM-JJ` | date civile du driver |
| `datetime`, `datetime2(n)` | `AAAA-MM-JJ`, `AAAA-MM-JJTHH:MM[:SS]` | date et heure civiles du driver, sans fuseau |
| `smalldatetime` | `AAAA-MM-JJ`, `AAAA-MM-JJTHH:MM` (pas de secondes : le type arrondit à la minute) | date et heure civiles du driver |

- Les longueurs se vérifient parce que SQL Server tronque sans erreur une chaîne trop
  longue affectée à une variable : `COLUMNSTORE_ARCHIVE` dans `@compressionType varchar(10)`
  tournerait comme `COLUMNSTOR`.
- `varchar` refuse les caractères non ASCII, que la page de code du serveur pourrait
  remplacer par `?` sans erreur.
- Les dates sont liées avec un type temporel du driver plutôt qu'en chaîne, pour ne pas
  dépendre du `DATEFORMAT` de la session : sous un login français, `2026-10-04` en chaîne
  vers `datetime` peut se lire en `AAAA-JJ-MM`.
- Aucune fraction de seconde, pour aucun type date et heure : l'analyse de Go accepte
  `23:59:59.999` même quand le format ne la nomme pas, et le serveur l'arrondit
  (`2020-12-31T23:59:59.999` en `datetime` devient le 1er janvier 2021).
- L'année et la date restent dans l'intervalle du type, que le driver ou le serveur
  ramènerait sans erreur (l'année `0000` devient `0001`) : au moins `0001` pour `date` et
  `datetime2`, au moins `1753-01-01` pour `datetime`, de `1900-01-01` à `2079-06-06` pour
  `smalldatetime`.
- Tout autre type (`decimal`, `float`, `uniqueidentifier`, `xml`…) : la ligne n'est pas
  surchargeable, l'entrée est `rejected` (`parameter "p": type decimal not supported`).

### Refus au moment de construire le catalogue

| Cas | Raison du refus |
|---|---|
| paramètre du marqueur sans ligne de déclaration conforme | l'en-tête promet un réglage qui n'existe pas, ou que la réécriture casserait |
| `@p` déclaré plus d'une fois | on ne sait pas laquelle réécrire |
| type non accepté | la valeur ne pourrait pas être validée |
| `@p` en position d'affectation ailleurs dans le script | la valeur passée serait modifiée sans bruit |

Le dernier cas est celui où tout semble avoir marché alors que la réponse est fausse : un
résultat pour `SRV-APP01` calculé sur une autre valeur. La position d'affectation se juge
sur les jetons du texte nettoyé, pour chaque occurrence de `@p` hors de sa déclaration :

- `@p` suivi d'un opérateur composé (`+=`, `-=`, `*=`, `/=`, `%=`, `&=`, `|=`, `^=`), de
  `OUT` ou de `OUTPUT` : affectation, partout ;
- `@p` suivi de `=`, à toute profondeur de parenthèses, quand le jeton qui précède `@p`
  est `(` ou l'un de `WHERE`, `AND`, `OR`, `NOT`, `ON`, `WHEN`, `HAVING`, `IF`, `WHILE`,
  `THEN`, `ELSE` : comparaison (`IIF(@online = 1, …)`, `WHERE @p = 1`), acceptée ;
- `@p` suivi de `=` dans tout autre contexte (`SET @p =`, `SELECT @p =`,
  `SELECT TOP (1) @p =`, `SELECT @x = 1, @p = 2`, `(SELECT @p = 2)`,
  `SELECT 1 UNION (SELECT @p = 2)`) : affectation. La profondeur ne prouve rien, une
  affectation entre parenthèses affecte ;
- un mot qui commence par un chiffre ou `$` et se termine par `@p` après son premier `@`
  (`SELECT TOP 1@p = …`) : `Lex` y voit un seul mot, SQL Server le nombre `1` puis `@p`.
  Refusé partout. `x@p` est un identifiant pour les deux et reste accepté.

Le nom de variable lui-même doit être comparable sans le serveur. Sous une collation
insensible à la largeur et à la casse, `SET @ｐ = 7` (p pleine chasse) modifie `@p`, et
`SET @strasse = 2` modifie `@straße`. Une surcharge est donc refusée quand le nom du
paramètre n'est pas fait de lettres ASCII, de chiffres et de `_`, ou quand un jeton du
fichier qui contient `@` porte un caractère non ASCII.

La règle penche du côté du refus : un contexte non prévu compte comme une affectation, et le
script est `rejected`, ce qui se voit. Les autres manières d'affecter une variable
(`FETCH … INTO`, `EXEC … OUTPUT`, `UPDATE … SET @p =`) sont déjà refusées par le garde-fou
(`INTO`, `EXEC`, `UPDATE`).

### Refus à l'exécution, avant toute connexion (code 1)

- Un `-param` absent du marqueur. Sans ce refus, `-param hostnme=SRV-APP01` (faute de
  frappe) ferait tourner le défaut `'%'` sur toutes les sessions.
- Une valeur qui ne passe pas la validation de son type.

Le garde-fou s'applique au texte réécrit, en filet, en plus du texte d'origine.

Les règles sur l'expression unique, la déclaration conditionnelle, l'affectation entre
parenthèses, la variable collée à un nombre, les noms non ASCII, les fractions de seconde
et les bornes de dates viennent de la relecture du code de la tâche 5 : chacun de ces cas a
été prouvé sur un vrai SQL Server, où la valeur liée était modifiée ou perdue sans erreur.

## 12. Contrat CLI

Trois sources de SQL, mutuellement exclusives : `-query`, `-file`, `-saved`.

| Drapeau | Effet |
|---|---|
| `-list-queries` | imprime le catalogue en JSON et sort. `-profile` facultatif (§7). |
| `-saved <nom>` | exécute l'entrée du catalogue de ce nom (question 1 du 2 septembre : le nom est gardé) |
| `-save-query <nom>` | après un run `-query` ou `-file` réussi, écrit la requête sous `profiles/<profil>/` |
| `-summary <texte>` | résumé de l'en-tête écrit par `-save-query`, obligatoire avec lui |
| `-queries <dir>` | force le répertoire de la source `bundled` |
| `-tsql-scripts <dir>` | chemin du clone tsql-scripts |

### `-save-query`

- Refuse toute requête que `FindWrites` signale, y compris sur un profil `readwrite` et y
  compris avec `-allow-write`. Le catalogue ne contient que des lectures.
- `-summary` tient sur une ligne, et ne contient ni `/*` ni `*/` : sinon refus, code 1,
  avant même l'exécution. Un `/*` ferait du fichier un seul commentaire non terminé, qu'on
  croirait vérifié et qui ne rendrait rien ; un `*/` injecterait du SQL.
- Le fichier écrit est un bloc `/* … */` portant le résumé et une ligne `Parameters:`
  générée par `sqlq` à partir des paramètres déduits (§9), suivi des octets exacts du SQL
  exécuté.
- Avant de rendre la main, `sqlq` relit le fichier écrit avec l'analyseur du catalogue. S'il
  n'en sort pas une entrée valide du même nom, le fichier est supprimé et le run échoue en
  code 1 : aucune entrée `rejected` ne peut naître d'une sauvegarde.

### Catalogue

Exemple sur des fichiers réels, le premier avec le marqueur proposé au §8 :

```json
{"queries":[
  {"name":"tables-largest","summary":"Largest user tables in the current database, by space reserved.",
   "params":[],"scope":"generic","source":"bundled","path":"tables-largest.sql",
   "verified":null,"dirty_reads":false,"heavy":false},
  {"name":"sessions-from-host","summary":"Get sessions from a specific host",
   "params":[{"name":"hostname","type":"sysname"}],
   "scope":"generic","source":"tsql-scripts","path":"diagnostics/sessions/sessions-from-host.sql",
   "verified":null,"dirty_reads":true,"heavy":false},
  {"name":"waits-statistics","source":"tsql-scripts",
   "path":"diagnostics/wait-statistics/waits-statistics.sql",
   "rejected":"batch separator GO at line 9"}
],
 "messages":[]}
```

- `params` d'une entrée tsql-scripts donne le nom et le type ; d'une entrée `bundled` ou
  `personal`, le nom seul (`{"name":"cutoff"}`). Le défaut n'est pas publié : dans le
  corpus, des défauts portent un chemin local (`N'C:\temp\blocked_processes*.xel'`) ou du
  SQL (`DATEADD(hour, -@LookbackHours, GETDATE())`). L'agent le lit dans l'en-tête du fichier,
  qu'il lit de toute façon avant la première exécution.
- `path` est relatif à la racine de sa source. Le chemin absolu du clone ou du répertoire
  personnel n'est jamais imprimé.
- `dirty_reads` : `true` si les jetons du texte nettoyé contiennent `NOLOCK`,
  `READUNCOMMITTED`, ou `READ` immédiatement suivi de `UNCOMMITTED` (quel que soit
  l'espacement, saut de ligne compris). Calculé, donc impossible à oublier.
- `heavy` : déclaré dans le marqueur (tsql-scripts) ou par `Heavy: yes` (`bundled`,
  `personal`). Relève du jugement : une analyse lexicale ne sait pas ce que coûte un scan.

### Résultat d'un run `-saved`

Le JSON habituel gagne un champ `saved`, pour que l'agent ne puisse pas se tromper sur ce
qui a tourné :

```json
"saved":{"name":"sessions-from-host","source":"tsql-scripts",
         "path":"diagnostics/sessions/sessions-from-host.sql",
         "params":{"hostname":"SRV-APP01"},"defaults":[],"verified":null}
```

`params` ne contient que les valeurs passées, `defaults` les noms des paramètres laissés à
leur défaut. `verified` y est l'état avant le run.

Codes de sortie inchangés : `3` reste réservé au garde-fou. Les nouveaux refus (nom
inconnu, entrée `rejected`, portée incompatible, paramètre manquant, inconnu ou invalide,
collision à la sauvegarde, résumé refusé) sont des erreurs d'usage : code 1, JSON habituel
sur stdout.

### Ce qui est publié

`-list-queries` est lu à chaque session et part chez le fournisseur du modèle, comme
`-list-profiles`. Ce qu'il imprime est donc limité par construction : pas de chemin absolu,
pas de serveur ni de base dans `verified`, pas d'extrait de SQL dans `rejected`, pas de
valeur par défaut de paramètre, pas de texte d'erreur système (qui porte des chemins) dans
`messages`, et les
répertoires de profil absents de la vue sans `-profile`. Une entrée tsql-scripts `rejected` dont
le nom est invalide ne publie pas ce nom (qui peut être un chemin ou un serveur) : elle prend
le nom du fichier sans extension s'il est un nom valide, sinon un nom vide ; il en va de même
des entrées rejetées avant toute lecture (`unreadable`, `symbolic link`, `file too large`,
`not UTF-8 text`). Ce nom n'a pas été revendiqué par l'auteur et ne compte pas dans les
collisions (revue de code de la tâche 7). Restent les résumés et les noms de
fichiers, écrits par l'auteur : la règle pour lui, consignée dans le `CLAUDE.md` de
tsql-scripts et dans le skill, est de ne mettre ni client, ni hôte, ni base dans le résumé
ou le nom d'un fichier de portée `generic`.

## 13. Le coût en tokens

| | Coût |
|---|---|
| Catalogue de 40 entrées, une fois par session | ~1 500 tokens |
| Réécrire une requête comme `tables-largest` | ~900 tokens de sortie, plus 1 à 3 allers-retours d'erreur : 2 000 à 4 000 |
| Rejouer par son nom | ~15 tokens, plus la lecture de l'en-tête à la première exécution de la session |

Le poste principal reste les lignes de résultat. Les chiffres du catalogue sont des
estimations à remplacer par une mesure sur le premier lot de marqueurs.

## 14. Changements au skill `live-query`

- La table de décision disparaît. Règle unique : appeler `-list-queries -profile <profil>`
  avant d'écrire du SQL, et préférer une entrée du catalogue à une requête ad hoc.
- Les avertissements propres à une requête, qui vivent aujourd'hui dans cette table
  (« lire l'en-tête d'abord », `-maxrows 70` pour `missing-indexes`), passent dans l'en-tête
  du fichier. Le skill dit : avant la première exécution d'une requête dans une session,
  lire son en-tête. Pour une entrée tsql-scripts, le fichier est sous le clone, à `path`.
- Une entrée du catalogue se lance toujours avec `-maxrows`. Le garde de borne `TOP (n)`
  d'`AGENTS.md` vise le SQL qu'écrit l'agent ; pour une requête stockée, c'est son auteur
  qui répond de la taille du résultat, et `-maxrows` reste le filet.
- `verified: null` : le dire avant de lancer (« jamais exécutée avec `sqlq` ici »).
- `rejected` : ne pas lancer, ne pas contourner en réécrivant le script en ad hoc, rapporter
  la raison. La correction revient à l'utilisateur, dans le fichier source.
- `dirty_reads: true` : ne jamais s'appuyer sur le résultat pour répondre à une question sur
  la justesse des données.
- `heavy: true` sur un profil prod : annoncer le coût et attendre un oui, même si la session
  a déjà été confirmée.
- `more_results` : un script peut rendre plusieurs jeux, tous à lire avant de conclure.
- Après une requête ad hoc réussie et utile : proposer la sauvegarde en une ligne, avec un
  nom et un résumé. Jamais sans accord explicite.
- La règle prod (« nommer le profil et attendre un oui ») est inchangée.
- La limite « PRINT et RAISERROR non capturés » des « Known limits » disparaît.

Écarts au design du 2 septembre :

- la ligne `Verified:` dans le fichier est remplacée par le registre (§10) ;
- `verified` n'imprime plus le serveur ni la base ;
- la ligne `Parameters:` devient facultative et, quand elle existe, contrôlée (§8) ;
- la déclaration de variable est définie précisément (§9) ;
- `-summary` est ajouté, et `-save-query` relit le fichier qu'il écrit (§12) ;
- la couche personnelle passe sous `profiles/<profil>/` ;
- `-list-queries` accepte `-profile`, et le livré l'emporte sur une collision (§7) ;
- un fichier `bundled` ou `personal` à l'en-tête invalide est `rejected` au lieu d'être
  absent ;
- `Heavy: yes`, `dirty_reads`, `more_results` et `messages` sont nouveaux.

## 15. Côté tsql-scripts

- Documenter la ligne marqueur dans son `CLAUDE.md`, à côté du gabarit d'en-tête : clés,
  placement juste sous la ligne de description, règle de nommage, forme exigée des
  `DECLARE` surchargés et types acceptés, règle de publication du §12, et le fait qu'un
  script à `GO`, `USE` ou `EXEC` ne sera pas accepté.
- Poser un premier lot de marqueurs, choisi par l'utilisateur, et le vérifier par
  `TestRealCloneHasNoRejectedEntry` (§16) et un run réel de chacun.
- Convertir à la main en `DECLARE` avec défaut les scripts à marqueur `<…>` qu'on veut au
  catalogue. La conversion sert aussi dans SSMS.

Aucune modification de tsql-scripts n'est nécessaire pour que `sqlq` fonctionne : sans
marqueur, la source est simplement vide.

## 16. Tests

Sur un mini-dépôt dans `testdata/`, jamais sur le vrai clone. Chaque test supprime une
classe de défaut.

| Test | Classe supprimée |
|---|---|
| `TestMarkedScriptRefusedByGuardIsListedAsRejected` | le marqueur posé qui fait disparaître le script sans explication |
| `TestUnmarkedScriptIsAbsent` | l'entrée accidentelle |
| `TestMarkerOutsideHeaderIsRejected` | le marqueur mal placé qui fait disparaître le script |
| `TestMarkerAttemptIsCaseAndSpaceInsensitive` | `--SQLQ:` pris pour une absence |
| `TestBOMIsStripped` | l'en-tête invisible derrière un BOM |
| `TestUnknownMarkerKeyIsRejected` | `heavy` mal orthographié pris pour son absence |
| `TestSummaryIsNearestLineAboveMarker` | la licence ou une URL prise pour le résumé |
| `TestBundledWinsCollisionOthersRejected` | la requête du canon désactivée par une note d'ailleurs |
| `TestCollisionBetweenNonBundledRejectsAll` | le masquage d'une source par une autre |
| `TestProfileViewListsOnlyThatProfile` | le répertoire d'un autre profil publié |
| `TestProfileDirRejectsDotDotAndCaseTwins` | l'écriture hors des requêtes, la portée partagée par deux profils |
| `TestDeclareOverrideBindsNotConcatenates` | l'injection par `-param` |
| `TestDeclareOverrideKeepsOffsetsWithAccents` | la réécriture décalée par un caractère multi-octet |
| `TestDeclareLineMustStandAlone` | l'instruction effacée sur la même ligne, l'initialiseur sur deux lignes |
| `TestCompoundAssignmentIsRejected` | `SET @p += 1` qui modifie la valeur passée |
| `TestSelectAssignmentIsRejected` | `SELECT TOP (1) @p =`, `SELECT @x = 1, @p = 2` |
| `TestComparisonIsAccepted` | `IIF(@p = 1, …)` et `WHERE @p = x` refusés à tort |
| `TestParamValueValidatedByType` | la troncature silencieuse, le `bit` à 2, la date lue selon `DATEFORMAT` |
| `TestUnsupportedTypeIsRejected` | la valeur d'un `decimal` arrondie sans bruit |
| `TestUnknownParamRefusedBeforeConnecting` | la faute de frappe qui lance le défaut `'%'` |
| `TestParameterNamesAreCaseInsensitive` | `@HostName` déclaré, `hostname` passé |
| `TestVerifiedDropsWhenSQLChanges` | la vérification qui survit à une modification |
| `TestVerifiedSurvivesRenameAndMarkerEdit` | la vérification perdue par un renommage |
| `TestVerifiedHashesBytesThatRan` | la vérification d'un fichier modifié pendant le run |
| `TestRegistryCorruptIsTreatedAsEmpty` | le catalogue rendu inutilisable par un fichier abîmé |
| `TestDirtyReadsDetectedOnTokens` | `READUNCOMMITTED`, `READ` et `UNCOMMITTED` sur deux lignes |
| `TestListQueriesPublishesNoPathServerOrSQL` | le chemin local, le serveur ou un extrait de SQL publié à chaque session |
| `TestMissingTsqlScriptsSourceIsAMessageNotAnError` | le catalogue cassé par un clone absent |
| `TestDeclaredVariableIsOnlyTheDeclareTarget` | `DECLARE @local = @cutoff` qui fait disparaître le paramètre |
| `TestParametersLineIsOptionalButChecked` | les fichiers livrés `No parameter.` rejetés, l'en-tête qui ment |
| `TestCatalogListsExactlyTheFilesPresent` | l'entrée fantôme et la requête invisible |
| `TestBoundQueryRefusedOnAnotherProfile` | la bonne réponse à la mauvaise base |
| `TestQueryNameRejectsTraversalAndCase` | l'écriture hors du répertoire de requêtes |
| `TestNothingIsSavedWhenTheRunFailed` | du SQL jamais exécuté entré dans la bibliothèque |
| `TestSaveRefusesAVisibleName` | la sauvegarde qui en masque une autre |
| `TestSaveRefusesAWritingQuery` | l'instruction destructrice appelable par un nom court |
| `TestSaveRefusesCommentBreakingSummary` | le fichier sauvé devenu un commentaire, ou du SQL injecté |
| `TestSavedFileReparsesAsValidEntry` | l'entrée `rejected` née d'une sauvegarde |
| `TestMissingParameterIsRefusedBeforeConnecting` | l'erreur serveur là où une erreur d'usage suffit |
| `TestEveryResultSetIsReturned` | la réponse partielle d'un script à plusieurs jeux |
| `TestMessagesAreCaptured` | `PRINT` perdu |

S'y ajoutent les tests existants `TestBundledQueriesPassTheReadOnlyGuard` et
`TestEveryFlagIsDocumented`, qui couvrira les nouveaux drapeaux. Les deux derniers tests de
la table demandent une instance. Il n'existe pas encore de test d'intégration dans
`tools/` : ils lisent `$SQLQ_TEST_PROFILES` (chemin d'un fichier de profils) et
`$SQLQ_TEST_PROFILE` (le nom d'un profil `readonly` de test), et sont sautés si l'une des
deux manque. Ils ne créent aucun objet : `SELECT 1; SELECT 2;` et `PRINT` suffisent.

Contrôle sur le vrai clone, sauté si `$DB_AI_TOOLKIT_TSQL_SCRIPTS` est absent :
`TestRealCloneHasNoRejectedEntry`. Il sert à l'utilisateur après avoir posé des marqueurs.

## 17. Limites connues

- `verified` atteste d'une exécution réussie, pas d'une réponse juste.
- Le registre et la couche personnelle ne sont pas synchronisés entre machines, et deux runs
  simultanés peuvent perdre une entrée du registre (§10).
- Le garde-fou laisse hors catalogue 130 des 344 scripts tsql-scripts (`GO`, tables
  temporaires, `EXEC`). Ce design ne l'assouplit pas.
- La surcharge ne couvre que les déclarations d'une ligne, de type simple. Les marqueurs
  `<…>` et les noms d'objets en dur ne sont pas réglables tant qu'on ne les a pas convertis.
- La détection d'affectation est lexicale. Elle penche vers le refus ; un faux refus se lit
  dans `rejected`.
- Une requête de portée profil casse si le profil est renommé.
- Pas de recherche par mots-clés : l'index complet suffit jusqu'à une centaine d'entrées.
- Un clone tsql-scripts sur une branche ou un commit ancien fournit cette version-là. `sqlq`
  ne fait pas de `git pull`.
