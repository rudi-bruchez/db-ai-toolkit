# Registre : sqlq, reste de la spec des messages serveur

Les décisions prises seul pendant la nuit du 25 octobre 2026, avec leur raison et ce
qu'elles coûtent si elles sont fausses. Le plan est
`2026-10-25-sqlq-server-messages-remainder.md`.

Cadre fixé par Rudi avant la nuit : toute la spec, tests d'intégration lancés par moi
sur `sql2022` seulement, fusion sur `main` en local si tout passe, sans push.

## Décisions de conception

| # | Décision | Raison | Coût si fausse |
|---|---|---|---|
| D1 | `messages` reste `[]string` | `67ea7fb` l'a livré et `SKILL.md` le documente depuis le 4 octobre 2026 ; la fenêtre de la spec est fermée | un changement de contrat plus tard, si quelqu'un veut le numéro et la sévérité des notices |
| D2 | Les noms `more_results` et `incomplete` restent ; `incomplete` couvre aussi le timeout | noms documentés et testés | aucun : la spec n'a pas de consommateur |
| D3 | Pas d'appariement plan/jeu : `plan` = premier, `more_plans` = suivants | mesuré : `SELECT 1 AS a; UPDATE …` et `SELECT … FROM t` produisent le même flux (un jeu, un plan) | un lecteur doit lire `StatementText` pour savoir à quoi se rapporte un plan |
| D4 | `-maxmessages 200`, `-maxresultsets 20`, plans bornés par la même valeur | défauts de la spec ; un plan par itération de `WHILE` est le même canal non borné | un plafond de plans trop bas pour un script à beaucoup d'instructions ; `0` le lève |
| D5 | `-statistics` sans analyse | mesuré : le texte suit `SET LANGUAGE` | aucun |
| D6 | Timeout reconnu à la réception de `MsgNextResultSet` | contrainte 2 de la spec | un faux succès si le pilote changeait encore de comportement ; `TestTimeoutIsAnErrorNotPartialSuccess` le garde |

## Journal

- 25 octobre 2026, 00 h 50 : worktree `../db-ai-toolkit-sqlq-messages`, branche
  `feat/sqlq-server-messages` depuis `main` (`81b2d22`). Le dépôt principal avait des
  modifications d'une autre session (`plugins/sqlserver-toolkit/README.md`,
  `skills/page-salvage/`) : je n'y touche pas.
- État de départ : les sept tests d'intégration passent sur le labo ; 40 lignes
  `--- PASS` pour `./cmd/sqlq/`.
- Mesures de D3 et D5 faites avec `sqlq` et une sonde Go hors dépôt (scratchpad de la
  session). La sonde a exécuté `UPDATE Stage.Stage SET Duree = Duree WHERE 1 = 0` sur
  `PachaDataFormation` de `sql2022` : aucune ligne touchée.
