# Catalogue de requêtes `sqlq` : plan d'implémentation

> For agentic workers: REQUIRED SUB-SKILL: use superpowers:subagent-driven-development
> (recommended) or superpowers:executing-plans to implement this plan task by task. Steps
> use checkbox (`- [ ]`) syntax for tracking.

Goal : donner à `sqlq` un catalogue de requêtes appelables par leur nom (`-list-queries`,
`-saved`, `-save-query`), alimenté par les requêtes livrées, une couche personnelle et les
scripts marqués de tsql-scripts, et rendre tous les jeux de résultats et les messages
serveur.

Architecture : tout ce qui doit être vrai vit dans `tools/internal/sqlq/`, en fichiers à
responsabilité unique (lexème, refus, en-têtes, paramètres, surcharge, registre,
catalogue, sauvegarde), testés sans instance. `tools/cmd/sqlq/main.go` ne fait que câbler
les drapeaux, l'exécution et la sortie JSON. L'exécution passe au flux de messages de
`sqlexp` pour lire chaque jeu de résultats et chaque `PRINT`.

Tech stack : Go 1.27, `github.com/microsoft/go-mssqldb` v1.11.0, `github.com/golang-sql/sqlexp`
v0.1.0 et `github.com/golang-sql/civil` (déjà en dépendances indirectes, promus en directes),
bibliothèque standard pour le reste.

Spec : `docs/superpowers/specs/2026-10-04-query-catalog-design.md` (v2). Les numéros de
paragraphe cités ci-dessous (§7, §11…) sont ceux de la spec. Lire la spec avant chaque
tâche : le plan dit comment, la spec dit pourquoi, et en cas de désaccord entre les deux,
s'arrêter et le signaler.

## Global Constraints

- Aucune nouvelle dépendance hors `sqlexp` et `civil`, qui sont déjà dans `go.sum`.
- Toutes les commandes Go se lancent depuis `tools/` : `cd tools && go test ./... && go vet ./...`.
- `sqlq` imprime un seul objet JSON sur stdout, en succès comme en échec. Codes de sortie
  inchangés : `0` succès, `1` usage ou configuration, `2` erreur SQL, `3` refus du
  garde-fou, `4` connexion.
- Nom de requête : `^[a-z][a-z0-9-]{1,48}$`, validé avant toute construction de chemin.
- Segment de répertoire de profil : `^[a-z0-9][a-z0-9._-]*$`, ni `.` ni `..`.
- `sqlq` n'écrit jamais dans le plugin ni dans tsql-scripts ; il n'écrit que sous
  `~/.config/db-ai-toolkit/queries/profiles/<profil>/` et
  `~/.config/db-ai-toolkit/verified.json`.
- Rien de ce que `-list-queries` imprime ne contient un chemin absolu, un nom de serveur,
  un nom de base, ni un extrait de SQL.
- Les tests unitaires ne touchent ni le vrai clone tsql-scripts ni le vrai
  `~/.config/db-ai-toolkit/` : répertoires temporaires (`t.TempDir()`) et `testdata/`.
- Les tests d'intégration lisent `$SQLQ_TEST_PROFILES` et `$SQLQ_TEST_PROFILE`, et appellent
  `t.Skip` si l'une manque. Ils ne créent aucun objet sur l'instance.
- Messages de commit : prose qui explique pourquoi, sans gras ni tiret cadratin, et sans
  aucune ligne d'attribution (`Co-Authored-By`, `Generated with`, etc.).

## Review Focus

Les entrées que la spec implique et qu'aucun test unitaire ne couvre naturellement, la plus
probable en premier, chacune épinglée par un test dans la tâche qui possède le code :

1. Un fichier CRLF de tsql-scripts dont la ligne `DECLARE` surchargée finit par `;\r` :
   la réécriture doit garder le `\r` et ne pas inclure `\r` dans l'initialiseur
   (tâche 5, `TestOverrideKeepsCRLF`).
2. Un initialiseur vide dans le texte nettoyé mais pas dans l'original (`= '';`) : il doit
   être accepté (tâche 5, `TestEmptyStringDefaultIsAccepted`).
3. Un `-param` passé deux fois pour le même nom : refus, pas « le dernier gagne »
   (tâche 9, `TestDuplicateParamIsRefused`).
4. Un fichier livré dont l'en-tête est valide mais dont le nom de fichier ne respecte pas la
   règle de nommage : `rejected`, et le test des requêtes livrées tombe (tâche 7,
   `TestInvalidFileNameIsRejected`).
5. `-list-queries` lancé sans fichier de profils : il liste quand même le catalogue sans
   profil (tâche 9, `TestListQueriesWorksWithoutProfilesFile`).

---

## Structure des fichiers

| Fichier | Responsabilité |
|---|---|
| `tools/internal/sqlq/lex.go` | découpe du texte nettoyé en jetons avec ligne, offsets en runes et profondeur de parenthèses |
| `tools/internal/sqlq/refusals.go` | `Refusals()` : les refus du garde-fou avec mot-clé et ligne, partagés par `main.go`, les tests et le catalogue |
| `tools/internal/sqlq/header.go` | analyse des deux formats d'en-tête et du marqueur |
| `tools/internal/sqlq/params.go` | paramètres déduits d'une requête `bundled` ou `personal`, et `DirtyReads()` |
| `tools/internal/sqlq/override.go` | analyse, réécriture et liaison typée des `DECLARE` surchargés |
| `tools/internal/sqlq/registry.go` | le registre `verified.json` |
| `tools/internal/sqlq/catalog.go` | sources, vue, collisions, entrées `rejected` |
| `tools/internal/sqlq/save.go` | le contenu du fichier écrit par `-save-query` |
| `tools/internal/sqlq/result.go` | ajout de `MoreResults` et `Saved` |
| `tools/cmd/sqlq/main.go` | drapeaux, exécution à jeux multiples, `-list-queries`, `-saved`, `-save-query` |
| `tools/cmd/sqlq/integration_test.go` | tests contre une instance, sautés sans elle |
| `plugins/sqlserver-toolkit/skills/live-query/SKILL.md`, `plugins/sqlserver-toolkit/README.md`, `AGENTS.md` | documentation |

Chaque tâche ci-dessous liste les noms exacts qu'elle consomme et produit. Un implémenteur
ne voit que sa tâche : ces blocs sont la façon dont il apprend les noms des voisines.

---

### Task 1 : lexème et refus partagés

Ferme aussi la tâche Todoist « sqlq : partager la liste des refus entre main.go et le test
des requêtes livrées » (`6hg68cRcMChRpR9p`).

Files :
- Create : `tools/internal/sqlq/lex.go`, `tools/internal/sqlq/lex_test.go`
- Create : `tools/internal/sqlq/refusals.go`, `tools/internal/sqlq/refusals_test.go`
- Modify : `tools/internal/sqlq/bundled_queries_test.go` (supprimer la fonction locale
  `refusals` et `TestRefusalsCatchesWhatSqlqRefuses`, appeler `Refusals`)
- Modify : `tools/cmd/sqlq/main.go:178-221` (les trois contrôles appellent `Refusals`)

Interfaces :
- Consumes : `Sanitize`, `FindWrites`, `FindContextChanges`, `FindBatchSeparators`,
  `writeKeywords`, `contextKeywords` (existants, `guard.go`).
- Produces :
  - `type Token struct { Text string; Line, Start, End, Depth int }`
  - `func Lex(sanitized string) []Token`
  - `type Refusal struct { Kind RefusalKind; Keyword string; Line int; Statement string }`
  - `type RefusalKind int` avec `RefusalWrite`, `RefusalContext`, `RefusalSeparator`
  - `func Refusals(sql string) []Refusal` : au plus un refus par genre, dans l'ordre
    écriture, contexte, séparateur.
  - `func (r Refusal) Reason() string` : `write keyword EXEC at line 12`, `USE at line 1`,
    `batch separator GO at line 9`. Jamais d'extrait de texte.

- [ ] Step 1 : écrire les tests qui échouent

`tools/internal/sqlq/lex_test.go` :

```go
package sqlq

import "testing"

func TestLexTracksLineOffsetAndDepth(t *testing.T) {
	src := "SELECT IIF(@a = 1, 2, 3);\r\nSET @b += 1;"
	toks := Lex(Sanitize(src))
	want := []struct {
		text  string
		line  int
		depth int
	}{
		{"SELECT", 1, 0}, {"IIF", 1, 0}, {"(", 1, 0}, {"@a", 1, 1}, {"=", 1, 1},
		{"1", 1, 1}, {",", 1, 1}, {"2", 1, 1}, {",", 1, 1}, {"3", 1, 1}, {")", 1, 0},
		{";", 1, 0}, {"SET", 2, 0}, {"@b", 2, 0}, {"+=", 2, 0}, {"1", 2, 0}, {";", 2, 0},
	}
	if len(toks) != len(want) {
		t.Fatalf("got %d tokens %v, want %d", len(toks), toks, len(want))
	}
	for i, w := range want {
		if toks[i].Text != w.text || toks[i].Line != w.line || toks[i].Depth != w.depth {
			t.Errorf("token %d = %+v, want %+v", i, toks[i], w)
		}
	}
	// Offsets are rune offsets into the text, so they survive multi-byte runes.
	rs := []rune("-- é\nSELECT @x;")
	toks = Lex(Sanitize(string(rs)))
	if got := string(rs[toks[1].Start:toks[1].End]); got != "@x" {
		t.Errorf("rune offsets point at %q, want @x", got)
	}
}
```

`tools/internal/sqlq/refusals_test.go` :

```go
package sqlq

import "testing"

func TestRefusalsGiveKeywordAndLine(t *testing.T) {
	cases := []struct {
		sql, reason string
	}{
		{"SELECT 1;\nDELETE FROM dbo.T;", "write keyword DELETE at line 2"},
		{"\n\nUSE master;\nSELECT 1;", "USE at line 3"},
		{"SELECT 1;\nGO\nSELECT 2;", "batch separator GO at line 2"},
		{"SELECT 'DROP TABLE x' AS s; -- EXEC", ""},
	}
	for _, c := range cases {
		r := Refusals(c.sql)
		got := ""
		if len(r) > 0 {
			got = r[0].Reason()
		}
		if got != c.reason {
			t.Errorf("Refusals(%q)[0].Reason() = %q, want %q", c.sql, got, c.reason)
		}
	}
}

// Refusals must refuse exactly what the three historical guards refuse, or the
// catalogue and the command line would disagree about the same file.
func TestRefusalsAgreeWithTheGuards(t *testing.T) {
	corpus := []string{
		"SELECT name FROM sys.objects;",
		"SELECT create_date, TAG_CREATE FROM t;",
		"SELECT * INTO #t FROM sys.objects;",
		"EXEC sp_who;",
		"USE tempdb;",
		"SELECT 1\nGO 2\n",
		"SELECT * FROM OPENQUERY(L, 'DELETE FROM t');",
		"SELECT 'GO' AS g;\n/* GO */ SELECT 2;",
	}
	for _, sql := range corpus {
		kinds := map[RefusalKind]bool{}
		for _, r := range Refusals(sql) {
			kinds[r.Kind] = true
		}
		if kinds[RefusalWrite] != (len(FindWrites(sql)) > 0) {
			t.Errorf("%q: write refusal %v, FindWrites %v", sql, kinds[RefusalWrite], FindWrites(sql))
		}
		if kinds[RefusalContext] != (len(FindContextChanges(sql)) > 0) {
			t.Errorf("%q: context refusal disagrees with FindContextChanges", sql)
		}
		if kinds[RefusalSeparator] != (len(FindBatchSeparators(sql)) > 0) {
			t.Errorf("%q: separator refusal disagrees with FindBatchSeparators", sql)
		}
	}
}
```

- [ ] Step 2 : vérifier qu'ils échouent

Run : `cd tools && go test ./internal/sqlq -run '^(TestLex|TestRefusals)' -count=1 -v`
Expected : échec de compilation (`undefined: Lex`, `undefined: Refusals`).

- [ ] Step 3 : implémenter

`tools/internal/sqlq/lex.go` :

```go
package sqlq

import "unicode"

// Token is one lexical unit of sanitized SQL. Start and End are rune offsets,
// which Sanitize preserves, so they index the original text as well.
type Token struct {
	Text  string
	Line  int // 1-based
	Start int
	End   int
	Depth int // parenthesis depth the token sits at; "(" and ")" sit at the outer level
}

// twoCharOps are the operators that must not be split, because "+=" assigns
// and "=" may only compare.
var twoCharOps = map[string]bool{
	"+=": true, "-=": true, "*=": true, "/=": true, "%=": true, "&=": true,
	"|=": true, "^=": true, "<=": true, ">=": true, "<>": true, "!=": true,
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '@' || r == '#' || r == '$'
}

// Lex splits sanitized SQL into words and punctuation. Unlike tokens(), it keeps
// operators, commas, parentheses and semicolons, which is what the parameter
// and override analyses reason about.
func Lex(sanitized string) []Token {
	rs := []rune(sanitized)
	var out []Token
	line, depth := 1, 0
	for i := 0; i < len(rs); {
		r := rs[i]
		switch {
		case r == '\n':
			line++
			i++
		case unicode.IsSpace(r):
			i++
		case isWordRune(r):
			j := i
			for j < len(rs) && isWordRune(rs[j]) {
				j++
			}
			out = append(out, Token{Text: string(rs[i:j]), Line: line, Start: i, End: j, Depth: depth})
			i = j
		default:
			if i+1 < len(rs) && twoCharOps[string(rs[i:i+2])] {
				out = append(out, Token{Text: string(rs[i : i+2]), Line: line, Start: i, End: i + 2, Depth: depth})
				i += 2
				continue
			}
			if r == ')' && depth > 0 {
				depth--
			}
			out = append(out, Token{Text: string(r), Line: line, Start: i, End: i + 1, Depth: depth})
			if r == '(' {
				depth++
			}
			i++
		}
	}
	return out
}
```

`tools/internal/sqlq/refusals.go` :

```go
package sqlq

import (
	"fmt"
	"strings"
)

// RefusalKind says which guard refused a batch. The kinds need different exit
// codes and messages on the command line.
type RefusalKind int

const (
	RefusalWrite RefusalKind = iota
	RefusalContext
	RefusalSeparator
)

// Refusal is one reason sqlq will not run a batch.
type Refusal struct {
	Kind      RefusalKind
	Keyword   string
	Line      int
	Statement string // sanitized statement text; for interactive messages only, never the catalogue
}

// Reason names the keyword and the line, never the text: the catalogue prints
// it on every session, and a statement can carry identifiers.
func (r Refusal) Reason() string {
	switch r.Kind {
	case RefusalWrite:
		return fmt.Sprintf("write keyword %s at line %d", r.Keyword, r.Line)
	case RefusalContext:
		return fmt.Sprintf("%s at line %d", r.Keyword, r.Line)
	default:
		return fmt.Sprintf("batch separator GO at line %d", r.Line)
	}
}

// Refusals lists why sqlq will not run this text: at most one refusal per kind,
// writes first, then context changes, then batch separators. It is the single
// place main.go, the bundled-query test and the catalogue ask.
func Refusals(sql string) []Refusal {
	var out []Refusal
	toks := Lex(Sanitize(sql))
	firstOf := func(set map[string]bool) (Token, bool) {
		for _, t := range toks {
			if set[strings.ToUpper(t.Text)] {
				return t, true
			}
		}
		return Token{}, false
	}
	if v := FindWrites(sql); len(v) > 0 {
		t, _ := firstOf(writeKeywords)
		out = append(out, Refusal{Kind: RefusalWrite, Keyword: v[0].Keyword, Line: t.Line, Statement: v[0].Statement})
	}
	if c := FindContextChanges(sql); len(c) > 0 {
		t, _ := firstOf(contextKeywords)
		out = append(out, Refusal{Kind: RefusalContext, Keyword: c[0].Keyword, Line: t.Line, Statement: c[0].Statement})
	}
	if lines := FindBatchSeparators(sql); len(lines) > 0 {
		out = append(out, Refusal{Kind: RefusalSeparator, Keyword: "GO", Line: lines[0]})
	}
	return out
}
```

Le mot-clé vient de `FindWrites` (même règle qu'aujourd'hui) ; la ligne vient du premier
jeton de la même famille. Si une mesure montre que les deux peuvent désigner des mots-clés
différents, s'arrêter et le signaler plutôt que d'ajuster le test.

Dans `bundled_queries_test.go`, remplacer l'appel `refusals(string(body))` par :

```go
			if r := Refusals(string(body)); len(r) > 0 {
				t.Errorf("sqlq would refuse this bundled query: %s", r[0].Reason())
			}
```

et supprimer la fonction `refusals` et `TestRefusalsCatchesWhatSqlqRefuses` (remplacée par
`TestRefusalsAgreeWithTheGuards`). Retirer l'import `fmt` et `strings` s'ils ne servent
plus.

Dans `main.go`, remplacer les trois blocs `FindWrites` / `FindContextChanges` /
`FindBatchSeparators` de `run` par une boucle sur `sqlq.Refusals(sqlText)` qui garde
exactement les messages, l'ordre et les codes actuels : écriture (`exitRefused`, sauf
profil `readwrite` avec `-allow-write`), contexte (`exitRefused`), séparateur
(`exitUsage`). Les messages gardent `truncate(r.Statement, 120)` comme aujourd'hui : ils
répondent à l'appelant d'une requête qu'il vient d'écrire, pas au catalogue.

- [ ] Step 4 : vérifier que tout passe

Run : `cd tools && go test ./internal/sqlq -run '^(TestLex|TestRefusals|TestBundledQueriesPassTheReadOnlyGuard)' -count=1 -v`
Expected : `TestLexTracksLineOffsetAndDepth`, `TestRefusalsGiveKeywordAndLine`,
`TestRefusalsAgreeWithTheGuards` et `TestBundledQueriesPassTheReadOnlyGuard` (avec ses
7 sous-tests) passent. Exactement 4 tests de premier niveau ; tout autre nombre veut dire
que le filtre ou le travail est faux.

Puis : `cd tools && go test ./... -count=1 && go vet ./...`, tout vert.

- [ ] Step 5 : casser pour voir tomber

Dans `Refusals`, remplacer `Line: t.Line` par `Line: 1` pour l'écriture. Relancer le
filtre du step 4 : `TestRefusalsGiveKeywordAndLine` doit tomber (cas `DELETE` à la ligne 2),
les trois autres restent verts. « 3 sur 4 » est le succès de cette étape. Remettre le code.

- [ ] Step 6 : commit

