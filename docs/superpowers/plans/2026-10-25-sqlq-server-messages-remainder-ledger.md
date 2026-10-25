# Registre : sqlq, reste de la spec des messages serveur

Les décisions prises seul pendant la nuit du 25 octobre 2026, avec leur raison et ce
qu'elles coûtent si elles sont fausses. Le plan est
`2026-10-25-sqlq-server-messages-remainder.md`.

Cadre fixé par Rudi avant la nuit : toute la spec, tests d'intégration lancés par moi
sur `sql2022` seulement, fusion sur `main` en local si tout passe, sans push.

## Décisions de conception

| # | Décision | Raison | Coût si fausse |
|---|---|---|---|
| D0 | Un relais (goroutine et file sans borne) vide la file de 15 du pilote | mesuré : blocage jusqu'au timeout au-delà de 15 notices pendant un jeu ou, sous `sp_executesql`, entre deux jeux ; le prototype le lève et passe sous `-race` | mémoire : la file grossit du nombre de notices d'une instruction ; une course à l'échéance, préexistante, reste ouverte |
| D1 | `messages` reste `[]string` | `67ea7fb` l'a livré et `SKILL.md` le documente depuis le 4 octobre 2026 ; la fenêtre de la spec est fermée | un changement de contrat plus tard, si quelqu'un veut le numéro et la sévérité des notices |
| D2 | Les noms `more_results` et `incomplete` restent ; `incomplete` couvre aussi le timeout | noms documentés et testés | aucun : la spec n'a pas de consommateur |
| D3 | Pas d'appariement plan/jeu : `plan` = premier, `more_plans` = suivants | mesuré : sous `sp_executesql`, `SELECT 1 AS a; UPDATE …` et `SELECT … FROM t` produisent le même flux ; le XML ne permet pas non plus l'appariement | un lecteur doit lire `StatementText` ; `sqlplan` dessine désormais le premier plan |
| D4 | `-maxmessages 200`, `-maxresultsets 20`, plans bornés par la même valeur ; `errSlots` gardé | défauts de la spec ; l'appariement par position reste juste sous un plafond monotone | un plafond de plans trop bas pour un script à beaucoup d'instructions ; `0` le lève |
| D5 | `-statistics` sans analyse | mesuré : le texte suit `SET LANGUAGE` | aucun |
| D6 | Timeout reconnu à la réception ; jeu coupé gardé s'il a des lignes ; aucun marquage après la boucle ; la première `MsgError` gagne sur le timeout | contrainte 2, contrainte 3 et § 4.2 de la spec ; le marquage après la boucle mesuré faux | un faux succès si le pilote changeait encore de comportement ; `TestTimeoutIsAnErrorNotPartialSuccess` le garde |
| D7 | Sortie 4 = la requête n'a pas démarré ; tout échec ensuite sort en 2, transport compris | distinguer transport et `Scan` après coup n'est pas testable sur le labo ; contredit la spec | un appelant qui relancerait sur 4 ne relance pas une coupure réseau en cours de lecture |

## Relecture du plan (commit `0d73ac1`, rebasé en `4d68880`)

Cinq lecteurs : agy directif et neutre, codex directif et neutre, un sous-agent Claude
sur le prompt neutre. Rapports dans le scratchpad de la session, étiquette
`92e618b2df29`. Union des remarques, avec ce que j'en ai fait :

