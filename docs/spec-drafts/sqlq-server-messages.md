# sqlq — messages serveur et jeux de résultats multiples

**Statut** : spécifié, pas implémenté. Rédigé le 2026-09-17 à partir d'une tâche issue d'un
retour d'usage réel (session Bazefield du 17/09/2026, dépôt Neoen
`bazefield/2026.09.17.monitoring/`, commit `2ea4759`). Chaque affirmation de la tâche a été
vérifiée dans le code du dépôt et dans le cache de modules Go avant d'être reprise ici ; le § 9
liste ce que la vérification a corrigé ou ajouté.

**Révisé deux fois le 2026-09-17**, après deux revues adverses externes : huit points puis neuf.
La première a corrigé des affirmations fausses ; la seconde, des **décisions que le document
décrivait sans les prendre**, dont une contradiction entre deux de ses propres contraintes. Voir
le § 9.

**Reste à trancher avant d'écrire du code** : rien sur le contrat. Le seul point encore ouvert
est l'ordre d'intercalation des jeux showplan (§ 4.3), qui ne se décide pas — il se mesure contre
une instance réelle, et le § 8 dit comment.

> **Convention de langue.** Le code, les messages, les clés JSON et l'aide restent **en
> anglais**. Ce document de spécification est en français, comme `docs/reviews/`.

## 1. Le besoin, tel que la session l'a produit

Une analyse de fraîcheur de données sur une base de staging, ~24 allers-retours SQL, menée avec
un wrapper PowerShell/`sqlcmd` écrit à la volée et réécrit trois fois.

`sqlq` aurait couvert une vingtaine de ces appels, et mieux : caviardage du mot de passe là où
le wrapper passait `-P` en argument de processus, `truncated`/`rowcount` honnêtes là où le
wrapper masquait silencieusement avec `Select-Object -First`, `LOCK_TIMEOUT`, erreurs
structurées. Les ~4 allers-retours perdus en plomberie disparaissent aussi.

**Les 4 appels restants sont ceux qui ont mesuré les coûts, et ce sont eux qui ont tranché.**
C'est ce que cette spec vise, et le critère de réussite est là :

- « Option A : 1 312 lectures logiques, 24 ms ; Option B : 2 113 lectures, 26 ms » — la phrase
  centrale du livrable ;
- `APP.VALUE_HISTORY` écartée sur « Scan count 3792, logical reads 0 », soit 3 792 seeks
  pour zéro ligne.

Sans les messages serveur, la sortie aurait été « 0 ligne, 7 ms » et la conclusion beaucoup plus
faible. **`elapsed_ms` ne remplace rien** : c'est le mur côté client, connexion comprise, ni CPU
serveur ni lectures logiques.

## 2. Faits vérifiés (2026-09-17)

| Élément | État constaté |
|---|---|
| `Result.Messages` | déclaré `result.go:34`, normalisé à `[]` ligne 53. **Aucun écrivain** : zéro occurrence dans `cmd/sqlq/main.go` |
| `collect()` | garde le premier jeu, **draine** les suivants (`main.go`, commentaire « one query, one answer ») |
| `github.com/golang-sql/sqlexp` | **v0.1.0 déjà présente** : `go.mod` en `// indirect`, `go.sum` renseigné, présente dans le cache de modules. Promotion, pas ajout |
| `MsgNotice.Message` | `fmt.Stringer`, concrètement `mssql.Error` — porte `Number`, `Message`, `Class` (sévérité), `LineNo`. **Pas une chaîne** |
| `QueryContext` avec `msgq` | rend `(Rowsq, nil)` **inconditionnellement** (`mssql.go:794`). Voir § 3 |
| `Message(ctx)` sur `ctx.Done()` | rend **`MsgNextResultSet{}`**, pas une erreur (`sqlexp/messages.go`). Voir § 3 |
| File de messages | canal bufferisé à **15** ; `ReturnMessageEnqueue` bloque quand il est plein |
| `GO` | **déjà réglé le 2026-09-17** : `FindBatchSeparators` + refus explicite avec le numéro de ligne. Le point 4 de la tâche est clos |
| Garde d'écriture, littéraux | la requête Query Store filtrant sur `LIKE '%insert %'` passe. **Mesuré**, pas déduit |
| Garde d'écriture, `_` mot | `ITEM_CREATE`, `ROW_INSERT`, `create_date`, `[DELETE]` passent. **Mesuré** |
| `MsgRowsAffected` | mis en file **seulement si** `done.Status & doneCount != 0` (`token.go:1064` et `1106`). `SET NOCOUNT ON` du `preamble` supprime ce bit. Voir § 7 |
| `rowcount` | signifie aujourd'hui *tout ce que le serveur a envoyé*, même quand `rows` est plafonné (`result.go`, commentaire de `RowSet`). Invariant à préserver, voir contrainte 3 |
| Tests d'intégration | **aucune infrastructure** : `tests/` est vide, aucun `//go:build`, aucun test piloté par l'environnement |

## 3. Le piège central : adopter `ReturnMessage` supprime les erreurs SQL

C'est le point le plus important du document, et la tâche d'origine ne le voyait pas.

`sqlexp` le dit à mots couverts — *« Queries that receive this message should also not return
SQL errors from the Query method, but wait to return it in a Message »* — et `go-mssqldb`
l'applique sans nuance :

```go
// mssql.go:793-794
// For apps using a message queue, return right away and let Rowsq do all the work
if reader.outs.msgq != nil {
    res = &Rowsq{stmt: s, reader: reader, cols: nil, cancel: cancel}
    return res, nil          // err est nil, toujours
}
```

Le code actuel de `execute()` fait :

```go
rows, err := conn.QueryContext(ctx, sqlText, args...)
if err != nil { /* erreur SQL structurée, exitSQL */ }
```

Passer `&sqlexp.ReturnMessage{}` rend ce `err` **systématiquement nil** pour tout ce qui vient du
serveur. Un nom d'objet invalide, une erreur de syntaxe, un refus de permission : `sqlq`
rendrait `exit 0`, zéro ligne, `error: null`. **L'outil mentirait sur le succès.**