```bash
git add tools/internal/sqlq/lex.go tools/internal/sqlq/lex_test.go \
        tools/internal/sqlq/refusals.go tools/internal/sqlq/refusals_test.go \
        tools/internal/sqlq/bundled_queries_test.go tools/cmd/sqlq/main.go
git commit -m "refactor(sqlq): one Refusals() for main, the tests and the catalogue" -m "The bundled-query test copied the order of main.go's three guards, so a fourth refusal added to main.go would have let a bundled query pass its test and fail in front of an instance. The catalogue needs the same answer with a line number and no statement text, because it prints refusal reasons on every session. A lexer that keeps operators and parenthesis depth comes with it, since the parameter analyses need both."
```

---

### Task 2 : jeux de résultats multiples et messages

Spec §5.

Files :
- Modify : `tools/internal/sqlq/result.go`, `tools/internal/sqlq/result_test.go`
- Modify : `tools/cmd/sqlq/main.go` (`execute`, `collect`)
- Create : `tools/cmd/sqlq/integration_test.go`
- Modify : `tools/go.mod`, `tools/go.sum` (`go mod tidy` promeut `sqlexp` en direct)

Interfaces :
- Produces :
  - `type ResultSet struct { Columns []Column; Rows []Row; RowCount int; Truncated bool }`
    (tags JSON `columns`, `rows`, `rowcount`, `truncated`)
  - champ `Result.MoreResults []ResultSet` (tag `more_results`, toujours un tableau)
  - `func testProfile(t *testing.T) (sqlq.Profile, sqlq.SecretResolver)` dans
    `integration_test.go`, réutilisé par les tâches 9 et 10.

- [ ] Step 1 : test unitaire de forme JSON qui échoue

Ajouter à `result_test.go` :

```go
func TestMoreResultsIsAlwaysAnArray(t *testing.T) {
	b, err := json.Marshal(Result{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"more_results":[]`) {
		t.Errorf("empty result should carry more_results:[], got %s", b)
	}
	b, _ = json.Marshal(Result{MoreResults: []ResultSet{{}}})
	if !strings.Contains(string(b), `"more_results":[{"columns":[],"rows":[],"rowcount":0,"truncated":false}]`) {
		t.Errorf("an empty extra set should still have arrays, got %s", b)
	}
}
```

(ajouter `strings` aux imports si absent.)

- [ ] Step 2 : test d'intégration qui échoue

`tools/cmd/sqlq/integration_test.go` :

```go
package main

import (
	"os"
	"strings"
	"testing"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/sqlq"
)

// testProfile loads the profile named by $SQLQ_TEST_PROFILE from the file named
// by $SQLQ_TEST_PROFILES, or skips. The profile must be readonly: these tests
// create nothing.
func testProfile(t *testing.T) (sqlq.Profile, sqlq.SecretResolver) {
	t.Helper()
	path, name := os.Getenv("SQLQ_TEST_PROFILES"), os.Getenv("SQLQ_TEST_PROFILE")
	if path == "" || name == "" {
		t.Skip("SQLQ_TEST_PROFILES and SQLQ_TEST_PROFILE not set; no instance to test against")
	}
	profiles, err := sqlq.LoadProfiles(path)
	if err != nil {
		t.Fatal(err)
	}
	p, err := profiles.Get(name)
	if err != nil {
		t.Fatal(err)
	}
	if !p.ReadOnly() {
		t.Fatalf("profile %q must be readonly for the integration tests", name)
	}
	r := sqlq.NewResolver(os.Getenv, "", func(string) {})
	return p, r.Resolve
}

func TestEveryResultSetIsReturned(t *testing.T) {
	p, resolve := testProfile(t)
	res, code := execute(p, "SELECT 1 AS a; SELECT 2 AS b UNION ALL SELECT 3;", nil,
		options{maxRows: 1, timeoutSec: 30}, resolve)
	if code != exitOK || res.Error != nil {
		t.Fatalf("code %d, error %+v", code, res.Error)
	}
	if len(res.Rows) != 1 || res.Rows[0]["a"] != int64(1) {
		t.Errorf("first set: %+v", res.Rows)
	}
	if len(res.MoreResults) != 1 {
		t.Fatalf("want 1 extra set, got %d", len(res.MoreResults))
	}
	extra := res.MoreResults[0]
	if extra.RowCount != 2 || !extra.Truncated || len(extra.Rows) != 1 {
		t.Errorf("-maxrows applies per set: %+v", extra)
	}
}

func TestMessagesAreCaptured(t *testing.T) {
	p, resolve := testProfile(t)
	res, code := execute(p, "PRINT 'hello from print'; RAISERROR('low severity', 10, 1); SELECT 1 AS a;", nil,
		options{maxRows: 50, timeoutSec: 30}, resolve)
	if code != exitOK {
		t.Fatalf("code %d, error %+v", code, res.Error)
	}
	joined := strings.Join(res.Messages, "\n")
	if !strings.Contains(joined, "hello from print") || !strings.Contains(joined, "low severity") {
		t.Errorf("messages = %q", res.Messages)
	}
}

func TestErrorAfterFirstSetKeepsRows(t *testing.T) {
	p, resolve := testProfile(t)
	res, code := execute(p, "SELECT 1 AS a; SELECT 1/0 AS b;", nil,
		options{maxRows: 50, timeoutSec: 30}, resolve)
	if code != exitSQL || res.Error == nil || res.Error.Number != 8134 {
		t.Fatalf("want exit 2 with error 8134, got %d %+v", code, res.Error)
	}
	if len(res.Rows) != 1 {
		t.Errorf("rows read before the error must be kept: %+v", res.Rows)
	}
}
```

Préparer l'instance de test et lancer, dans la même invocation du shell (l'état ne survit
pas d'un appel à l'autre) :

```bash
cd tools && \
podman start dbai-catalog-test >/dev/null 2>&1 || podman run -d --name dbai-catalog-test \
  -e ACCEPT_EULA=Y -e MSSQL_SA_PASSWORD="$DBAI_TEST_SA_PASSWORD" -p 11544:1433 \
  mcr.microsoft.com/mssql/server:2025-latest >/dev/null && \
until podman exec dbai-catalog-test /opt/mssql-tools18/bin/sqlcmd -C -S localhost -U sa \
  -P "$DBAI_TEST_SA_PASSWORD" -Q "SELECT 1" >/dev/null 2>&1; do sleep 2; done && \
export SQLQ_TEST_PROFILES="$DBAI_TEST_PROFILES" SQLQ_TEST_PROFILE=catalog-test \
       MSSQL_CATALOG_TEST_PWD="$DBAI_TEST_SA_PASSWORD" && \
go test ./cmd/sqlq -run '^(TestEveryResultSetIsReturned|TestMessagesAreCaptured|TestErrorAfterFirstSetKeepsRows|TestTimeoutIsAnErrorNotPartialSuccess)$' -count=1 -v
```

Le contrôleur fournit `DBAI_TEST_SA_PASSWORD` et `DBAI_TEST_PROFILES` (un fichier de
profils hors dépôt qui contient
`{"catalog-test":{"server":"localhost,11544","database":"master","auth":"sql","user":"sa","passwordEnv":"MSSQL_CATALOG_TEST_PWD","mode":"readonly"}}`).
Ne jamais écrire le mot de passe dans un fichier du dépôt ni dans un message.

Expected : `TestEveryResultSetIsReturned` échoue à la compilation (`res.MoreResults
undefined`). Les tests `SKIP` au lieu de `FAIL` veulent dire que les variables manquent :
c'est un échec du setup, pas un succès.

- [ ] Step 3 : implémenter

Dans `result.go`, ajouter :

```go
// ResultSet is one result set after the first. The first stays in Result's own
// columns, rows, rowcount and truncated, so a single-query caller sees nothing new.
type ResultSet struct {
	Columns   []Column `json:"columns"`
	Rows      []Row    `json:"rows"`
	RowCount  int      `json:"rowcount"`
	Truncated bool     `json:"truncated"`
}

func (s ResultSet) MarshalJSON() ([]byte, error) {
	type alias ResultSet
	out := alias(s)
	if out.Columns == nil {
		out.Columns = []Column{}
	}
	if out.Rows == nil {
		out.Rows = []Row{}
	}
	return json.Marshal(out)
}
```

et le champ `MoreResults []ResultSet `json:"more_results"`` dans `Result`, placé après
`Truncated`, avec dans `Result.MarshalJSON` : `if out.MoreResults == nil { out.MoreResults = []ResultSet{} }`.

Dans `main.go`, `execute` passe un `*sqlexp.ReturnMessage` et délègue à `collect`, qui lit
le flux de messages au lieu de `NextResultSet` seul :

```go
	retmsg := &sqlexp.ReturnMessage{}
	rows, err := conn.QueryContext(ctx, sqlText, append(args, retmsg)...)
	if err != nil {
		result.ElapsedMS = time.Since(started).Milliseconds()
		result.Error = sqlError(err, secret)
		return result, exitSQL
	}
	defer rows.Close()

	sqlErr, err := collect(ctx, rows, retmsg, &result, o.maxRows)
	result.ElapsedMS = time.Since(started).Milliseconds()
	switch {
	case err != nil:
		result.Error = sqlError(err, secret)
		return result, exitSQL
	case sqlErr != nil:
		result.Error = sqlError(sqlErr, secret)
		return result, exitSQL
	}
	return result, exitOK
```

```go
// collect reads every result set and every informational message, in the order
// the server sends them. The first non-showplan set fills the result's own
// fields, later ones go to MoreResults, and a showplan goes to Plan. The first
// SQL error is returned separately so the sets read before it are kept.
func collect(ctx context.Context, rows *sql.Rows, retmsg *sqlexp.ReturnMessage,
	result *sqlq.Result, maxRows int) (sqlErr error, err error) {
	first := true
	for active := true; active; {
		switch m := retmsg.Message(ctx).(type) {
		case sqlexp.MsgNotice:
			result.Messages = append(result.Messages, m.Message.String())
		case sqlexp.MsgError:
			if sqlErr == nil {
				sqlErr = m.Error
			}
		case sqlexp.MsgNext:
			cols, err := rows.ColumnTypes()
			if err != nil {
				return sqlErr, err
			}
			if isShowplan(cols) {
				plan, err := readSingleString(rows)
				if err != nil {
					return sqlErr, err
				}
				result.Plan = plan
				continue
			}
			set := sqlq.NewRowSet(maxRows)
			columns := make([]sqlq.Column, len(cols))
			for i, c := range cols {
				columns[i] = sqlq.Column{Name: c.Name(), Type: c.DatabaseTypeName()}
			}
			if err := scanAll(rows, cols, set); err != nil {
				return sqlErr, err
			}
			if first {
				result.Columns, result.Rows = columns, set.Rows
				result.RowCount, result.Truncated = set.RowCount, set.Truncated
				first = false
			} else {
				result.MoreResults = append(result.MoreResults, sqlq.ResultSet{
					Columns: columns, Rows: set.Rows, RowCount: set.RowCount, Truncated: set.Truncated})
			}
		case sqlexp.MsgNextResultSet:
			active = rows.NextResultSet()
		}
	}
	// On timeout, Message returns MsgNextResultSet (sqlexp v0.1.0,
	// messages.go), so the loop ends as if the batch had: without this check a
	// query cut short by -timeout would exit 0 with partial rows.
	if ctx.Err() != nil {
		return sqlErr, ctx.Err()
	}
	return sqlErr, rows.Err()
}
```

Importer `github.com/golang-sql/sqlexp`, puis `cd tools && go mod tidy`. Vérifier par
`git diff go.mod` que `sqlexp` est passé de `// indirect` au bloc `require` direct et que
rien d'autre n'a changé ; sinon s'arrêter et le signaler.

Ajouter à `integration_test.go` le test qui épingle le timeout :

```go
func TestTimeoutIsAnErrorNotPartialSuccess(t *testing.T) {
	p, resolve := testProfile(t)
	res, code := execute(p, "SELECT 1 AS a; WAITFOR DELAY '00:00:05'; SELECT 2 AS b;", nil,
		options{maxRows: 5, timeoutSec: 2}, resolve)
	if code == exitOK || res.Error == nil {
		t.Errorf("a timed-out batch must fail, got code %d", code)
	}
}
```

et l'ajouter au filtre des steps 2 et 4 (4 tests au lieu de 3).

- [ ] Step 4 : vérifier

Relancer la commande du step 2 (même invocation unique du shell). Expected : les 4 tests
`PASS`, aucun `SKIP`. Puis `cd tools && go test ./internal/sqlq -run '^TestMoreResultsIsAlwaysAnArray$' -count=1 -v`
(1 test, PASS), puis `cd tools && go test ./... -count=1 && go vet ./...`.

- [ ] Step 5 : casser pour voir tomber

