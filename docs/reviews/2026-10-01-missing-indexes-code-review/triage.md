# Revue de code externe de missing-indexes : tri

Branche `feat/missing-indexes`, commit relu `08a875d`, base `main`. Relecteurs : agy et codex,
même consigne (`prompt.md`), chacun dans son worktree détaché, sans accès à aucune base.
opencode n'est pas installé sur cette machine.

Sous Windows, codex tourne en `-s read-only` : il a lu le code et testé des mutations en
mémoire, mais n'a pu lancer ni `go test` ni `go vet` (le bac à sable refuse l'écriture du cache
Go). agy n'a rien exécuté non plus ; ses conclusions viennent de la lecture.

Verdicts : agy « aucun défaut sérieux », codex « trois Important dans le protocole ». Les
verdicts ne comptent pas ; les constats sont pris un par un.

## Retenus

| # | constat | relecteur | vérifié | traitement |
|---|---|---|---|---|
| 1 | Le protocole fait élargir par `INCLUDE` une clé primaire **clustered**. `INCLUDE` n'existe que pour un index nonclustered, et la feuille d'un index clustered porte déjà toutes les colonnes. | codex | oui, documentation `CREATE INDEX` | Le §3 ne compare plus qu'aux index `NONCLUSTERED` ; nouvelle règle : un index clustered ne s'élargit jamais, une suggestion que sa clé ne sert pas appelle un index nonclustered. |
| 2 | Les règles de préfixe s'appliquent aussi aux index **hash** d'une table en mémoire, alors qu'un hash ne sert qu'une égalité sur toutes ses colonnes de clé. | codex | oui, guide de conception des index hash | Le point 4 du §3 l'écrit et exclut les index hash des règles de préfixe. |
| 3 | « L'ensemble d'égalité est inclus dans les colonnes de tête » laisse passer `[a], [x], [b]` pour `{a, b}` : une colonne étrangère au milieu casse la recherche. Même flou dans l'autre sens (`[a], [x]` face à `{a, b}`). | codex | oui, par raisonnement sur la règle écrite | Règles réécrites avec N = taille de l'ensemble : les N premières colonnes de clé sont exactement l'ensemble ; ou toutes les colonnes de clé de l'index existant sont dans l'ensemble. L'inégalité doit être la colonne N + 1. |
| 4 | `DATEDIFF(minute, ...)` compte des passages de minute, pas des minutes écoulées : 23 h 59 min 01 s donnent `1.0`, et l'arrêt « moins d'un jour » est franchi trop tôt. | codex | oui, documentation `DATEDIFF` | `DATEDIFF(second, ...) / 86400.0`. L'écart restant est d'une seconde au plus. Le test fige la nouvelle formule (vu RED puis GREEN). |
| 5 | Le test de contrat cherche des fragments et laisse passer des retouches structurelles : `LEFT JOIN` devenu `JOIN` sur l'usage, `ORDER BY` final modifié. | agy, codex | oui, par mutation | Deux motifs ajoutés ; chaque mutation fait échouer le test. |

## Rejetés ou laissés

| constat | relecteur | raison |
|---|---|---|
| Le test ne voit pas un `OR 1 = 1` ajouté au filtre de base ou au plafond, ni un `GROUP BY` élargi d'une colonne. | agy, codex | Vrai, et assumé : le test fige un contrat textuel contre une retouche distraite, pas contre une retouche hostile. La justesse se prouve sur instance (tâche 4 du plan : M5 pour la base, M3b pour les plafonds, M7b pour les partitions). |
| Le CTE `suggestion` est lu plusieurs fois, sans cliché : les totaux du `context` peuvent différer des lignes. | agy | Déjà connu et écrit dans l'en-tête (« This is not a snapshot ») et dans le protocole (§2 : relancer si un total est inférieur aux lignes montrées). |
| Le login de base décrit dans AGENTS.md n'a pas `VIEW DATABASE STATE`. | codex | Pas un défaut de la requête : l'en-tête liste les permissions nécessaires, et les cas M8 et M9 de la validation consignent ce qui arrive sans elles. |
| L'en-tête annonce « Tested on SQL Server 2019 » sans document de validation. | codex | Exact à ce stade : la validation est la tâche 4, qui crée ce document. À revoir si la tâche 4 ne passe pas sur 2019. |

## Vérifications après correction

`cd tools && go test ./... && go vet ./...` : PASS, `gofmt -l` vide. Mutations rejouées sur la
requête corrigée : `LEFT JOIN` → `JOIN` et `ORDER BY table_rank, seq` font échouer le test, la
requête restaurée le fait passer.
