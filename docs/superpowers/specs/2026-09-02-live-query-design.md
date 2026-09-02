# Design — `sqlq` + skill `live-query` (interrogation d'une instance SQL Server connectée)

Date : 2026-09-02
Statut : approuvé (brainstorming), implémentation en cours.

## 1. Objectif

Permettre à Claude Code de répondre à des questions posées sur une **instance
SQL Server réelle**, en connexion directe, sans que l'agent réinvente à chaque
fois la plomberie de connexion ni les requêtes catalogue.

Questions cibles, telles que formulées par l'utilisateur :

| Question | Nature | Ce qu'il faut pour y répondre |
|---|---|---|
| Quelles sont les tables les plus grandes ? | factuelle | une requête canonique correcte |
| Quelles procédures référencent telle table ? | factuelle, à pièges | requête + repli sur le SQL dynamique |
| Pourquoi cette vue ne retourne pas la donnée voulue ? | diagnostic ouvert | source + checklist + raisonnement |
| Cette procédure est-elle bien écrite ? | revue experte | source + checklist |
| Y a-t-il des triggers mal construits ? | revue experte | inventaire + checklist |

Deux des cinq questions sont mécanisables ; **trois relèvent du raisonnement**.
Cette asymétrie commande toute l'architecture : l'outil déterministe transporte
et cadre, le skill porte le savoir, le modèle juge.

## 2. Enquête préalable (constats vérifiés le 2026-09-02)

### 2.1 Outillage présent sur le poste de développement

| Élément | État constaté |
|---|---|
| `go-sqlcmd` | v1.10.0, `C:\Program Files\SqlCmd\sqlcmd.exe` |
| `dbatools` (PowerShell) | 2.7.27 |
| Extension VS Code `ms-mssql.mssql` | 1.45.1 |
| Toolchain Go | 1.26.5 windows/amd64 |
| Plugin `erikdarling/sqlserver-query-plans` | installé (analyse de plans d'exécution) |

### 2.2 L'extension VS Code `mssql` ne sert pas Claude Code

Piste envisagée : utiliser Claude Code dans VS Code et s'appuyer sur l'extension
`ms-mssql`. Inspection du `package.json` et du bundle `dist/extension.js` de la
version 1.45.1 :

- Elle expose 13 outils (`mssql_run_query`, `mssql_list_tables`, `mssql_connect`,
  …) via `contributes.languageModelTools` — l'API *Language Model Tools de
  VS Code*, réservée à Copilot agent mode. **Claude Code ne consomme pas ce
  registre.**
- Elle déclare aussi `contributes.mcpServerDefinitionProviders` :
  `{ "id": "mssql-sqltools-mcp", "when": "config.mssql.copilot.enableSqlToolsMcp" }`.
  Le bundle construit une `McpStdioServerDefinition` pointant sur
  `sqltools-mcp/<version>/win-x64/SQLtoolsMCPserver.exe`, avec les variables
  `SQLtools__ConnectionProvider=vscode`, `SQLtools__ExecutionMode=READ_WRITE`,
  `SQLtools__VsCodeBridgeEndpoint=<endpoint>`.
- **Le dossier `sqltools-mcp/` est absent de l'installation 1.45.1** (binaire
  téléchargé à la demande ou réservé à certains canaux). Et
  `ConnectionProvider=vscode` implique un pont avec l'extension hôte.

Conclusion : piste écartée. L'extension reste une bonne UI humaine pour exécuter
des requêtes à la main ; elle n'apporte rien à l'agent.

### 2.3 Le « SQL MCP Server » de Microsoft est hors sujet

D'après Microsoft Learn, le *SQL MCP Server* officiel est celui de **Data API
builder** (DAB ≥ 1.7, transports stdio et HTTP). Il expose des **entités
applicatives** configurées en JSON avec RBAC — c'est un serveur CRUD pour
applications agentiques, pas un outil d'introspection et de diagnostic DBA.
Il ne répond à aucune des cinq questions cibles.

### 2.4 `go-mssqldb` couvre les trois modes d'authentification requis

Source : README de `microsoft/go-mssqldb` (le driver sur lequel `go-sqlcmd`
est lui-même bâti).