Dans `collect`, ne plus ajouter à `MoreResults` (commenter l'`append`). Relancer les 4 tests
d'intégration : `TestEveryResultSetIsReturned` tombe, les trois autres passent. Remettre.
Supprimer le contrôle `ctx.Err()` après la boucle : `TestTimeoutIsAnErrorNotPartialSuccess`
tombe. Remettre.
Puis remplacer `result.Messages = append(...)` par rien : `TestMessagesAreCaptured` tombe.
Remettre.

- [ ] Step 6 : commit

```bash
git add tools/internal/sqlq/result.go tools/internal/sqlq/result_test.go \
        tools/cmd/sqlq/main.go tools/cmd/sqlq/integration_test.go tools/go.mod tools/go.sum
git commit -m "feat(sqlq): return every result set and the server's messages" -m "Many tsql-scripts diagnostics return several result sets, and sqlq kept the first and drained the rest, so a catalogued script would have given a partial answer without saying so. PRINT and low-severity RAISERROR output was lost the same way. Reading the driver's message stream fixes both at once. The first set stays where single-query callers already read it."
```

---

### Task 3 : analyse des en-têtes

Spec §8.

Files :
- Create : `tools/internal/sqlq/header.go`, `tools/internal/sqlq/header_test.go`

Interfaces :
- Produces :
  - `func StripBOM(b []byte) []byte`
  - `type BlockHeader struct { Summary string; Params []string; HasParamsLine bool; Heavy bool }`
  - `func ParseBlockHeader(text string) (BlockHeader, error)`
  - `type Marker struct { Name string; Params []string; Heavy bool; Line int }`
    (`Params` en minuscules, sans `@` ; `Line` 1-based)
  - `type MarkedHeader struct { Summary string; Marker Marker }`
  - `func ParseMarkedHeader(text string) (h MarkedHeader, found bool, err error)` :
    `found` faux seulement quand le fichier n'a aucune tentative de marqueur.

- [ ] Step 1 : tests qui échouent

`tools/internal/sqlq/header_test.go` :

```go
package sqlq

import (
	"reflect"
	"strings"
	"testing"
)

func TestBlockHeaderSummaryIsFirstParagraph(t *testing.T) {
	h, err := ParseBlockHeader("\n/*  Missing indexes in the current database, beside the\n    indexes that already exist.\n\n    No parameter. Read-only.\n*/\nSELECT 1;")
	if err != nil {
		t.Fatal(err)
	}
	if h.Summary != "Missing indexes in the current database, beside the indexes that already exist." {
		t.Errorf("summary = %q", h.Summary)
	}
	if h.HasParamsLine {
		t.Errorf("'No parameter.' is not a Parameters line")
	}
}

func TestParametersLineIsOptionalButParsed(t *testing.T) {
	h, _ := ParseBlockHeader("/* S.\n\n    Parameter: @name  - the object.\n    Heavy: yes\n*/\nSELECT 1;")
	if !h.HasParamsLine || !reflect.DeepEqual(h.Params, []string{"name"}) || !h.Heavy {
		t.Errorf("got %+v", h)
	}
	h, _ = ParseBlockHeader("/* S.\n\n    Parameters: none.\n*/\nSELECT 1;")
	if !h.HasParamsLine || len(h.Params) != 0 {
		t.Errorf("Parameters: none. -> %+v", h)
	}
	if _, err := ParseBlockHeader("SELECT 1; /* late */"); err == nil {
		t.Errorf("a file not opening with a comment block must be refused")
	}
}

const markedTemplate = `-----------------------------------------------------------------
-- Get sessions from a specific host
-- sqlq: name=sessions-from-host params=hostname heavy
--
-- rudi@babaluga.com, go ahead license
-----------------------------------------------------------------

DECLARE @hostname sysname = N'%';
SELECT 1;
`

func TestMarkedHeaderParses(t *testing.T) {
	h, found, err := ParseMarkedHeader(markedTemplate)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	want := MarkedHeader{Summary: "Get sessions from a specific host",
		Marker: Marker{Name: "sessions-from-host", Params: []string{"hostname"}, Heavy: true, Line: 3}}
	if !reflect.DeepEqual(h, want) {
		t.Errorf("got %+v, want %+v", h, want)
	}
}

func TestUnmarkedScriptIsAbsent(t *testing.T) {
	_, found, err := ParseMarkedHeader("-- just a script\nSELECT 1;")
	if found || err != nil {
		t.Errorf("found=%v err=%v; a file without marker must simply be absent", found, err)
	}
}

func TestMarkerAttemptIsCaseAndSpaceInsensitive(t *testing.T) {
	_, found, err := ParseMarkedHeader("-- Summary line\n--SQLQ: name=x-y\nSELECT 1;")
	if !found || err != nil {
		t.Errorf("found=%v err=%v", found, err)
	}
}

func TestMarkerOutsideHeaderIsRejected(t *testing.T) {
	_, found, err := ParseMarkedHeader("SET NOCOUNT ON;\n-- Summary\n-- sqlq: name=x-y\nSELECT 1;")
	if !found || err == nil || !strings.Contains(err.Error(), "outside the header") {
		t.Errorf("found=%v err=%v", found, err)
	}
	_, _, err = ParseMarkedHeader("-- S\n-- sqlq: name=a-b\n-- sqlq: name=c-d\nSELECT 1;")
	if err == nil {
		t.Errorf("two markers must be refused")
	}
}

func TestUnknownMarkerKeyIsRejected(t *testing.T) {
	_, _, err := ParseMarkedHeader("-- S\n-- sqlq: name=a-b haevy\nSELECT 1;")
	if err == nil || !strings.Contains(err.Error(), `"haevy"`) {
		t.Errorf("err = %v", err)
	}
	_, _, err = ParseMarkedHeader("-- S\n-- sqlq: params=a\nSELECT 1;")
	if err == nil {
		t.Errorf("a marker without name must be refused")
	}
}

func TestSummaryIsNearestLineAboveMarker(t *testing.T) {
	src := "-- https://example.com/provenance\n-- Wait statistics\n--\n-- sqlq: name=w-s\n-- rudi@babaluga.com, go ahead license\nSELECT 1;"
	h, _, err := ParseMarkedHeader(src)
	if err != nil || h.Summary != "Wait statistics" {
		t.Errorf("summary = %q, err = %v", h.Summary, err)
	}
	_, _, err = ParseMarkedHeader("-- https://example.com\n-- sqlq: name=w-s\n-- licence\nSELECT 1;")
	if err == nil || !strings.Contains(err.Error(), "no summary") {
		t.Errorf("a URL is not a summary: err = %v", err)
	}
}

func TestBOMIsStripped(t *testing.T) {
	b := StripBOM([]byte("\xEF\xBB\xBF-- S\n-- sqlq: name=a-b\nSELECT 1;"))
	if _, found, err := ParseMarkedHeader(string(b)); !found || err != nil {
		t.Errorf("found=%v err=%v", found, err)
	}
}

func TestHeaderAcceptsCRLF(t *testing.T) {
	src := strings.ReplaceAll(markedTemplate, "\n", "\r\n")
	h, found, err := ParseMarkedHeader(src)
	if !found || err != nil || h.Summary != "Get sessions from a specific host" || h.Marker.Name != "sessions-from-host" {
		t.Errorf("CRLF: %+v found=%v err=%v", h, found, err)
	}
}
```

- [ ] Step 2 : vérifier l'échec

Run : `cd tools && go test ./internal/sqlq -run '^(TestBlockHeaderSummaryIsFirstParagraph|TestParametersLineIsOptionalButParsed|TestMarkedHeaderParses|TestUnmarkedScriptIsAbsent|TestMarkerAttemptIsCaseAndSpaceInsensitive|TestMarkerOutsideHeaderIsRejected|TestUnknownMarkerKeyIsRejected|TestSummaryIsNearestLineAboveMarker|TestBOMIsStripped|TestHeaderAcceptsCRLF)$' -count=1 -v`
Expected : échec de compilation (`undefined: ParseBlockHeader`).

- [ ] Step 3 : implémenter

`tools/internal/sqlq/header.go` :

```go
package sqlq

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// StripBOM removes a UTF-8 byte order mark. Windows editors add one, and it
// would hide the first line of a header from every rule below.
func StripBOM(b []byte) []byte {
	return bytes.TrimPrefix(b, []byte("\xEF\xBB\xBF"))
}

func splitLines(text string) []string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	return lines
}

// BlockHeader is what the catalogue reads from a bundled or personal query.
type BlockHeader struct {
	Summary       string
	Params        []string // from a Parameter(s): line, lower-cased, without @
	HasParamsLine bool
	Heavy         bool
}

var paramsLine = regexp.MustCompile(`(?i)^parameters?:`)
var atName = regexp.MustCompile(`@([A-Za-z_][A-Za-z0-9_]*)`)

// ParseBlockHeader reads the /* ... */ block that opens a bundled or personal
// query. The summary is the block's first paragraph, joined on one line.
func ParseBlockHeader(text string) (BlockHeader, error) {
	trimmed := strings.TrimLeft(text, " \t\r\n")
	if !strings.HasPrefix(trimmed, "/*") {
		return BlockHeader{}, errors.New("no header comment")
	}
	end := strings.Index(trimmed, "*/")
	if end < 0 {
		return BlockHeader{}, errors.New("header comment is not closed")
	}
	var h BlockHeader
	var para []string
	inSummary := true
	for _, line := range splitLines(trimmed[2:end]) {
		l := strings.TrimSpace(line)
		switch {
		case inSummary && l == "" && len(para) > 0:
			inSummary = false
		case inSummary && l != "":
			para = append(para, l)
		}
		if paramsLine.MatchString(l) {
			h.HasParamsLine = true
			for _, m := range atName.FindAllStringSubmatch(l, -1) {
				h.Params = append(h.Params, strings.ToLower(m[1]))
			}
		}
		if strings.EqualFold(l, "heavy: yes") {
			h.Heavy = true
		}
	}
	h.Summary = strings.Join(para, " ")
	if h.Summary == "" {
		return BlockHeader{}, errors.New("header has no summary")
	}
	if h.HasParamsLine && h.Params == nil {
		h.Params = []string{}
	}
	return h, nil
}

// Marker is the parsed "-- sqlq:" line of a tsql-scripts file.
type Marker struct {
	Name   string
	Params []string
	Heavy  bool
	Line   int
}

// MarkedHeader is what the catalogue reads from a tsql-scripts file.
type MarkedHeader struct {
	Summary string
	Marker  Marker
}

var markerAttempt = regexp.MustCompile(`(?i)^\s*--\s*sqlq\s*:(.*)$`)
var commentLine = regexp.MustCompile(`^\s*--`)

// ParseMarkedHeader finds the sqlq marker of a tsql-scripts file. found is
// false only when no line even looks like a marker: such a file is not in the
// catalogue. Every other problem is an error, so a misplaced or misspelt marker
// shows up as rejected instead of making the script vanish.
func ParseMarkedHeader(text string) (MarkedHeader, bool, error) {
	lines := splitLines(text)
	headerEnd := 0
	for headerEnd < len(lines) && (strings.TrimSpace(lines[headerEnd]) == "" || commentLine.MatchString(lines[headerEnd])) {
		headerEnd++
	}
	var at []int
	for i, l := range lines {
		if markerAttempt.MatchString(l) {
			at = append(at, i)
		}
	}
	switch {
	case len(at) == 0:
		return MarkedHeader{}, false, nil
	case len(at) > 1:
		return MarkedHeader{}, true, fmt.Errorf("more than one sqlq marker (lines %d and %d)", at[0]+1, at[1]+1)
	case at[0] >= headerEnd:
		return MarkedHeader{}, true, fmt.Errorf("marker outside the header at line %d", at[0]+1)
	}
	m, err := parseMarker(markerAttempt.FindStringSubmatch(lines[at[0]])[1])
	if err != nil {
		return MarkedHeader{}, true, err
	}
	m.Line = at[0] + 1
	for i := at[0] - 1; i >= 0; i-- {
		c := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[i]), "--"))
		if c == "" || strings.Trim(c, "-") == "" || strings.HasPrefix(c, "http://") || strings.HasPrefix(c, "https://") {
			continue
		}
		return MarkedHeader{Summary: c, Marker: m}, true, nil
	}
	return MarkedHeader{}, true, errors.New("no summary above the marker")
}

func parseMarker(rest string) (Marker, error) {
	var m Marker
	for _, field := range strings.Fields(rest) {
		key, value, hasValue := strings.Cut(field, "=")
		switch {
		case key == "name" && hasValue:
			m.Name = value
		case key == "params" && hasValue:
			for _, p := range strings.Split(value, ",") {
				p = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(p), "@"))
				if p == "" {
					return Marker{}, errors.New("empty name in params=")
				}
				m.Params = append(m.Params, p)
			}
		case key == "heavy" && !hasValue:
			m.Heavy = true
		default:
			return Marker{}, fmt.Errorf("unknown marker key %q", field)
		}
	}
	if m.Name == "" {
		return Marker{}, errors.New("marker has no name=")
	}
	return m, nil
}
```

- [ ] Step 4 : vérifier

Relancer la commande du step 2. Expected : 10 tests `PASS`
(`TestBlockHeaderSummaryIsFirstParagraph`, `TestParametersLineIsOptionalButParsed`,
`TestMarkedHeaderParses`, `TestUnmarkedScriptIsAbsent`,
`TestMarkerAttemptIsCaseAndSpaceInsensitive`, `TestMarkerOutsideHeaderIsRejected`,
`TestUnknownMarkerKeyIsRejected`, `TestSummaryIsNearestLineAboveMarker`, `TestBOMIsStripped`,
`TestHeaderAcceptsCRLF`). Tout autre nombre : le filtre ou le travail est faux.

Puis vérifier sur les 7 requêtes livrées, sans écrire de test : un programme jetable dans
`tools/cmd/zzprobe/` (supprimé ensuite) qui appelle `ParseBlockHeader` sur chacune et
imprime résumé et paramètres. Les 7 doivent donner un résumé complet (une phrase entière) ;
coller la sortie dans le rapport.

- [ ] Step 5 : casser pour voir tomber

Dans `ParseMarkedHeader`, supprimer le test `at[0] >= headerEnd`. Relancer :
`TestMarkerOutsideHeaderIsRejected` tombe, les 9 autres passent. Remettre.

- [ ] Step 6 : commit

```bash
git add tools/internal/sqlq/header.go tools/internal/sqlq/header_test.go
git commit -m "feat(sqlq): parse catalogue headers and the tsql-scripts marker" -m "Real tsql-scripts headers are not all instances of their template: some are split by blank lines, some open on a provenance URL, four have no description and would have offered the licence as a summary. Every line that looks like a marker is therefore either accepted or rejected with a reason, never silently ignored, and the summary is the nearest description above the marker, which the author controls."
```

---

### Task 4 : paramètres des requêtes livrées et lectures sales

Spec §9 et §12 (`dirty_reads`).

Files :
- Create : `tools/internal/sqlq/params.go`, `tools/internal/sqlq/params_test.go`

Interfaces :
- Consumes : `Lex`, `Sanitize`.
- Produces :
  - `func QueryParams(sql string) []string` : noms en minuscules, sans `@`, dans l'ordre de
    première apparition, sans doublon.
  - `func DirtyReads(sql string) bool`

- [ ] Step 1 : tests qui échouent

```go
package sqlq

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDeclaredVariableIsOnlyTheDeclareTarget(t *testing.T) {
	got := QueryParams("DECLARE @local INT = @cutoff, @other INT = @Limit;\nSELECT @local, @other, @@ROWCOUNT, @Cutoff;")
	if want := []string{"cutoff", "limit"}; !reflect.DeepEqual(got, want) {
		t.Errorf("QueryParams = %v, want %v", got, want)
	}
	// A DECLARE without semicolon ends at the next statement keyword: the comma
	// of the SELECT list does not declare @b.
	got = QueryParams("DECLARE @a int = 1\nSELECT @a, @b")
	if want := []string{"b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("QueryParams = %v, want %v", got, want)
	}
	if got := QueryParams("SELECT 1 WHERE 'x' LIKE '%@@ROWCOUNT%' AND N'@notaparam' = N'';"); len(got) != 0 {
		t.Errorf("literals must not yield parameters: %v", got)
	}
}

// The header of every bundled query must name exactly the parameters its SQL
// references, when it has a Parameters line at all.
func TestDeclaredParametersMatchTheSQL(t *testing.T) {
	paths, _ := filepath.Glob(filepath.Join(bundledQueriesDir, "*.sql"))
	if len(paths) == 0 {
		t.Fatal("no bundled queries found")
	}
	for _, p := range paths {
		b, _ := os.ReadFile(p)
		h, err := ParseBlockHeader(string(StripBOM(b)))
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(p), err)
			continue
		}
		if !h.HasParamsLine {
			continue
		}
		got := QueryParams(string(b))
		if len(got) == 0 && len(h.Params) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, h.Params) {
			t.Errorf("%s: header says %v, SQL references %v", filepath.Base(p), h.Params, got)
		}
	}
}

func TestDirtyReadsDetectedOnTokens(t *testing.T) {
	yes := []string{
		"SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;",
		"SET TRANSACTION ISOLATION LEVEL READ\n    UNCOMMITTED;",
		"SELECT 1 FROM sys.objects WITH (READUNCOMMITTED);",
		"SELECT 1 FROM t WITH (nolock);",
	}
	for _, s := range yes {
		if !DirtyReads(s) {
			t.Errorf("DirtyReads(%q) = false", s)
		}
	}
	no := []string{
		"SELECT 1; -- READ UNCOMMITTED",
		"SELECT 'NOLOCK' AS hint;",
		"SET TRANSACTION ISOLATION LEVEL READ COMMITTED;",
	}
	for _, s := range no {
		if DirtyReads(s) {
			t.Errorf("DirtyReads(%q) = true", s)
		}
	}
}
```

- [ ] Step 2 : vérifier l'échec

Run : `cd tools && go test ./internal/sqlq -run '^(TestDeclaredVariableIsOnlyTheDeclareTarget|TestDeclaredParametersMatchTheSQL|TestDirtyReadsDetectedOnTokens)$' -count=1 -v`
Expected : échec de compilation (`undefined: QueryParams`).

- [ ] Step 3 : implémenter

```go
package sqlq

import "strings"

// statementStarters end a DECLARE written without its semicolon. Without them,
// the comma of a following SELECT list would declare the next variable.
var statementStarters = map[string]bool{
	"SELECT": true, "SET": true, "IF": true, "WHILE": true, "WITH": true, "DECLARE": true,
	"BEGIN": true, "RETURN": true, "PRINT": true, "RAISERROR": true, "THROW": true,
	"INSERT": true, "UPDATE": true, "DELETE": true, "MERGE": true, "EXEC": true, "EXECUTE": true,
}

// QueryParams returns the variables a batch references without declaring them:
// the parameters a bundled or personal query expects through -param.
// A variable is declared when it is the target of a DECLARE: the first @x after
// DECLARE, and the first @x after each depth-zero comma of the same statement.
func QueryParams(sql string) []string {
	toks := Lex(Sanitize(sql))
	declared := map[string]bool{}
	for i := 0; i < len(toks); i++ {
		if !strings.EqualFold(toks[i].Text, "DECLARE") {
			continue
		}
		base := toks[i].Depth
		expectTarget := true
		for j := i + 1; j < len(toks); j++ {
			t := toks[j]
			if t.Depth == base && (t.Text == ";" || statementStarters[strings.ToUpper(t.Text)]) {
				break
			}
			if expectTarget && strings.HasPrefix(t.Text, "@") && !strings.HasPrefix(t.Text, "@@") {
				declared[strings.ToLower(t.Text)] = true
				expectTarget = false
				continue
			}
			if t.Text == "," && t.Depth == base {
				expectTarget = true
			}
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, t := range toks {
		name := strings.ToLower(t.Text)
		if !strings.HasPrefix(name, "@") || strings.HasPrefix(name, "@@") || declared[name] || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, strings.TrimPrefix(name, "@"))
	}
	return out
}

// DirtyReads reports whether a batch reads uncommitted data on its own: a
// NOLOCK or READUNCOMMITTED hint, or the READ UNCOMMITTED isolation level.
func DirtyReads(sql string) bool {
	toks := Lex(Sanitize(sql))
	for i, t := range toks {
		switch strings.ToUpper(t.Text) {
		case "NOLOCK", "READUNCOMMITTED":
			return true
		case "READ":
			if i+1 < len(toks) && strings.EqualFold(toks[i+1].Text, "UNCOMMITTED") {
				return true
			}
		}
	}
	return false
}
```

- [ ] Step 4 : vérifier

Relancer le step 2 : 3 tests `PASS`. Si `TestDeclaredParametersMatchTheSQL` signale une
requête livrée dont l'en-tête et le SQL divergent, ne pas corriger l'en-tête : s'arrêter et
rapporter le fichier et les deux listes.

- [ ] Step 5 : casser pour voir tomber

Retirer la condition `t.Depth == base` du test de virgule. Relancer :
`TestDeclaredVariableIsOnlyTheDeclareTarget` doit tomber si une virgule en profondeur non
nulle déclare une variable ; si elle ne tombe pas, ajouter au test le cas
`DECLARE @a nvarchar(10) = LEFT(@src, 3);` (attendu : `src` paramètre) et vérifier qu'il
tombe alors. Remettre le code.

- [ ] Step 6 : commit

```bash
git add tools/internal/sqlq/params.go tools/internal/sqlq/params_test.go
git commit -m "feat(sqlq): infer query parameters and detect dirty reads on tokens" -m "A parameter is a variable referenced and never declared, and the September design left 'declared' loose enough that DECLARE @local = @cutoff hid the parameter it initialises from. Dirty reads are detected on tokens because eight guard-passing tsql-scripts files use the READUNCOMMITTED hint, which a substring search for READ UNCOMMITTED misses."
```

---

### Task 5 : surcharge des `DECLARE`

Spec §11. C'est la tâche dont dépend la justesse de toute réponse paramétrée d'un script
tsql-scripts. Lire le §11 en entier avant de commencer.

Files :
- Create : `tools/internal/sqlq/override.go`, `tools/internal/sqlq/override_test.go`

Interfaces :
- Consumes : `Lex`, `Sanitize`, `Token`, `statementStarters` (tâche 4, `params.go`).
- Produces :
  - `type ParamType struct { Base string; Length int }` (`Base` en minuscules ; `Length` en
    caractères pour les chaînes, `-1` pour `max`, `0` sinon), `func (t ParamType) String() string`
  - `type OverrideParam struct { Name string; Type ParamType; Default string; start, end int }`
  - `func AnalyseOverrides(sql string, names []string) ([]OverrideParam, error)`
  - `func Rewrite(sql string, params []OverrideParam, passed map[string]bool) string`
  - `func BindValue(t ParamType, value string) (any, error)`

- [ ] Step 1 : tests qui échouent

```go
package sqlq

import (
	"strings"
	"testing"

	"github.com/golang-sql/civil"
)

const hostScript = "SET NOCOUNT ON;\nDECLARE @hostname sysname = N'%';\nSELECT host_name FROM sys.dm_exec_sessions WHERE host_name LIKE @hostname;\n"

func TestDeclareOverrideBindsNotConcatenates(t *testing.T) {
	ps, err := AnalyseOverrides(hostScript, []string{"hostname"})
	if err != nil {
		t.Fatal(err)
	}
	if ps[0].Type.String() != "sysname" || ps[0].Default != "N'%'" {
		t.Errorf("param = %+v", ps[0])
	}
	got := Rewrite(hostScript, ps, map[string]bool{"hostname": true})
	want := strings.Replace(hostScript, "= N'%';", "= @sqlq_hostname;", 1)
	if got != want {
		t.Errorf("Rewrite =\n%s\nwant\n%s", got, want)
	}
	if Rewrite(hostScript, ps, map[string]bool{}) != hostScript {
		t.Errorf("a parameter not passed must leave the text untouched")
	}
}

func TestDeclareOverrideKeepsOffsetsWithAccents(t *testing.T) {
	src := "-- sessions de l'hôte, données à jour\nDECLARE @h sysname = N'é%';\nSELECT @h;"
	ps, err := AnalyseOverrides(src, []string{"h"})
	if err != nil {
		t.Fatal(err)
	}
	if got := Rewrite(src, ps, map[string]bool{"h": true}); !strings.Contains(got, "DECLARE @h sysname = @sqlq_h;\nSELECT @h;") {
		t.Errorf("Rewrite = %q", got)
	}
}

func TestOverrideKeepsCRLF(t *testing.T) {
	src := "DECLARE @n int = 20;\r\nSELECT TOP (@n) name FROM sys.objects;\r\n"
	ps, err := AnalyseOverrides(src, []string{"n"})
	if err != nil {
		t.Fatal(err)
	}
	if got := Rewrite(src, ps, map[string]bool{"n": true}); got != "DECLARE @n int = @sqlq_n;\r\nSELECT TOP (@n) name FROM sys.objects;\r\n" {
		t.Errorf("Rewrite = %q", got)
	}
}

func TestEmptyStringDefaultIsAccepted(t *testing.T) {
	if _, err := AnalyseOverrides("DECLARE @indexName sysname = '';\nSELECT @indexName;", []string{"indexname"}); err != nil {
		t.Errorf("'' is a value, not an empty initializer: %v", err)
	}
	if _, err := AnalyseOverrides("DECLARE @x int = ;\nSELECT @x;", []string{"x"}); err == nil {
		t.Errorf("a truly empty initializer must be refused")
	}
}

func TestDeclareLineMustStandAlone(t *testing.T) {
	cases := []struct{ name, src, variable string }{
		{"statement after", "DECLARE @n int = 20 SELECT TOP (@n) name FROM sys.objects;", "n"},
		{"no semicolon", "DECLARE @n int = 20\nSELECT @n;", "n"},
		{"two lines", "DECLARE @p nvarchar(200) = N'%'\n  + N'Orders%';\nSELECT @p;", "p"},
		{"case on two lines", "DECLARE @d int = CASE\n WHEN 1 = 1 THEN 1 END;\nSELECT @d;", "d"},
		{"literal semicolon", "DECLARE @p varchar(10) = 'a;b' SELECT 2;", "p"},
		{"multi variable", "DECLARE @p int = 1, @q int = 2;\nSELECT @p, @q;", "p"},
		{"code before", "SELECT 1; DECLARE @p int = 1;\nSELECT @p;", "p"},
		{"not declared", "SELECT @p;", "p"},
		{"declared twice", "DECLARE @p int = 1;\nDECLARE @P int = 2;\nSELECT @p;", "p"},
		{"no initializer", "DECLARE @p int;\nSELECT @p;", "p"},
	}
	for _, c := range cases {
		if _, err := AnalyseOverrides(c.src, []string{c.variable}); err == nil {
			t.Errorf("%s: %q accepted", c.name, c.src)
		}
	}
}

func TestCompoundAssignmentIsRejected(t *testing.T) {
	_, err := AnalyseOverrides("DECLARE @p int = 1;\nSET @p += 2;\nSELECT @p;", []string{"p"})
	if err == nil || !strings.Contains(err.Error(), "assigned at line 2") {
		t.Errorf("err = %v", err)
	}
}

func TestSelectAssignmentIsRejected(t *testing.T) {
	for _, src := range []string{
		"DECLARE @db sysname = N'%';\nSELECT TOP (1) @db = name FROM sys.databases;\nSELECT @db;",
		"DECLARE @db sysname = N'%';\nDECLARE @x int = 0;\nSELECT @x = 1, @db = N'master';",
		"DECLARE @db sysname = N'%';\nSET @db = N'x';",
	} {
		if _, err := AnalyseOverrides(src, []string{"db"}); err == nil {
			t.Errorf("assignment accepted: %q", src)
		}
	}
}

func TestComparisonIsAccepted(t *testing.T) {
	src := "DECLARE @online bit = 1;\nSELECT IIF(@online = 1, 'ON', 'OFF');\nSELECT name FROM sys.objects WHERE @online = 1 AND 1 = 1;\nIF @online = 0 SELECT 0;"
	if _, err := AnalyseOverrides(src, []string{"online"}); err != nil {
		t.Errorf("comparisons refused: %v", err)
	}
}

func TestReservedPrefixIsRejected(t *testing.T) {
	if _, err := AnalyseOverrides("DECLARE @p int = 1;\nDECLARE @sqlq_p int = 2;\nSELECT @p;", []string{"p"}); err == nil {
		t.Errorf("@sqlq_ identifiers must be refused")
	}
}

func TestUnsupportedTypeIsRejected(t *testing.T) {
	_, err := AnalyseOverrides("DECLARE @r decimal(5,1) = 1.2;\nSELECT @r;", []string{"r"})
	if err == nil || !strings.Contains(err.Error(), "decimal") {
		t.Errorf("err = %v", err)
	}
}

func TestParameterNamesAreCaseInsensitive(t *testing.T) {
	if _, err := AnalyseOverrides("DECLARE @HostName sysname = N'%';\nSELECT @HOSTNAME;", []string{"hostname"}); err != nil {
		t.Errorf("err = %v", err)
	}
}

func TestParamValueValidatedByType(t *testing.T) {
	ok := []struct {
		typ  ParamType
		in   string
		want any
	}{
		{ParamType{"sysname", 128}, "SRV-APP01", "SRV-APP01"},
		{ParamType{"nvarchar", -1}, strings.Repeat("é", 5000), strings.Repeat("é", 5000)},
		{ParamType{"varchar", 10}, "ROW", "ROW"},
		{ParamType{"int", 0}, "-42", int64(-42)},
		{ParamType{"tinyint", 0}, "255", int64(255)},
		{ParamType{"bit", 0}, "TRUE", true},
		{ParamType{"date", 0}, "2026-10-04", civil.Date{Year: 2026, Month: 10, Day: 4}},
		{ParamType{"datetime2", 0}, "2026-10-04T10:30", civil.DateTime{Date: civil.Date{Year: 2026, Month: 10, Day: 4}, Time: civil.Time{Hour: 10, Minute: 30}}},
	}
	for _, c := range ok {
		got, err := BindValue(c.typ, c.in)
		if err != nil || got != c.want {
			t.Errorf("BindValue(%v, %q) = %v, %v; want %v", c.typ, c.in, got, err, c.want)
		}
	}
	bad := []struct {
		typ ParamType
		in  string
	}{
		{ParamType{"varchar", 10}, "COLUMNSTORE_ARCHIVE"},
		{ParamType{"varchar", 10}, "été"},
		{ParamType{"nchar", 3}, "abcd"},
		{ParamType{"tinyint", 0}, "256"},
		{ParamType{"int", 0}, "1.5"},
		{ParamType{"bit", 0}, "2"},
		{ParamType{"date", 0}, "04/10/2026"},
		{ParamType{"datetime", 0}, "2026-10-04 10:30"},
	}
	for _, c := range bad {
		if _, err := BindValue(c.typ, c.in); err == nil {
			t.Errorf("BindValue(%v, %q) accepted", c.typ, c.in)
		}
	}
}
```

- [ ] Step 2 : vérifier l'échec

Run : `cd tools && go test ./internal/sqlq -run '^(TestDeclareOverrideBindsNotConcatenates|TestDeclareOverrideKeepsOffsetsWithAccents|TestOverrideKeepsCRLF|TestEmptyStringDefaultIsAccepted|TestDeclareLineMustStandAlone|TestCompoundAssignmentIsRejected|TestSelectAssignmentIsRejected|TestComparisonIsAccepted|TestReservedPrefixIsRejected|TestUnsupportedTypeIsRejected|TestParameterNamesAreCaseInsensitive|TestParamValueValidatedByType)$' -count=1 -v`
Expected : échec de compilation (`undefined: AnalyseOverrides`).

- [ ] Step 3 : implémenter

```go
package sqlq

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/golang-sql/civil"
)

// ParamType is a declared type an override knows how to validate and bind.
type ParamType struct {
	Base   string // lower case
	Length int    // characters for string types, -1 for max, 0 otherwise
}

func (t ParamType) String() string {
	switch {
	case t.Length == -1:
		return t.Base + "(max)"
	case t.Length > 0 && t.Base != "sysname":
		return fmt.Sprintf("%s(%d)", t.Base, t.Length)
	default:
		return t.Base
	}
}

var stringTypes = map[string]bool{"nvarchar": true, "nchar": true, "varchar": true, "char": true}
var intRanges = map[string][2]int64{
	"tinyint": {0, 255}, "smallint": {-32768, 32767},
	"int": {-2147483648, 2147483647}, "bigint": {-9223372036854775808, 9223372036854775807},
}
var otherTypes = map[string]bool{"bit": true, "date": true, "datetime": true, "datetime2": true, "smalldatetime": true, "sysname": true}

// OverrideParam is one marker parameter, located in the batch.
type OverrideParam struct {
	Name    string // lower case, without @
	Type    ParamType
	Default string // initializer as written in the file, trimmed
	start   int    // rune offset of the initializer, just after "="
	end     int    // rune offset of the closing ";"
}

// comparisonContext are the tokens after which "@p =" compares rather than assigns.
var comparisonContext = map[string]bool{
	"WHERE": true, "AND": true, "OR": true, "NOT": true, "ON": true, "WHEN": true,
	"HAVING": true, "IF": true, "WHILE": true, "THEN": true, "ELSE": true,
}

var compoundOps = map[string]bool{"+=": true, "-=": true, "*=": true, "/=": true, "%=": true, "&=": true, "|=": true, "^=": true}

// AnalyseOverrides locates each marker parameter's declaration and checks that
// replacing its initializer is safe. Any doubt is an error: a refused script is
// visible in the catalogue, a wrong override is not.
func AnalyseOverrides(sql string, names []string) ([]OverrideParam, error) {
	rs := []rune(sql)
	toks := Lex(Sanitize(sql))
	for _, t := range toks {
		if strings.HasPrefix(strings.ToLower(t.Text), "@sqlq_") {
			return nil, fmt.Errorf("identifier %s at line %d uses the reserved @sqlq_ prefix", t.Text, t.Line)
		}
	}
	var out []OverrideParam
	for _, name := range names {
		p, err := analyseOne(rs, toks, name)
		if err != nil {
			return nil, fmt.Errorf("parameter %q: %w", name, err)
		}
		out = append(out, p)
	}
	return out, nil
}

func analyseOne(rs []rune, toks []Token, name string) (OverrideParam, error) {
	target := "@" + name
	decl := -1
	for i := 0; i+1 < len(toks); i++ {
		if strings.EqualFold(toks[i].Text, "DECLARE") && strings.EqualFold(toks[i+1].Text, target) {
			if decl >= 0 {
				return OverrideParam{}, errors.New("declared more than once")
			}
			decl = i
		}
	}
	if decl < 0 {
		return OverrideParam{}, fmt.Errorf("no \"DECLARE @%s <type> = <value>;\" line", name)
	}
	line := toks[decl].Line
	var lineToks []Token
	for _, t := range toks {
		if t.Line == line {
			lineToks = append(lineToks, t)
		}
	}
	if lineToks[0] != toks[decl] {
		return OverrideParam{}, fmt.Errorf("line %d: the declaration must stand alone on its line", line)
	}
	last := lineToks[len(lineToks)-1]
	if last.Text != ";" || last.Depth != toks[decl].Depth {
		return OverrideParam{}, fmt.Errorf("line %d: the declaration must end with ';' on its own line", line)
	}
	typ, eq, err := parseDeclaredType(lineToks)
	if err != nil {
		return OverrideParam{}, fmt.Errorf("line %d: %w", line, err)
	}
	expr := lineToks[eq+1 : len(lineToks)-1]
	for _, t := range expr {
		if t.Text == ";" || (t.Text == "," && t.Depth == toks[decl].Depth) {
			return OverrideParam{}, fmt.Errorf("line %d: one variable per declaration", line)
		}
		// "DECLARE @n int = 20 SELECT ...;" ends with a semicolon too: the
		// statement that follows would be swallowed by the rewrite.
		if t.Depth == toks[decl].Depth && statementStarters[strings.ToUpper(t.Text)] {
			return OverrideParam{}, fmt.Errorf("line %d: another statement follows the declaration", line)
		}
	}
	start, end := lineToks[eq].End, last.Start
	def := strings.TrimSpace(string(rs[start:end]))
	if def == "" {
		return OverrideParam{}, fmt.Errorf("line %d: empty initializer", line)
	}
	if err := checkNotAssigned(toks, decl+1, target); err != nil {
		return OverrideParam{}, err
	}
	return OverrideParam{Name: name, Type: typ, Default: def, start: start, end: end}, nil
}

// parseDeclaredType reads "DECLARE @p <type> =" and returns the type and the
// index of "=" in the line's tokens.
func parseDeclaredType(lt []Token) (ParamType, int, error) {
	if len(lt) < 5 {
		return ParamType{}, 0, errors.New("incomplete declaration")
	}
	base := strings.ToLower(lt[2].Text)
	i := 3
	t := ParamType{Base: base}
	if lt[i].Text == "(" {
		if i+2 >= len(lt) || lt[i+2].Text != ")" {
			return ParamType{}, 0, fmt.Errorf("type %s: unsupported length", base)
		}
		arg := strings.ToLower(lt[i+1].Text)
		if arg == "max" {
			t.Length = -1
		} else if n, err := strconv.Atoi(arg); err == nil && n >= 0 {
			t.Length = n // datetime2(0) is a precision of 0, not a missing length
		} else {
			return ParamType{}, 0, fmt.Errorf("type %s: unsupported length %q", base, arg)
		}
		i += 3
	}
	switch {
	case base == "sysname":
		t.Length = 128
	case stringTypes[base]:
		if t.Length == 0 {
			t.Length = 1 // nvarchar without length is nvarchar(1) in a DECLARE
		}
	case base == "datetime2":
		t.Length = 0 // the precision is the server's business
	case intRanges[base] != [2]int64{} || otherTypes[base]:
		if t.Length != 0 {
			return ParamType{}, 0, fmt.Errorf("type %s takes no length", base)
		}
	default:
		return ParamType{}, 0, fmt.Errorf("type %s not supported", base)
	}
	if i >= len(lt) || lt[i].Text != "=" {
		return ParamType{}, 0, errors.New("no initializer")
	}
	return t, i, nil
}

// checkNotAssigned refuses any occurrence of the variable, after its
// declaration, that sits where a value is assigned rather than compared.
func checkNotAssigned(toks []Token, from int, target string) error {
	for i := from; i < len(toks); i++ {
		if !strings.EqualFold(toks[i].Text, target) || i+1 >= len(toks) {
			continue
		}
		next := toks[i+1].Text
		if compoundOps[next] {
			return fmt.Errorf("assigned at line %d", toks[i].Line)
		}
		if next != "=" || toks[i].Depth > 0 {
			continue
		}
		if i > 0 && comparisonContext[strings.ToUpper(toks[i-1].Text)] {
			continue
		}
		return fmt.Errorf("assigned at line %d", toks[i].Line)
	}
	return nil
}

// Rewrite replaces the initializer of each passed parameter by @sqlq_<name>.
func Rewrite(sql string, params []OverrideParam, passed map[string]bool) string {
	rs := []rune(sql)
	// From the last offset to the first, so earlier offsets stay valid.
	for i := len(params) - 1; i >= 0; i-- {
		p := params[i]
		if !passed[p.Name] {
			continue
		}
		repl := []rune(" @sqlq_" + p.Name)
		rs = append(rs[:p.start], append(repl, rs[p.end:]...)...)
	}
	return string(rs)
}

// BindValue validates value against the declared type and returns what to bind.
// SQL Server truncates an oversized string assigned to a variable without an
// error, so lengths are checked here; dates are bound as civil types so they
// do not depend on the session's DATEFORMAT.
func BindValue(t ParamType, value string) (any, error) {
	switch {
	case t.Base == "sysname" || stringTypes[t.Base]:
		if t.Length > 0 && utf8.RuneCountInString(value) > t.Length {
			return nil, fmt.Errorf("value longer than %s", t)
		}
		if (t.Base == "varchar" || t.Base == "char") && !isASCII(value) {
			return nil, fmt.Errorf("%s accepts ASCII only", t)
		}
		return value, nil
	case intRanges[t.Base] != [2]int64{}:
		n, err := strconv.ParseInt(value, 10, 64)
		r := intRanges[t.Base]
		if err != nil || n < r[0] || n > r[1] {
			return nil, fmt.Errorf("value is not a %s", t)
		}
		return n, nil
	case t.Base == "bit":
		switch strings.ToLower(value) {
		case "1", "true":
			return true, nil
		case "0", "false":
			return false, nil
		}
		return nil, errors.New("bit accepts 0, 1, true or false")
	case t.Base == "date":
		d, err := time.Parse("2006-01-02", value)
		if err != nil {
			return nil, errors.New("date must be YYYY-MM-DD")
		}
		return civil.DateOf(d), nil
	default: // datetime, datetime2, smalldatetime
		for _, layout := range []string{"2006-01-02", "2006-01-02T15:04", "2006-01-02T15:04:05"} {
			if d, err := time.Parse(layout, value); err == nil {
				return civil.DateTimeOf(d), nil
			}
		}
		return nil, fmt.Errorf("%s must be YYYY-MM-DD or YYYY-MM-DDTHH:MM[:SS]", t)
	}
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}
```

Puis `cd tools && go mod tidy` pour promouvoir `civil`, et vérifier par `git diff go.mod`
que seul `civil` a changé de bloc.

- [ ] Step 4 : vérifier

Relancer le step 2. Expected : 12 tests `PASS` : `TestDeclareOverrideBindsNotConcatenates`,
`TestDeclareOverrideKeepsOffsetsWithAccents`, `TestOverrideKeepsCRLF`,
`TestEmptyStringDefaultIsAccepted`, `TestDeclareLineMustStandAlone`,
`TestCompoundAssignmentIsRejected`, `TestSelectAssignmentIsRejected`,
`TestComparisonIsAccepted`, `TestReservedPrefixIsRejected`, `TestUnsupportedTypeIsRejected`,
`TestParameterNamesAreCaseInsensitive`, `TestParamValueValidatedByType`. Un autre nombre :
le filtre ou le travail est faux.

Puis mesurer sur le vrai corpus, sans écrire de test : un programme jetable
`tools/cmd/zzprobe/` (supprimé ensuite) qui, pour chacune des 45 lignes `DECLARE` d'une ligne
de type simple des fichiers de tsql-scripts qui passent le garde-fou, appelle
`AnalyseOverrides(fichier, []string{variable})` et imprime accepté ou la raison. Lister dans
le rapport chaque refus et dire s'il est juste. Un refus que la spec n'explique pas est à
signaler, pas à faire disparaître en assouplissant la règle.

- [ ] Step 5 : casser pour voir tomber

Supprimer le `if compoundOps[next]` de `checkNotAssigned`. Relancer :
`TestCompoundAssignmentIsRejected` tombe, les autres passent. Remettre. Puis remplacer
`toks[i].Depth > 0` par `false` : `TestComparisonIsAccepted` tombe. Remettre.

- [ ] Step 6 : commit

```bash
git add tools/internal/sqlq/override.go tools/internal/sqlq/override_test.go tools/go.mod tools/go.sum
git commit -m "feat(sqlq): override tsql-scripts DECLARE defaults with bound, typed values" -m "The agent reports the value it passed, so an override is only accepted when nothing else can change it: the declaration stands alone on its line, no later statement assigns the variable, and the value fits the declared type. SQL Server truncates an oversized string assigned to a variable without a word and reads a date string by the session's DATEFORMAT, so values are checked and bound as typed Go values before connecting. Assignment is detected on tokens and leans toward refusal, which shows in the catalogue, rather than toward a wrong answer, which does not."
```

---

### Task 6 : le registre

Spec §10.

Files :
- Create : `tools/internal/sqlq/registry.go`, `tools/internal/sqlq/registry_test.go`

Interfaces :
- Produces :
  - `type Verified struct { Date string `json:"date"`; Profile string `json:"profile"` }`
  - `type Registry struct { entries map[string]Verified }`
  - `func DefaultRegistryPath() string` : `~/.config/db-ai-toolkit/verified.json`
  - `func LoadRegistry(path string) (Registry, string)` : le second retour est un message
    (`""` si rien à dire)
  - `func (r Registry) Lookup(hash string) *Verified`
  - `func RecordVerified(path, hash string, v Verified) error`
  - `func ContentHash(b []byte) string` : SHA-256 en hexadécimal minuscule

- [ ] Step 1 : tests qui échouent

```go
package sqlq

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRegistryRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "verified.json")
	r, msg := LoadRegistry(path)
	if msg != "" || r.Lookup("x") != nil {
		t.Fatalf("absent registry should be empty and silent, got %q", msg)
	}
	if err := RecordVerified(path, "aaa", Verified{Date: "2026-10-04", Profile: "p1"}); err != nil {
		t.Fatal(err)
	}
	if err := RecordVerified(path, "bbb", Verified{Date: "2026-10-05", Profile: "p2"}); err != nil {
		t.Fatal(err)
	}
	r, _ = LoadRegistry(path)
	if v := r.Lookup("aaa"); v == nil || v.Profile != "p1" {
		t.Errorf("aaa lost: %+v", v)
	}
	if v := r.Lookup("bbb"); v == nil || v.Date != "2026-10-05" {
		t.Errorf("bbb: %+v", v)
	}
}

func TestRegistryCorruptIsTreatedAsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "verified.json")
	os.WriteFile(path, []byte("{not json"), 0o600)
	r, msg := LoadRegistry(path)
	if msg == "" || r.Lookup("x") != nil {
		t.Errorf("corrupt registry: msg %q", msg)
	}
	if err := RecordVerified(path, "ccc", Verified{Date: "d", Profile: "p"}); err != nil {
		t.Fatal(err)
	}
	if r, msg = LoadRegistry(path); msg != "" || r.Lookup("ccc") == nil {
		t.Errorf("rewritten registry should load cleanly: %q", msg)
	}
}

func TestContentHashIsStable(t *testing.T) {
	if ContentHash([]byte("SELECT 1;")) != ContentHash([]byte("SELECT 1;")) ||
		ContentHash([]byte("SELECT 1;")) == ContentHash([]byte("SELECT 2;")) {
		t.Error("hash must depend on content only")
	}
}
```

- [ ] Step 2 : vérifier l'échec

Run : `cd tools && go test ./internal/sqlq -run '^(TestRegistryRoundTrip|TestRegistryCorruptIsTreatedAsEmpty|TestContentHashIsStable)$' -count=1 -v`
Expected : échec de compilation.

- [ ] Step 3 : implémenter

```go
package sqlq

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Verified records the last successful run of a content.
type Verified struct {
	Date    string `json:"date"`
	Profile string `json:"profile"`
}

// Registry maps content hashes to their last successful run on this machine.
type Registry struct {
	entries map[string]Verified
}

// DefaultRegistryPath is where sqlq keeps the registry.
func DefaultRegistryPath() string {
	return filepath.Join(filepath.Dir(DefaultProfilePath()), "verified.json")
}

// ContentHash is the registry key of a content.
func ContentHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// LoadRegistry reads the registry. An absent file is an empty registry; an
// unreadable one is also empty, with a message, and is rewritten on the next
// success. Neither is an error: losing verifications errs on the side of caution.
func LoadRegistry(path string) (Registry, string) {
	r := Registry{entries: map[string]Verified{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return r, ""
	}
	if err != nil {
		return r, "verification registry unreadable, treated as empty: " + err.Error()
	}
	if err := json.Unmarshal(b, &r.entries); err != nil {
		r.entries = map[string]Verified{}
		return r, "verification registry is not valid JSON, treated as empty"
	}
	return r, ""
}

// Lookup returns the verification of a content, or nil.
func (r Registry) Lookup(hash string) *Verified {
	if v, ok := r.entries[hash]; ok {
		return &v
	}
	return nil
}

// RecordVerified re-reads the registry, merges one entry and replaces the file
// atomically. Two runs finishing at the same instant can still lose one entry;
// the consequence is a query shown as unverified, which is the cautious side.
func RecordVerified(path, hash string, v Verified) error {
	r, _ := LoadRegistry(path)
	r.entries[hash] = v
	b, err := json.MarshalIndent(r.entries, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "verified-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
```

`DefaultProfilePath()` rend `~/.config/db-ai-toolkit/mssql-profiles.json` (lire
`profile.go:192` pour le vérifier) ; si ce n'est pas le cas, s'arrêter et le signaler.

- [ ] Step 4 : vérifier

Relancer le step 2 : 3 tests `PASS`.

- [ ] Step 5 : casser pour voir tomber

Dans `RecordVerified`, remplacer `r, _ := LoadRegistry(path)` par
`r := Registry{entries: map[string]Verified{}}`. `TestRegistryRoundTrip` tombe (`aaa lost`).
Remettre.

- [ ] Step 6 : commit

```bash
git add tools/internal/sqlq/registry.go tools/internal/sqlq/registry_test.go
git commit -m "feat(sqlq): registry of successful runs keyed by content hash" -m "sqlq never writes into the plugin or into tsql-scripts, so a Verified line inside the file cannot be how a successful run is recorded. Keying on the content rather than the path keeps a verification across a rename and drops it the moment the SQL changes."
```

---

### Task 7 : le catalogue

Spec §7, §8 (`rejected`), §10 (empreinte), §12 (forme JSON).

Files :
- Create : `tools/internal/sqlq/catalog.go`, `tools/internal/sqlq/catalog_test.go`
- Create : `tools/internal/sqlq/testdata/catalog/` (arborescence ci-dessous)

Interfaces :
- Consumes : `StripBOM`, `ParseBlockHeader`, `ParseMarkedHeader`, `QueryParams`,
  `DirtyReads`, `Refusals`, `AnalyseOverrides`, `OverrideParam`, `Registry`, `Verified`,
  `ContentHash`.
- Produces :
  - `type Source string` avec `SourceBundled = "bundled"`, `SourcePersonal = "personal"`,
    `SourceTsqlScripts = "tsql-scripts"`
  - `type CatalogParam struct { Name, Type, Default string }` (tags `name`,
    `type,omitempty`, `default,omitempty`)
  - `type Entry struct { Name, Summary string; Params []CatalogParam; Scope string; Source Source; Path string; Verified *Verified; DirtyReads, Heavy bool; Rejected string; SQL string; Hash string; Overrides []OverrideParam; QueryParams []string }`
    avec `func (e Entry) MarshalJSON() ([]byte, error)` : une entrée `rejected` ne publie que
    `name`, `source`, `path`, `rejected` ; une entrée valide publie `name`, `summary`,
    `params` (tableau), `scope`, `source`, `path`, `verified` (null si absent),
    `dirty_reads`, `heavy`.
  - `type CatalogConfig struct { BundledDir, PersonalDir, TsqlScriptsDir, Profile string; ProfileNames []string; Registry Registry }`
  - `type Catalog struct { Entries []Entry; Messages []string }`
  - `func LoadCatalog(cfg CatalogConfig) Catalog`
  - `func (c Catalog) Find(name string) (Entry, bool)`
  - `func ProfileDir(personalDir, profile string, all []string) (string, error)`
  - `func ValidQueryName(name string) bool`
  - `func DefaultQueriesDir() string` : `~/.config/db-ai-toolkit/queries`

Arborescence de test (créer chaque fichier tel quel) :

```
testdata/catalog/bundled/tables-largest.sql      /* Largest tables.\n\n    Parameters: none.\n*/\nSELECT TOP (5) name FROM sys.tables;\n
testdata/catalog/bundled/object-refs.sql         /* References of an object.\n\n    Parameter: @name - the object.\n*/\nSELECT TOP (5) name FROM sys.objects WHERE name = @name;\n
testdata/catalog/bundled/Bad_Name.sql            /* Bad name.\n*/\nSELECT 1;\n
testdata/catalog/personal/_generic/tables-largest.sql   /* Shadow attempt.\n*/\nSELECT 2;\n
testdata/catalog/personal/_generic/my-generic.sql       /* Mine.\n*/\nSELECT 3;\n
testdata/catalog/personal/profiles/au-prd/node1/orders-late.sql  /* Late orders.\n*/\nSELECT 4;\n
testdata/catalog/personal/profiles/other/orders-late.sql         /* Other profile.\n*/\nSELECT 5;\n
testdata/catalog/tsql/diagnostics/sessions-from-host.sql   (le markedTemplate de la tâche 3, sans heavy, suivi de SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;)
testdata/catalog/tsql/diagnostics/waits.sql        -- Waits\n-- sqlq: name=waits\nSELECT 1;\nGO\n
testdata/catalog/tsql/diagnostics/unmarked.sql     -- Nothing\nSELECT 1;\n
testdata/catalog/tsql/a/dup.sql                    -- Dup A\n-- sqlq: name=dup\nSELECT 1;\n
testdata/catalog/tsql/b/dup.sql                    -- Dup B\n-- sqlq: name=dup\nSELECT 2;\n
testdata/catalog/tsql/x/takes-bundled.sql          -- Takes a bundled name\n-- sqlq: name=tables-largest\nSELECT 1;\n
testdata/catalog/tsql/.git/ignored.sql             -- Ignored\n-- sqlq: name=ignored\nSELECT 1;\n
```

Les `\n` ci-dessus sont des fins de ligne réelles. Le dossier `.git` de testdata est un
répertoire ordinaire nommé `.git` ; vérifier que `git add` l'accepte (un dépôt Git refuse
d'indexer un chemin `.git`) : s'il est refusé, le créer dans le test avec `t.TempDir()` au
lieu de testdata, et le dire dans le rapport.

- [ ] Step 1 : tests qui échouent

```go
package sqlq

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testCatalogConfig(profile string) CatalogConfig {
	root := filepath.Join("testdata", "catalog")
	return CatalogConfig{
		BundledDir:     filepath.Join(root, "bundled"),
		PersonalDir:    filepath.Join(root, "personal"),
		TsqlScriptsDir: filepath.Join(root, "tsql"),
		Profile:        profile,
		ProfileNames:   []string{"AU-PRD/node1", "other"},
	}
}

func entryByPath(c Catalog, source Source, path string) (Entry, bool) {
	for _, e := range c.Entries {
		if e.Source == source && e.Path == path {
			return e, true
		}
	}
	return Entry{}, false
}

func TestCatalogListsExactlyTheFilesPresent(t *testing.T) {
	c := LoadCatalog(testCatalogConfig(""))
	var got []string
	for _, e := range c.Entries {
		got = append(got, string(e.Source)+":"+e.Path)
	}
	want := []string{
		"bundled:Bad_Name.sql", "bundled:object-refs.sql", "bundled:tables-largest.sql",
		"personal:_generic/my-generic.sql", "personal:_generic/tables-largest.sql",
		"tsql-scripts:a/dup.sql", "tsql-scripts:b/dup.sql",
		"tsql-scripts:diagnostics/sessions-from-host.sql", "tsql-scripts:diagnostics/waits.sql",
		"tsql-scripts:x/takes-bundled.sql",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("entries:\n got %v\nwant %v", got, want)
	}
}

func TestMarkedScriptRefusedByGuardIsListedAsRejected(t *testing.T) {
	e, _ := entryByPath(LoadCatalog(testCatalogConfig("")), SourceTsqlScripts, "diagnostics/waits.sql")
	if e.Rejected != "batch separator GO at line 4" {
		t.Errorf("rejected = %q", e.Rejected)
	}
}

func TestInvalidFileNameIsRejected(t *testing.T) {
	e, _ := entryByPath(LoadCatalog(testCatalogConfig("")), SourceBundled, "Bad_Name.sql")
	if !strings.Contains(e.Rejected, "invalid name") {
		t.Errorf("rejected = %q", e.Rejected)
	}
}

func TestBundledWinsCollisionOthersRejected(t *testing.T) {
	c := LoadCatalog(testCatalogConfig(""))
	b, _ := entryByPath(c, SourceBundled, "tables-largest.sql")
	p, _ := entryByPath(c, SourcePersonal, "_generic/tables-largest.sql")
	x, _ := entryByPath(c, SourceTsqlScripts, "x/takes-bundled.sql")
	if b.Rejected != "" || !strings.Contains(p.Rejected, "taken by bundled") || !strings.Contains(x.Rejected, "taken by bundled") {
		t.Errorf("bundled %q, personal %q, tsql %q", b.Rejected, p.Rejected, x.Rejected)
	}
	if e, ok := c.Find("tables-largest"); !ok || e.Source != SourceBundled {
		t.Errorf("Find must return the bundled entry: %+v", e)
	}
}

func TestCollisionBetweenNonBundledRejectsAll(t *testing.T) {
	c := LoadCatalog(testCatalogConfig(""))
	a, _ := entryByPath(c, SourceTsqlScripts, "a/dup.sql")
	b, _ := entryByPath(c, SourceTsqlScripts, "b/dup.sql")
	if a.Rejected == "" || b.Rejected == "" {
		t.Errorf("both dup entries must be rejected: %q / %q", a.Rejected, b.Rejected)
	}
	if _, ok := c.Find("dup"); ok {
		t.Errorf("Find must not return a rejected entry")
	}
}

func TestProfileViewListsOnlyThatProfile(t *testing.T) {
	c := LoadCatalog(testCatalogConfig("AU-PRD/node1"))
	e, ok := entryByPath(c, SourcePersonal, "profiles/au-prd/node1/orders-late.sql")
	if !ok || e.Scope != "AU-PRD/node1" || e.Rejected != "" {
		t.Errorf("own profile entry: %+v ok=%v", e, ok)
	}
	if _, ok := entryByPath(c, SourcePersonal, "profiles/other/orders-late.sql"); ok {
		t.Errorf("another profile's directory must not be in the view")
	}
}

func TestProfileDirRejectsDotDotAndCaseTwins(t *testing.T) {
	if _, err := ProfileDir("/q", "../etc", nil); err == nil {
		t.Error("'..' accepted")
	}
	if _, err := ProfileDir("/q", "a/./b", nil); err == nil {
		t.Error("'.' accepted")
	}
	if _, err := ProfileDir("/q", "Prod-ERP", []string{"Prod-ERP", "prod-erp"}); err == nil {
		t.Error("case twins accepted")
	}
	got, err := ProfileDir("/q", "AU-PRD/dbcsqlaueprd01-nbr", []string{"AU-PRD/dbcsqlaueprd01-nbr"})
	if err != nil || got != filepath.Join("/q", "profiles", "au-prd", "dbcsqlaueprd01-nbr") {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestTsqlEntryCarriesTypedParamsAndDirtyReads(t *testing.T) {
	e, _ := entryByPath(LoadCatalog(testCatalogConfig("")), SourceTsqlScripts, "diagnostics/sessions-from-host.sql")
	if e.Rejected != "" || !e.DirtyReads || e.Name != "sessions-from-host" ||
		len(e.Params) != 1 || e.Params[0] != (CatalogParam{Name: "hostname", Type: "sysname", Default: "N'%'"}) {
		t.Errorf("entry = %+v", e)
	}
}

func TestVerifiedSurvivesRenameAndMarkerEdit(t *testing.T) {
	dir := t.TempDir()
	body := "SET NOCOUNT ON;\nSELECT 1;\n"
	os.WriteFile(filepath.Join(dir, "a.sql"), []byte("-- One\n-- sqlq: name=one\n"+body), 0o600)
	cfg := CatalogConfig{TsqlScriptsDir: dir}
	e, _ := LoadCatalog(cfg).Find("one")
	reg := filepath.Join(t.TempDir(), "verified.json")
	RecordVerified(reg, e.Hash, Verified{Date: "2026-10-04", Profile: "p"})
	os.Remove(filepath.Join(dir, "a.sql"))
	os.WriteFile(filepath.Join(dir, "b.sql"), []byte("-- One\n-- sqlq: name=uno heavy\n"+body), 0o600)
	cfg.Registry, _ = LoadRegistry(reg)
	if e, _ := LoadCatalog(cfg).Find("uno"); e.Verified == nil {
		t.Errorf("verification lost by a rename and a marker edit")
	}
}

func TestVerifiedDropsWhenSQLChanges(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.sql"), []byte("-- One\n-- sqlq: name=one\nSELECT 1;\n"), 0o600)
	cfg := CatalogConfig{TsqlScriptsDir: dir}
	e, _ := LoadCatalog(cfg).Find("one")
	reg := filepath.Join(t.TempDir(), "verified.json")
	RecordVerified(reg, e.Hash, Verified{Date: "d", Profile: "p"})
	os.WriteFile(filepath.Join(dir, "a.sql"), []byte("-- One\n-- sqlq: name=one\nSELECT 2;\n"), 0o600)
	cfg.Registry, _ = LoadRegistry(reg)
	if e, _ := LoadCatalog(cfg).Find("one"); e.Verified != nil {
		t.Errorf("verification survived a change of SQL")
	}
}

func TestMissingTsqlScriptsSourceIsAMessageNotAnError(t *testing.T) {
	c := LoadCatalog(CatalogConfig{BundledDir: filepath.Join("testdata", "catalog", "bundled")})
	if len(c.Messages) == 0 || !strings.Contains(c.Messages[0], "not configured") {
		t.Errorf("messages = %v", c.Messages)
	}
	c = LoadCatalog(CatalogConfig{TsqlScriptsDir: filepath.Join(t.TempDir(), "absent")})
	if len(c.Messages) == 0 || !strings.Contains(c.Messages[0], "not found") {
		t.Errorf("messages = %v", c.Messages)
	}
}

func TestListQueriesPublishesNoPathServerOrSQL(t *testing.T) {
	cfg := testCatalogConfig("AU-PRD/node1")
	abs, _ := filepath.Abs(cfg.TsqlScriptsDir)
	cfg.TsqlScriptsDir = abs
	b, _ := json.Marshal(LoadCatalog(cfg))
	s := string(b)
	for _, forbidden := range []string{abs, "SELECT", "\"sql\"", "\"hash\"", "/home/", "\\\\Users"} {
		if strings.Contains(s, forbidden) {
			t.Errorf("catalogue JSON contains %q", forbidden)
		}
	}
	if !strings.Contains(s, `"verified":null`) {
		t.Errorf("valid entries must carry verified:null")
	}
}
```

`Catalog` se sérialise en `{"queries":[...],"messages":[...]}` (tags `queries` et
`messages`, tableaux jamais `null`).

- [ ] Step 2 : vérifier l'échec

Run : `cd tools && go test ./internal/sqlq -run '^(TestCatalogListsExactlyTheFilesPresent|TestMarkedScriptRefusedByGuardIsListedAsRejected|TestInvalidFileNameIsRejected|TestBundledWinsCollisionOthersRejected|TestCollisionBetweenNonBundledRejectsAll|TestProfileViewListsOnlyThatProfile|TestProfileDirRejectsDotDotAndCaseTwins|TestTsqlEntryCarriesTypedParamsAndDirtyReads|TestVerifiedSurvivesRenameAndMarkerEdit|TestVerifiedDropsWhenSQLChanges|TestMissingTsqlScriptsSourceIsAMessageNotAnError|TestListQueriesPublishesNoPathServerOrSQL)$' -count=1 -v`
Expected : échec de compilation.

- [ ] Step 3 : implémenter

```go
package sqlq

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type Source string

const (
	SourceBundled     Source = "bundled"
	SourcePersonal    Source = "personal"
	SourceTsqlScripts Source = "tsql-scripts"
)

var queryName = regexp.MustCompile(`^[a-z][a-z0-9-]{1,48}$`)
var profileSegment = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// ValidQueryName checks a name before it is ever turned into a path.
func ValidQueryName(name string) bool { return queryName.MatchString(name) }

// DefaultQueriesDir is the personal layer's root.
func DefaultQueriesDir() string {
	return filepath.Join(filepath.Dir(DefaultProfilePath()), "queries")
}

type CatalogParam struct {
	Name    string `json:"name"`
	Type    string `json:"type,omitempty"`
	Default string `json:"default,omitempty"`
}

type Entry struct {
	Name, Summary string
	Params        []CatalogParam
	Scope         string
	Source        Source
	Path          string
	Verified      *Verified
	DirtyReads    bool
	Heavy         bool
	Rejected      string

	SQL         string          // the bytes read, BOM stripped: what -saved executes
	Hash        string          // registry key
	Overrides   []OverrideParam // tsql-scripts only
	QueryParams []string        // bundled and personal only
}

func (e Entry) MarshalJSON() ([]byte, error) {
	if e.Rejected != "" {
		return json.Marshal(struct {
			Name     string `json:"name"`
			Source   Source `json:"source"`
			Path     string `json:"path"`
			Rejected string `json:"rejected"`
		}{e.Name, e.Source, e.Path, e.Rejected})
	}
	params := e.Params
	if params == nil {
		params = []CatalogParam{}
	}
	return json.Marshal(struct {
		Name       string         `json:"name"`
		Summary    string         `json:"summary"`
		Params     []CatalogParam `json:"params"`
		Scope      string         `json:"scope"`
		Source     Source         `json:"source"`
		Path       string         `json:"path"`
		Verified   *Verified      `json:"verified"`
		DirtyReads bool           `json:"dirty_reads"`
		Heavy      bool           `json:"heavy"`
	}{e.Name, e.Summary, params, e.Scope, e.Source, e.Path, e.Verified, e.DirtyReads, e.Heavy})
}

type CatalogConfig struct {
	BundledDir, PersonalDir, TsqlScriptsDir string
	Profile                                 string   // "" for the profile-free view
	ProfileNames                            []string // every configured profile, for case twins
	Registry                                Registry
}

type Catalog struct {
	Entries  []Entry  `json:"queries"`
	Messages []string `json:"messages"`
}

func (c Catalog) MarshalJSON() ([]byte, error) {
	type alias Catalog
	out := alias(c)
	if out.Entries == nil {
		out.Entries = []Entry{}
	}
	if out.Messages == nil {
		out.Messages = []string{}
	}
	return json.Marshal(out)
}

// Find returns the valid entry of that name in the view.
func (c Catalog) Find(name string) (Entry, bool) {
	for _, e := range c.Entries {
		if e.Name == name && e.Rejected == "" {
			return e, true
		}
	}
	return Entry{}, false
}

// ProfileDir maps a profile name to its personal directory. The names
// registered-servers generates carry "/" for SSMS groups, which become
// subdirectories; anything that could leave the directory, or share it with
// another profile on a case-insensitive file system, is refused.
func ProfileDir(personalDir, profile string, all []string) (string, error) {
	lower := strings.ToLower(profile)
	segs := strings.Split(lower, "/")
	for _, s := range segs {
		if s == "." || s == ".." || !profileSegment.MatchString(s) {
			return "", fmt.Errorf("profile %q cannot name a directory (segment %q)", profile, s)
		}
	}
	for _, other := range all {
		if other != profile && strings.EqualFold(other, profile) {
			return "", fmt.Errorf("profiles %q and %q differ only by case and would share a directory", profile, other)
		}
	}
	return filepath.Join(append([]string{personalDir, "profiles"}, segs...)...), nil
}

// LoadCatalog reads every source of the view and resolves name collisions.
func LoadCatalog(cfg CatalogConfig) Catalog {
	var c Catalog
	if cfg.BundledDir != "" {
		c.Entries = append(c.Entries, loadBlockDir(cfg, cfg.BundledDir, "", SourceBundled, "generic")...)
	}
	if cfg.PersonalDir != "" {
		c.Entries = append(c.Entries, loadBlockDir(cfg, filepath.Join(cfg.PersonalDir, "_generic"), "_generic", SourcePersonal, "generic")...)
		if cfg.Profile != "" {
			dir, err := ProfileDir(cfg.PersonalDir, cfg.Profile, cfg.ProfileNames)
			if err != nil {
				c.Messages = append(c.Messages, "personal queries disabled for this profile: "+err.Error())
			} else {
				rel, _ := filepath.Rel(cfg.PersonalDir, dir)
				c.Entries = append(c.Entries, loadBlockDir(cfg, dir, filepath.ToSlash(rel), SourcePersonal, cfg.Profile)...)
			}
		}
	}
	switch {
	case cfg.TsqlScriptsDir == "":
		c.Messages = append(c.Messages, "tsql-scripts source not configured")
	default:
		if st, err := os.Stat(cfg.TsqlScriptsDir); err != nil || !st.IsDir() {
			c.Messages = append(c.Messages, "tsql-scripts source not found")
		} else {
			c.Entries = append(c.Entries, loadTsqlScripts(cfg)...)
		}
	}
	resolveCollisions(c.Entries)
	return c
}

func loadBlockDir(cfg CatalogConfig, dir, relPrefix string, src Source, scope string) []Entry {
	paths, _ := filepath.Glob(filepath.Join(dir, "*.sql"))
	sort.Strings(paths)
	var out []Entry
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		rel := filepath.Base(p)
		if relPrefix != "" {
			rel = relPrefix + "/" + rel
		}
		e := Entry{Name: strings.TrimSuffix(filepath.Base(p), ".sql"), Source: src, Path: rel, Scope: scope}
		if err != nil {
			e.Rejected = "unreadable"
			out = append(out, e)
			continue
		}
		body := StripBOM(raw)
		e.SQL, e.Hash = string(body), ContentHash(body)
		e.Rejected = blockEntryProblem(&e)
		e.Verified = cfg.Registry.Lookup(e.Hash)
		out = append(out, e)
	}
	return out
}

func blockEntryProblem(e *Entry) string {
	if !ValidQueryName(e.Name) {
		return "invalid name"
	}
	h, err := ParseBlockHeader(e.SQL)
	if err != nil {
		return err.Error()
	}
	e.Summary, e.Heavy = h.Summary, h.Heavy
	e.QueryParams = QueryParams(e.SQL)
	if h.HasParamsLine && strings.Join(h.Params, ",") != strings.Join(e.QueryParams, ",") {
		return fmt.Sprintf("Parameters line names %v but the SQL references %v", h.Params, e.QueryParams)
	}
	for _, p := range e.QueryParams {
		e.Params = append(e.Params, CatalogParam{Name: p})
	}
	if r := Refusals(e.SQL); len(r) > 0 {
		return r[0].Reason()
	}
	e.DirtyReads = DirtyReads(e.SQL)
	return ""
}

func loadTsqlScripts(cfg CatalogConfig) []Entry {
	var out []Entry
	root := cfg.TsqlScriptsDir
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(p), ".sql") {
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		body := string(StripBOM(raw))
		h, found, herr := ParseMarkedHeader(body)
		if !found {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		e := Entry{Name: h.Marker.Name, Source: SourceTsqlScripts, Path: filepath.ToSlash(rel), Scope: "generic", SQL: body}
		e.Hash = ContentHash([]byte(withoutLine(body, h.Marker.Line)))
		switch {
		case herr != nil:
			e.Rejected = herr.Error()
		case !ValidQueryName(e.Name):
			e.Rejected = "invalid name"
		default:
			e.Rejected = tsqlEntryProblem(&e, h)
		}
		e.Verified = cfg.Registry.Lookup(e.Hash)
		out = append(out, e)
		return nil
	})
	return out
}

func tsqlEntryProblem(e *Entry, h MarkedHeader) string {
	e.Summary, e.Heavy = h.Summary, h.Marker.Heavy
	if r := Refusals(e.SQL); len(r) > 0 {
		return r[0].Reason()
	}
	ps, err := AnalyseOverrides(e.SQL, h.Marker.Params)
	if err != nil {
		return err.Error()
	}
	e.Overrides = ps
	for _, p := range ps {
		e.Params = append(e.Params, CatalogParam{Name: p.Name, Type: p.Type.String(), Default: p.Default})
	}
	e.DirtyReads = DirtyReads(e.SQL)
	return ""
}

// withoutLine drops one 1-based line, so editing the marker does not change
// the hash of the SQL it describes. Line 0 (no marker) returns the text as is.
func withoutLine(text string, line int) string {
	if line <= 0 {
		return text
	}
	lines := strings.SplitAfter(text, "\n")
	if line > len(lines) {
		return text
	}
	return strings.Join(append(lines[:line-1:line-1], lines[line:]...), "")
}

// resolveCollisions applies the rule of the spec's section 7: bundled keeps its
// name, anything else claiming it is rejected; among non-bundled sources, every
// holder of a duplicated name is rejected.
func resolveCollisions(entries []Entry) {
	holders := map[string][]int{}
	for i, e := range entries {
		if e.Name != "" {
			holders[e.Name] = append(holders[e.Name], i)
		}
	}
	for name, idx := range holders {
		if len(idx) < 2 {
			continue
		}
		bundled := -1
		for _, i := range idx {
			if entries[i].Source == SourceBundled && entries[i].Rejected == "" {
				bundled = i
			}
		}
		for _, i := range idx {
			switch {
			case i == bundled:
			case bundled >= 0:
				entries[i].Rejected = fmt.Sprintf("name %q is taken by bundled", name)
			default:
				entries[i].Rejected = fmt.Sprintf("name %q is defined more than once", name)
			}
		}
	}
}
```

Le message « defined more than once » ne nomme pas les autres chemins, pour que la raison
reste courte et sans chemin ; le JSON liste déjà chaque entrée avec son `path`.

- [ ] Step 4 : vérifier

Relancer le step 2. Expected : 12 tests `PASS` (`TestCatalogListsExactlyTheFilesPresent`,
`TestMarkedScriptRefusedByGuardIsListedAsRejected`, `TestInvalidFileNameIsRejected`,
`TestBundledWinsCollisionOthersRejected`, `TestCollisionBetweenNonBundledRejectsAll`,
`TestProfileViewListsOnlyThatProfile`, `TestProfileDirRejectsDotDotAndCaseTwins`,
`TestTsqlEntryCarriesTypedParamsAndDirtyReads`, `TestVerifiedSurvivesRenameAndMarkerEdit`,
`TestVerifiedDropsWhenSQLChanges`, `TestMissingTsqlScriptsSourceIsAMessageNotAnError`,
`TestListQueriesPublishesNoPathServerOrSQL`). Un autre nombre : le filtre ou le travail est faux.

Puis : `cd tools && go test ./... -count=1 && go vet ./...`.

- [ ] Step 5 : casser pour voir tomber

Dans `resolveCollisions`, supprimer la branche `case bundled >= 0`. Relancer :
`TestBundledWinsCollisionOthersRejected` tombe. Remettre. Puis remplacer
`withoutLine(body, h.Marker.Line)` par `body` : `TestVerifiedSurvivesRenameAndMarkerEdit`
tombe. Remettre.

- [ ] Step 6 : commit

```bash
git add tools/internal/sqlq/catalog.go tools/internal/sqlq/catalog_test.go tools/internal/sqlq/testdata/catalog
git commit -m "feat(sqlq): the query catalogue across bundled, personal and tsql-scripts sources" -m "Every file that cannot run is listed with a short reason instead of disappearing, and the reasons name a keyword and a line, never SQL, because the catalogue reaches the model provider on every session. The bundled canon keeps its name when another source claims it, since the skill depends on it, while the claimant is listed as rejected rather than silently hidden."
```

---

### Task 8 : le fichier de `-save-query`

Spec §12 (`-save-query`).

Files :
- Create : `tools/internal/sqlq/save.go`, `tools/internal/sqlq/save_test.go`

Interfaces :
- Consumes : `QueryParams`, `ParseBlockHeader`.
- Produces : `func SavedFileContent(summary, sqlText string) (string, error)`

- [ ] Step 1 : tests qui échouent

```go
package sqlq

import (
	"strings"
	"testing"
)

func TestSaveRefusesCommentBreakingSummary(t *testing.T) {
	for _, s := range []string{"a /* b", "a */ SELECT 1; /*", "line\nbreak", "cr\rhere", "", "   "} {
		if _, err := SavedFileContent(s, "SELECT 1;"); err == nil {
			t.Errorf("summary %q accepted", s)
		}
	}
}

func TestSavedFileReparsesAsValidEntry(t *testing.T) {
	sqlText := "SELECT TOP (5) name FROM sys.objects WHERE name LIKE @pattern AND type = @Type;"
	got, err := SavedFileContent("Objects matching a pattern.", sqlText)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, "\n"+sqlText) {
		t.Errorf("the executed bytes must follow the header unchanged:\n%s", got)
	}
	h, err := ParseBlockHeader(got)
	if err != nil || h.Summary != "Objects matching a pattern." || strings.Join(h.Params, ",") != "pattern,type" {
		t.Errorf("reparsed %+v, %v", h, err)
	}
	if strings.Join(QueryParams(got), ",") != "pattern,type" {
		t.Errorf("params of the saved file: %v", QueryParams(got))
	}
}
```

- [ ] Step 2 : vérifier l'échec

Run : `cd tools && go test ./internal/sqlq -run '^(TestSaveRefusesCommentBreakingSummary|TestSavedFileReparsesAsValidEntry)$' -count=1 -v`
Expected : échec de compilation.

- [ ] Step 3 : implémenter

```go
package sqlq

import (
	"errors"
	"strings"
)

// SavedFileContent builds what -save-query writes: a header with the summary
// and a Parameters line derived from the SQL, then the executed bytes unchanged.
// A summary that could open or close a comment is refused: "/*" would turn the
// whole file into one unterminated comment, verified and returning nothing;
// "*/" would inject SQL.
func SavedFileContent(summary, sqlText string) (string, error) {
	s := strings.TrimSpace(summary)
	switch {
	case s == "":
		return "", errors.New("-summary is empty")
	case strings.ContainsAny(summary, "\r\n"):
		return "", errors.New("-summary must fit on one line")
	case strings.Contains(summary, "/*") || strings.Contains(summary, "*/"):
		return "", errors.New("-summary must not contain /* or */")
	}
	params := "none."
	if ps := QueryParams(sqlText); len(ps) > 0 {
		params = "@" + strings.Join(ps, ", @") + "."
	}
	return "/*  " + s + "\n\n    Parameters: " + params + "\n*/\n" + sqlText, nil
}
```

- [ ] Step 4 : vérifier

Relancer le step 2 : 2 tests `PASS`.

- [ ] Step 5 : casser pour voir tomber

Supprimer le cas `strings.Contains(summary, "/*") || ...`. Relancer :
`TestSaveRefusesCommentBreakingSummary` tombe. Remettre.

- [ ] Step 6 : commit

```bash
git add tools/internal/sqlq/save.go tools/internal/sqlq/save_test.go
git commit -m "feat(sqlq): build the file -save-query writes" -m "The summary comes from the agent and lands inside a comment, so the two character pairs that open or close one are refused before anything runs. The Parameters line is derived from the SQL rather than written by the agent, so the header cannot disagree with what it describes."
```

---

### Task 9 : `-list-queries` et `-saved`

Spec §7, §11 (exécution), §12.

Files :
- Modify : `tools/cmd/sqlq/main.go`
- Create : `tools/cmd/sqlq/catalog_cli_test.go`
- Modify : `tools/cmd/sqlq/integration_test.go`

Interfaces :
- Consumes : tout ce que produisent les tâches 1 à 7.
- Produces :
  - options : `listQueries bool`, `saved string`, `queriesDir string`, `tsqlScriptsDir string`
  - `func catalogConfig(o options, profileNames []string) sqlq.CatalogConfig`
  - `func bundledQueriesDir() string` : `<dir(os.Executable())>/../skills/live-query/queries`
  - `func prepareSaved(e sqlq.Entry, params paramList) (sqlText string, args []any, run *sqlq.SavedRun, err error)`
  - dans `result.go` : `type SavedRun struct { Name string; Source sqlq.Source; Path string; Params map[string]string; Defaults []string; Verified *Verified }`
    (tags `name`, `source`, `path`, `params` (objet, jamais null), `defaults` (tableau,
    jamais null), `verified`) et `Result.Saved *SavedRun` (tag `saved,omitempty`).
    `SavedRun` vit dans `internal/sqlq/result.go`, donc `Source` y est écrit `Source`.

- [ ] Step 1 : tests qui échouent

`tools/cmd/sqlq/catalog_cli_test.go` :

```go
package main

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/sqlq"
)

func tsqlEntry(t *testing.T) sqlq.Entry {
	t.Helper()
	src := "-- Sessions\n-- sqlq: name=sessions params=hostname,top_n\nDECLARE @hostname sysname = N'%';\nDECLARE @top_n int = 20;\nSELECT TOP (@top_n) host_name FROM sys.dm_exec_sessions WHERE host_name LIKE @hostname;\n"
	ps, err := sqlq.AnalyseOverrides(src, []string{"hostname", "top_n"})
	if err != nil {
		t.Fatal(err)
	}
	return sqlq.Entry{Name: "sessions", Source: sqlq.SourceTsqlScripts, Path: "d/s.sql", SQL: src, Overrides: ps}
}

func TestPrepareSavedRewritesAndBinds(t *testing.T) {
	text, args, run, err := prepareSaved(tsqlEntry(t), paramList{"HostName=SRV-APP01"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "DECLARE @hostname sysname = @sqlq_hostname;") || !strings.Contains(text, "DECLARE @top_n int = 20;") {
		t.Errorf("text = %s", text)
	}
	if len(args) != 1 || args[0] != sql.Named("sqlq_hostname", "SRV-APP01") {
		t.Errorf("args = %#v", args)
	}
	if run.Params["hostname"] != "SRV-APP01" || strings.Join(run.Defaults, ",") != "top_n" {
		t.Errorf("run = %+v", run)
	}
}

func TestUnknownParamRefusedBeforeConnecting(t *testing.T) {
	_, _, _, err := prepareSaved(tsqlEntry(t), paramList{"hostnme=SRV"})
	if err == nil || !strings.Contains(err.Error(), "hostnme") {
		t.Errorf("err = %v", err)
	}
}

func TestDuplicateParamIsRefused(t *testing.T) {
	if _, _, _, err := prepareSaved(tsqlEntry(t), paramList{"hostname=a", "HOSTNAME=b"}); err == nil {
		t.Error("a parameter passed twice must be refused")
	}
}

func TestInvalidValueRefusedBeforeConnecting(t *testing.T) {
	if _, _, _, err := prepareSaved(tsqlEntry(t), paramList{"top_n=many"}); err == nil {
		t.Error("top_n=many accepted for an int")
	}
}

func TestMissingParameterIsRefusedBeforeConnecting(t *testing.T) {
	e := sqlq.Entry{Name: "refs", Source: sqlq.SourceBundled, Path: "refs.sql",
		SQL: "SELECT name FROM sys.objects WHERE name = @name;", QueryParams: []string{"name"}}
	if _, _, _, err := prepareSaved(e, nil); err == nil || !strings.Contains(err.Error(), `"name" required`) {
		t.Errorf("err = %v", err)
	}
	if _, _, _, err := prepareSaved(e, paramList{"name=x", "extra=y"}); err == nil {
		t.Error("an unknown -param on a bundled query must be refused")
	}
	_, args, _, err := prepareSaved(e, paramList{"name=dbo.T"})
	if err != nil || len(args) != 1 || args[0] != sql.Named("name", "dbo.T") {
		t.Errorf("args = %#v, err = %v", args, err)
	}
}

func TestListQueriesWorksWithoutProfilesFile(t *testing.T) {
	o := options{listQueries: true, profilesPath: "/nonexistent/profiles.json", queriesDir: "../../internal/sqlq/testdata/catalog/bundled"}
	out, code := captureRun(t, o)
	if code != exitOK || !strings.Contains(out, `"name":"tables-largest"`) {
		t.Errorf("code %d, out %s", code, out)
	}
}
```

`captureRun` est une aide à ajouter dans ce fichier : elle redirige `os.Stdout` vers un
`os.Pipe` le temps d'un `run(o)` et rend la sortie et le code. Les tests de `cmd/sqlq`
tournent en séquence (pas de `t.Parallel()`), ce qui rend la redirection sûre.

```go
func captureRun(t *testing.T, o options) (string, int) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	code := run(o)
	w.Close()
	os.Stdout = old
	b, _ := io.ReadAll(r)
	return string(b), code
}
```

(ajouter `io` et `os` aux imports.)

Le cas `-list-queries` sans `-profiles` lisible doit aussi isoler la vraie couche
personnelle : `o.personalDir` n'existe pas comme drapeau, donc `catalogConfig` lit
`$DB_AI_TOOLKIT_QUERIES` s'il est défini, sinon `sqlq.DefaultQueriesDir()`, et le test pose
`t.Setenv("DB_AI_TOOLKIT_QUERIES", t.TempDir())`, `t.Setenv("DB_AI_TOOLKIT_TSQL_SCRIPTS", "")`
et `t.Setenv("DB_AI_TOOLKIT_REGISTRY", filepath.Join(t.TempDir(), "v.json"))` (même règle
pour le registre : `$DB_AI_TOOLKIT_REGISTRY`, sinon `sqlq.DefaultRegistryPath()`). Ces deux
variables sont des points d'entrée de test et de développement, documentés comme tels dans
le README (tâche 11). Ajouter ces trois `t.Setenv` en tête de
`TestListQueriesWorksWithoutProfilesFile`.

Ajouter à `integration_test.go` :

```go
func TestOverrideBindsTypedValuesOnServer(t *testing.T) {
	p, resolve := testProfile(t)
	src := "-- When\n-- sqlq: name=when params=d,n\nSET DATEFORMAT ydm;\nDECLARE @d datetime = '2000-01-01';\nDECLARE @n varchar(10) = 'x';\nSELECT CONVERT(char(10), @d, 23) AS d, @n AS n;\n"
	ps, err := sqlq.AnalyseOverrides(src, []string{"d", "n"})
	if err != nil {
		t.Fatal(err)
	}
	e := sqlq.Entry{Name: "when", Source: sqlq.SourceTsqlScripts, SQL: src, Overrides: ps}
	text, args, _, err := prepareSaved(e, paramList{"d=2026-10-04", "n=ROW"})
	if err != nil {
		t.Fatal(err)
	}
	res, code := execute(p, text, args, options{maxRows: 5, timeoutSec: 30}, resolve)
	if code != exitOK || len(res.Rows) != 1 || res.Rows[0]["d"] != "2026-10-04" || res.Rows[0]["n"] != "ROW" {
		t.Errorf("code %d rows %+v error %+v", code, res.Rows, res.Error)
	}
}
```

Ce test prouve que la date passe sans dépendre de `DATEFORMAT` : sous `ydm`, la chaîne
`'2026-10-04'` serait lue comme le 10 avril.

- [ ] Step 2 : vérifier l'échec

Run : `cd tools && go test ./cmd/sqlq -run '^(TestPrepareSavedRewritesAndBinds|TestUnknownParamRefusedBeforeConnecting|TestDuplicateParamIsRefused|TestInvalidValueRefusedBeforeConnecting|TestMissingParameterIsRefusedBeforeConnecting|TestListQueriesWorksWithoutProfilesFile)$' -count=1 -v`
Expected : échec de compilation (`undefined: prepareSaved`).

- [ ] Step 3 : implémenter

Drapeaux, dans `defineFlags` :

```go
	fs.BoolVar(&o.listQueries, "list-queries", false, "print the query catalogue as JSON and exit; -profile adds that profile's saved queries")
	fs.StringVar(&o.saved, "saved", "", "run the catalogue query of that name")
	fs.StringVar(&o.queriesDir, "queries", "", "directory of the bundled queries (default: next to the binary)")
	fs.StringVar(&o.tsqlScriptsDir, "tsql-scripts", "", "local clone of tsql-scripts (default $DB_AI_TOOLKIT_TSQL_SCRIPTS)")
```

`catalogConfig` :

```go
func catalogConfig(o options, profileNames []string) sqlq.CatalogConfig {
	cfg := sqlq.CatalogConfig{
		BundledDir:     o.queriesDir,
		PersonalDir:    envOr("DB_AI_TOOLKIT_QUERIES", sqlq.DefaultQueriesDir()),
		TsqlScriptsDir: o.tsqlScriptsDir,
		Profile:        o.profileName,
		ProfileNames:   profileNames,
	}
	if cfg.BundledDir == "" {
		cfg.BundledDir = bundledQueriesDir()
	}
	if cfg.TsqlScriptsDir == "" {
		cfg.TsqlScriptsDir = os.Getenv("DB_AI_TOOLKIT_TSQL_SCRIPTS")
	}
	return cfg
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func registryPath() string { return envOr("DB_AI_TOOLKIT_REGISTRY", sqlq.DefaultRegistryPath()) }

// bundledQueriesDir finds the skill's queries relative to the binary, which the
// plugin installs in its bin/ directory.
func bundledQueriesDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Join(filepath.Dir(exe), "..", "skills", "live-query", "queries")
}
```

Dans `run`, avant tout le reste :

```go
	if o.listQueries {
		profiles, perr := sqlq.LoadProfiles(resolveProfilesPath(o.profilesPath))
		if o.profileName != "" {
			if perr != nil {
				return fail(exitUsage, perr)
			}
			if _, err := profiles.Get(o.profileName); err != nil {
				return fail(exitUsage, err)
			}
		}
		cfg := catalogConfig(o, profiles.Names())
		reg, msg := sqlq.LoadRegistry(registryPath())
		cfg.Registry = reg
		cat := sqlq.LoadCatalog(cfg)
		if msg != "" {
			cat.Messages = append(cat.Messages, msg)
		}
		return emit(cat)
	}
```

(`profiles.Names()` sur une map nil rend une liste vide ; le vérifier dans `profile.go:300`.)

`readQuery` devient à trois sources exclusives : `-query`, `-file`, `-saved`. Avec
`-saved` :

```go
	var saved *sqlq.SavedRun
	var entryHash string
	if o.saved != "" {
		cfg := catalogConfig(o, profiles.Names())
		cfg.Registry, _ = sqlq.LoadRegistry(registryPath())
		cat := sqlq.LoadCatalog(cfg)
		e, ok := cat.Find(o.saved)
		if !ok {
			return fail(exitUsage, missingQueryError(cat, cfg, o.saved))
		}
		text, args, run, err := prepareSaved(e, o.params)
		if err != nil {
			return fail(exitUsage, err)
		}
		sqlText, named, saved, entryHash = text, args, run, e.Hash
	}
```

```go
// missingQueryError explains why a name did not resolve in this profile's view.
func missingQueryError(cat sqlq.Catalog, cfg sqlq.CatalogConfig, name string) error {
	for _, e := range cat.Entries {
		if e.Name == name && e.Rejected != "" {
			return fmt.Errorf("query %q (%s:%s) is rejected: %s", name, e.Source, e.Path, e.Rejected)
		}
	}
	var bound string
	root := filepath.Join(cfg.PersonalDir, "profiles")
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == name+".sql" && bound == "" {
			rel, _ := filepath.Rel(root, filepath.Dir(p))
			bound = filepath.ToSlash(rel)
		}
		return nil
	})
	if bound != "" {
		return fmt.Errorf("query %q is bound to profile directory %q and cannot run on profile %q", name, bound, cfg.Profile)
	}
	return fmt.Errorf("no query named %q (use -list-queries -profile %s)", name, cfg.Profile)
}
```

(`name` a déjà passé `ValidQueryName` : ajouter ce contrôle en tête du bloc `-saved`, avec
`fail(exitUsage, …)`, pour qu'aucun nom non validé n'entre dans un chemin. Importer `io/fs`
et `sort`.)

`readQuery` prend `saved string` en troisième argument : deux sources ou plus parmi
`-query`, `-file`, `-saved` rendent `give exactly one of -query, -file or -saved`, aucune
rend `one of -query, -file or -saved is required`, et `-saved` seul rend `("", nil)`, le
texte venant du catalogue.

Les contrôles du garde-fou (`sqlq.Refusals`) s'appliquent ensuite au `sqlText` final,
réécrit compris, comme pour `-query`. `namedArgs(o.params)` n'est appelé que hors `-saved`.

`prepareSaved` :

```go
// prepareSaved turns a catalogue entry and the -param values into the text to
// send and the arguments to bind, refusing before any connection everything
// that would make the run differ from what the agent will report.
func prepareSaved(e sqlq.Entry, params paramList) (string, []any, *sqlq.SavedRun, error) {
	run := &sqlq.SavedRun{Name: e.Name, Source: e.Source, Path: e.Path, Params: map[string]string{}, Defaults: []string{}, Verified: e.Verified}
	passed := map[string]string{}
	for _, p := range params {
		name, value, _ := strings.Cut(p, "=")
		name = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(name), "@"))
		if _, dup := passed[name]; dup {
			return "", nil, nil, fmt.Errorf("-param %q given more than once", name)
		}
		passed[name] = value
	}
	if e.Source == sqlq.SourceTsqlScripts {
		known := map[string]sqlq.OverrideParam{}
		for _, o := range e.Overrides {
			known[o.Name] = o
		}
		var args []any
		use := map[string]bool{}
		for name, value := range passed {
			o, ok := known[name]
			if !ok {
				return "", nil, nil, fmt.Errorf("query %q has no parameter %q (declared: %s)", e.Name, name, overrideNames(e.Overrides))
			}
			v, err := sqlq.BindValue(o.Type, value)
			if err != nil {
				return "", nil, nil, fmt.Errorf("-param %s: %w", name, err)
			}
			args = append(args, sql.Named("sqlq_"+name, v))
			use[name] = true
			run.Params[name] = value
		}
		for _, o := range e.Overrides {
			if !use[o.Name] {
				run.Defaults = append(run.Defaults, o.Name)
			}
		}
		sort.Slice(args, func(i, j int) bool { return args[i].(sql.NamedArg).Name < args[j].(sql.NamedArg).Name })
		return sqlq.Rewrite(e.SQL, e.Overrides, use), args, run, nil
	}
	want := map[string]bool{}
	for _, q := range e.QueryParams {
		want[q] = true
		if _, ok := passed[q]; !ok {
			return "", nil, nil, fmt.Errorf("parameter %q required by query %q", q, e.Name)
		}
	}
	var args []any
	for _, q := range e.QueryParams {
		args = append(args, sql.Named(q, passed[q]))
		run.Params[q] = passed[q]
	}
	for name := range passed {
		if !want[name] {
			return "", nil, nil, fmt.Errorf("query %q has no parameter %q", e.Name, name)
		}
	}
	return e.SQL, args, run, nil
}

func overrideNames(ps []sqlq.OverrideParam) string {
	var names []string
	for _, p := range ps {
		names = append(names, p.Name)
	}
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}
```

Le message de `TestMissingParameterIsRefusedBeforeConnecting` attend `"name" required` :
le texte ci-dessus est `parameter "name" required by query "refs"`, qui le contient.

Après `execute` :

```go
	result, code := execute(profile, sqlText, named, o, resolver.Resolve)
	result.Saved = saved
	if code == exitOK && entryHash != "" {
		if err := sqlq.RecordVerified(registryPath(), entryHash, sqlq.Verified{
			Date: time.Now().Format("2006-01-02"), Profile: profile.Name}); err != nil {
			result.Messages = append(result.Messages, "verification not recorded: "+err.Error())
		}
	}
	_ = emit(result)
	return code
```

`SavedRun` et son `MarshalJSON` (pour `params` objet et `defaults` tableau jamais `null`)
s'ajoutent à `internal/sqlq/result.go`.

- [ ] Step 4 : vérifier

Relancer le filtre du step 2. Expected : 6 tests `PASS`.

Puis l'intégration, même invocation unique que la tâche 2 (setup inclus), avec le filtre
`'^TestOverrideBindsTypedValuesOnServer$'` : 1 test `PASS`, pas `SKIP`.

Puis `cd tools && go test ./... -count=1 && go vet ./...`. `TestEveryFlagIsDocumented`
va échouer sur les nouveaux drapeaux : c'est attendu, la documentation est la tâche 11.
Le noter dans le rapport et ne pas le corriger ici.

- [ ] Step 5 : casser pour voir tomber

Dans `prepareSaved`, supprimer le contrôle `dup`. `TestDuplicateParamIsRefused` tombe.
Remettre. Puis remplacer `sqlq.BindValue(o.Type, value)` par `value, nil` (en adaptant les
types) : `TestInvalidValueRefusedBeforeConnecting` tombe, et sur l'instance
`TestOverrideBindsTypedValuesOnServer` rend `2026-04-10` ou une erreur de conversion.
Coller la sortie. Remettre.

- [ ] Step 6 : commit

```bash
git add tools/cmd/sqlq/main.go tools/cmd/sqlq/catalog_cli_test.go tools/cmd/sqlq/integration_test.go tools/internal/sqlq/result.go
git commit -m "feat(sqlq): -list-queries and -saved" -m "A stored query runs by its name, with every check that could make the run differ from what the agent reports done before connecting: an unknown or repeated -param, a value its declared type would truncate or reread, a required parameter left out. The result says which values were passed and which defaults ran, and a successful run records its content in the registry."
```

---

### Task 10 : `-save-query`

Spec §4, §12.

Files :
- Modify : `tools/cmd/sqlq/main.go`
- Modify : `tools/cmd/sqlq/catalog_cli_test.go`, `tools/cmd/sqlq/integration_test.go`

Interfaces :
- Consumes : `SavedFileContent`, `ProfileDir`, `LoadCatalog`, `ValidQueryName`,
  `RecordVerified`, `ContentHash`, `catalogConfig`, `registryPath`, `captureRun`.
- Produces : options `saveQuery string`, `summary string` ;
  `func checkSave(o options, profile sqlq.Profile, profileNames []string, sqlText string) (path, content string, err error)`.

- [ ] Step 1 : tests qui échouent

Ajouter à `catalog_cli_test.go` :

```go
func saveEnv(t *testing.T) string {
	t.Helper()
	q := t.TempDir()
	t.Setenv("DB_AI_TOOLKIT_QUERIES", q)
	t.Setenv("DB_AI_TOOLKIT_TSQL_SCRIPTS", "")
	t.Setenv("DB_AI_TOOLKIT_REGISTRY", filepath.Join(t.TempDir(), "v.json"))
	return q
}

func TestQueryNameRejectsTraversalAndCase(t *testing.T) {
	saveEnv(t)
	p := sqlq.Profile{Name: "dev"}
	for _, name := range []string{"../../x", "Orders", "a", "x/y", "orders_late"} {
		o := options{saveQuery: name, summary: "S.", queriesDir: t.TempDir()}
		if _, _, err := checkSave(o, p, []string{"dev"}, "SELECT 1;"); err == nil {
			t.Errorf("name %q accepted", name)
		}
	}
}

func TestSaveRefusesAWritingQuery(t *testing.T) {
	saveEnv(t)
	o := options{saveQuery: "purge", summary: "S.", queriesDir: t.TempDir(), allowWrite: true}
	if _, _, err := checkSave(o, sqlq.Profile{Name: "dev", Mode: sqlq.ModeReadWrite}, []string{"dev"}, "DELETE FROM dbo.T;"); err == nil {
		t.Error("a writing query was accepted for saving")
	}
}

func TestSaveRefusesAVisibleName(t *testing.T) {
	saveEnv(t)
	o := options{saveQuery: "tables-largest", summary: "S.", queriesDir: "../../internal/sqlq/testdata/catalog/bundled"}
	if _, _, err := checkSave(o, sqlq.Profile{Name: "dev"}, []string{"dev"}, "SELECT 1;"); err == nil {
		t.Error("saving over a bundled name was accepted")
	}
}

func TestSaveRequiresSummary(t *testing.T) {
	saveEnv(t)
	o := options{saveQuery: "orders-late", queriesDir: t.TempDir()}
	if _, _, err := checkSave(o, sqlq.Profile{Name: "dev"}, []string{"dev"}, "SELECT 1;"); err == nil || !strings.Contains(err.Error(), "-summary") {
		t.Errorf("err = %v", err)
	}
}
```

Ajouter à `integration_test.go` :

```go
func TestNothingIsSavedWhenTheRunFailed(t *testing.T) {
	p, _ := testProfile(t)
	q := t.TempDir()
	t.Setenv("DB_AI_TOOLKIT_QUERIES", q)
	t.Setenv("DB_AI_TOOLKIT_REGISTRY", filepath.Join(t.TempDir(), "v.json"))
	o := options{profileName: p.Name, profilesPath: os.Getenv("SQLQ_TEST_PROFILES"), query: "SELECT 1/0 AS x;",
		saveQuery: "will-fail", summary: "Fails.", maxRows: 5, timeoutSec: 30, queriesDir: t.TempDir()}
	if _, code := captureRun(t, o); code != exitSQL {
		t.Fatalf("code %d", code)
	}
	if matches, _ := filepath.Glob(filepath.Join(q, "profiles", "*", "will-fail.sql")); len(matches) != 0 {
		t.Errorf("a failed run left %v", matches)
	}
}

func TestSaveWritesVerifiedEntry(t *testing.T) {
	p, _ := testProfile(t)
	q := t.TempDir()
	t.Setenv("DB_AI_TOOLKIT_QUERIES", q)
	reg := filepath.Join(t.TempDir(), "v.json")
	t.Setenv("DB_AI_TOOLKIT_REGISTRY", reg)
	o := options{profileName: p.Name, profilesPath: os.Getenv("SQLQ_TEST_PROFILES"),
		query: "SELECT TOP (1) name FROM sys.objects WHERE name LIKE @pattern;", params: paramList{"pattern=sys%"},
		saveQuery: "objects-like", summary: "Objects matching a pattern.", maxRows: 5, timeoutSec: 30, queriesDir: t.TempDir()}
	if out, code := captureRun(t, o); code != exitOK {
		t.Fatalf("code %d: %s", code, out)
	}
	o2 := options{listQueries: true, profileName: p.Name, profilesPath: os.Getenv("SQLQ_TEST_PROFILES"), queriesDir: o.queriesDir}
	out, _ := captureRun(t, o2)
	var cat struct {
		Queries []struct {
			Name, Source, Rejected string
			Verified               *sqlq.Verified
		} `json:"queries"`
	}
	if err := json.Unmarshal([]byte(out), &cat); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, q := range cat.Queries {
		if q.Name == "objects-like" {
			found = true
			if q.Source != "personal" || q.Rejected != "" || q.Verified == nil {
				t.Errorf("saved entry: %+v", q)
			}
		}
	}
	if !found {
		t.Errorf("saved entry missing: %s", out)
	}
}
```

(ajouter `encoding/json` et `path/filepath` aux imports de `integration_test.go`.)

- [ ] Step 2 : vérifier l'échec

Run : `cd tools && go test ./cmd/sqlq -run '^(TestQueryNameRejectsTraversalAndCase|TestSaveRefusesAWritingQuery|TestSaveRefusesAVisibleName|TestSaveRequiresSummary)$' -count=1 -v`
Expected : échec de compilation (`undefined: checkSave`).

- [ ] Step 3 : implémenter

Drapeaux :

```go
	fs.StringVar(&o.saveQuery, "save-query", "", "after a successful -query or -file run, save it under that name for this profile")
	fs.StringVar(&o.summary, "summary", "", "one-line summary written into the header by -save-query (required with it)")
```

`checkSave` tourne avant toute connexion, juste après les contrôles du garde-fou :

```go
// checkSave refuses, before anything runs, every save that could not end in a
// valid catalogue entry, and returns where to write and what.
func checkSave(o options, profile sqlq.Profile, profileNames []string, sqlText string) (string, string, error) {
	if o.saved != "" {
		return "", "", errors.New("-save-query saves a -query or -file run, not a -saved one")
	}
	if !sqlq.ValidQueryName(o.saveQuery) {
		return "", "", fmt.Errorf("query name %q must match ^[a-z][a-z0-9-]{1,48}$", o.saveQuery)
	}
	if len(sqlq.FindWrites(sqlText)) > 0 {
		return "", "", errors.New("the catalogue holds reads only: this batch would write")
	}
	if o.summary == "" {
		return "", "", errors.New("-save-query needs -summary")
	}
	content, err := sqlq.SavedFileContent(o.summary, sqlText)
	if err != nil {
		return "", "", err
	}
	cfg := catalogConfig(o, profileNames)
	cfg.Profile = profile.Name
	for _, e := range sqlq.LoadCatalog(cfg).Entries {
		if e.Name == o.saveQuery {
			return "", "", fmt.Errorf("name %q is already used by %s:%s", o.saveQuery, e.Source, e.Path)
		}
	}
	dir, err := sqlq.ProfileDir(cfg.PersonalDir, profile.Name, profileNames)
	if err != nil {
		return "", "", err
	}
	return filepath.Join(dir, o.saveQuery+".sql"), content, nil
}
```

Dans `run`, quand `o.saveQuery != ""` : appeler `checkSave` avant `execute` (échec :
`fail(exitUsage, err)`). Après `execute`, seulement si `code == exitOK` :

```go
		if err := writeSavedQuery(o, profile, profileNames, savePath, saveContent); err != nil {
			result.Error = &sqlq.SQLError{Message: "query ran but was not saved: " + err.Error()}
			_ = emit(result)
			return exitUsage
		}
		_ = sqlq.RecordVerified(registryPath(), sqlq.ContentHash([]byte(saveContent)),
			sqlq.Verified{Date: time.Now().Format("2006-01-02"), Profile: profile.Name})
```

```go
// writeSavedQuery creates the file, never over an existing one, and rereads it
// through the catalogue; a file this run created and the catalogue rejects is removed.
func writeSavedQuery(o options, profile sqlq.Profile, profileNames []string, path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err // includes "file exists": nothing of ours to remove
	}
	_, werr := f.WriteString(content)
	if err := errors.Join(werr, f.Close()); err != nil {
		os.Remove(path)
		return err
	}
	cfg := catalogConfig(o, profileNames)
	cfg.Profile = profile.Name
	e, ok := sqlq.LoadCatalog(cfg).Find(o.saveQuery)
	if !ok || e.Source != sqlq.SourcePersonal {
		os.Remove(path)
		return fmt.Errorf("the saved file does not read back as a valid catalogue entry")
	}
	return nil
}
```

`O_EXCL` ferme la course entre la vérification du nom et l'écriture.

- [ ] Step 4 : vérifier

Le filtre du step 2 : 4 tests `PASS`. Puis l'intégration (setup de la tâche 2 dans la même
invocation) avec `'^(TestNothingIsSavedWhenTheRunFailed|TestSaveWritesVerifiedEntry)$'` :
2 tests `PASS`, pas `SKIP`.

