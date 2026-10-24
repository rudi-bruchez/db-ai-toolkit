# sqlq : ce qui reste de la spec des messages serveur

Plan d'implémentation, 25 octobre 2026, révisé le même jour après la relecture par cinq
lecteurs (agy et codex, prompts directif et neutre, un sous-agent Claude). Il part de la
spec `docs/spec-drafts/sqlq-server-messages.md` (17 septembre 2026) et du code de `main`
(`c3f2581`). La spec a été écrite avant le commit `67ea7fb` (4 octobre 2026), qui en a
implémenté le cœur sous d'autres noms. Ce plan ne refait pas ce qui existe : il mesure
l'écart, tranche ce que la spec laissait ouvert, et le comble. Le tri des remarques de la
relecture est dans le registre.

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
| Contrainte 3, compte inexact | fait en partie, sous le nom `incomplete`, seulement quand une erreur SQL coupe le jeu | `TestSetCutShortByErrorIsIncomplete` passe |
| Erreurs multiples | fait : `error` garde la première, les suivantes vont dans `messages` sous la forme `error <n>: <text>` | `TestLaterErrorsKeepTheirPlaceInMessages` passe |
| Notices (`PRINT`, `RAISERROR` ≤ 10, `STATISTICS IO`) | fait, en `[]string` | `sqlq -query "SET STATISTICS IO ON; SELECT COUNT(*) AS n FROM Stage.Stage;"` rend `Table 'Stage'. Scan count 1, logical reads 3, …` |
| Infrastructure d'intégration | faite : `tools/cmd/sqlq/integration_test.go`, pilotée par `SQLQ_TEST_PROFILES` et `SQLQ_TEST_PROFILE`, sans balise de build | les sept tests passent sur le labo |
| § 8, erreur avec un numéro non nul et sortie 2 | fait pour une erreur d'exécution (8134) | `TestErrorAfterFirstSetKeepsRows` |
| § 8, ordre des messages autour d'une erreur | fait | `TestLaterErrorsKeepTheirPlaceInMessages` |

Les sept tests d'intégration existants passent : `TestEveryResultSetIsReturned`,
`TestMessagesAreCaptured`, `TestErrorAfterFirstSetKeepsRows`,
`TestTimeoutIsAnErrorNotPartialSuccess`, `TestManyMessagesDoNotHang`,
`TestSetCutShortByErrorIsIncomplete`, `TestLaterErrorsKeepTheirPlaceInMessages`.

## 2. L'écart, point par point

| # | Écart | Mesure | Tâche |
|---|---|---|---|
| E0 | Blocage jusqu'au timeout dès que plus de 15 messages arrivent pendant qu'un appel bloquant attend le pilote. La file de `sqlexp.ReturnMessage` a 15 places (`ReturnMessageInit`, `make(chan RawMessage, 15)`) ; le pilote y range les notices depuis le même fil qui alimente les lignes. Pendant que `scanAll` attend la fin du jeu, ou que `rows.NextResultSet()` attend l'instruction suivante, personne ne vide la file : le pilote s'arrête sur la seizième notice, et la fin du jeu n'arrive jamais | trouvé par agy, reproduit : un `SELECT` sous `SET STATISTICS IO ON` qui lit 18 vues catalogue rend `context deadline exceeded` après 3 s, ses 23 notices perdues ; un lot paramétré (`-param p=1`) `SELECT 1 AS a;` suivi de 16 `PRINT` puis `SELECT @p AS b;` aussi (sous `sp_executesql` et `SET NOCOUNT ON`, un `PRINT` n'émet pas de `DONE`). `-statistics` (tâche 4) rendrait le cas courant | 0 |
| E1 | Les notices et les plans ne passent pas par `redact()` : `collect()` range `m.Message.String()` et le plan tels quels. Un `PRINT` qui recrache le mot de passe sort en clair ; une requête qui le contient en littéral le retrouve dans le plan, échappé pour XML (`&amp;`, `&lt;`, `&gt;`, `&quot;`, `&apos;`) et les apostrophes doublées, mesuré, donc hors de portée de `secretForms` | lecture de `collect()` ; plan de `WHERE name = 'a&b<c>d"e''f'` : `a&amp;b&lt;c&gt;d&quot;e&apos;&apos;f` | 1 |
| E2 | Avec `-plan` et plusieurs instructions, chaque plan écrase le précédent : `plan` rend le dernier | `sqlq -plan -query "SELECT COUNT(*) AS a FROM sys.objects; SELECT COUNT(*) AS b FROM sys.columns;"` : le `StatementText` du plan est `SELECT COUNT(*) AS b FROM sys.columns` | 2 |
| E3 | Ni `-maxmessages` ni `-maxresultsets` : les messages et les jeux sont un canal non borné vers le contexte du modèle | `grep -n maxmessages tools/cmd/sqlq/main.go` ne trouve rien | 3 |
| E4 | Pas de `-statistics` | idem | 4 |
| E5 | Un jeu coupé par le timeout n'est pas marqué `incomplete`, et ses lignes déjà lues sont perdues : `scanAll` rend l'erreur et `collect` sort avant d'avoir rangé le jeu. Le timeout se détecte par `ctx.Err()` à la sortie de la boucle, ce que la contrainte 2 de la spec écarte | lecture de `collect()` | 5 |
| E5b | La première erreur SQL disparaît quand un timeout la suit : `execute()` teste `err != nil` avant `len(sqlErrs) > 0`, et `collect()` ne réserve d'emplacement que pour les erreurs suivantes. La spec (§ 4.2, priorité des trois sources) donne la première `MsgError` gagnante | trouvé par trois lecteurs, reproduit : `RAISERROR('boom',16,1) WITH NOWAIT; RAISERROR('boom2',16,1) WITH NOWAIT; WAITFOR DELAY '00:00:05';` sous `-timeout 2` rend `error: context deadline exceeded` et `messages: ["error 50000: boom2"]`, `boom` nulle part | 5 |
| E6 | Les deux formes de requête que la spec veut épingler (§ 5) ne sont pas dans `guard_test.go` : le filtre Query Store `LIKE '%insert %'` et le mot-clé en suffixe après un souligné (`ITEM_CREATE`, `ROW_INSERT`, `[DELETE]`). Elles passent aujourd'hui | les deux requêtes rendent leur ligne sur le labo | 6 |
| E7 | `VIEW SERVER STATE` est présenté comme faisant partie du minimum dans `SKILL.md` (ligne 276), `README.md` (ligne 118), `AGENTS.md` (ligne 121) et le commentaire de tête de `main.go` (lignes 7 et 8) ; rien sur le repli par le Query Store (spec § 6) | lecture | 7 |
| E8 | Quatre cas de la matrice de la spec (§ 8) n'ont pas de test : objet inexistant (erreur de compilation, 208), jeu vide suivi d'un jeu rempli, notices de part et d'autre d'un jeu, forme JSON complète d'un lot à deux jeux avec plan, notice et erreur | `grep -n '^func Test' tools/cmd/sqlq/integration_test.go` | 8 |