| Mode requis | Couverture |
|---|---|
| Windows intégré | SSPI automatique quand `user id` est vide ; `authenticator=winsspi` par défaut sur Windows |
| Login SQL | natif (`user id` / `password`) |
| Entra ID | package `github.com/microsoft/go-mssqldb/azuread`, `fedauth=ActiveDirectoryDefault`, `ActiveDirectoryInteractive`, `ActiveDirectoryAzCli`, `ActiveDirectoryServicePrincipal`, … |
| Intégré hors Windows | Kerberos via `github.com/microsoft/go-mssqldb/integratedauth/krb5` (`authenticator=krb5`, `krb5-realm`, `krb5-keytabfile`, …) |

## 3. Options examinées

| Option | Verdict | Motif |
|---|---|---|
| **Statu quo** — l'agent écrit ses requêtes et appelle `sqlcmd` à la main | insuffisant | Cérémonie de connexion rejouée à chaque tour ; sortie en colonnes alignées coûteuse en tokens ; aucun garde-fou ; pièges catalogue non documentés |
| **Skill seul**, sans binaire | insuffisant | Les garde-fous ne seraient que suggérés, pas appliqués. Inacceptable en production lecture seule |
| **Serveur MCP dédié** | écarté | Les définitions d'outils MCP sont chargées à **chaque** session, en coût fixe permanent. Un skill se charge à la demande ; un binaire ne coûte rien tant qu'il n'est pas appelé |
| **Binaire Go appelant `go-sqlcmd`** | écarté | Cumule deux dépendances au lieu d'en supprimer une, et conserve la couche texte |
| **Binaire Go embarquant `go-mssqldb`** | **retenu** | Une seule dépendance, supprimée à la compilation |
| **Dépôt séparé `mssql-live`** | écarté | `db-ai-toolkit` fournit déjà marketplace, module Go, script de build, licence et le pattern `bin/` sur le PATH |

### Ce que l'exécution en process apporte, concrètement

1. **Erreurs SQL structurées.** `mssql.Error` porte numéro, sévérité, état,
   numéro de ligne et nom de la procédure. `sqlcmd` ne rend qu'un code de sortie
   et du texte. Décisif pour « pourquoi cette procédure échoue ».
2. **Plans d'exécution intacts.** `SET STATISTICS XML ON` renvoie un XML très
   large que `sqlcmd` découpe selon les largeurs de colonnes. En process, la
   chaîne est récupérée telle quelle, prête pour le plugin
   `sqlserver-query-plans`.
3. **Fidélité des types.** `datetime2`, `decimal`, `varbinary`, `NULL` vs chaîne
   vide sont sérialisés depuis les types réels, sans passer par du texte
   reformaté. Supprime le besoin de suffixer chaque requête de `FOR JSON PATH`.
4. **Messages informationnels.** `PRINT` et `RAISERROR` de sévérité ≤ 10 sont
   *capturables* — via la boucle `sqlexp.ReturnMessage`. **Non implémenté en
   v1** : cette API restructure toute la boucle d'exécution et n'était pas
   testable sans instance. Le champ `messages` existe et reste vide. Voir la
   section 11.

## 4. Architecture retenue

Intégration dans `db-ai-toolkit`, selon les conventions du dépôt :

```
tools/cmd/sqlq/                                  # binaire, module Go existant
tools/internal/sqlq/                             # logique réutilisable et testable
plugins/sqlserver-toolkit/skills/live-query/     # SKILL.md + queries/ + references/
scripts/build-tools.ps1                          # + sqlq
scripts/build-tools.sh                           # nouveau : équivalent POSIX
```

Le binaire compilé atterrit dans `plugins/sqlserver-toolkit/bin/`, que Claude
Code ajoute au PATH de l'outil Bash quand le plugin est actif. Conformément aux
conventions, **le skill doit se dégrader proprement si le binaire est absent**
(il explique alors comment le construire).

## 5. Profils de connexion

Fichier hors dépôt, résolu dans l'ordre : `-profiles <chemin>`, puis
`$MSSQL_PROFILES`, puis `~/.config/db-ai-toolkit/mssql-profiles.json`.