- [ ] Step 5 : casser pour voir tomber

Remplacer `if code == exitOK` par `if true` autour de l'écriture :
`TestNothingIsSavedWhenTheRunFailed` tombe. Remettre.

- [ ] Step 6 : commit

```bash
git add tools/cmd/sqlq/main.go tools/cmd/sqlq/catalog_cli_test.go tools/cmd/sqlq/integration_test.go
git commit -m "feat(sqlq): -save-query keeps a successful run under a name" -m "Nothing enters the personal library without having run: the file is written only after a zero exit, never over an existing one, and is reread through the catalogue before the run reports success, so a save cannot produce an entry the catalogue would reject."
```

---

### Task 11 : documentation

Spec §14.

Files :
- Modify : `plugins/sqlserver-toolkit/skills/live-query/SKILL.md`
- Modify : `plugins/sqlserver-toolkit/README.md`
- Modify : `AGENTS.md` (section « Querying a live SQL Server instance »)

- [ ] Step 1 : constater l'échec

Run : `cd tools && go test ./cmd/sqlq -run '^TestEveryFlagIsDocumented$' -count=1 -v`
Expected : FAIL, une ligne par drapeau non documenté et par document : `-list-queries`,
`-saved`, `-save-query`, `-summary`, `-queries`, `-tsql-scripts`, dans `README.md` et
`SKILL.md`, soit 12 erreurs. Un autre nombre : s'arrêter et comprendre pourquoi.