Le cas « procédure stockée » de la matrice n'est pas testé : `EXEC` est refusé par la
garde sur un profil `readonly`, et les tests ne créent aucun objet. Le secret dans le
texte d'une erreur est testé à la tâche 1.

## 3. Décisions

Chacune contredit ou complète la spec. Elles sont reprises dans le registre
`2026-10-25-sqlq-server-messages-remainder-ledger.md`.

### D0. Un relais vide la file du pilote en continu

`collect()` ne lit plus `retmsg.Message(ctx)` directement. Une fonction `relay(ctx,
retmsg)` lance une goroutine qui lit `retmsg.Message(ctx)` sans arrêt et range chaque
message dans une file sans borne protégée par un `sync.Mutex`, signalée par un canal
`chan struct{}` de capacité 1 ; elle rend une fonction `next()` que la boucle de
`collect()` appelle à la place de `retmsg.Message(ctx)`. La goroutine s'arrête après
avoir rangé le message qui suit l'expiration de `ctx` (le `MsgNextResultSet`
synthétique que `Message` rend sur `ctx.Done()`) ; `execute()` annule `ctx` en sortant,
donc elle ne survit pas à l'appel.

`Message` n'est qu'une réception sur un canal : la lire depuis une autre goroutine
pendant que la boucle principale appelle `rows.Next()` ne partage aucun état. L'ordre
des messages est conservé, puisque la boucle les dépile dans l'ordre où le relais les a
reçus.

Prototype mesuré le 25 octobre 2026 : les deux lots de E0 rendent leurs 23 et 16
messages sans erreur, et la suite `./cmd/sqlq/` passe sous `-race` (40 lignes
`--- PASS`).

La file du relais n'a pas de borne : elle grossit pendant qu'un seul jeu se lit, du
nombre de notices que l'instruction émet. Les plafonds de la tâche 3 portent sur ce
qui est rangé dans le résultat, pas sur cette file. Le dire dans un commentaire
`ponytail:` au-dessus de `relay`.

Course connue, non corrigée : quand `ctx` expire, `Message` choisit au hasard entre
`ctx.Done()` et un message déjà en file. Une `MsgError` arrivée juste avant l'échéance
peut ainsi être perdue. C'était déjà le cas avant le relais ; la corriger demanderait
de lire la file privée de `sqlexp`.

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
coupure pendant la lecture des lignes, pas seulement une erreur SQL : timeout et erreur
de transport aussi. C'est la contrainte 3 de la spec.

### D3. Les plans ne s'apparient pas aux jeux : `plan` est le premier, `more_plans` les suivants

La spec (§ 4.3) voulait un `plan` par entrée de `result_sets`, rattaché par la règle
« au dernier jeu émis s'il n'a pas encore de plan », et la marquait « à confirmer sur
une instance réelle ». Mesuré avec une sonde qui imprime le flux du pilote sous
`SET STATISTICS XML ON`, `B` étant un `MsgNextResultSet` :

| Lot | Paramètre | Flux reçu |
|---|---|---|
| `SELECT COUNT(*) AS a FROM sys.objects;` | non | jeu `a`, B, plan `a` |
| `SELECT 1 AS a; UPDATE Stage.Stage SET Duree = Duree WHERE 1 = 0;` | non | jeu `a`, B, B, plan `UPDATE` |
| `SELECT COUNT(*) AS a FROM sys.objects WHERE object_id > @p;` | oui | jeu `a`, B, plan `a` |
| `SELECT 1 AS a; UPDATE Stage.Stage SET Duree = Duree WHERE Duree = @p AND 1 = 0;` | oui | jeu `a`, B, plan `UPDATE` |
| `DECLARE @x int; SELECT 1 AS a; SELECT @x = COUNT(*) FROM sys.objects WHERE object_id > @p;` | oui | jeu `a`, B, plan de l'affectation |

Le plan d'une instruction arrive après ses lignes. Une instruction triviale sans table
(`SELECT 1`, `SELECT @x`, compilée en `SELECT WITHOUT QUERY`) n'a pas de plan ; une
instruction sans jeu de données (`UPDATE`, affectation) en a un. Sans paramètre, le
`DONE` de l'instruction triviale ajoute une frontière qui distinguerait les deux cas
(codex l'a relevé). Avec un paramètre, le lot passe par `sp_executesql`, où
`SET NOCOUNT ON` supprime ce `DONE` : les flux 3 et 4 sont identiques, et la règle de la
spec rattacherait le plan de l'`UPDATE` au `SELECT 1`. Tout `-param` et tout `-saved`
paramétré prennent ce chemin. Le XML ne tranche pas non plus : `StatementId` repart à 1
pour chaque instruction, les colonnes de sortie du plan sont des `Expr1002` et non les
alias, et une affectation a `StatementType="SELECT"`.

Aucun appariement n'est donc fait. `plan` garde le premier plan reçu (ce que la spec
demandait pour la racine, et l'inverse du comportement actuel), et les suivants vont
dans `more_plans`, dans l'ordre d'arrivée. Chaque plan contient son `StatementText`, qui
dit à quelle instruction il se rapporte. Le tableau est toujours présent, vide par
défaut.

Conséquence pour `sqlplan`, qui dessine le champ `plan` : avec plusieurs instructions,
il dessine désormais le premier plan et non le dernier. `SKILL.md` le dit.

### D4. Les plafonds

- `-maxmessages`, défaut `200` ; `-maxresultsets`, défaut `20`. `0` = pas de plafond ;
  une valeur négative est refusée par `options.validate()`, comme `-maxrows`.
- Ce qui est compté pour `-maxmessages` : ce que `collect()` range dans `messages`, soit
  les notices et les erreurs suivantes. Les messages que `run()` ajoute lui-même sur un
  `-saved` (catalogue, registre) ne comptent pas : ils sont bornés par le catalogue.
- Ce qui est compté pour `-maxresultsets` : les jeux de données, le jeu racine compris.
  Avec `-maxresultsets 2`, on garde la racine et un jeu dans `more_results`.
- Les plans sont bornés par la même valeur, séparément : avec `-maxresultsets N`, on
  garde `plan` et au plus `N - 1` entrées de `more_plans`. Une boucle `WHILE` sous
  `-plan` rend sinon un plan par itération.
- Plafonner ce qu'on range, jamais ce qu'on lit (contrainte 4 de la spec). Au-delà du
  plafond, la boucle continue de lire : les messages sont lus et jetés, les lignes d'un
  jeu en trop sont lues dans un `RowSet` jetable, les plans en trop sont lus et jetés.
  Seuls les drapeaux `messages_truncated`, `more_results_truncated` et
  `more_plans_truncated` passent à `true`.
- Une erreur suivante qui tombe au-delà du plafond de messages n'est pas rangée ;
  `error` garde toujours la première. L'appariement par position `errSlots[i]` ↔
  `sqlErrs[i+1]` reste juste : le nombre d'éléments de `messages` ne fait que croître,
  donc une fois le plafond atteint tout ce qui suit est jeté, et les erreurs jetées
  forment toujours une fin de `sqlErrs`. Il suffit de ne pas réserver d'emplacement à
  une erreur jetée. Un commentaire le dit à cet endroit (version précédente du plan :
  réécriture en paires, retirée après la relecture, voir le registre).

### D5. `-statistics` rend le texte du serveur tel quel

`-statistics` ajoute `SET STATISTICS IO ON;` et `SET STATISTICS TIME ON;` au préambule de
session, sur le modèle de `-plan`. Aucune analyse du texte : mesuré, il suit la langue de
la session. Sous `SET LANGUAGE French`, la notice IO devient
`Table 'sysschobjs'. Nombre d'analyses '1', lectures logiques 66, …` et la notice TIME
contient `Temps UC = 0 ms, temps écoulé = 0 ms` (espaces insécables avant les unités).
Un parseur calé sur l'anglais rendrait zéro compteur sans erreur (spec § 4.4, temps 1
seulement).

