# sqlq : ce qui reste de la spec des messages serveur

Plan d'implémentation, 25 octobre 2026. Il part de la spec
`docs/spec-drafts/sqlq-server-messages.md` (17 septembre 2026) et du code de `main`
(`81b2d22`). La spec a été écrite avant le commit `67ea7fb` (4 octobre 2026), qui en a
implémenté le cœur sous d'autres noms. Ce plan ne refait pas ce qui existe : il mesure
l'écart, tranche ce que la spec laissait ouvert, et le comble.

> Convention de langue : le code, les messages, les clés JSON et l'aide restent en
> anglais ; ce document est en français, comme la spec.

## 1. Ce qui existe déjà (mesuré le 25 octobre 2026)

Mesures faites sur le labo `sql2022` (SQL Server 2022 CU26), profil `labo-sql2022` en
`readonly`, par `sqlq` et par une sonde Go qui imprime le flux du pilote.

| Point de la spec | État dans le code | Preuve |
|---|---|---|
| § 4.1 boucle `sqlexp.ReturnMessage` | fait : `collect()` dans `tools/cmd/sqlq/main.go` | lecture |
| Contrainte 1, `MsgError` seule voie des erreurs SQL | fait | `TestErrorAfterFirstSetKeepsRows` passe sur le labo |
| § 4.3 jeux multiples | fait, sous le nom `more_results` | `TestEveryResultSetIsReturned` passe |
| Contrainte 3, compte inexact | fait en partie, sous le nom `incomplete`, **seulement quand une erreur SQL coupe le jeu** | `TestSetCutShortByErrorIsIncomplete` passe |
| Erreurs multiples | fait : `error` garde la première, les suivantes vont dans `messages` sous la forme `error <n>: <text>` | `TestLaterErrorsKeepTheirPlaceInMessages` passe |
| Notices (`PRINT`, `RAISERROR` ≤ 10, `STATISTICS IO`) | fait, en `[]string` | `sqlq -query "SET STATISTICS IO ON; SELECT COUNT(*) AS n FROM Stage.Stage;"` rend `Table 'Stage'. Scan count 1, logical reads 3, …` |
| Infrastructure d'intégration | faite : `tools/cmd/sqlq/integration_test.go`, pilotée par `SQLQ_TEST_PROFILES` et `SQLQ_TEST_PROFILE`, sans balise de build | les sept tests passent sur le labo |

Les sept tests d'intégration existants passent : `TestEveryResultSetIsReturned`,
`TestMessagesAreCaptured`, `TestErrorAfterFirstSetKeepsRows`,
`TestTimeoutIsAnErrorNotPartialSuccess`, `TestManyMessagesDoNotHang`,
`TestSetCutShortByErrorIsIncomplete`, `TestLaterErrorsKeepTheirPlaceInMessages`.

## 2. L'écart, point par point

| # | Écart | Mesure | Tâche |
|---|---|---|---|
| E1 | Les notices ne passent pas par `redact()` : `collect()` fait `result.Messages = append(result.Messages, m.Message.String())`. Seules les erreurs suivantes, écrites par `execute()` via `sqlError`, sont caviardées. Un `PRINT` qui recrache le mot de passe sort en clair | lecture de `collect()` | 1 |
| E2 | Avec `-plan` et plusieurs instructions, chaque plan écrase le précédent : `plan` rend le **dernier** | `sqlq -plan -query "SELECT COUNT(*) AS a FROM sys.objects; SELECT COUNT(*) AS b FROM sys.columns;"` : le `StatementText` du plan est `SELECT COUNT(*) AS b FROM sys.columns` | 2 |
| E3 | Ni `-maxmessages` ni `-maxresultsets` : les messages et les jeux sont un canal non borné vers le contexte du modèle | `grep -n maxmessages tools/cmd/sqlq/main.go` ne trouve rien | 3 |
| E4 | Pas de `-statistics` | idem | 4 |
| E5 | Un jeu coupé par le timeout n'est pas marqué `incomplete`, et ses lignes déjà lues sont perdues : `scanAll` rend l'erreur et `collect` sort avant d'avoir rangé le jeu. Le timeout se détecte par `ctx.Err()` **à la sortie** de la boucle, ce que la contrainte 2 de la spec écarte | lecture de `collect()` | 5 |
| E6 | Les deux formes de requête que la spec veut épingler (§ 5) ne sont pas dans `guard_test.go` : le filtre Query Store `LIKE '%insert %'` et le mot-clé en suffixe après un souligné (`ITEM_CREATE`, `ROW_INSERT`, `[DELETE]`). Elles passent aujourd'hui | les deux requêtes rendent leur ligne sur le labo | 6 |
| E7 | Le skill présente `VIEW SERVER STATE` comme faisant partie du minimum, et ne dit rien du repli par le Query Store (spec § 6) | `SKILL.md`, ligne 276 | 7 |

