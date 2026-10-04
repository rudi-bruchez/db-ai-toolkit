# Tri de la relecture finale de branche (commit ca7c2cf)

Deux lecteurs à périmètres disjoints, chacun sur un worktree détaché : codex (« casser » les
garde-fous et les tests, sans serveur) et un sous-agent Claude (« conformité » du code et de
la documentation à la spec, avec l'instance de test). Le rapport de codex est dans
`review-codex.md` ; celui du sous-agent a été rendu dans la session et est résumé ici.

## Retenu et corrigé

| Trouvaille | Lecteur | Correction |
|---|---|---|
| `SELECT NEXT VALUE FOR dbo.seq` passe le garde-fou en lecture seule et fait avancer la séquence | codex | refusé sous le mot-clé `NEXT VALUE FOR` (`4fa8fbe`) |
| `-save-query` écrit à travers un dossier de profil qui est un lien symbolique, hors de la racine personnelle | codex | tout lien sur le chemin arrête la sauvegarde avant création (`4fa8fbe`) |
| Les refus de surcharge citent l'identifiant fautif, publié par le catalogue | codex, claude | la raison ne donne que la ligne (`4fa8fbe`) |
| `-save-query` garde un run `-database` sans le noter : le `-saved` suivant répond depuis la base du profil | claude | refusé, comme `-dirty-reads` |
| Un canon illisible ou vide laisse `-saved` exécuter un homonyme | claude | `needCanon` exige au moins une entrée livrée |
| `-summary` sans `-save-query` ignoré en silence | claude | refusé, code 1 |
| Les erreurs suivantes sont ajoutées en fin de `messages` au lieu de leur rang d'arrivée | claude | une place réservée à l'arrivée, remplie après nettoyage du secret |
| Une entrée rejetée à l'analyse du marqueur publie un nom vide au lieu du nom du fichier | claude | nom du fichier si valide |
| `numeric(5,2)` signale la longueur au lieu du type | claude | type vérifié avant sa longueur |
| Statut de la spec resté « en attente de plan » ; règle des raisons non amendée | claude | spec mise à jour |

## Écarté ou laissé

- Texte `-help` et erreur de `-list-queries -profile X` sans fichier de profils qui affichent
  un chemin absolu (claude) : antérieurs à la branche, sur la sortie locale de l'utilisateur.
- `AGENTS.md` ne recopie pas la table des sources ni l'exemple JSON complet (claude, par
  lecture) : il renvoie au skill, qui les porte.
- Un `-saved -dirty-reads` enregistre `verified` sans dire qu'il a lu en `READ UNCOMMITTED`
  (claude, par lecture) : `verified` atteste que le contenu s'exécute, pas la réponse.