### D6. Le timeout se reconnaît à la réception, et la première erreur SQL gagne

Contrainte 2 et § 4.2 de la spec. Dans `collect()` :

1. à la réception d'un `MsgNextResultSet`, si `ctx.Err() != nil`, la boucle s'arrête et
   rend `ctx.Err()` : c'est la marque du message synthétique que
   `sqlexp.ReturnMessage.Message` rend sur `ctx.Done()`. Aucun jeu n'est marqué : un jeu
   dont `scanAll` est revenu sans erreur a été lu jusqu'à sa fin ;
2. quand `scanAll` rend une erreur au milieu d'un jeu (contexte ou transport), ce jeu
   est rangé avec ses lignes déjà lues et `Incomplete: true`, puis l'erreur est rendue.
   S'il n'a lu aucune ligne, il n'est pas rangé (spec, contrainte 3 : on ne rend pas un
   jeu vide pour dire qu'on ne sait rien) ;
3. sinon, après la boucle, `rows.Err()` est rendu tel quel, sans marquer aucun jeu.

Le test `if ctx.Err() != nil` placé après la boucle disparaît. La version précédente du
plan marquait, au point 3, le dernier jeu ouvert : deux lecteurs ont mesuré que cela
marque `incomplete` un jeu lu en entier quand le timeout tombe dans
`rows.NextResultSet()` sur l'instruction suivante. Retiré.

Dans `execute()`, la priorité devient : la première `MsgError` (`sqlErrs[0]`), puis
l'erreur de `collect()`. Quand les deux existent, `error` est la `MsgError` et la sortie
est `exitSQL`.

### D7. Le code de sortie 4 veut dire « la requête n'a pas démarré »

La spec (contrainte 2, § 4.2) distingue l'erreur de transport (sortie 4) de l'annulation
(sortie 2). Aujourd'hui, toute erreur rendue par `collect()` sort en 2, et le
commentaire de `exitConnected` dit « connection or transport failure ». Classer les
erreurs par type après le démarrage de la requête demanderait de distinguer une coupure
réseau d'une erreur de `rows.Scan` ou de `sql: Rows are closed`, sans moyen de provoquer
la première sur le labo pour le tester. Décision : la sortie 4 reste celle d'une
connexion qui échoue avant la requête (`db.Conn`, préambule) ; tout échec après le
démarrage sort en 2, transport compris. Le commentaire de `exitConnected` et la
documentation des codes de sortie le disent.

## 4. Commun à toutes les tâches

Le module Go est `tools/` (`github.com/rudi-bruchez/db-ai-toolkit/tools`). Le binaire
est `tools/cmd/sqlq`, les types de résultat sont dans `tools/internal/sqlq/result.go`.

Noms réels à employer : `execute(profile, sqlText, args, o, resolve)` (les arguments sont
des `sql.Named(...)` dans un `[]any`), `collect(ctx, rows, retmsg, result, maxRows)` (sa
signature change à la tâche 3), `scanAll(rows, cols, set)`, `isShowplan(cols)`,
`readSingleString(rows)`, `redact(text, secret)`, `secretForms(secret)`,
`redactErr(e, secret)`, `sqlError(err, secret)`, `options` et `options.validate()`,
`defineFlags(fs)`, `sqlq.Result`, `sqlq.ResultSet`, `sqlq.RowSet` et `sqlq.NewRowSet`,
`testProfile(t)` dans `integration_test.go`. Les codes de sortie sont `exitOK`,
`exitUsage`, `exitSQL`, `exitRefused`, `exitConnected`.

Tout littéral `options{...}` d'un test d'intégration donne `maxRows` et `timeoutSec`
positifs (`timeoutSec: 120` sauf mention contraire). `execute()` est appelé sans passer
par `validate()` : un `timeoutSec` omis vaut `0` et donne un contexte déjà expiré, et le
test échoue en sortie 4 avant d'avoir rien testé.

Tests d'intégration : sur le labo `sql2022`, jamais ailleurs. Le réglage et le test
tiennent dans une seule invocation du shell, sinon les tests d'intégration s'ignorent
(`t.Skip`) et affichent `ok` :

```sh
set -a; . "$HOME/Sources/Repos/atelier/livres-informatiques/optimiser-sql-server/edition-2026/harnais/.env"; set +a
export SQLQ_TEST_PROFILES="$HOME/.config/db-ai-toolkit/mssql-profiles.json" SQLQ_TEST_PROFILE=labo-sql2022
cd tools && go test ./cmd/sqlq/ -run '^(TestA|TestB)$' -count=1 -v
```