## 3. Décisions

Chacune contredit ou complète la spec. Elles sont reprises dans le registre
`2026-10-25-sqlq-server-messages-remainder-ledger.md`.

### D1. `messages` reste un tableau de chaînes

La spec (§ 4.2) voulait `[]SQLError`, au motif que le champ n'avait jamais été écrit et
qu'on pouvait encore le structurer sans rien casser. Cette fenêtre s'est fermée le
4 octobre : `67ea7fb` a livré `[]string`, et `SKILL.md` documente la forme
`error <n>: <text>` des erreurs suivantes. Changer le type casse un contrat documenté
pour un gain que personne n'a demandé depuis. La règle de la spec « `severity >= 11`
distingue une erreur d'une notice » est remplacée par le préfixe `error <n>: `, déjà
documenté.

### D2. Les noms livrés restent

`more_results` (et non `result_sets`) et `incomplete` (et non `rowcount_exact`, de sens
inverse) sont documentés et testés. Les nouveaux champs suivent ces noms :
`messages_truncated`, `more_results_truncated`, `more_plans`, `more_plans_truncated`, en
minuscules avec soulignés comme `elapsed_ms` et `more_results`.

`incomplete` garde son sens (« ses lignes ne sont pas tout le jeu ») et l'étend à toute
coupure, pas seulement une erreur SQL : timeout et erreur de transport aussi. C'est la
contrainte 3 de la spec.

### D3. Les plans ne s'apparient pas aux jeux : `plan` est le premier, `more_plans` les suivants

La spec (§ 4.3) voulait un `plan` par entrée de `result_sets`, rattaché par la règle
« au dernier jeu émis s'il n'a pas encore de plan », et la marquait « à confirmer sur
une instance réelle ». Mesuré avec une sonde qui imprime le flux du pilote, sous
`SET STATISTICS XML ON` :

| Lot | Flux reçu |
|---|---|
| `SELECT COUNT(*) AS a FROM sys.objects; SELECT COUNT(*) AS b FROM sys.columns;` | jeu `a`, plan, jeu `b`, plan |
| `SELECT 1 AS a WHERE 1 = 0; SELECT 2 AS b;` | jeu `a` (0 ligne), plan, jeu `b` |
| `DECLARE @x int; SELECT @x = COUNT(*) FROM Stage.Stage; SELECT @x AS x;` | plan, jeu `x` |
| `SELECT 1 AS a; UPDATE Stage.Stage SET Duree = Duree WHERE 1 = 0;` | jeu `a`, plan |

Le plan d'une instruction arrive après ses lignes. Une instruction qui ne lit aucune
table (`SELECT 1`, `SELECT @x`) n'a pas de plan ; une instruction sans jeu de données
(`UPDATE`, affectation) en a un. Le dernier lot est donc indiscernable, dans le flux, de
`SELECT … FROM t` : un jeu suivi d'un plan. Dans un cas le plan est celui du jeu, dans
l'autre celui de l'`UPDATE`. La règle de la spec rattacherait le plan de l'`UPDATE` au
`SELECT 1`.

Aucun appariement n'est donc fait. `plan` garde le **premier** plan reçu (ce que la spec
demandait pour la racine, et l'inverse du comportement actuel), et les suivants vont
dans `more_plans`, dans l'ordre d'arrivée. Chaque plan porte son `StatementText`, qui
dit à quelle instruction il se rapporte. Le tableau est toujours présent, vide par
défaut.

### D4. Les plafonds

- `-maxmessages`, défaut `200` ; `-maxresultsets`, défaut `20`. `0` = pas de plafond ;
  une valeur négative est refusée par `options.validate()`, comme `-maxrows`.