C'est la forme la plus coûteuse de régression, et celle que ce dépôt rencontre pour la cinquième
fois : tout continue de fonctionner, seul le signal disparaît. Donc :

> **Contrainte 1.** `sqlexp.MsgError` devient l'unique voie des erreurs SQL. La boucle doit la
> traiter exactement comme `toSQLError` traite aujourd'hui le retour de `QueryContext` :
> erreur structurée, caviardée, `exitSQL`. Un test doit vérifier qu'une requête fautive rend
> toujours un `error.number` non nul et un code de sortie `2`.

### Le second piège : un timeout se présente comme une fin normale

```go
func (m *ReturnMessage) Message(ctx context.Context) RawMessage {
	select {
	case <-ctx.Done():
		return MsgNextResultSet{}
	case raw := <-m.queue:
		return raw
	}
}
```

Sur expiration du contexte, `Message` rend `MsgNextResultSet{}`. Une boucle écrite naïvement
appelle alors `rows.NextResultSet()`, obtient `false`, sort proprement — et `sqlq` rapporte un
succès avec les lignes lues jusque-là. Le `-timeout` cesserait d'être visible.

Mais tester `ctx.Err()` **à la sortie** de la boucle ne suffit pas non plus : une requête
terminée normalement juste avant l'échéance serait alors étiquetée « timeout » parce que le
contexte a expiré entre le dernier message et le test. Un succès sortirait en code `2`.

> **Contrainte 2.** La boucle doit enregistrer **pourquoi** elle s'arrête, et trois fins sont à
> distinguer :
>
> Le classement est **ordonné** : la première ligne qui s'applique gagne. Sans cet ordre, une
> annulation survenue pendant la lecture des lignes serait rapportée comme une panne de réseau.
>
> | Ordre | Fin | Détection |
> |---|---|---|
> | 1 | Annulation / timeout **pendant l'attente d'un message** | `ctx.Err() != nil` **au moment où** `MsgNextResultSet` est reçu — la marque du message synthétique produit par le `select` sur `ctx.Done()` |
> | 2 | Annulation / timeout **pendant la lecture des lignes** | `errors.Is(rows.Err(), context.DeadlineExceeded)` ou `context.Canceled` |
> | 3 | Erreur de transport | `rows.Err()` non nul et **pas** une erreur de contexte, hors `MsgError` |
> | 4 | Terminaison normale | aucune des trois, `rows.NextResultSet()` rend `false` |
>
> La ligne 2 manquait à la première rédaction, et elle n'est pas théorique. **Vérifié dans
> `database/sql`** (`sql.go:3006-3020` et `3136-3145`) : une goroutine `awaitDone` range
> l'erreur de contexte dans `contextDone` et ferme les `Rows`, et `Rows.Err()` rend cette
> erreur **en priorité** sur toute autre. Un timeout qui tombe pendant `rows.Next()` ou
> `rows.NextResultSet()` ne produit donc aucun message synthétique : il sort par `rows.Err()`,
> et le tableau d'origine l'aurait rangé en « erreur de transport ».
>
> Le test se fait donc **à la réception**, pas à la sortie. Il reste une course résiduelle : si
> l'échéance tombe dans la même microseconde qu'un `MsgNextResultSet` légitime, le `select` du
> driver choisit au hasard et la requête sera rapportée comme annulée. C'est une requête qui a
> duré exactement le timeout ; la réponse est défendable, et il vaut mieux l'écrire ici que
> laisser croire à une détection parfaite.
>
> Une consolation mesurée au passage, sur l'autre moitié de la course : `Rows.Err()` ne rend
> l'erreur de contexte **que si `hitEOF` est faux** (`sql.go:3141`). Une requête terminée
> normalement dont le contexte expire ensuite ne sera donc pas étiquetée « annulée » par le
> chemin 2 — le cas que la contrainte redoutait est déjà couvert par la bibliothèque standard.

### Le troisième piège : `rowcount` perd son sens sur une lecture écourtée

`rowcount` ne signifie pas « lignes rendues » : il signifie **tout ce que le serveur a envoyé**,
même quand `rows` est plafonné par `-maxrows`. C'est ce qui rend une réponse tronquée honnête, et
c'est écrit dans le commentaire de `RowSet`.

Une lecture interrompue par le contexte casse cet invariant en silence : le reste du jeu n'a
jamais été reçu, donc le compte n'est plus un total, c'est « ce qui est arrivé avant la coupure ».
Un consommateur qui lit `rowcount: 4812` sans savoir cela en tire une conclusion fausse sur la
taille de la table.

> **Contrainte 3.** `rowcount` garde son sens actuel, et un champ **`rowcount_exact`** (booléen,
> `true` par défaut) passe à `false` dès que la lecture n'est pas allée jusqu'au bout du jeu.
> `truncated` conserve le sien — « le serveur en a envoyé plus que nous n'en rendons » — et les
> deux ne sont pas synonymes : `truncated` dit que nous avons coupé, `rowcount_exact: false` dit
> que **nous ne savons pas** ce qui restait.
>
> **Le champ est par jeu, pas global.** Chaque jeu a sa propre lecture, donc sa propre
> interruption : un lot dont le premier jeu est lu entièrement et le second coupé par le timeout
> doit rendre `rowcount_exact: true` sur le premier et `false` sur le second. Un booléen unique
> à la racine forcerait à choisir entre mentir sur le premier et jeter l'information du second.
> Le champ racine suit la même règle que `rowcount` et `truncated` : c'est **celui du premier
> jeu**, et les suivants portent le leur dans `result_sets`. Un jeu jamais commencé — le lot
> s'est arrêté avant — n'apparaît pas du tout : on ne rend pas un jeu vide pour dire qu'on ne
> sait rien.
>
> S'appuyer sur « `error != null`, donc ne faites pas confiance à `rowcount` » serait implicite,
> et c'est exactement le genre d'implicite que ce document existe pour supprimer.

## 4. Ce qu'il faut implémenter

Les points 1 et 2 de la tâche **tombent du même refactoring** : `MsgNotice`, `MsgNext` et
`MsgNextResultSet` arrivent dans le même flux. Les faire séparément n'a pas de sens.

### 4.1 La boucle de messages, qui remplace `collect()`