```json
{
  "prod-erp":    { "server": "SRV01", "database": "ERP",
                   "auth": "integrated", "mode": "readonly" },
  "prod-azure":  { "server": "x.database.windows.net", "database": "D",
                   "auth": "entra", "fedauth": "ActiveDirectoryDefault",
                   "mode": "readonly" },
  "prod-legacy": { "server": "SRV02", "database": "L", "auth": "sql",
                   "user": "svc_claude", "passwordEnv": "MSSQL_LEGACY_PWD",
                   "mode": "readonly" }
}
```

**Aucun mot de passe dans le fichier** : seul le *nom* de la variable
d'environnement qui le porte est stocké (`passwordEnv`).

### Réserve : « integrated » n'est pas portable

Sur Windows, `auth: "integrated"` signifie SSPI et fonctionne sans configuration.
Sur Linux et macOS, il signifie Kerberos et exige un realm, un `krb5.conf` et un
keytab ou un cache de credentials. Le profil doit l'expliciter (`krb5Realm`,
`krb5Keytab`, …) plutôt que laisser croire à une équivalence.

## 6. Contrat CLI

```
sqlq -profile <nom> (-query "<SQL>" | -file <chemin.sql>)
     [-param nom=valeur]...   # paramètres SQL typés (jamais de substitution textuelle)
     [-database <nom>]        # surcharge la base du profil
     [-maxrows 50] [-timeout 30] [-plan] [-profiles <chemin>]
sqlq -list-profiles
```

Sortie : **un objet JSON sur stdout**, toujours la même forme.

```json
{
  "profile": "prod-erp", "server": "SRV01", "database": "ERP",
  "elapsed_ms": 123,
  "columns": [{"name": "schema_name", "type": "NVARCHAR"}],
  "rows": [{"schema_name": "dbo"}],
  "rowcount": 1, "truncated": false,
  "messages": [],
  "plan": null,
  "error": null
}
```

En cas d'échec SQL, `error` porte `{number, severity, state, line, procedure,
message}` et le code de sortie est non nul. L'erreur reste **du JSON sur
stdout** : le skill n'a jamais deux formats à savoir lire.

`-param` passe des valeurs comme paramètres SQL typés. Il n'existe
**aucune substitution textuelle** dans les requêtes : un nom d'objet se filtre
par un prédicat sur les vues catalogue, il ne se concatène pas.

## 7. Garde-fous

Appliqués dans le binaire, en mode `readonly` :

1. **Préambule injecté** : `SET NOCOUNT ON; SET LOCK_TIMEOUT 5000;
   SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;` — l'agent ne peut ni
   bloquer ni rester bloqué sur une instance de production.
2. **Refus du DML/DDL** : après retrait des commentaires et des littéraux
   chaîne, tout lot dont une instruction commence par un mot-clé écrivant est
   rejeté sans être envoyé au serveur.
3. **Plafond de lignes** : au-delà de `-maxrows`, la sortie est tronquée,
   `truncated` passe à `true` et `rowcount` reste le compte réel.
4. **Délais** : timeout de requête et timeout de connexion bornés.

> **Le garde-fou qui compte reste le login SQL** : `db_datareader` +
> `VIEW DEFINITION` + `VIEW SERVER STATE`. Le filtre du binaire est de la
> **prévention d'accident, pas de la sécurité** — un filtre lexical se contourne.
> Le dire explicitement dans le README et le SKILL.md.

## 8. Contenu du skill `live-query`

`queries/` — requêtes canoniques, écrites une fois correctement :

| Fichier | Le piège qu'il évite |
|---|---|
| `tables-largest.sql` | `index_id IN (0,1)` pour les lignes ; espace réservé sur toutes les partitions ; LOB et row-overflow comptés à part |
| `object-references.sql` | `sys.sql_expression_dependencies` **plus repli obligatoire** sur `sys.sql_modules … LIKE` : les dépendances déclarées ratent le SQL dynamique |
| `view-diagnose.sql` | définition, schemabinding, politiques RLS, synonymes sous-jacents |
| `triggers-inventory.sql` | `is_disabled`, `is_instead_of`, source complète |
| `proc-source.sql` | `OBJECT_DEFINITION` et signature des paramètres |

`references/review-checklists.md` — les trois revues :

- **Procédure** : `SET NOCOUNT ON`, `TRY/CATCH` + `THROW`, `XACT_ABORT`,
  parameter sniffing, curseurs RBAR, `sp_executesql` contre concaténation
  (injection), préfixe de schéma manquant, table variable contre `#temp`,
  `NOLOCK` abusif.