- Ce qui est compté pour `-maxmessages` : ce que `collect()` range dans `messages`, soit
  les notices et les erreurs suivantes. Les messages que `run()` ajoute lui-même sur un
  `-saved` (ceux du catalogue) ne comptent pas : ils sont bornés par le catalogue.
- Ce qui est compté pour `-maxresultsets` : les jeux de **données**, le jeu racine
  compris. Avec `-maxresultsets 2`, on garde la racine et un jeu dans `more_results`.
  Les plans ne comptent pas dans ce nombre ; ils ont le leur, voir ci-dessous.
- Les plans sont bornés par la même valeur, séparément : avec `-maxresultsets N`, on
  garde `plan` et au plus `N - 1` entrées de `more_plans`. Une boucle `WHILE` sous
  `-plan` rend sinon un plan par itération, et c'est le même canal non borné.
- **Plafonner ce qu'on range, jamais ce qu'on lit** (contrainte 4 de la spec). Au-delà
  du plafond, la boucle continue de lire : les messages sont lus et jetés, les lignes
  d'un jeu en trop sont lues dans un `RowSet` jetable, les plans en trop sont lus et
  jetés. Seuls les drapeaux `messages_truncated`, `more_results_truncated` et
  `more_plans_truncated` passent à `true`. Une boucle qui cesserait de lire bloquerait le
  serveur sur la file de 15 du pilote jusqu'au timeout.
- Une erreur suivante qui tombe au-delà du plafond de messages n'est pas rangée ;
  `error` garde toujours la première. Aujourd'hui, `errSlots[i]` désigne implicitement
  `sqlErrs[i+1]` : dès qu'une erreur peut être jetée, cet appariement par position
  devient faux. La tâche 3 le remplace par un appariement explicite.

### D5. `-statistics` rend le texte du serveur tel quel

`-statistics` ajoute `SET STATISTICS IO ON;` et `SET STATISTICS TIME ON;` au préambule de
session, sur le modèle de `-plan`. Aucune analyse du texte : mesuré, il suit la langue de
la session. Sous `SET LANGUAGE French`, la notice devient
`Table 'sysschobjs'. Nombre d'analyses '1', lectures logiques 66, …`. Un parseur calé sur
l'anglais rendrait zéro compteur sans erreur (spec § 4.4, temps 1 seulement).

### D6. Le timeout se reconnaît à la réception

Contrainte 2 de la spec. Dans `collect()` :

1. à la réception d'un `MsgNextResultSet`, si `ctx.Err() != nil`, la boucle s'arrête et
   la fin est une annulation : c'est la marque du message synthétique que
   `sqlexp.ReturnMessage.Message` rend sur `ctx.Done()` ;
2. sinon, une erreur de `rows.Err()` (contexte ou transport) est rendue telle quelle ;
3. sinon la fin est normale, même si le contexte a expiré entre-temps.

Le test `if ctx.Err() != nil` placé après la boucle disparaît. Dans les cas 1 et 2, le
jeu ouvert, ou en cours de lecture, est marqué `incomplete` et garde les lignes déjà
lues.

## 4. Commun à toutes les tâches

Le module Go est `tools/` (`github.com/rudi-bruchez/db-ai-toolkit/tools`). Le binaire
est `tools/cmd/sqlq`, les types de résultat sont dans `tools/internal/sqlq/result.go`.

Noms réels à employer : `execute(profile, sqlText, args, o, resolve)`,
`collect(ctx, rows, retmsg, result, maxRows)` (sa signature change à la tâche 3),
`scanAll(rows, cols, set)`, `isShowplan(cols)`, `readSingleString(rows)`,
`redact(text, secret)`, `sqlError(err, secret)`, `options` et `options.validate()`,
`defineFlags(fs)`, `sqlq.Result`, `sqlq.ResultSet`, `sqlq.RowSet` et `sqlq.NewRowSet`,
`testProfile(t)` dans `integration_test.go`. Les codes de sortie sont `exitOK`,
`exitUsage`, `exitSQL`, `exitRefused`, `exitConnected`.

Tests d'intégration : sur le labo `sql2022`, **jamais ailleurs**. Le réglage et le test
tiennent dans **une seule invocation du shell**, sinon les tests d'intégration
s'ignorent (`t.Skip`) et affichent `ok` :