Compter les lignes `--- PASS` : le nombre attendu est donné à chaque tâche, et tout
autre nombre, supérieur compris, veut dire que le filtre ou le travail est faux. Les
nouveaux tests n'utilisent pas `t.Run` : une ligne `--- PASS` par test. Un `--- SKIP`
sur un test d'intégration veut dire que le réglage n'est pas dans la même invocation.

Les tests d'intégration n'écrivent rien : le profil est `readonly`, et `testProfile`
refuse un profil qui ne l'est pas. Ils restent indépendants de la base : objets système
seulement (`sys.objects`, `sys.columns`, `sys.all_columns`, vues catalogue), jamais les
tables de `PachaDataFormation`. Toute requête qui lit beaucoup de lignes a un `TOP (n)`
(règle de `AGENTS.md`) : `-maxrows` ne borne que la sortie, pas le travail du serveur.

Chaque tâche finit par la suite complète, sous `-race`, `go vet ./...` et `gofmt -l .`
vides, dans la même invocation que le réglage, puis un commit. Message en anglais,
préfixe `feat(sqlq):`, `fix(sqlq):`, `test(sqlq):` ou `docs:` comme l'historique, corps
en prose qui dit pourquoi, sans ligne d'attribution. Ne jamais lancer `git checkout` ni
`git restore` sur un fichier qui contient du travail non commité.

`TestEveryFlagIsDocumented` exige que tout drapeau soit écrit dans
`plugins/sqlserver-toolkit/README.md` et dans
`plugins/sqlserver-toolkit/skills/live-query/SKILL.md`. Une tâche qui ajoute un drapeau
le documente dans les deux, dans le même commit.

Si une mesure contredit ce plan, s'arrêter et le dire dans le rapport plutôt que faire
coller le code au plan.

## Tâche 0. Le relais des messages (E0, D0)

Fichiers : `tools/cmd/sqlq/main.go`, `tools/cmd/sqlq/integration_test.go`.

- Ajouter `relay(ctx context.Context, retmsg *sqlexp.ReturnMessage) func() sqlexp.RawMessage`
  près de `collect()`, comme décrit en D0, avec son commentaire : pourquoi (la file de
  15 du pilote, les deux appels bloquants), et la ligne `ponytail:` sur la file sans
  borne. Importer `sync`.
- Dans `collect()`, `next := relay(ctx, retmsg)` avant la boucle, et
  `switch m := next().(type)` à la place de `retmsg.Message(ctx)`.

Tests d'intégration, options `{maxRows: 5, timeoutSec: 10}` :

- `TestManyNoticesInsideOneSetDoNotHang` : lot
  `SET STATISTICS IO ON; SELECT (SELECT COUNT(*) FROM sys.objects) + (SELECT COUNT(*) FROM sys.columns) + (SELECT COUNT(*) FROM sys.indexes) + (SELECT COUNT(*) FROM sys.index_columns) + (SELECT COUNT(*) FROM sys.partitions) + (SELECT COUNT(*) FROM sys.allocation_units) + (SELECT COUNT(*) FROM sys.types) + (SELECT COUNT(*) FROM sys.schemas) + (SELECT COUNT(*) FROM sys.sql_modules) + (SELECT COUNT(*) FROM sys.foreign_keys) + (SELECT COUNT(*) FROM sys.database_principals) + (SELECT COUNT(*) FROM sys.database_permissions) + (SELECT COUNT(*) FROM sys.xml_schema_collections) + (SELECT COUNT(*) FROM sys.fulltext_catalogs) + (SELECT COUNT(*) FROM sys.assemblies) + (SELECT COUNT(*) FROM sys.certificates) + (SELECT COUNT(*) FROM sys.symmetric_keys) + (SELECT COUNT(*) FROM sys.asymmetric_keys) AS total;`.
  Attendu : `exitOK`, `res.Error` nul, `len(res.Rows) == 1`, `len(res.Messages) >= 16`.
- `TestManyNoticesInAParameterizedBatchDoNotHang` : args
  `[]any{sql.Named("p", 1)}`, lot `"SELECT 1 AS a;\n" + strings.Repeat("PRINT 'm';\n", 20) + "SELECT @p AS b;"`.
  Attendu : `exitOK`, `res.Error` nul, `len(res.Messages) == 20`,
  `len(res.MoreResults) == 1`.

Étape de rupture, obligatoire : remettre `retmsg.Message(ctx)` à la place de `next()`
(afficher la ligne par `grep -n`), constater que les deux tests échouent par timeout
après environ 10 s, remettre le relais.

Compte :
`-run '^(TestManyNoticesInsideOneSetDoNotHang|TestManyNoticesInAParameterizedBatchDoNotHang)$'`
2 tests.

## Tâche 1. Caviarder les notices et les plans (E1)

Fichiers : `tools/cmd/sqlq/main.go`, `tools/cmd/sqlq/integration_test.go`,
`tools/cmd/sqlq/redact_test.go`.