Protocole confirmé par le test du driver lui-même (`queries_go19_test.go:1975`) :

```go
retmsg := &sqlexp.ReturnMessage{}
rows, err := conn.QueryContext(ctx, sqlText, append(args, retmsg)...)
// err ne couvre plus que le transport : plus aucune erreur SQL ne passe par ici.

active := true
for active {
    switch m := retmsg.Message(ctx).(type) {
    case sqlexp.MsgNotice:        // PRINT, STATISTICS IO, STATISTICS TIME
    case sqlexp.MsgNext:          // un jeu est prêt : rows.Next() / Scan
    case sqlexp.MsgNextResultSet: active = rows.NextResultSet()
    case sqlexp.MsgError:         // désormais la SEULE voie des erreurs SQL
    case sqlexp.MsgRowsAffected:  // disponible, aujourd'hui non rendu
    }
}
if ctx.Err() != nil { /* contrainte 2 */ }
```

Conséquences sur le code existant, à traiter d'un bloc :

- `collect()` et `scanAll()` ne pilotent plus `rows.Next()` en boucle libre : c'est `MsgNext`
  qui le déclenche, un jeu à la fois.
- `isShowplan(cols)` reste, mais s'évalue **par jeu**, à l'intérieur de la boucle.
- Le secret reste résolu **une seule fois** (`resolve(profile)`) et alimente le DSN *et* le
  caviardage. Cela vaut aussi pour les messages : ils partent sur stdout, donc ils passent par
  `redact`.

### 4.2 Forme de sortie des messages

`Messages` est `[]string` aujourd'hui, mais **rien n'y a jamais été écrit** : aucun consommateur
ne peut dépendre de la forme d'un élément. C'est la seule fenêtre pour choisir la bonne
structure sans rien casser, et `MsgNotice` porte déjà `Number`, `Class` et `LineNo`.

> **Décision recommandée** : `Messages []SQLError`, c'est-à-dire **exactement le type déjà
> utilisé par `error`** — `{number, severity, state, line, procedure, message}` (`result.go:14`).
> Pas un type jumeau à quatre champs : un `PRINT` dans une procédure a bien un `procedure` et un
> `state`, et deux formes proches mais différentes pour la même chose serveur sont un piège pour
> le lecteur comme pour l'implémenteur. Le coût est nul aujourd'hui et prohibitif plus tard.
> `messages: []` reste la valeur vide, donc la forme documentée du JSON ne change pas pour un
> lecteur qui ne trouvait jamais rien dedans.

> **Nommage des clés JSON.** L'objet `Result` est en minuscules d'un seul mot, avec un seul
> composé : `elapsed_ms` (`result.go:29`). Les nouvelles clés suivent ce précédent —
> **`rowcount_exact`, `messages_truncated`, `result_sets`, `result_sets_truncated`** — et non le
> camelCase du **fichier de profils**, qui est un autre document avec sa propre convention
> (`passwordEnv`, `trustServerCertificate`). Une convention par objet, tenue.

> **Si le type dynamique n'est pas celui attendu.** `MsgNotice.Message` est un `fmt.Stringer`,
> et les champs ne viennent pas de lui : ils viennent de l'assertion vers `mssql.Error`. Si elle
> échoue — driver modifié, message forgé ailleurs dans la pile — il ne faut ni paniquer ni jeter
> le message : `number`, `severity`, `state` et `line` valent `0`, `procedure` est vide, et
> `message` porte le `String()`. Un message sans métadonnées reste un message.

> ⚠️ **Les messages passent par `redact()`, comme les erreurs.** C'est un canal de sortie neuf
> et c'est exactement le trou qu'un canal neuf ouvre : tout le travail de caviardage de `sqlq`
> s'applique aujourd'hui au texte d'erreur, et un `PRINT` ou une notice serveur qui recracherait
> une chaîne de connexion sortirait à côté. La règle n'est pas « les notices sont inoffensives »,
> elle est « rien ne quitte ce processus sans passer par le même filtre ».

Deux règles qui ne vont pas de soi :

- **Les messages doivent être bornés.** C'est tout le propos de `sqlq` : `-maxrows` borne les
  lignes, et on s'apprête à ouvrir un second canal, non borné, directement dans le contexte du
  modèle. `SET STATISTICS IO ON` émet **une notice par table et par jeu** ; une requête sur une
  vue imbriquée en produit des dizaines, un curseur des milliers. Il faut un plafond et un
  drapeau `messages_truncated`, sur le modèle exact de `truncated`.

  > **`-maxmessages`, défaut `200`.** Le défaut n'est pas arbitraire : `SET STATISTICS IO ON`
  > émet une notice **par table et par jeu**, et les lots réels de la session touchaient une
  > dizaine de tables sur trois ou quatre jeux — quelques dizaines de notices pour un usage
  > normal, donc un plafond qui ne se déclenche jamais dans ce cas. `200` laisse passer le cas
  > utile avec une marge d'un ordre de grandeur, et coupe le curseur qui en produit mille.
  >
  > Sémantique **alignée sur `-maxrows`**, parce qu'un second jeu de règles pour la même idée
  > serait de la complexité gratuite : `0` ou moins = pas de plafond, et une valeur négative est
  > refusée à l'analyse des drapeaux, comme `-maxrows` et `-timeout` le sont déjà. Ce qui est
  > compté, ce sont les messages **conservés** ; `messages_truncated` dit qu'il y en a eu
  > d'autres, jamais combien — les compter tous demanderait de les garder.