```sh
set -a; . "$HOME/Sources/Repos/atelier/livres-informatiques/optimiser-sql-server/edition-2026/harnais/.env"; set +a
export SQLQ_TEST_PROFILES="$HOME/.config/db-ai-toolkit/mssql-profiles.json" SQLQ_TEST_PROFILE=labo-sql2022
cd tools && go test ./cmd/sqlq/ -run '^(TestA|TestB)$' -count=1 -v
```

Compter les lignes `--- PASS` : le nombre attendu est donné à chaque tâche, et tout
autre nombre, supérieur compris, veut dire que le filtre ou le travail est faux. Un
`--- SKIP` sur un test d'intégration veut dire que le réglage n'est pas dans la même
invocation.

Les tests d'intégration n'écrivent rien : le profil est `readonly`, et `testProfile`
refuse un profil qui ne l'est pas. Ils doivent rester indépendants de la base : objets
système seulement (`sys.objects`, `sys.columns`, `sys.all_columns`), jamais
`PachaDataFormation`.

Chaque tâche finit par la suite complète, `go vet ./...` et `gofmt -l .` vides, dans la
même invocation que le réglage, puis un commit. Message en anglais, préfixe
`feat(sqlq):`, `fix(sqlq):`, `test(sqlq):` ou `docs:` comme l'historique, corps en prose
qui dit pourquoi, sans ligne d'attribution.

`TestEveryFlagIsDocumented` exige que tout drapeau soit écrit dans
`plugins/sqlserver-toolkit/README.md` **et** dans
`plugins/sqlserver-toolkit/skills/live-query/SKILL.md`. Une tâche qui ajoute un drapeau
le documente dans les deux, dans le même commit.

## Tâche 1. Caviarder les notices (E1)

Fichiers : `tools/cmd/sqlq/main.go`, `tools/cmd/sqlq/integration_test.go`.

Dans `execute()`, juste après l'appel à `collect()`, passer chaque élément de
`result.Messages` par `redact(…, secret)`. Les emplacements d'erreurs suivantes sont
écrits après, par `sqlError`, qui caviarde déjà : l'ordre est indifférent pour eux. Le
commentaire dit pourquoi : un canal de sortie neuf naît non filtré si personne ne le
filtre.

Test d'intégration `TestNoticesAreRedacted` :

- obtenir le secret par `resolve(p)` (la fonction rendue par `testProfile`) ;
- lot `PRINT '<secret, apostrophes doublées>'; SELECT 1 AS a;`, options
  `{maxRows: 5, timeoutSec: 120}` ;
- attendu : `exitOK`, aucun élément de `res.Messages` ne contient le secret, au moins
  un contient `[redacted]` ;
- **en cas d'échec, n'imprimer que des comptes**, jamais `res.Messages` : le message
  d'échec écrirait le mot de passe dans le journal du test.

Étape de rupture, obligatoire : retirer la boucle de caviardage, constater que
`TestNoticesAreRedacted` échoue, la remettre. Afficher la ligne modifiée par `grep -n`
avant de lancer le test.

Compte : `-run '^TestNoticesAreRedacted$'`, **1** test.

## Tâche 2. Le premier plan à la racine, les suivants dans `more_plans` (E2, D3)

Fichiers : `tools/internal/sqlq/result.go`, `tools/internal/sqlq/result_test.go`,
`tools/cmd/sqlq/main.go`, `tools/cmd/sqlq/integration_test.go`,
`plugins/sqlserver-toolkit/skills/live-query/SKILL.md`.

- `sqlq.Result` gagne `MorePlans []string \`json:"more_plans"\``, placé juste après
  `Plan`. `Result.MarshalJSON` le rend `[]` quand il est `nil`, comme `MoreResults`.
- Dans `collect()`, sur un jeu showplan : si `result.Plan == nil`, le plan va dans
  `result.Plan` ; sinon, s'il n'est pas `nil`, il est ajouté à `result.MorePlans`.