- `secretForms` gagne deux formes, pour le texte des plans : le secret échappé pour XML
  comme le fait SQL Server (`&` → `&amp;`, `<` → `&lt;`, `>` → `&gt;`, `"` → `&quot;`,
  `'` → `&apos;`, dans cet ordre, `&` d'abord), et la même chose appliquée au secret
  dont les apostrophes sont doublées (le littéral SQL, tel que
  `ParameterCompiledValue` le montre). Ajouter aussi le secret aux apostrophes doublées,
  non échappé. Mesure de l'échappement en E1. Un `strings.NewReplacer` suffit ; ne pas
  utiliser `xml.EscapeText`, qui écrit `&#39;` et `&#34;`.
- Dans `execute()`, juste après l'appel à `collect()`, passer chaque élément de
  `result.Messages` et `*result.Plan` (s'il n'est pas nul) par `redact(…, secret)`. Les
  emplacements d'erreurs suivantes sont écrits après, par `sqlError`, qui caviarde déjà.
  Le commentaire dit pourquoi : un canal de sortie neuf naît non filtré si personne ne
  le filtre. Les messages que `run()` ajoute ensuite sur un `-saved` viennent du
  catalogue et du registre locaux, jamais du serveur : ils ne passent pas par là, et le
  commentaire le dit.

Tests :

- `TestSecretFormsCoverXMLEscaping` (unitaire, `redact_test.go`) : pour le secret
  synthétique `a&b<c>d"e'f`, `redact` remplace par `[redacted]` chacune des chaînes
  `a&amp;b&lt;c&gt;d&quot;e&apos;f`, `a&amp;b&lt;c&gt;d&quot;e&apos;&apos;f` et
  `a&b<c>d"e''f`.
- `TestNoticesAreRedacted` (intégration) : secret obtenu par `resolve(p)` ; lot
  `PRINT '<secret, apostrophes doublées>'; SELECT 1 AS a;`. Attendu : `exitOK`, aucun
  élément de `res.Messages` ne contient le secret, au moins un contient `[redacted]`.
- `TestPlanIsRedacted` (intégration) : `wantPlan: true`, lot
  `SELECT COUNT(*) AS n FROM sys.objects WHERE name = '<secret, apostrophes doublées>';`.
  Attendu : `exitOK`, `res.Plan` non nul, ne contient aucune des formes de
  `secretForms(secret)`, contient `[redacted]`.
- `TestErrorTextIsRedacted` (intégration) : lot
  `RAISERROR('<secret, apostrophes doublées, % doublés>', 16, 1);`. Attendu : `exitSQL`,
  `res.Error.Message` ne contient pas le secret et contient `[redacted]`.
- Dans les trois tests d'intégration, en cas d'échec, n'imprimer que des comptes et des
  booléens, jamais `res.Messages`, `res.Plan` ni `res.Error` : le message d'échec
  écrirait le mot de passe dans le journal du test.

Étapes de rupture, chacune avec la ligne affichée par `grep -n` : retirer le caviardage
des messages, `TestNoticesAreRedacted` échoue ; retirer celui du plan,
`TestPlanIsRedacted` échoue ; retirer la forme XML de `secretForms`,
`TestSecretFormsCoverXMLEscaping` échoue. Si le mot de passe du labo ne contient aucun
caractère échappé par XML, `TestPlanIsRedacted` ne casse pas sur la troisième rupture :
le dire, c'est le test unitaire qui garde ce point.

Comptes : `go test ./cmd/sqlq/ -run '^(TestSecretFormsCoverXMLEscaping|TestNoticesAreRedacted|TestPlanIsRedacted|TestErrorTextIsRedacted)$'`
4 tests.

## Tâche 2. Le premier plan à la racine, les suivants dans `more_plans` (E2, D3)

Fichiers : `tools/internal/sqlq/result.go`, `tools/internal/sqlq/result_test.go`,
`tools/cmd/sqlq/main.go`, `tools/cmd/sqlq/integration_test.go`,
`plugins/sqlserver-toolkit/skills/live-query/SKILL.md`,
`plugins/sqlserver-toolkit/README.md`.

- `sqlq.Result` gagne `MorePlans []string \`json:"more_plans"\``, placé juste après
  `Plan`. `Result.MarshalJSON` le rend `[]` quand il est `nil`, comme `MoreResults`.
- Dans `collect()`, sur un jeu showplan : si `result.Plan == nil`, le plan va dans
  `result.Plan` ; sinon, s'il n'est pas `nil`, il est ajouté à `result.MorePlans`.
- Dans `execute()`, le caviardage de la tâche 1 couvre aussi chaque élément de
  `result.MorePlans`.
- `SKILL.md` : ajouter `"more_plans":[]` à l'objet JSON d'exemple, après `"plan":null`,
  et une puce : `plan` est le plan de la première instruction qui en a un ; les suivants
  sont dans `more_plans`, dans l'ordre d'exécution ; chaque plan contient le
  `StatementText` de son instruction ; aucun n'est rattaché à un jeu de `more_results`,
  parce que le flux du serveur ne le permet pas sans erreur (une instruction triviale
  sans table n'a pas de plan, un `UPDATE` en a un sans jeu de données). Réécrire les
  deux passages qui disent le contraire : la ligne du tableau « Why is this query
  slow » (ligne 89 : « only the last statement's plan is kept ») dit que `plan` est le
  premier, que les autres sont dans `more_plans`, et que `sqlplan` dessine `plan` ; la
  puce de « Known limits » (ligne 290) disparaît ou dit la même chose.
- `README.md`, lignes 152 et 153 (« only the last statement's plan is kept ») : même
  correction.

Tests :

- `TestMorePlansIsAlwaysAnArray` (unitaire, `result_test.go`) : `json.Marshal` d'un
  `Result{}` contient `"more_plans":[]`.
- `TestFirstPlanStaysAtTheRoot` (intégration) : `wantPlan: true`, lot
  `SELECT COUNT(*) AS a FROM sys.objects; SELECT COUNT(*) AS b FROM sys.columns;`.
  Attendu : `res.Plan` contient `FROM sys.objects`, `len(res.MorePlans) == 1`, et
  `res.MorePlans[0]` contient `FROM sys.columns`.

Étapes de rupture : remettre l'écrasement (`result.Plan = plan` sans condition),
`TestFirstPlanStaysAtTheRoot` échoue ; retirer la ligne de `MarshalJSON`,
`TestMorePlansIsAlwaysAnArray` échoue.

Comptes : `go test ./internal/sqlq/ -run '^TestMorePlansIsAlwaysAnArray$'` 1 ;
`go test ./cmd/sqlq/ -run '^TestFirstPlanStaysAtTheRoot$'` 1 ; vérifier par
`grep -n "last statement" plugins/sqlserver-toolkit/README.md plugins/sqlserver-toolkit/skills/live-query/SKILL.md`
qu'il ne reste rien.

## Tâche 3. `-maxmessages` et `-maxresultsets` (E3, D4)

Fichiers : `tools/cmd/sqlq/main.go`, `tools/internal/sqlq/result.go`,
`tools/cmd/sqlq/integration_test.go`, `tools/cmd/sqlq/flags_test.go`,
`plugins/sqlserver-toolkit/README.md`,
`plugins/sqlserver-toolkit/skills/live-query/SKILL.md`.

- `options` gagne `maxMessages int` et `maxResultSets int`. `defineFlags` déclare
  `-maxmessages` (défaut `200`, aide « maximum server messages to keep; 0 means
  unlimited ») et `-maxresultsets` (défaut `20`, aide « maximum data result sets, and
  plans, to keep; 0 means unlimited »). `options.validate()` refuse une valeur négative
  de l'un ou de l'autre ; le message reprend la forme de celui de `-maxrows` dans
  `validate()`, avec le nom du drapeau et la valeur reçue.
- `sqlq.Result` gagne, après `MoreResults`, `MoreResultsTruncated bool
  \`json:"more_results_truncated"\``, après `Messages`, `MessagesTruncated bool
  \`json:"messages_truncated"\``, et après `MorePlans`, `MorePlansTruncated bool
  \`json:"more_plans_truncated"\``.
- `collect()` reçoit les deux plafonds (passer `o` ou deux entiers, au choix ; mettre à
  jour tous les appels). Règles, toutes de D4 :
  - une notice est rangée tant que le nombre d'éléments de `result.Messages` est
    inférieur au plafond ; au-delà, `MessagesTruncated = true` et la boucle continue ;
  - une erreur suivante (`len(sqlErrs) > 1`) reçoit un emplacement sous la même règle ;
    au-delà, pas d'emplacement, `MessagesTruncated = true`. `errSlots` reste tel quel,
    avec un commentaire qui dit pourquoi l'appariement par position reste juste (D4) ;
  - un jeu de données est rangé tant que le nombre de jeux rangés, racine comprise, est
    inférieur au plafond ; au-delà, ses lignes sont lues par `scanAll` dans un
    `sqlq.NewRowSet(1)` jetable, `MoreResultsTruncated = true`, et `open` ne désigne
    aucun jeu (`-1`) ;
  - un plan est rangé tant que `plan` plus `more_plans` font moins que le plafond ;
    au-delà il est lu, jeté, et `MorePlansTruncated = true` ;
  - un plafond à `0` veut dire pas de plafond.
- Un commentaire dans `collect()`, au-dessus du `switch`, dit pourquoi
  `sqlexp.MsgRowsAffected` n'est pas traité : `SET NOCOUNT ON` du préambule le supprime,
  et le compte des lignes d'un jeu vient de `RowSet` (spec § 7).
- Documentation : les deux drapeaux dans `README.md` et `SKILL.md` ; dans `SKILL.md`,
  les trois drapeaux de troncature, dans l'exemple JSON et en une puce chacun, avec la
  règle « au-delà du plafond, sqlq lit tout et ne range plus ».

`TestManyMessagesDoNotHang` construit ses options par littéral : `maxMessages` y vaut
`0`, donc pas de plafond, et le test reste vrai sans modification. Ne pas le modifier.

Tests :

- `TestOptionsRejectNegativeCaps` (unitaire, `flags_test.go`, sans `t.Run`) : partir
  d'un `options` valide (même base que `TestOptionsRejectNonsenseValues`), vérifier que
  `maxMessages: -1` est refusé, que `maxResultSets: -1` est refusé, et que `0` est
  accepté pour les deux.
- `TestMessageCapStopsStoringNotReading` (intégration) : lot
  `"SELECT 1 AS a;\n" + strings.Repeat("PRINT 'm';\n", 40) + "SELECT 2 AS b;"`,
  `maxMessages: 10`. Attendu : `exitOK`, `len(res.Messages) == 10`,
  `res.MessagesTruncated`, `len(res.MoreResults) == 1` (le jeu `b`, après les 40
  notices, a été lu : la boucle a continué).
- `TestResultSetCapStopsStoringNotReading` (intégration) : lot
  `SELECT 1 AS a; SELECT 2 AS b; SELECT 3 AS c; SELECT 4 AS d; RAISERROR('after the cap', 16, 1);`,
  `maxResultSets: 2`. Attendu : `exitSQL`, `res.Error.Message` contient `after the cap`,
  `res.Rows` est le jeu `a`, `len(res.MoreResults) == 1` et c'est `b`,
  `res.MoreResultsTruncated`.
- `TestLaterErrorBeyondMessageCapIsDropped` (intégration) : lot
  `RAISERROR('e1', 16, 1); RAISERROR('e2', 16, 1); RAISERROR('e3', 16, 1); SELECT 1 AS a;`,
  `maxMessages: 1`. Attendu : `exitSQL`, `res.Error.Message` est `e1`, `res.Messages`
  vaut exactement `["error 50000: e2"]`, `res.MessagesTruncated`, `len(res.Rows) == 1`.
- `TestPlanCapStopsStoringNotReading` (intégration) : `wantPlan: true`,
  `maxResultSets: 1`, lot
  `SELECT COUNT(*) AS a FROM sys.objects; SELECT COUNT(*) AS b FROM sys.columns; RAISERROR('after the plans', 16, 1);`.
  Attendu : `exitSQL`, `res.Error.Message` contient `after the plans` (la boucle a lu
  au-delà du plan jeté), `res.Plan` non nul, `len(res.MorePlans) == 0`,
  `res.MorePlansTruncated`, `res.Rows` est le jeu `a`.

Étapes de rupture, chacune avec la ligne modifiée affichée par `grep -n` :

1. faire sortir `collect()` (`return`) quand le plafond de messages est atteint :
   `TestMessageCapStopsStoringNotReading` échoue ;
2. ne plus mettre `MoreResultsTruncated` à `true` :
   `TestResultSetCapStopsStoringNotReading` échoue ;
3. réserver un emplacement à une erreur jetée (ne plus tester le plafond pour les
   erreurs suivantes) : `TestLaterErrorBeyondMessageCapIsDropped` échoue ;
4. faire sortir `collect()` quand le plafond de plans est atteint :
   `TestPlanCapStopsStoringNotReading` échoue.

Rapporter « trois sur quatre ont échoué comme prévu » est un résultat de cette étape,
pas un échec de la tâche : dire lequel n'a pas échoué et pourquoi.

Comptes : `-run '^TestOptionsRejectNegativeCaps$'` 1 ;
`-run '^(TestMessageCapStopsStoringNotReading|TestResultSetCapStopsStoringNotReading|TestLaterErrorBeyondMessageCapIsDropped|TestPlanCapStopsStoringNotReading)$'`
4 ; `-run '^TestEveryFlagIsDocumented$'` 1.

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
  en D5), qu'on le cite tel quel, et qu'il compte dans `-maxmessages` (une ligne par
  table lue).

Tests d'intégration :

- `TestStatisticsArriveInMessages` : `wantStatistics: true`, lot
  `SELECT COUNT(*) AS n FROM sys.objects;`. Attendu : `exitOK`, au moins un message
  contient `logical reads` ou `lectures logiques`, et au moins un contient `CPU time`
  ou `Temps UC` (la langue du profil de test n'est pas connue d'avance).
- `TestStatisticsFollowTheSessionLanguage` : `wantStatistics: true`, lot
  `SET LANGUAGE French; SELECT COUNT(*) AS n FROM sys.objects;`. Attendu : un message
  contient `lectures logiques` et un contient `Temps UC` (mesurés en D5).

Étape de rupture : retirer l'ajout au préambule, constater que les deux tests échouent.

Comptes : `-run '^(TestStatisticsArriveInMessages|TestStatisticsFollowTheSessionLanguage)$'`
2 ; `-run '^TestEveryFlagIsDocumented$'` 1.

## Tâche 5. Timeout reconnu à la réception, jeu coupé gardé, première erreur gagnante (E5, E5b, D6, D7)

Fichiers : `tools/cmd/sqlq/main.go`, `tools/cmd/sqlq/integration_test.go`,
`plugins/sqlserver-toolkit/skills/live-query/SKILL.md`,
`plugins/sqlserver-toolkit/README.md`, `AGENTS.md`.

- Dans `collect()`, sur `MsgNextResultSet` : si `ctx.Err() != nil`, arrêter la boucle
  et rendre `ctx.Err()`, sans marquer de jeu (D6, point 1).
- Quand `scanAll` rend une erreur au milieu d'un jeu, ranger ce jeu (colonnes et lignes
  déjà lues, sous les règles de plafond de la tâche 3) avec `Incomplete: true` s'il a
  lu au moins une ligne, puis rendre l'erreur (D6, point 2).
- Après la boucle : retirer `if ctx.Err() != nil { return …, ctx.Err() }` et son
  commentaire ; rendre `rows.Err()` sans marquer de jeu (D6, point 3).
- Dans `execute()`, inverser la priorité : le cas `len(sqlErrs) > 0` passe avant
  `err != nil`. Quand les deux existent, `error` est `sqlErrs[0]` et la sortie est
  `exitSQL`.
- D7 : le commentaire de `exitConnected` devient « could not connect or start the
  query » ; dans `README.md` et `SKILL.md`, la description du code `4` dit la même
  chose, et celle du code `2` couvre tout échec après le démarrage, timeout et
  transport compris. Chercher les passages par `grep -n "transport\|exit code" `.
- `incomplete` : la définition dit « an error or the timeout cut that set short »
  dans `SKILL.md` (puce ligne 167), `README.md` (ligne 148) et `AGENTS.md`
  (ligne 113).

Tests d'intégration :

- `TestSetCutByTimeoutKeepsItsRows` : options `{maxRows: 5, timeoutSec: 1}`, lot
  `SELECT TOP (20000000) a.object_id FROM sys.all_columns AS a CROSS JOIN sys.all_columns AS b;`.
  Attendu : code différent de `exitOK`, `res.Error` non nul, `res.Incomplete`,
  `res.RowCount > 0`, `len(res.Rows) <= 5`. Mesures faites (ne pas les refaire sans
  `TOP`) : `sys.all_columns` a 12 835 lignes sur le labo, le produit sans `TOP` se lit
  en 93,6 s pour 164 737 225 lignes, et en une seconde la boucle de lecture en lit
  entre 1,7 et 2,2 millions. Avec `TOP (20000000)`, le lot dure 11,1 s sans timeout
  (mesuré par `sqlq -maxrows 1 -timeout 60`) : il ne finit pas en une seconde, et le
  serveur s'arrête de lui-même.
- `TestQueryEndingBeforeTheTimeoutSucceeds` : options `{maxRows: 5, timeoutSec: 3}`,
  lot `WAITFOR DELAY '00:00:01'; SELECT 1 AS a;`. Attendu : `exitOK`, `res.Error` nul,
  `res.Incomplete` faux, `len(res.Rows) == 1`.
- `TestFirstErrorWinsOverTheTimeout` : options `{maxRows: 5, timeoutSec: 1}`, lot
  `RAISERROR('first', 16, 1) WITH NOWAIT; RAISERROR('second', 16, 1) WITH NOWAIT; WAITFOR DELAY '00:00:05';`.
  Attendu : `exitSQL`, `res.Error.Number == 50000`, `res.Error.Message == "first"`,
  `res.Messages` contient `error 50000: second`.
- `TestTimeoutAfterACompleteSetLeavesItComplete` : options `{maxRows: 5, timeoutSec: 1}`,
  lot `SELECT 1 AS a; WAITFOR DELAY '00:00:05'; SELECT 2 AS b;`. Attendu : code
  différent de `exitOK`, `res.Error` non nul, `len(res.Rows) == 1`, `res.Incomplete`
  faux (le jeu `a` a été lu en entier avant le timeout).

Étapes de rupture, chacune avec la ligne affichée par `grep -n` :

1. ne plus marquer le jeu `incomplete` quand `scanAll` échoue :
   `TestSetCutByTimeoutKeepsItsRows` échoue ;
2. traiter tout `MsgNextResultSet` comme une coupure (remplacer la condition par
   `true`) : `TestQueryEndingBeforeTheTimeoutSucceeds` échoue, sur `len(res.Rows)` (le
   `DONE` du `WAITFOR` arrive avant le jeu) ;
3. remettre `err != nil` avant `len(sqlErrs) > 0` dans `execute()` :
   `TestFirstErrorWinsOverTheTimeout` échoue ;
4. marquer après la boucle le dernier jeu ouvert quand `rows.Err()` n'est pas nul (la
   version retirée) : `TestTimeoutAfterACompleteSetLeavesItComplete` échoue.

`TestTimeoutIsAnErrorNotPartialSuccess`, existant, reste vert dans la version finale.

Comptes :
`-run '^(TestSetCutByTimeoutKeepsItsRows|TestQueryEndingBeforeTheTimeoutSucceeds|TestFirstErrorWinsOverTheTimeout|TestTimeoutAfterACompleteSetLeavesItComplete|TestTimeoutIsAnErrorNotPartialSuccess)$'`
5.

## Tâche 6. Épingler les requêtes que la garde laisse passer (E6)

Fichier : `tools/internal/sqlq/guard_test.go`.

Deux tests unitaires, sur le modèle de `TestFindWritesAllowsReadOnlyDiagnostics` (même
fonction appelée, même forme d'assertion), mais sans `t.Run` :

- `TestFindWritesAllowsQueryStoreLiteralFilters` : la requête de la session d'origine,
  `SELECT TOP (1) query_sql_text FROM sys.query_store_query_text WHERE query_sql_text LIKE '%insert %' OR query_sql_text LIKE '%update %' OR query_sql_text LIKE '%merge %';`,
  n'est pas une écriture.
- `TestFindWritesAllowsKeywordsAfterAnUnderscore` :
  `SELECT 1 AS ITEM_CREATE, 2 AS ROW_INSERT, 3 AS [DELETE], 4 AS create_date;` n'est pas
  une écriture.

Le commentaire de chaque test cite son origine : une garde qui refuserait ces requêtes
rendrait l'outil inutilisable pour l'analyse du Query Store, sans que rien le signale.

Étapes de rupture : retirer `_` des caractères de mot dans `isWordRune`
(`tools/internal/sqlq/lex.go`), le second test échoue ; ne plus blanchir les littéraux
dans `Sanitize`, le premier échoue. Remettre le code à l'identique après chaque rupture
(`git diff` vide sur `lex.go` et `guard.go`).

Compte : `-run '^(TestFindWritesAllowsQueryStoreLiteralFilters|TestFindWritesAllowsKeywordsAfterAnUnderscore)$'`
2.

## Tâche 7. Documentation : droits et repli Query Store (E7), état de la spec

Fichiers : `plugins/sqlserver-toolkit/skills/live-query/SKILL.md`,
`plugins/sqlserver-toolkit/README.md`, `AGENTS.md`, `tools/cmd/sqlq/main.go`
(commentaire de tête seulement), `docs/spec-drafts/sqlq-server-messages.md`.

- Les quatre passages qui recommandent un login avec `db_datareader`,
  `VIEW DEFINITION` et `VIEW SERVER STATE` (`SKILL.md` ligne 276, `README.md`
  ligne 118, `AGENTS.md` ligne 121, `main.go` lignes 7 et 8) : `VIEW SERVER STATE`
  n'est pas un prérequis ; il ouvre les DMV de l'instance, dont `sys.dm_exec_sql_text`
  (le texte des requêtes en cours, valeurs littérales comprises) ; sans lui il reste les
  catalogues, les DMV de la base avec `VIEW DATABASE STATE`, et le Query Store. Le
  commentaire de `main.go` en une ligne ; le détail dans `SKILL.md`.
- `SKILL.md`, une courte section sur le repli Query Store (spec § 6) :
  `sys.query_store_runtime_stats` donne `avg_logical_io_reads` et `avg_duration` en jeu
  de résultats ordinaire, avec `VIEW DATABASE STATE`. Ce sont des moyennes sur un
  intervalle d'agrégation, écrites avec un délai : un indice historique pour trouver les
  requêtes coûteuses, pas la mesure d'un appel. Pour mesurer un appel, `-statistics`.
- Spec : remplacer le bloc de statut en tête par un statut daté du 25 octobre 2026 :
  implémentée par `67ea7fb` et par ce plan ; les décisions D0 à D7 de ce plan priment
  sur les §§ 4.2, 4.3, la contrainte 3 et le code de sortie 4 ; renvoi vers ce plan et
  son registre. Ne pas réécrire le reste : c'est l'historique des revues.

Compte : la suite complète ; `TestEveryFlagIsDocumented` reste vert ;
`grep -n "VIEW SERVER STATE" plugins/sqlserver-toolkit/README.md AGENTS.md tools/cmd/sqlq/main.go plugins/sqlserver-toolkit/skills/live-query/SKILL.md`
ne montre plus aucun passage qui le range dans le minimum.

## Tâche 8. Les cas restants de la matrice (E8)

Fichier : `tools/cmd/sqlq/integration_test.go`.

- `TestInvalidObjectIsASQLError` : lot `SELECT 1 AS a FROM dbo.sqlq_no_such_table;`.
  Attendu : `exitSQL`, `res.Error.Number == 208`, `len(res.Rows) == 0`.
- `TestEmptySetThenPopulatedSet` : lot `SELECT 1 AS a WHERE 1 = 0; SELECT 2 AS b;`.
  Attendu : `exitOK`, `len(res.Rows) == 0`, `res.RowCount == 0`, `len(res.Columns) == 1`,
  `len(res.MoreResults) == 1`, `res.MoreResults[0].RowCount == 1`, aucun `incomplete`.
- `TestNoticesAroundASetKeepTheirOrder` : lot `PRINT 'before'; SELECT 1 AS a; PRINT 'after';`.
  Attendu : `exitOK`, `res.Messages` vaut exactement `["before", "after"]`,
  `len(res.Rows) == 1`.
- `TestCombinedOutputShape` : `wantPlan: true`, lot
  `PRINT 'n'; SELECT COUNT(*) AS a FROM sys.objects; SELECT COUNT(*) AS b FROM sys.columns; RAISERROR('boom', 16, 1);`.
  Passer `res` par `json.Marshal`, décoder dans un `map[string]any`. Attendu :
  l'ensemble des clés de premier niveau est exactement celui des étiquettes `json` de
  `sqlq.Result` (lues dans `result.go` au moment de la tâche, pas recopiées de ce
  plan) ; `more_results` est un tableau d'un élément dont les clés sont exactement
  celles de `sqlq.ResultSet` ; `more_plans` est un tableau d'un élément ; `messages`
  vaut `["n"]` ; `error.number` vaut `50000` ; les trois drapeaux `*_truncated` valent
  `false`. Sortie `exitSQL`.

Pas d'étape de rupture propre : ces tests épinglent un comportement existant. Les
lancer une fois sur une copie où l'on casse ce qu'ils gardent (par exemple, ranger les
notices en ordre inverse) et dire lesquels tombent.

Compte :
`-run '^(TestInvalidObjectIsASQLError|TestEmptySetThenPopulatedSet|TestNoticesAroundASetKeepTheirOrder|TestCombinedOutputShape)$'`
4.

## Fin

- Suite complète sous `-race`, intégration comprise, dans une seule invocation. Départ
  mesuré le 25 octobre 2026 : 40 lignes `--- PASS` pour `go test ./cmd/sqlq/ -count=1 -v`
  (sous-tests compris). Attendu à la fin : 40 + 2 (tâche 0) + 4 (tâche 1) + 1 (tâche 2)
  + 5 (tâche 3) + 2 (tâche 4) + 4 (tâche 5) + 4 (tâche 8) = 62 lignes. Pour
  `./internal/sqlq/`, le compte de départ plus 1 (tâche 2) plus 2 (tâche 6).
- `go vet ./...`, `gofmt -l .` vides.
- Rebâtir le binaire (`go install ./cmd/sqlq`) et refaire à la main les deux lots de E0,
  la mesure de E2 et celle de D5.
- Fusion sur `main` en local, sans push (décision de Rudi du 25 octobre 2026). Rebaser
  d'abord sur `main` s'il a bougé ; si le dépôt principal a des modifications non
  commitées sur un fichier que la branche touche, ne rien écraser : laisser la branche
  non fusionnée et le dire.