- **Vue qui ne rend pas la donnée** : `INNER` au lieu de `LEFT`, prédicat sur
  `NULL`, RLS, permissions colonne, conversion implicite et collation, `TOP`
  sans `ORDER BY`, vue sur synonyme repointé, schemabinding périmé après
  `ALTER TABLE`.
- **Triggers** : l'hypothèse mono-ligne (défaut n° 1 — `inserted` et `deleted`
  sont des *tables*), `ROLLBACK` dans un trigger, imbrication et récursivité,
  `SET NOCOUNT` absent, traitement lourd synchrone.

Arbre de décision du skill : question factuelle → requête canonique ; question
de revue → source puis checklist ; question de performance →
`SET STATISTICS XML ON` puis passage de relais au plugin
`sqlserver-query-plans`.

## 9. Distribution

Option retenue : **compilation locale** (`scripts/build-tools.*`), conformément
au pattern déjà en place pour `errorlog-parse`. Pas de release binaire pour
l'instant.

Conséquence assumée : la toolchain Go est un prérequis. Si le skill devait un
jour être diffusé à des DBA sans Go, il faudra ajouter des binaires de release
GitHub pour six cibles — décision reportée, pas écartée.

`tools/go.mod` passe de `go 1.22` à `go 1.27`.

## 10. Tests

Testable **sans instance SQL Server** :

- résolution des profils (trois modes d'auth, variable d'environnement absente,
  profil inconnu, fichier absent) ;
- construction de la chaîne de connexion pour chaque mode ;
- détection DML : retrait des commentaires `--` et `/* */` et des littéraux
  chaîne avant analyse, afin que `SELECT 'DROP TABLE x'` passe et que
  `/* ok */ DELETE FROM t` soit rejeté ;
- troncature et cohérence de `rowcount` / `truncated` ;
- forme de la sortie JSON, succès comme erreur.

Test de bout en bout contre une instance réelle : **à faire par l'utilisateur**,
aucune instance n'ayant été désignée à ce stade.

## 11. Limites connues de la v1

| Limite | Conséquence | Suite |
|---|---|---|
| `messages` toujours vide | La sortie `PRINT` / `RAISERROR` sévérité ≤ 10 est perdue | Implémenter la boucle `sqlexp.ReturnMessage` |
| Un seul jeu de résultats rendu | Les jeux suivants sont vidés, sauf un showplan qui alimente `plan` | Suffisant pour les cinq questions cibles |
| Binaire de ~15 Mo | Le support Entra ID tire `azidentity` et MSAL | Tags de build si le poids devient gênant |
| `EXEC` et `DBCC` refusés en lecture seule | `sp_helptext` et `DBCC SHOW_STATISTICS` inaccessibles | `OBJECT_DEFINITION()` remplace le premier |
| Le filtre du garde-fou est lexical | Contournable par construction | C'est le login qui protège, pas le filtre |

## 12. État de validation

Vérifié sur le poste, sans instance SQL Server :

- `go vet` et `go test ./...` : 124 tests au vert sur 4 paquets ;
- les cinq requêtes canoniques passent le garde-fou (test `TestBundledQueriesPassTheReadOnlyGuard`,
  qui relit les fichiers `.sql` livrés) ;
- `scripts/build-tools.ps1` et `scripts/build-tools.sh` produisent les deux
  binaires dans `plugins/sqlserver-toolkit/bin/` ;
- codes de sortie du binaire conformes au contrat : `0` succès,
  `1` configuration, `3` refus du garde-fou (`DELETE`, `EXEC`),
  `4` échec de connexion.

**Non vérifié** : tout ce qui exige une instance réelle — exécution de requête,
capture de plan, conversion des types, remontée d'erreur SQL structurée, et les
trois modes d'authentification. À faire par l'utilisateur.