| Remarque | Qui | Vérifiée | Suite |
|---|---|---|---|
| Blocage au-delà de 15 messages pendant `scanAll` ou `NextResultSet` (file de `sqlexp`) | agy ×2 | oui, les deux lots, sur le binaire livré | E0, D0, tâche 0 ; relais prototypé et mesuré |
| La première erreur SQL disparaît quand un timeout la suit | Claude, codex ×2, agy ×2 | oui | E5b, D6, tâche 5 |
| Le marquage `incomplete` après la boucle marque un jeu complet | agy ×2, Claude (hypothèse) | par la lecture du code et leurs traces | retiré ; `TestTimeoutAfterACompleteSetLeavesItComplete` et rupture 4 |
| La rupture « `if true` » ne peut pas faire tomber `TestQueryEndingBeforeTheTimeoutSucceeds` | agy ×2, Claude | par leurs traces | assertion `len(res.Rows) == 1` ajoutée |
| `timeoutSec` absent des littéraux des tâches 3 et 4 : sortie 4 immédiate | agy ×2, codex ×2, Claude | par lecture (`context.WithTimeout` de 0) | règle générale en § 4 et littéraux corrigés |
| La réécriture `errSlots` en paires ne corrige rien ; sa rupture ne peut pas tomber | agy ×2, codex ×2, Claude | raisonnement vérifié (plafond monotone) | retirée ; nouvelle rupture 3 |
| D3 : une frontière de plus distingue `SELECT 1; UPDATE` | codex directif | oui, sans paramètre | vrai sans paramètre, faux sous `sp_executesql` (agy, remesuré par moi) ; D3 garde sa décision, son argument est réécrit |
| D3 : `SELECT 1 AS a WHERE 1 = 0` a un plan ; seul `SELECT WITHOUT QUERY` n'en a pas | agy directif | oui (table de D3) | formulation corrigée |
| Docs périmées : « only the last statement's plan » (`SKILL.md` 89 et 290, `README.md` 152-153) | agy ×2, Claude | grep | tâche 2 |
| Docs périmées : `incomplete` défini par l'erreur seule (`README.md` 148, `AGENTS.md` 113) | Claude, codex | grep | tâche 5 |
| `VIEW SERVER STATE` dans `README.md`, `AGENTS.md`, `main.go` aussi | codex neutre, Claude | grep | tâche 7 |
| `plan` et `more_plans` non caviardés | Claude | oui ; et le secret y est échappé pour XML, hors de `secretForms` (trouvé en vérifiant) | tâches 1 et 2 |
| Messages du catalogue sur `-saved` non caviardés | Claude, codex directif | lecture : ils viennent de fichiers locaux | non retenu (agy directif a conclu de même) ; le commentaire le dit |
| Matrice § 8 non couverte | codex ×2, agy directif, Claude | grep des tests | tâche 8 ; procédure stockée écartée (EXEC, pas d'objet persistant) ; secret dans l'erreur en tâche 1 |
| Erreur de transport en sortie 2 et non 4 | codex directif, agy directif, Claude | lecture | D7 : la doc s'aligne sur le code |
| Produit cartésien sans `TOP` : règle de `AGENTS.md` et charge du labo | codex neutre, Claude | mesuré : 93,6 s sans `TOP` ; 11,1 s avec `TOP (20000000)` | `TOP (20000000)` ; mesure longue retirée du plan |
| Compte `--- PASS` faussé par `t.Run` | agy ×2, Claude | lecture de `flags_test.go` | règle « sans `t.Run` » en § 4 |
| Rupture de la tâche 6 dans `tokens()` alors que `_` vit dans `isWordRune` (`lex.go`) | agy directif | grep | corrigée |
| `TestPlanCapStopsStoringNotReading` ne prouve pas que la lecture continue | agy neutre | lecture | `RAISERROR` après les plans |
| Mesure du libellé français de `STATISTICS TIME` renvoyée à l'implémenteur | agy ×2 | oui (`Temps UC`) | faite, dans D5 et le test |
| Commentaire sur `MsgRowsAffected` demandé par la spec § 7 | Claude | lecture | tâche 3 |
| `sqlplan` lit `plan` : son comportement change | Claude | lecture | D3 et tâche 2 |
| Jeu à zéro ligne rangé `incomplete` contre la contrainte 3 | agy directif | lecture de la spec | D6 point 2 |
| Format du message de refus « comme celui de `-maxrows` » | agy directif | lecture | précisé |

## Répartition de la revue pendant l'exécution

Je lance moi-même la suite complète à la fin de chaque tâche, et je relis chaque diff ;
un relecteur sous-agent seulement pour les tâches 0, 1 et 5, qui touchent la
concurrence, le caviardage et la priorité des erreurs. Coût si c'est faux : un défaut
de doc ou de test des tâches 2, 3, 4, 6, 7, 8 qu'un second lecteur aurait vu.

## Journal

- 25 octobre 2026, 00 h 50 : worktree `../db-ai-toolkit-sqlq-messages`, branche
  `feat/sqlq-server-messages` depuis `main` (`81b2d22`). Le dépôt principal avait des
  modifications d'une autre session (`plugins/sqlserver-toolkit/README.md`,
  `skills/page-salvage/`) : je n'y touche pas.