- `SKILL.md` : ajouter `"more_plans":[]` à l'objet JSON d'exemple, après `"plan":null`,
  et une puce : `plan` est le plan de la première instruction qui en a un ; les suivants
  sont dans `more_plans`, dans l'ordre d'exécution ; chaque plan porte le
  `StatementText` de son instruction ; aucun n'est rattaché à un jeu de `more_results`,
  parce que le flux du serveur ne permet pas de le faire sans erreur (une instruction
  sans table n'a pas de plan, un `UPDATE` en a un sans jeu de données).

Tests :

- `TestMorePlansIsAlwaysAnArray` (unitaire, `result_test.go`) : `json.Marshal` d'un
  `Result{}` contient `"more_plans":[]`.
- `TestFirstPlanStaysAtTheRoot` (intégration) : options
  `{maxRows: 5, timeoutSec: 120, wantPlan: true}`, lot
  `SELECT COUNT(*) AS a FROM sys.objects; SELECT COUNT(*) AS b FROM sys.columns;`.
  Attendu : `res.Plan` contient `FROM sys.objects`, `len(res.MorePlans) == 1`, et
  `res.MorePlans[0]` contient `FROM sys.columns`.

Étape de rupture : remettre l'écrasement (`result.Plan = plan` sans condition), constater
que `TestFirstPlanStaysAtTheRoot` échoue ; retirer la ligne de `MarshalJSON`, constater
que `TestMorePlansIsAlwaysAnArray` échoue.

Comptes : `go test ./internal/sqlq/ -run '^TestMorePlansIsAlwaysAnArray$'` **1** ;
`go test ./cmd/sqlq/ -run '^TestFirstPlanStaysAtTheRoot$'` **1**.

## Tâche 3. `-maxmessages` et `-maxresultsets` (E3, D4)

Fichiers : `tools/cmd/sqlq/main.go`, `tools/internal/sqlq/result.go`,
`tools/cmd/sqlq/integration_test.go`, un test unitaire d'options dans le fichier où vit
`TestOptionsRejectNonsenseValues`, `plugins/sqlserver-toolkit/README.md`,
`plugins/sqlserver-toolkit/skills/live-query/SKILL.md`.

- `options` gagne `maxMessages int` et `maxResultSets int`. `defineFlags` déclare
  `-maxmessages` (défaut `200`, aide « maximum server messages to keep; 0 means
  unlimited ») et `-maxresultsets` (défaut `20`, aide « maximum data result sets, and
  plans, to keep; 0 means unlimited »). `options.validate()` refuse une valeur négative
  de l'un ou de l'autre, avec un message formé comme celui de `-maxrows`.
- `sqlq.Result` gagne, après `MoreResults`, `MoreResultsTruncated bool
  \`json:"more_results_truncated"\``, après `Messages`, `MessagesTruncated bool
  \`json:"messages_truncated"\``, et après `MorePlans`, `MorePlansTruncated bool
  \`json:"more_plans_truncated"\``.
- `collect()` reçoit les deux plafonds (passer `o` ou deux entiers ; le choix est libre,
  le mettre à jour aux deux appels s'il y en a deux). Règles, toutes de D4 :
  - une notice est rangée tant que le nombre d'éléments de `result.Messages` est
    inférieur au plafond ; au-delà, `MessagesTruncated = true` et la boucle continue ;
  - une erreur suivante (`len(sqlErrs) > 1`) occupe un emplacement de message sous la
    même règle ; si elle est jetée, elle n'a pas d'emplacement. Remplacer `errSlots []int`
    par un appariement explicite, par exemple une tranche de paires
    `{slot int; err error}`, que `execute()` parcourt pour écrire
    `fmt.Sprintf("error %d: %s", …)`. L'appariement par position (`sqlErrs[i+1]`) est
    faux dès qu'une erreur est jetée ;
  - un jeu de données est rangé tant que le nombre de jeux rangés, racine comprise, est
    inférieur au plafond ; au-delà, ses lignes sont lues par `scanAll` dans un
    `sqlq.NewRowSet(1)` jetable, `MoreResultsTruncated = true`, et le jeu n'est pas
    ouvert pour le marquage `incomplete` (`open` ne désigne aucun jeu rangé) ;
  - un plan est rangé tant que `plan` plus `more_plans` font moins que le plafond ; au-delà
    il est lu, jeté, et `MorePlansTruncated = true` ;
  - un plafond à `0` veut dire pas de plafond.
- Documentation : les deux drapeaux dans `README.md` et `SKILL.md` ; dans `SKILL.md`, les
  trois drapeaux de troncature, dans l'exemple JSON et en une puce chacun, avec la règle
  « au-delà du plafond, sqlq lit tout et ne range plus ».

`TestManyMessagesDoNotHang` construit ses options par littéral : `maxMessages` y vaut
`0`, donc pas de plafond, et le test reste vrai sans modification. Ne pas le modifier.

Tests :

- `TestOptionsRejectNegativeCaps` (unitaire) : `maxMessages: -1` refusé,
  `maxResultSets: -1` refusé, `0` accepté pour les deux, les autres champs valides
  (`timeoutSec: 30`).
- `TestMessageCapStopsStoringNotReading` (intégration) : lot
  `"SELECT 1 AS a;\n" + strings.Repeat("PRINT 'm';\n", 40) + "SELECT 2 AS b;"`,
  `maxMessages: 10`. Attendu : `exitOK`, `len(res.Messages) == 10`,
  `res.MessagesTruncated`, `len(res.MoreResults) == 1` (le jeu `b`, après les 40
  notices, a été lu : la boucle a continué).
- `TestResultSetCapStopsStoringNotReading` (intégration) : lot
  `SELECT 1 AS a; SELECT 2 AS b; SELECT 3 AS c; SELECT 4 AS d; RAISERROR('after the cap', 16, 1);`,
  `maxResultSets: 2`. Attendu : `exitSQL`, `res.Error.Message` contient `after the cap`
  (l'erreur venue après les jeux jetés a été lue), `res.Rows` est le jeu `a`,
  `len(res.MoreResults) == 1` et c'est `b`, `res.MoreResultsTruncated`.
- `TestLaterErrorBeyondMessageCapIsDropped` (intégration) : lot
  `RAISERROR('e1', 16, 1); RAISERROR('e2', 16, 1); RAISERROR('e3', 16, 1); SELECT 1 AS a;`,
  `maxMessages: 1`. Attendu : `exitSQL`, `res.Error.Message` est `e1`,
  `res.Messages` vaut exactement `["error 50000: e2"]`, `res.MessagesTruncated`,
  `len(res.Rows) == 1`.
- `TestPlanCapStopsStoringNotReading` (intégration) : `wantPlan: true`,
  `maxResultSets: 1`, lot de la tâche 2. Attendu : `exitOK`, `res.Plan` non nul,
  `len(res.MorePlans) == 0`, `res.MorePlansTruncated`, et `res.Rows` est le jeu `a`.

Étapes de rupture, chacune avec la ligne modifiée affichée par `grep -n` :

1. faire sortir `collect()` (`return`) quand le plafond de messages est atteint :
   `TestMessageCapStopsStoringNotReading` doit échouer ;
2. ne plus mettre `MoreResultsTruncated` à `true` : `TestResultSetCapStopsStoringNotReading`
   doit échouer ;
3. remettre l'appariement par position des erreurs suivantes :
   `TestLaterErrorBeyondMessageCapIsDropped` doit échouer, ou dire pourquoi il ne le
   fait pas ;
4. ranger les plans sans plafond : `TestPlanCapStopsStoringNotReading` doit échouer.

Rapporter « trois sur quatre ont échoué comme prévu » est un résultat de cette étape,
pas un échec de la tâche : dire lequel n'a pas échoué et pourquoi.

Comptes : `-run '^TestOptionsRejectNegativeCaps$'` **1** ;
`-run '^(TestMessageCapStopsStoringNotReading|TestResultSetCapStopsStoringNotReading|TestLaterErrorBeyondMessageCapIsDropped|TestPlanCapStopsStoringNotReading)$'`
**4** ; `-run '^TestEveryFlagIsDocumented$'` **1**.

## Tâche 4. `-statistics` (E4, D5)

Fichiers : `tools/cmd/sqlq/main.go`, `tools/cmd/sqlq/integration_test.go`,
`plugins/sqlserver-toolkit/README.md`, `plugins/sqlserver-toolkit/skills/live-query/SKILL.md`.

- `options` gagne `wantStatistics bool` ; `defineFlags` déclare `-statistics` (aide
  « run SET STATISTICS IO and TIME ON; their output arrives in messages as the server
  wrote it, in the session language »).
- Dans `execute()`, comme `-plan` : si `o.wantStatistics`,
  `setup += "\nSET STATISTICS IO ON;\nSET STATISTICS TIME ON;"`.
- Documentation : le drapeau dans `README.md` et `SKILL.md`. Dans `SKILL.md`, dire que
  le texte n'est pas analysé, qu'il suit la langue de session (exemple français mesuré
  en D5), et qu'on le cite tel quel.

Tests d'intégration :

- `TestStatisticsArriveInMessages` : `wantStatistics: true`, lot
  `SELECT COUNT(*) AS n FROM sys.objects;`. Attendu : `exitOK`, au moins un message
  contient `logical reads` **ou** `lectures logiques`, et au moins un contient
  `CPU time` **ou** `Temps UC` (la langue du profil de test n'est pas connue d'avance).
- `TestStatisticsFollowTheSessionLanguage` : `wantStatistics: true`, lot
  `SET LANGUAGE French; SELECT COUNT(*) AS n FROM sys.objects;`. Attendu : un message
  contient `lectures logiques`. Mesuré sur le labo le 25 octobre 2026 ; si la mesure du
  libellé exact de `STATISTICS TIME` en français est nécessaire, la faire et l'écrire
  dans le rapport.

Étape de rupture : retirer l'ajout au préambule, constater que les deux tests échouent.

Comptes : `-run '^(TestStatisticsArriveInMessages|TestStatisticsFollowTheSessionLanguage)$'`
**2** ; `-run '^TestEveryFlagIsDocumented$'` **1**.

## Tâche 5. Coupure par le timeout : reconnue à la réception, jeu marqué `incomplete` (E5, D6)

Fichiers : `tools/cmd/sqlq/main.go`, `tools/cmd/sqlq/integration_test.go`,
`plugins/sqlserver-toolkit/skills/live-query/SKILL.md`.

- Dans `collect()`, sur `MsgNextResultSet` : si `ctx.Err() != nil`, marquer `incomplete`
  le jeu ouvert, arrêter la boucle et rendre `ctx.Err()`.
- Quand `scanAll` rend une erreur au milieu d'un jeu, ranger quand même ce jeu (colonnes
  et lignes déjà lues, sous les règles de plafond de la tâche 3) avec `Incomplete: true`,
  puis rendre l'erreur. Aujourd'hui le jeu est perdu.
- Après la boucle : retirer `if ctx.Err() != nil { return …, ctx.Err() }` et son
  commentaire ; rendre `rows.Err()`. Si `rows.Err()` n'est pas nul, marquer `incomplete`
  le dernier jeu rangé qui était encore ouvert au moment où la boucle a pris fin (garder
  sa trace avant que `open` ne soit remis à `-1`).
- `SKILL.md` : la puce sur `incomplete` dit « an error or the timeout cut that set
  short ».

Tests d'intégration :

- `TestSetCutByTimeoutKeepsItsRows` : options `{maxRows: 5, timeoutSec: 1}`, lot
  `SELECT a.object_id FROM sys.all_columns AS a CROSS JOIN sys.all_columns AS b;`
  (plusieurs dizaines de millions de lignes sur une instance ordinaire, qui ne se lisent
  pas en une seconde). Attendu : code différent de `exitOK`, `res.Error` non nul,
  `res.Incomplete`, `res.RowCount > 0`, `len(res.Rows) <= 5`. Mesurer d'abord la durée
  réelle du lot sans timeout sur le labo et l'écrire dans le rapport ; si elle est sous
  la seconde, le dire et agrandir le produit plutôt que d'abaisser l'assertion.
- `TestQueryEndingBeforeTheTimeoutSucceeds` : options `{maxRows: 5, timeoutSec: 3}`,
  lot `WAITFOR DELAY '00:00:01'; SELECT 1 AS a;`. Attendu : `exitOK`, `res.Error` nul,
  `res.Incomplete` faux.

Étapes de rupture : ne plus marquer le jeu `incomplete` sur coupure,
`TestSetCutByTimeoutKeepsItsRows` doit échouer ; traiter tout `MsgNextResultSet` comme
une coupure (remplacer la condition par `true`), `TestQueryEndingBeforeTheTimeoutSucceeds`
doit échouer. `TestTimeoutIsAnErrorNotPartialSuccess`, existant, doit rester vert dans
les deux versions finales.

Comptes :
`-run '^(TestSetCutByTimeoutKeepsItsRows|TestQueryEndingBeforeTheTimeoutSucceeds|TestTimeoutIsAnErrorNotPartialSuccess)$'`
**3**.

## Tâche 6. Épingler les requêtes que la garde laisse passer (E6)

Fichier : `tools/internal/sqlq/guard_test.go`.

Deux tests unitaires, sur le modèle de `TestFindWritesAllowsReadOnlyDiagnostics` (même
fonction appelée, même forme d'assertion) :

- `TestFindWritesAllowsQueryStoreLiteralFilters` : la requête de la session d'origine,
  `SELECT TOP (1) query_sql_text FROM sys.query_store_query_text WHERE query_sql_text LIKE '%insert %' OR query_sql_text LIKE '%update %' OR query_sql_text LIKE '%merge %';`,
  n'est pas une écriture.
- `TestFindWritesAllowsKeywordsAfterAnUnderscore` :
  `SELECT 1 AS ITEM_CREATE, 2 AS ROW_INSERT, 3 AS [DELETE], 4 AS create_date;` n'est pas
  une écriture.

Le commentaire de chaque test cite son origine : une garde qui refuserait ces requêtes
rendrait l'outil inutilisable pour l'analyse du Query Store, sans que rien le signale.

Étapes de rupture : faire de `_` un séparateur dans `tokens()`, le second test doit
échouer ; ne plus blanchir les littéraux dans `Sanitize`, le premier doit échouer.
Remettre le code à l'identique après chaque rupture (`git diff` vide sur `guard.go`).

Compte : `-run '^(TestFindWritesAllowsQueryStoreLiteralFilters|TestFindWritesAllowsKeywordsAfterAnUnderscore)$'`
**2**.

## Tâche 7. Documentation : droits et repli Query Store (E7), état de la spec

Fichiers : `plugins/sqlserver-toolkit/skills/live-query/SKILL.md`,
`docs/spec-drafts/sqlq-server-messages.md`.

- `SKILL.md`, le paragraphe qui recommande un login dédié avec `db_datareader`,
  `VIEW DEFINITION` et `VIEW SERVER STATE` : dire que `VIEW SERVER STATE` n'est pas un
  prérequis, qu'il ouvre les DMV de l'instance dont `sys.dm_exec_sql_text` (le texte des
  requêtes en cours, valeurs littérales comprises), et que sans lui il reste les
  catalogues, les DMV de la base avec `VIEW DATABASE STATE`, et le Query Store.
- `SKILL.md`, une courte section sur le repli Query Store (spec § 6) :
  `sys.query_store_runtime_stats` donne `avg_logical_io_reads` et `avg_duration` en jeu
  de résultats ordinaire, avec `VIEW DATABASE STATE`. Ce sont des moyennes sur un
  intervalle d'agrégation, écrites avec un délai : un indice historique pour trouver les
  requêtes coûteuses, pas la mesure d'un appel. Pour mesurer un appel, `-statistics`.
- Spec : remplacer le bloc de statut en tête par un statut daté du 25 octobre 2026 :
  implémentée par `67ea7fb` et par ce plan ; les décisions D1 à D6 de ce plan priment
  sur les §§ 4.2, 4.3 et la contrainte 3 ; renvoi vers ce plan et son registre. Ne pas
  réécrire le reste : c'est l'historique des revues.

Compte : la suite complète ; `TestEveryFlagIsDocumented` reste vert.

## Fin

- Suite complète, intégration comprise, dans une seule invocation : le nombre de
  `--- PASS` attendu est celui de départ (40 lignes `--- PASS`, sous-tests compris,
  pour `go test ./cmd/sqlq/ -count=1 -v`, mesuré le 25 octobre 2026) plus les tests
  ajoutés ici ; `./internal/sqlq/` se compte de la même façon avant et après.
- `go vet ./...`, `gofmt -l .` vides.
- Rebâtir le binaire (`go install ./cmd/sqlq`) et refaire à la main les deux mesures de
  l'écart E2 et de D5.
- Fusion sur `main` en local, sans push (décision de Rudi du 25 octobre 2026).