- [ ] Step 2 : réécrire `SKILL.md`

Garder le style du fichier (il utilise le gras pour ses consignes, c'est un fichier
d'instructions). Changements, chacun repris du §14 de la spec :

1. Workflow, étape 3 « Route the question » devient : « Run
   `sqlq -list-queries -profile <name>`. Prefer a catalogue entry to writing SQL. »
2. La table « Decision tree » est remplacée par une table plus courte : ce que veulent dire
   `verified: null`, `rejected`, `dirty_reads`, `heavy`, `params` (avec type et défaut pour
   tsql-scripts), et la ligne « Why is this query slow » (`-plan`) qui reste. Les consignes
   propres à chaque requête livrée qui ne figurent pas déjà dans l'en-tête du fichier y sont
   déplacées (vérifier fichier par fichier : `missing-indexes.sql` dit déjà `-maxrows 70`,
   `blocked-processes-check.sql` dit déjà de lire `instance_state`). Ne déplacer que ce qui
   manque, et le dire dans le rapport.
3. « Before the first run of a catalogue query in a session, read its header » : pour
   `bundled`, `${CLAUDE_PLUGIN_ROOT}/skills/live-query/queries/<path>` ; pour `tsql-scripts`,
   `$DB_AI_TOOLKIT_TSQL_SCRIPTS/<path>` ; pour `personal`,
   `~/.config/db-ai-toolkit/queries/<path>`.
4. « Always pass `-maxrows` to a catalogue query » et la raison (l'auteur répond de la
   borne, `-maxrows` reste le filet).
5. `heavy: true` sur un profil prod : annoncer et attendre un oui.
6. `dirty_reads: true` : jamais pour une question de justesse des données.
7. `rejected` : ne pas lancer, ne pas réécrire le script en ad hoc, rapporter la raison.
8. Après une requête ad hoc réussie et utile, proposer en une ligne
   `-save-query <name> -summary "<one line>"` ; jamais sans accord.
9. « Calling sqlq » : ajouter
   `sqlq -list-queries -profile <name>`,
   `sqlq -profile <name> -saved tables-largest -maxrows 20`,
   `sqlq -profile <name> -saved sessions-from-host -param hostname=SRV-APP01`,
   `sqlq -profile <name> -query "<sql>" -save-query orders-late -summary "Orders past their promised date."`,
   `-queries <dir>` et `-tsql-scripts <dir>` (développement et clone local).
10. JSON : ajouter `more_results` et `saved` à l'exemple, et une phrase sur `messages`.
11. « Known limits » : retirer les deux lignes sur `messages` vide et le premier jeu seul.
    Ajouter : le registre est propre à la machine ; la surcharge ne couvre que les
    `DECLARE` d'une ligne de type simple.

- [ ] Step 3 : `README.md` du plugin

Dans « The `sqlq` query runner » : un paragraphe « Query catalogue » qui décrit les trois
sources et leurs emplacements (§7 de la spec), `$DB_AI_TOOLKIT_TSQL_SCRIPTS`, le registre,
puis les six drapeaux. Mentionner `$DB_AI_TOOLKIT_QUERIES` et `$DB_AI_TOOLKIT_REGISTRY`
comme points d'entrée de test et de développement. Ajouter les exemples du step 2, point 9.

- [ ] Step 4 : `AGENTS.md`

Dans « Querying a live SQL Server instance », après le bloc de commandes : quatre lignes
`sqlq -list-queries -profile <name>`, `-saved`, `-save-query … -summary`, et une phrase
« Prefer a catalogue entry to writing SQL; read its header before its first run; a
`rejected` entry is reported, never rewritten ad hoc. » Dans « Output discipline », ajouter
que `more_results` porte les jeux suivants.

- [ ] Step 5 : vérifier

Run : `cd tools && go test ./... -count=1 && go vet ./...`
Expected : tout vert, `TestEveryFlagIsDocumented` compris. Relire les trois fichiers :
aucun drapeau inventé (chaque drapeau cité existe dans `defineFlags`), aucun chemin faux.

- [ ] Step 6 : commit

```bash
git add plugins/sqlserver-toolkit/skills/live-query/SKILL.md plugins/sqlserver-toolkit/README.md AGENTS.md
git commit -m "docs(live-query): route questions through the query catalogue" -m "The decision table described seven queries in a second place, where it could drift from the files. The skill now asks the catalogue, which reads the files themselves, and says what verified, rejected, dirty_reads and heavy require of the agent."
```

---

### Task 12 : tsql-scripts, premier lot et validation

Spec §15. Deux dépôts : tsql-scripts (`/home/rudi/Sources/Repos/tsql-scripts`, branche
courante, commit local sans push) et db-ai-toolkit.

Files :
- Modify : `/home/rudi/Sources/Repos/tsql-scripts/CLAUDE.md` (section « SQL File Header Template »)
- Modify : les fichiers `.sql` du premier lot (une ligne marqueur chacun, rien d'autre)
- Create : `docs/validation/2026-10-04-query-catalog.md` (db-ai-toolkit)
- Modify : `plugins/sqlserver-toolkit/.claude-plugin/plugin.json` (`0.4.0` vers `0.5.0`)
- Create : `tools/internal/sqlq/real_clone_test.go`

- [ ] Step 1 : `TestRealCloneHasNoRejectedEntry`

```go
package sqlq

import (
	"os"
	"testing"
)

// Run by the user after adding markers: every marked script of the real clone
// must be usable. Skipped without a clone.
func TestRealCloneHasNoRejectedEntry(t *testing.T) {
	dir := os.Getenv("DB_AI_TOOLKIT_TSQL_SCRIPTS")
	if dir == "" {
		t.Skip("DB_AI_TOOLKIT_TSQL_SCRIPTS not set")
	}
	c := LoadCatalog(CatalogConfig{TsqlScriptsDir: dir})
	if len(c.Entries) == 0 {
		t.Fatal("no marked script found: the clone path is wrong or no marker was added")
	}
	for _, e := range c.Entries {
		if e.Rejected != "" {
			t.Errorf("%s (%s): %s", e.Path, e.Name, e.Rejected)
		}
	}
}
```

- [ ] Step 2 : documenter le marqueur dans tsql-scripts

Dans `CLAUDE.md` de tsql-scripts, sous le gabarit d'en-tête, un paragraphe « Catalogue
marker for sqlq » : la ligne `-- sqlq: name=<name> [params=a,b] [heavy]` placée juste sous
la ligne de description ; la règle de nom ; la forme exigée d'un `DECLARE` surchargé (seul
sur sa ligne, `;` final, types acceptés de la table du §11 de la spec) ; aucune affectation
ultérieure de la variable ; pas de `GO`, `USE`, `EXEC`, `INTO`, table temporaire ; ni
client, ni hôte, ni base dans le résumé ou le nom ; et la commande de contrôle
`DB_AI_TOOLKIT_TSQL_SCRIPTS=<clone> go test ./internal/sqlq -run '^TestRealCloneHasNoRejectedEntry$'`
lancée depuis `db-ai-toolkit/tools`.

- [ ] Step 3 : choisir et marquer le premier lot

Partir de la table « Script for a need » de l'`AGENTS.md` de tsql-scripts et de la liste
des 45 déclarations surchargeables. Retenir 8 à 12 scripts qui passent le garde-fou, en
lecture de DMV seulement, dont au moins deux paramétrés (`sessions-from-host.sql` et un
script à paramètre `int` ou `bit`) et au moins un à plusieurs jeux de résultats
(`server-information/cores-and-numa.sql` si la mesure le confirme). Ne marquer aucun script
de la liste « Scripts that change the server » ni « Scripts that install objects ». Ajouter
la ligne marqueur et rien d'autre. Nommer chaque entrée d'après le besoin, pas d'après le
fichier.

Puis, dans une seule invocation du shell, depuis `db-ai-toolkit/tools` :

```bash
export DB_AI_TOOLKIT_TSQL_SCRIPTS=/home/rudi/Sources/Repos/tsql-scripts && \
go test ./internal/sqlq -run '^TestRealCloneHasNoRejectedEntry$' -count=1 -v && \
go build -o /tmp/sqlq-catalog ./cmd/sqlq && \
/tmp/sqlq-catalog -list-queries -queries ../plugins/sqlserver-toolkit/skills/live-query/queries
```

Expected : le test `PASS` (1 test) ; le catalogue liste les 7 requêtes livrées et le lot,
sans `rejected`. Un marqueur refusé : corriger le marqueur s'il est mal écrit, sinon retirer
le script du lot et noter pourquoi dans la validation. Ne jamais modifier le SQL d'un script
pour le faire entrer.

- [ ] Step 4 : valider sur l'instance de test

Avec le setup de la tâche 2 dans la même invocation, lancer chaque entrée du lot et chaque
requête livrée par `-saved` sur le profil `catalog-test`, avec `-maxrows 20`, puis chaque
entrée paramétrée une seconde fois avec une valeur passée. Pour chaque run, relever : code
de sortie, nombre de jeux (`1 + len(more_results)`), `rowcount` du premier, `messages`,
`verified` avant et après. Écrire `docs/validation/2026-10-04-query-catalog.md` sur le
modèle de `docs/validation/2026-10-01-missing-indexes.md` : instance (`SELECT @@VERSION`),
login, une table par run, et chaque écart avec son explication. Un script qui échoue sur
l'instance sort du lot : retirer son marqueur et le consigner.

Enfin, `sqlq -list-queries` une seconde fois : chaque entrée lancée avec succès porte
`verified`.

- [ ] Step 5 : version et nettoyage

Passer `plugins/sqlserver-toolkit/.claude-plugin/plugin.json` en `0.5.0`. Arrêter et
supprimer le conteneur `dbai-catalog-test` (`podman rm -f dbai-catalog-test`) et le fichier
de profils de test. Supprimer `/tmp/sqlq-catalog`. Vérifier `git status --porcelain -uall`
dans les deux dépôts : seuls les fichiers de cette tâche sont modifiés.

- [ ] Step 6 : commits

Dans tsql-scripts :

```bash
git -C /home/rudi/Sources/Repos/tsql-scripts add CLAUDE.md <les fichiers du lot>
git -C /home/rudi/Sources/Repos/tsql-scripts commit -m "docs: sqlq catalogue marker on a first set of diagnostics" -m "sqlq can now call these scripts by name from an agent session. The marker is a comment, so SSMS runs every file exactly as before; CLAUDE.md says what a script must look like to be accepted."
```

Dans db-ai-toolkit :

```bash
git add tools/internal/sqlq/real_clone_test.go docs/validation/2026-10-04-query-catalog.md plugins/sqlserver-toolkit/.claude-plugin/plugin.json
git commit -m "test(sqlq): validate the catalogue against tsql-scripts and an instance" -m "Every marked script and every bundled query ran through -saved on a SQL Server 2025 container, parameterised ones twice, and the record keeps what each returned. The check on the real clone is what the user runs after adding markers."
```