- État de départ : les sept tests d'intégration passent sur le labo ; 40 lignes
  `--- PASS` pour `./cmd/sqlq/`.
- Mesures de D3 et D5 faites avec `sqlq` et une sonde Go hors dépôt (scratchpad de la
  session). La sonde a exécuté `UPDATE Stage.Stage SET Duree = Duree WHERE 1 = 0` (et sa
  variante paramétrée, `AND 1 = 0`) sur `PachaDataFormation` de `sql2022` : aucune
  ligne touchée.
- L'autre session a commité (`c3f2581`, page-salvage) ; branche rebasée sur ce commit
  avant la révision du plan.
- Relecture du plan par le panel, tri ci-dessus, plan révisé.
- Exécution : tâches 0 à 8 par des implémenteurs sous-agents, une tâche à la fois
  (toutes touchent `main.go`) ; Sonnet pour les tâches 2, 4, 6, 7 et 8. J'ai relu le
  relais moi-même (45 lignes) au lieu d'un siège dédié, et remplacé les relectures
  par tâche des tâches 1 et 5 par une relecture finale de toute la branche.
- Tâche 0 : l'implémenteur a ajouté un cas que mon plan ignorait : si le dernier
  message rangé après l'échéance est un vrai message, `next()` rend ensuite le
  `MsgNextResultSet` synthétique au lieu d'attendre sans fin. Retenu.
- Tâche 5 : le test « timeout après un jeu complet » tel que je l'avais écrit ne
  pouvait rien vérifier : SQL Server garde la sortie d'une instruction dans son tampon
  pendant un `WAITFOR`, donc le jeu `a` n'arrivait jamais. Ajout d'un
  `RAISERROR('', 0, 1) WITH NOWAIT` avant l'attente ; la rupture 4 telle que libellée
  ne tombe pas (un second `MsgNextResultSet` remet `open` à -1), sa variante « dernier
  jeu rangé », qui est le défaut mesuré par les relecteurs, tombe. Retenu.
- Relecture finale (sous-agent neuf, sondes sur le labo) : huit remarques vérifiées,
  six hypothèses. Corrigées (258179d, f399a52) : la condition de tampon dans la doc,
  le droit `VIEW DATABASE STATE` du repli Query Store, `registered-servers/README.md`,
  une colonne nommée `*showplan*` prise pour un plan sans `-plan`, un `MsgNext` reçu
  après l'échéance, le drapeau de troncature perdu sur timeout, la doc du code 4
  (seule l'ouverture de la connexion le donne ; un préambule peut échouer pour une
  raison SQL, la permission SHOWPLAN), le libellé de `-maxmessages` sur un `-saved`,
  la phrase « first statement's plan » du README. Non corrigés, notés ici : la borne
  haute calculée d'un `LIKE 'prefixe%'` dans un plan montre le secret moins son
  dernier caractère incrémenté (entrée aussi artificielle que celle du test) ;
  `elapsed_ms` sous-estime un appel coupé par le timeout (le pilote draine jusqu'à
  2 × 5 s), antérieur à la branche ; la permission SHOWPLAN de `-plan` et une valeur
  tronquée dans le texte d'une erreur 2628, non confirmées.
- Fin : 63 lignes `--- PASS` pour `./cmd/sqlq/` (40 au départ), 206 pour
  `./internal/sqlq/`, 320 tests sous `-race` dans le module, `go vet` et `gofmt`
  vides.
