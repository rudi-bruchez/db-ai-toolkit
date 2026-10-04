# Tri du panel sur le plan du catalogue (commit 3104776)

Cinq lecteurs, mêmes sièges que pour la spec. Chacun a collé le code des tâches tel quel dans
une copie de `tools/` et lancé les filtres du plan. Les tâches 3, 5, 6, 7 et 8 compilent et
donnent les comptes annoncés ; les défauts sont ailleurs.

## Retenu, corrigé dans le plan (et dans la spec v2.1 quand la règle changeait)

| Trouvaille | Lecteurs | Correction |
|---|---|---|
| `Rewrite` suit l'ordre de `params=`, pas l'ordre des positions : `DECLARE @b @sqlq_bnt = 2`, littéral non terminé | codex-n, agy-n, codex-d, claude | tri par position décroissante, test à deux paramètres en ordre inverse (tâche 5) |
| Paramètre listé deux fois dans le marqueur : `@sqlq_hostnameq_hostname` | claude | refus dans `parseMarker` (tâche 3) |
| Longueur comptée en runes, `nvarchar(n)` compte des unités UTF-16 | codex-n, codex-d, claude | `utf16.Encode` (tâche 5, spec §11) |
| `smalldatetime` arrondit les secondes à la minute, `saved.params` mentirait | codex-d, claude | secondes refusées pour ce type (tâche 5, spec §11) |
| `(1 + 2))` accepté, `Lex` ne descend jamais sous zéro | codex-d | équilibre compté sur l'initialiseur (tâche 5, spec §11) |
| `@p OUTPUT` non compté comme affectation | agy-d | `OUT` et `OUTPUT` comptés (tâche 5, spec §11) ; le garde-fou refusait déjà `EXEC` et `INTO` |
| Défauts publiés tels quels : chemin `C:\temp\…`, extraits de SQL | claude | le catalogue ne publie plus le défaut (tâche 7, spec §12) |
| Message du registre portant le chemin absolu | claude | message sans texte d'erreur système (tâche 6, spec §12) |
| `TestRealCloneHasNoRejectedEntry` ne charge pas le canon et ne voit pas une collision | claude | charge aussi le répertoire livré (tâche 12) |
| La validation et les tests d'intégration écrivent dans le vrai registre ou lisent le vrai clone | claude | `testProfile` isole les trois variables, la validation exporte un registre temporaire (tâches 2, 12) |
| Canon livré introuvable : catalogue muet | claude | message `bundled queries not found` (tâche 7, spec §7) |
| Raison d'un rejet écrasée par « defined more than once » | claude | la première raison est gardée (tâche 7) |
| `missing_indexes_query_test.go` appelle `refusals`, supprimé par la tâche 1 | claude | ajouté aux fichiers de la tâche 1 |
| Étape « casser » de la tâche 4 qui ne casse rien, y compris son cas de repli | claude | cas `LEFT(@src, @len)` qui tombe (tâche 4) |
| `.git` sous testdata non indexé par Git, exclusion non testée | codex-d, claude, agy-d | `TestDotGitIsSkipped` en répertoire temporaire (tâche 7) |
| Tests de la spec absents : `TestBoundQueryRefusedOnAnotherProfile`, `TestVerifiedHashesBytesThatRan`, l'en-tête qui ment, la suppression d'un fichier sauvé illisible | agy-d, codex-d, claude | ajoutés (tâches 7, 9, 10) |
| Étapes décrites au lieu d'être montrées : `run`, `guard`, `readQuery`, `SavedRun`, imports, `t.Setenv` | codex-n, agy-n, codex-d, claude | code complet (tâches 1, 9, 10) |
| `RecordVerified` en erreur ignoré après `-save-query` | codex-d | `recordRun` l'ajoute à `messages` (tâches 9, 10) |
| Setup d'instance par `sqlcmd`, contraire à `AGENTS.md`, et profil sans `trustServerCertificate` | codex-n, codex-d, claude | setup fourni par le contrôleur, tests par `execute` seulement (tâche 2) |
| Validation de la tâche 12 sans valeurs pour les requêtes livrées à paramètre | codex-d | procédure explicite (tâche 12) |
| `-queries\b` trouvé dans `-list-queries` par le test de documentation | claude | regex corrigée (tâche 11) |
| Blocage possible : file de 15 messages pendant `NextResultSet` | agy-d, claude (écarté par lui faute de preuve) | `TestManyMessagesDoNotHang`, 40 `PRINT` entre deux jeux (tâche 2) |

## Écarté

- « `profile` indéfini dans `run` » (agy-d) : la variable existe dans `run` aujourd'hui.
- Comparaison `Parameters:` sensible à l'ordre, collation sensible à la casse (claude, mis de
  côté par lui) : faux refus visibles, gardés en limite.

## Constaté par le contrôleur

- La machine n'a plus que 1,2 Go de mémoire disponible : un second conteneur SQL Server
  s'arrête en erreur 701. Les tests d'intégration et la validation utilisent le conteneur
  `sql2025` déjà en place, en lecture seule, avec un délai de 120 secondes (19 secondes
  mesurées pour un `SELECT @@VERSION`).