- ⚠️ **Plafonner ce qu'on stocke, jamais ce qu'on lit.** La file est un canal bufferisé à 15 et
  `ReturnMessageEnqueue` bloque sur un `select` avec `ctx.Done()` quand elle est pleine. Une
  boucle qui cesse de lire une fois le plafond atteint n'économise donc rien : elle **arrête le
  serveur**, qui reste bloqué à l'émission jusqu'à l'expiration du timeout. Le plafond transforme
  alors une sortie bornée en requête suspendue — strictement pire que le problème qu'il résout.

  > **Contrainte 4.** La boucle draine la file **jusqu'au message terminal ou à l'annulation du
  > contexte**, quel que soit le nombre de messages déjà conservés. Au-delà du plafond, les
  > messages sont lus et jetés, et seul le compteur avance. La même règle vaut mot pour mot pour
  > le plafond de jeux de résultats du § 4.3 : on cesse de **stocker**, jamais de **consommer**.
  >
  > **Elle vaut aussi après une erreur.** Le code actuel peut sortir sur une erreur de lecture et
  > laisser `rows.Close()` au `defer` : c'est correct aujourd'hui, et ça ne le sera plus. Sur
  > réception d'un `MsgError`, il ne faut ni sortir de la boucle ni cesser de lire. Sortir sans
  > rien faire laisse le driver bloqué sur sa file de 15.
  >
  > **Une seule stratégie, et c'est le drainage.** La première rédaction offrait le choix entre
  > drainer jusqu'au terminal et annuler le contexte pour débloquer l'émetteur. Les deux
  > débloquent, mais l'annulation **détruit le diagnostic** : d'après la contrainte 2, un
  > contexte annulé fait classer la fin en timeout, si bien qu'une requête dont l'erreur est
  > `Invalid object name` serait rapportée comme annulée — le remède effacerait la cause. Deux
  > contraintes de ce document se contredisaient ; c'est la contrainte 2 qui gagne, et
  > l'annulation est exclue.
  >
  > **Priorité des trois sources d'erreur**, à écrire une fois pour toutes :
  >
  > | Rang | Source | Devient |
  > |---|---|---|
  > | 1 | Le **premier** `MsgError` reçu | `error`, code `2` |
  > | 2 | Erreur de contexte (chemins 1 et 2 de la contrainte 2) | annulation / timeout |
  > | 3 | `rows.Err()`, hors erreur de contexte | erreur de transport |
  >
  > Un `MsgError` reçu l'emporte donc sur tout ce qui suit, y compris sur l'expiration du
  > contexte pendant le drainage qui suit l'erreur — sans quoi une erreur SQL arrivant juste
  > avant l'échéance serait maquillée en timeout. L'ordre n'est pas une préférence de style :
  > c'est le seul qui laisse l'utilisateur voir ce que le serveur a réellement refusé.
- **Un message n'est pas une erreur.** Les notices de sévérité ≤ 10 vont dans `messages` ;
  `MsgError` va dans `error`. Un `RAISERROR ... WITH NOWAIT` de sévérité 11+ arrive en `MsgError`
  et doit garder le comportement actuel.

### 4.3 Jeux de résultats multiples

Une quinzaine des appels de la session groupaient 2 à 4 `SELECT` dans un même script (colonnes +
index + comptage). Aujourd'hui il faut les éclater en autant d'appels.

> **Décision recommandée** : le premier jeu reste exactement où il est — `columns`, `rows`,
> `rowcount`, `truncated` — et les suivants s'ajoutent dans un tableau `result_sets`, chacun avec
> la même structure. Le contrat JSON documenté ne change pas ; un consommateur qui ignore
> `result_sets` continue de fonctionner à l'identique.

- `-maxrows` s'applique **par jeu**, avec son propre `truncated`. Un plafond sur le *nombre* de
  jeux est également nécessaire, pour la même raison qu'au § 4.2 — et il obéit à la contrainte 4 :
  on cesse de stocker, jamais de consommer.

  > **`-maxresultsets`, défaut `20`.** Les lots de la session groupaient 2 à 4 `SELECT` ; `20`
  > est large pour l'usage visé et borne le lot pathologique — un curseur, une boucle `WHILE`
  > rendant un jeu par itération. Mêmes règles que `-maxmessages` : `0` ou moins = pas de
  > plafond, négatif refusé à l'analyse des drapeaux, le compte porte sur les jeux **conservés**,
  > et `result_sets_truncated` dit qu'il y en a eu d'autres.
  >
  > **Ce qui est compté : les jeux de données, pas les showplans.** Un jeu showplan n'est pas une
  > réponse à la question posée, c'est une annotation de la précédente ; le faire entrer dans le
  > compte ferait dépendre le plafond de la présence de `-plan`, et un même lot rendrait deux
  > nombres de jeux différents selon un drapeau qui ne change pas la requête. Le jeu racine
  > compte pour un.
- « Une requête, une réponse » reste une bonne discipline — mais **c'est au skill de l'imposer,
  pas à l'outil de rendre l'autre usage impossible.** Le commentaire actuel de `collect()` doit
  être réécrit dans ce sens plutôt que supprimé.

#### Les cas que « un jeu = colonnes + lignes » ne couvre pas

Un lot T-SQL ne produit pas une suite propre de jeux peuplés. Chacun de ces cas doit avoir une
règle écrite, faute de quoi l'implémenteur en inventera une par défaut :

| Cas | Règle |
|---|---|
| Jeu avec colonnes et **zéro ligne** | jeu émis normalement : `columns` renseigné, `rows: []`, `rowcount: 0`. C'est une réponse, pas une absence de réponse |
| Instruction **sans jeu de résultats** (DML, `SET`, `USE`) | aucun jeu émis. Elle ne se manifeste que par un `MsgRowsAffected`, lui-même généralement absent (§ 7) |
| **Plusieurs** `MsgRowsAffected` | non rendus, voir § 7. Si la décision change, ils s'attachent au jeu courant, pas à la racine |
| **Erreur après** un ou plusieurs jeux déjà lus | les jeux déjà constitués sont **rendus**, `error` est renseigné, code `2`. Ces lignes sont réelles et ont déjà coûté leur travail au serveur ; les jeter punirait l'utilisateur d'une erreur survenue plus loin dans son lot |
| **Plusieurs erreurs** dans le lot | `error` porte la **première** — c'est celle qui explique les suivantes. Les autres vont dans `messages`, où **`severity >= 11` est la règle qui les distingue d'une notice** : SQL Server réserve 0–10 à l'informatif, et un `RAISERROR` de sévérité 11+ arrive de toute façon en `MsgError`. Le champ `severity` existant suffit ; pas de champ `kind` en plus, qui serait dérivable de celui-là |
| Showplan **ailleurs qu'en dernier** | avec `-plan` et plusieurs instructions, un plan arrive **par instruction**. Chaque entrée de `result_sets` porte donc son propre `plan`, et le `plan` racine garde le **premier**, pour que le contrat actuel ne change pas |
| Showplan **sans jeu de données** pour la même instruction | `SET STATISTICS XML ON` annote chaque instruction exécutée, y compris celles qui ne rendent aucun jeu. **Règle d'appariement** : un jeu showplan se rattache à la **dernière entrée émise si elle n'a pas encore de `plan`** ; sinon il crée une entrée à lui, `columns: []`, `rows: []`, `rowcount: 0`, avec son `plan`. Sans cette règle, l'implémenteur rattache au voisin le plus proche et le plan d'une instruction se retrouve collé au jeu d'une autre. ⚠️ **À confirmer sur une instance réelle** : l'ordre exact d'intercalation est produit par le serveur, il est au § 8 |
| Jeu vide **et** showplan pour la même instruction | deux jeux distincts côté serveur ; `isShowplan(cols)` les sépare, comme aujourd'hui |

#### La forme complète, une fois

Les règles ci-dessus se lisent mal séparément. Voici l'objet entier pour le cas qui les active
toutes à la fois : deux jeux, `-plan`, une notice `STATISTICS IO`, et une erreur survenue après
que le second jeu a commencé à être lu.

```json
{
  "profile": "site-prd/payroll",
  "server": "SRV01",
  "database": "ERP",
  "elapsed_ms": 412,
  "columns": [{ "name": "id", "type": "INT" }],
  "rows": [{ "id": 1 }],
  "rowcount": 1,
  "truncated": false,
  "rowcount_exact": true,
  "plan": "<ShowPlanXML ...>",
  "result_sets": [
    {
      "columns": [{ "name": "name", "type": "NVARCHAR" }],
      "rows": [{ "name": "a" }],
      "rowcount": 1,
      "truncated": false,
      "rowcount_exact": false,
      "plan": "<ShowPlanXML ...>"
    }
  ],
  "result_sets_truncated": false,
  "messages": [
    {
      "number": 0, "severity": 10, "state": 1, "line": 3,
      "procedure": "",
      "message": "Table 'ORDERS'. Scan count 1, logical reads 1312, physical reads 0..."
    }
  ],
  "messages_truncated": false,
  "error": {
    "number": 208, "severity": 16, "state": 1, "line": 7,
    "procedure": "",
    "message": "Invalid object name 'APP.MISSING'."
  }
}
```

Ce que cet exemple fixe, et qu'aucune règle isolée ne montrait :

- le **premier** jeu reste à la racine, y compris son `plan` — `result_sets` ne contient que les
  **suivants**, il n'y a pas de duplication de l'entrée racine ;
- `rowcount_exact` diffère entre la racine (`true`, jeu lu en entier) et le second jeu (`false`,
  lecture interrompue par l'erreur) : c'est précisément pourquoi le champ est par jeu ;
- les lignes déjà lues **survivent** à l'erreur, et le code de sortie reste `2` ;
- `messages` porte des objets de la même forme que `error`, et la notice de sévérité `10` s'y
  distingue d'une erreur secondaire par ce seul champ.

### 4.4 `-statistics` : à ne pas implémenter comme un parseur, pas tout de suite

L'idée est bonne : poser `SET STATISTICS IO ON; SET STATISTICS TIME ON;` dans le `preamble` sur
le modèle de `-plan`, pour éviter au skill de le répéter. Le gain en tokens est réel — une ligne
`STATISTICS IO` fait ~250 caractères dont ~200 de compteurs à zéro.

⚠️ **Mais la sortie de `STATISTICS IO`/`TIME` est du texte produit par le serveur, et ce texte
suit la langue de la session.** Sur une instance en français, « Table 'X'. Scan count 3792,
logical reads 0 » n'a pas cette forme. Un parseur calé sur les mots anglais rendrait
silencieusement zéro compteur — et ce parc-ci compte des instances françaises et australiennes.
*Non vérifié sur ce parc : aucune connexion n'a été ouverte pour le confirmer. À mesurer avant
d'écrire la moindre expression régulière.*

> **Décision recommandée, en deux temps.**
> 1. `-statistics` pose les deux `SET` et rend les notices **telles quelles**, via le § 4.1. Le
>    gain principal — voir les lectures logiques — est acquis dès là, sans risque.
> 2. La structuration (`{table, scan_count, logical_reads, physical_reads}` + `{cpu_ms,
>    elapsed_ms}`) n'est décidée qu'après avoir tranché la question de la langue.

Piste à évaluer pour le temps 2, sans en faire une base d'architecture : le plan d'exécution
réel, que `-plan` capture déjà, porte des compteurs d'exécution en attributs XML, nommés en
anglais quelle que soit la langue du serveur.

⚠️ **Ce ne sont pas les mêmes compteurs.** Le plan expose des compteurs **par opérateur** ;
`STATISTICS IO` expose des lectures **par table**, agrégées sur toute l'instruction. Les seconds
ne se déduisent pas mécaniquement des premiers — un même table scan peut apparaître sous
plusieurs opérateurs, et le rapprochement table ↔ opérateur passe par des noms d'objets qui ne
sont pas garantis présents. C'est **une hypothèse à mesurer**, pas une équivalence : si elle
tombe, le temps 2 retombe sur le parsing localisé, avec le coût que le § 4.4 décrit.

## 5. Non-régression : ce qu'il ne faut pas casser

Le garde-fou n'aurait bloqué **aucune** requête de la session. Deux comportements en sont
responsables, tous deux corrects, et tous deux **mesurés aujourd'hui** :

- `Sanitize` blanchit les littéraux avant le scan, donc la requête Query Store filtrant sur
  `LIKE '%insert %' OR '%update %' OR '%merge %'` passe ;
- `tokens()` traite `_` comme caractère de mot, donc `ITEM_CREATE` ≠ `CREATE` — et
  `ROW_INSERT`, `create_date`, `[DELETE]` passent également.

`guard_test.go` couvre les principes (`SELECT 'DROP TABLE t'`, `SELECT create_date`) mais **pas
ces deux formes-là** : le littéral multiple en `WHERE`, et le mot-clé en *suffixe* après un
souligné. Ce sont exactement les requêtes que la session a produites.

> **À faire** : les épingler par un test nommé d'après leur origine. Une garde qui refuserait la
> requête Query Store rendrait l'outil inutilisable pour l'usage même que cette spec veut
> servir, et rien ne le signalerait aujourd'hui.

`-dirty-reads`, `-database`, `-timeout` et `-maxrows` ont tous répondu à un besoin réel de la
session : aucun n'est à revoir.

## 6. Repli documentable tout de suite, sans toucher au Go

Quand le Query Store est actif, `sys.query_store_runtime_stats` expose `avg_logical_io_reads` et
`avg_duration`, **en jeu de résultats normal**, avec le seul `VIEW DATABASE STATE`.

⚠️ **Ce n'est pas la mesure de l'exécution qu'on vient de lancer, et il ne faut pas l'écrire
comme si ça l'était.** Ces colonnes sont des **moyennes** : elles agrègent toutes les exécutions
du même plan sur un intervalle d'agrégation, et une valeur aberrante y est noyée — or c'est
précisément la valeur aberrante que l'on cherche quand on compare deux plans d'accès. S'y ajoute
un délai : les statistiques d'exécution ne sont pas écrites immédiatement, et l'exécution que
l'on vient de faire peut n'apparaître qu'après la vidange du tampon.

C'est donc un **indice historique**, utile pour trouver les requêtes coûteuses d'une charge —
l'usage exact de la session — et non un substitut à `STATISTICS IO` pour mesurer un appel précis.
Le § 4 reste nécessaire ; ce repli ne le remplace pas, il fait patienter.

Cela mérite d'entrer dans `live-query/SKILL.md` sans attendre le § 4, et cela découvre au passage
un défaut de la documentation actuelle :

> Le `SKILL.md` recommande en Configuration de donner `VIEW SERVER STATE` au login. Sur la
> session, ce droit n'était **pas** disponible (`db_owner` + `VIEW DATABASE STATE` seulement) et
> le Query Store a suffi. La documentation doit dire **ce qui reste possible sans
> `VIEW SERVER STATE`**, au lieu de le présenter comme un prérequis — faute de quoi un
> utilisateur qui ne l'a pas conclut que l'outil ne lui sert à rien.

Ce point rejoint le 🔴 des logins dédiés : la formule « `db_datareader` + `VIEW DEFINITION` +
`VIEW SERVER STATE` » est présentée comme le minimum, alors que `VIEW SERVER STATE` ouvre les
DMV — dont `sys.dm_exec_sql_text`, c'est-à-dire le texte des requêtes en cours, valeurs
littérales comprises. Moins de droits suffisent plus souvent qu'on ne le dit.

## 7. Points ouverts

- **Un skill séparé ?** `live-query` est écrit pour interroger un schéma et des objets (« les
  plus grosses tables », « pourquoi cette vue »). La session relevait de l'archéologie de charge
  et de la mesure de coût : lecture du Query Store pour trouver les chemins d'écriture,
  comparaison de plans d'accès, décodage de partitionnement. C'est un autre métier, et peut-être
  un autre skill, plutôt qu'un élargissement de `live-query`.
- **`-format tsv`** : sur un inventaire catalogue (120 lignes × 3 colonnes), JSON répète les clés
  à chaque ligne, ~80 caractères contre ~30 en délimité. Défendable pour les dumps larges, à
  mettre en regard du coût d'un second format de sortie à maintenir — l'argument qui a fait
  écarter un second wrapper au § 8 de `registered-servers` vaut ici aussi.
- **`-env-file`** : la convention du dépôt Neoen est un `.env` par répertoire de chantier.
  Documenter le geste (charger le `.env`, puis appeler `sqlq`) coûte une ligne ; un drapeau
  coûte un chemin de lecture de secret supplémentaire, qu'il faudra tester. Commencer par la
  ligne de documentation.
- **`MsgRowsAffected` ne sera généralement pas là**, contrairement à ce que la première rédaction
  affirmait. Vérifié : le driver ne le met en file que si `done.Status & doneCount != 0`
  (`token.go:1064` et `1106`), et le `preamble` de `sqlq` pose `SET NOCOUNT ON`, dont c'est
  exactement l'effet de supprimer ce bit. Le message existe donc dans le protocole, mais le
  préambule l'éteint.

  Deux issues, et il faut en choisir une explicitement plutôt que découvrir le silence à
  l'exécution : le rendre **optionnel et documenté comme habituellement absent**, ou retirer
  `SET NOCOUNT ON`. La seconde a un coût direct — `NOCOUNT ON` est une réduction délibérée du
  bavardage, donc des tokens, et le retirer ferait remonter un « N rows affected » par
  instruction dans le canal `messages` que le § 4.2 cherche justement à borner.

  > **Recommandation** : garder `SET NOCOUNT ON`, **ne pas exposer** `rowsAffected`, et écrire
  > pourquoi dans le code. Un champ qui vaut `null` neuf fois sur dix coûte plus en confusion
  > qu'il ne rapporte.

## 8. Tests : l'unitaire ne peut pas valider ce changement

Il faut le dire avant d'écrire la première ligne, parce que cela change ce que « fini » veut dire.

Le cœur du changement est **une interaction entre quatre composants** — `database/sql`,
`go-mssqldb`, `sqlexp.ReturnMessage` et SQL Server lui-même — et l'ordre des messages est produit
par le serveur, pas par nous. Un test unitaire ne peut ni fabriquer cet ordre de façon fidèle, ni
prouver qu'on l'a compris. Les trois contraintes du § 3 portent précisément sur ce que cette
interaction fait dans les cas tordus : si elles ne sont vérifiées que sur un faux driver, elles
sont vérifiées contre notre propre hypothèse.

**État actuel** : le dépôt n'a **aucune** infrastructure d'intégration. `tests/` est vide, aucun
fichier ne porte de balise `//go:build`, aucun test ne lit l'environnement. Il faut donc la créer,
et c'est une partie du travail, pas un supplément.

> **Forme proposée** : un paquet sous balise `//go:build integration`, piloté par une variable
> d'environnement nommant un profil `sqlq` existant, lancé **par l'utilisateur** contre une base
> de bac à sable — jamais dans une passe automatique, et jamais contre une instance approuvée en
> `environment: prod`. `go test ./...` reste vert sans elle.

Matrice obligatoire avant de déclarer le § 4 terminé :

| Cas | Ce qu'il prouve |
|---|---|
| `PRINT 'a'; SELECT 1; PRINT 'b'` | l'ordre `MsgNotice` / `MsgNext` / `MsgNextResultSet` est bien celui qu'on suppose |
| `SELECT 1; SELECT 2; SELECT 3` | plusieurs jeux rendus, dans l'ordre, chacun avec ses colonnes |
| `SELECT * FROM objet_inexistant` | **contrainte 1** : `error.number` non nul, code `2`. Le test qui manquerait rendrait la régression invisible |
| `SELECT 1; SELECT * FROM objet_inexistant` | erreur **après** un jeu déjà rendu : le jeu survit, l'erreur est portée |
| `WAITFOR DELAY` au-delà de `-timeout` | **contrainte 2** : annulation rapportée comme telle, pas comme une fin normale |
| Requête longue, `-timeout` juste au-dessus | l'autre côté de la contrainte 2 : un succès tardif **n'est pas** étiqueté timeout |
| `SET STATISTICS IO ON` sur une jointure multi-tables | les notices arrivent, et **`rowcount_exact` reste `true`** |
| Requête à très nombreuses notices, `-maxmessages` bas | **contrainte 4** : le plafond est respecté *et* la requête se termine. Un blocage ici est le défaut que la revue a trouvé |
| Procédure stockée rendant 2 jeux + `PRINT` | le cas réel du § 1, bout en bout |
| `SET STATISTICS XML ON` avec 2 instructions | un plan par instruction ; `plan` racine = le premier |
| Jeu à zéro ligne suivi d'un jeu peuplé | le jeu vide est rendu, pas avalé |
| Le même lot, instance en français | la question de la langue du § 4.4, tranchée par la mesure |
| `PRINT` d'une chaîne contenant le mot de passe du profil | **le caviardage couvre `messages`**, pas seulement `error`. Le canal est neuf : sans ce test, il naît non filtré |
| Une erreur dont le texte contient le mot de passe | le même filtre sur `MsgError`, qui ne passe plus par `toSQLError` |
| `RAISERROR('a',11,1); RAISERROR('b',11,1); SELECT 1` | erreurs multiples : `error` porte la première, la seconde est dans `messages` avec `severity >= 11`, et le `SELECT` est quand même rendu |
| Un lot où l'échéance tombe **pendant** `rows.Next()` | chemin 2 de la contrainte 2 : rapporté comme annulation, pas comme erreur de transport |
| `SET STATISTICS XML ON` sur un lot dont une instruction ne rend aucun jeu | la règle d'appariement plan/instruction du § 4.3, celle qui ne peut être confirmée que par le serveur |
| Sortie complète d'un lot à deux jeux avec `-plan` et une erreur | **le format exact** des nouveaux champs, comparé à l'objet du § 4.3. Un contrat sans test sur sa forme dérive au premier refactoring |

## 9. Ce que la vérification a changé par rapport à la tâche d'origine

La tâche était juste sur ses trois affirmations structurelles — `Result.Messages` sans écrivain,
`collect()` qui draine, `sqlexp` déjà en dépendance — et les trois ont été confirmées dans le
code. Quatre corrections :

| Point de la tâche | Après vérification |
|---|---|
| « `GO` : soit découper, soit refuser » | **Déjà fait** le 2026-09-17 : `FindBatchSeparators` + refus explicite avec numéro de ligne. Point clos |
| Usage de `ReturnMessage` | **Incomplet, et dangereusement** : `QueryContext` cesse de rendre les erreurs SQL. Contrainte 1, § 3 |
| Boucle `Message(ctx)` | **Piège non vu** : un timeout rend `MsgNextResultSet{}` et se présente comme une fin normale. Contrainte 2, § 3 |
| « Alimenter `Result.Messages` » (`[]string`) | `MsgNotice` porte `Number`, `Class`, `LineNo`. Le champ n'ayant **jamais** rien contenu, c'est la seule occasion de le structurer sans rien casser (§ 4.2) |
| `-statistics` avec parsing | La sortie `STATISTICS IO`/`TIME` est **localisée**. Livrer le texte brut d'abord, décider du parsing après mesure (§ 4.4) |

Et un ajout qui ne figurait nulle part : **les messages sont un canal de sortie non borné vers le
contexte du modèle**, là où tout le reste de `sqlq` est plafonné. `-maxmessages` et
`messages_truncated` ne sont pas du confort, ils sont la cohérence de l'outil.

### Revue adverse — 2026-09-17, sur la première rédaction

Huit points, **tous retenus**. Deux d'entre eux corrigent des affirmations fausses que j'avais
écrites ; un troisième corrige une parade qui aurait créé le défaut qu'elle visait.

| Point soulevé | Verdict | Suite |
|---|---|---|
| Blocage si `-maxmessages` est atteint | **Fondé, bloquant** | contrainte 4 : plafonner ce qu'on stocke, drainer toujours. La file est bufferisée à 15 et l'émission bloque : un plafond naïf suspend la requête au lieu de borner la sortie (§ 4.2) |
| `ctx.Err()` à la sortie ⇒ faux timeouts | **Fondé, bloquant — parade défectueuse de ma rédaction** | contrainte 2 réécrite : le test se fait **à la réception** de `MsgNextResultSet`, et les trois fins sont distinguées. La course résiduelle est écrite plutôt que niée (§ 3) |
| `rowcount` ambigu après interruption | **Fondé, bloquant** | vérifié : `rowcount` signifie « tout ce que le serveur a envoyé ». Nouveau champ `rowcount_exact`, contrainte 3 (§ 3) |
| Contrat `result_sets` incomplet | **Fondé** | tableau de sept cas : jeu vide, instruction sans jeu, erreur après un jeu rendu, erreurs multiples, showplan non terminal (§ 4.3) |
| `MsgRowsAffected` vs `SET NOCOUNT ON` | **Fondé — affirmation fausse de ma rédaction** | vérifié `token.go:1064` : le bit `doneCount` est ce que `NOCOUNT ON` supprime. Recommandation inversée : ne pas exposer le champ (§ 7) |
| Query Store ≠ mesure de cette exécution | **Fondé — présentation trompeuse de ma rédaction** | ce sont des moyennes sur un intervalle, écrites après un délai. Requalifié en indice historique (§ 6) |
| « Le plan XML porte les mêmes compteurs » | **Fondé** | compteurs **par opérateur** contre lectures **par table** : pas une équivalence. Requalifié en hypothèse à mesurer (§ 4.4) |
| L'unitaire ne peut pas valider ce changement | **Fondé** | nouveau § 8 : matrice d'intégration obligatoire, et l'infrastructure est à créer — `tests/` est vide aujourd'hui |

**Ce que la revue confirme sur la méthode.** Le motif observé sur `registered-servers` se répète :
sur huit points, **trois portent sur des défauts que la rédaction a introduits** plutôt que sur des
manques de la tâche d'origine — dont une parade (tester `ctx.Err()` à la sortie) qui aurait créé
un faux positif en corrigeant un faux négatif. Une correction reste cohérente avec elle-même et
fausse avec le reste ; elle n'est vérifiée que lorsqu'on a relu ce qu'elle touche ailleurs.

Et le point qui n'était atteignable qu'en ouvrant le cache de modules — le blocage sur file pleine
— ne se déduisait pas du texte. Comme pour la collision d'`id` et le décompte d'authentification
ailleurs : **ce qui tranche, c'est de mesurer.**

### Revue adverse — 2026-09-17, sur la deuxième rédaction

Neuf points. **Huit retenus, un retenu à moitié.** Le verdict d'ensemble de la revue — « la spec
n'est pas implémentable sans décisions supplémentaires » — était juste : quatre points portaient
sur des endroits où le document décrivait un choix au lieu de le faire.

| Point soulevé | Verdict | Suite |
|---|---|---|
| Fin après `MsgError` non déterministe | **Fondé, bloquant** | le document offrait « drainer **ou** annuler » et ne tranchait pas. Pire : les deux options n'étaient pas équivalentes, et l'une **contredisait la contrainte 2** — annuler le contexte fait classer la fin en timeout, donc l'erreur SQL disparaissait au profit de son remède. Drainage imposé, annulation exclue, et priorité des trois sources d'erreur écrite en toutes lettres (§ 4.2) |
| Classification des timeouts incomplète | **Fondé, bloquant** | **vérifié dans `database/sql`** (`sql.go:3006-3020`, `3136-3145`) : une annulation pendant `rows.Next()` sort par `rows.Err()` sans passer par le `MsgNextResultSet` synthétique, et le tableau l'aurait rangée en erreur de transport. Tableau réordonné en quatre chemins priorisés (§ 3) |
| Plan impossible à associer sans jeu de données | **Fondé, bloquant** | `SET STATISTICS XML ON` annote aussi les instructions qui ne rendent rien. Règle d'appariement écrite — rattachement à la dernière entrée sans plan, sinon entrée propre sans colonnes — et marquée **à confirmer sur instance réelle**, parce que l'ordre d'intercalation appartient au serveur (§ 4.3) |
| Limites CLI non spécifiées | **Fondé, bloquant** | `-maxmessages` défaut `200`, `-maxresultsets` défaut `20`, sémantique alignée sur `-maxrows` (`0` = illimité, négatif refusé), et ce qui est compté : les jeux de **données**, pas les showplans — sinon le plafond dépendrait de la présence de `-plan` (§ 4.2, § 4.3) |
| Contrat JSON incohérent | **Fondé** | mesuré : l'objet `Result` est en minuscules avec un seul composé, `elapsed_ms`. Le camelCase proposé venait du **fichier de profils**, qui est un autre document. Clés arrêtées en `snake_case`, et un exemple complet ajouté (§ 4.2, § 4.3) |
| `MsgNotice` ne porte pas les métadonnées directement | **Fondé** | comportement si l'assertion vers `mssql.Error` échoue : champs à zéro, `String()` conservé. Et surtout : **les messages passent par `redact()`** — un canal de sortie neuf naît non filtré si personne ne l'écrit (§ 4.2) |
| Erreurs suivantes indistinguables des notices | **Fondé à moitié** | le champ `severity` existait déjà dans la forme proposée et suffit : SQL Server réserve 0–10 à l'informatif. Un champ `kind` serait dérivable de `severity`, donc redondant et susceptible de le contredire. **Retenu** : la règle `severity >= 11` était implicite, elle est maintenant écrite (§ 4.3) |
| `rowcount_exact` doit être par jeu | **Fondé** | la contrainte 3 le définissait globalement alors que le § 4.3 rend les jeux indépendants. Par jeu, racine = premier jeu, et un jeu jamais commencé n'est pas rendu (§ 3) |
| Couverture de tests incomplète | **Fondé** | six cas ajoutés, dont les deux qui manquaient le plus : le caviardage d'un secret apparaissant dans une notice, et la forme exacte de l'objet rendu (§ 8) |

**Ce que cette revue-ci apprend, et qui diffère de la première.** La première trouvait des
affirmations fausses — des choses vérifiables dans le code que j'avais écrites de mémoire. La
seconde trouve surtout des **décisions non prises** : « défaut à discuter », « drainer ou
annuler », un champ défini globalement pour des objets indépendants. C'est un autre défaut, et il
est plus difficile à voir soi-même, parce que le texte se lit comme complet — la phrase est là,
la règle a l'air écrite. Le test est mécanique : **une spec est implémentable quand aucune de ses
phrases ne laisse deux implémentations conformes.**

Et une contradiction interne s'est révélée entre deux contraintes écrites à vingt minutes
d'intervalle, chacune juste isolément. C'est le même motif que sur `registered-servers` — une
correction reste cohérente avec elle-même et fausse avec le reste — sauf qu'ici les deux moitiés
étaient dans le même document.

## 10. Source

Tâche Todoist « sqlq : capturer les messages serveur (STATISTICS IO/TIME, PRINT) et rendre les
jeux de résultats multiples », créée le 2026-09-17. Livrables de la session : dépôt Neoen,
`bazefield/2026.09.17.monitoring/`, commit `2ea4759`.
