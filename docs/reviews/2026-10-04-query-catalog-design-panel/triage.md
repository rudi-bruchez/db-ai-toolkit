# Tri du panel sur la spec du catalogue (v1, commit 2811bfc)

Cinq lecteurs : agy (Gemini 3.1 Pro) directif et neutre, codex (GPT-6) directif et neutre,
un sous-agent Claude neutre. Aucun n'avait d'instance SQL Server ; tous ont lancé le vrai
garde-fou sur le vrai clone de tsql-scripts. Les comptes du §3 (344, 214, 98, 38) ont été
reproduits par quatre d'entre eux.

## Retenu, corrigé dans la v2

| Trouvaille | Lecteurs | Correction (§ de la v2) |
|---|---|---|
| Affectations qui échappent au refus : `SET @p += 1`, `SELECT TOP (1) @p =`, `SELECT @x = 1, @p = 2` | agy-n, codex-d, claude | règle de position d'affectation sur jetons, penchant vers le refus (§11) |
| Une règle « tout `@p =` » refuserait 9 comparaisons sur 13 (`IIF(@online = 1, …)`) | claude | comparaisons reconnues par profondeur et contexte (§11) |
| Réécriture qui efface l'instruction suivante sur la même ligne, ou laisse un initialiseur sur deux lignes valide mais faux (`+ N'Orders%';`), ou casse un `CASE` multiligne | agy-n, agy-d, claude | ligne de déclaration seule, `;` obligatoire sur la ligne (§11) |
| Troncature silencieuse d'une valeur trop longue (`varchar(10)`), date lue selon `DATEFORMAT` | codex-n, claude | validation par type et liaison typée avant connexion (§11) |
| Un seul jeu de résultats rendu, scripts à plusieurs `SELECT` | agy-d, codex-d, claude | jeux multiples et messages ajoutés au périmètre (§5) |
| `READUNCOMMITTED` (sans espace) et `READ`/`UNCOMMITTED` sur deux lignes non détectés | codex-n, codex-d, claude | détection sur jetons (§12) |
| Renommer un marqueur change le fichier donc l'empreinte : contradiction avec le §8 v1 | agy-n | empreinte du contenu privé de sa ligne marqueur, clé = empreinte (§10) |
| Empreinte reprise après le run, sur un fichier qui a pu changer | codex-n, codex-d | empreinte des octets lus pour l'exécution (§10) |
| `-list-queries` sans profil face à des collisions définies par profil | codex-n, codex-d, claude | `-profile` facultatif, vue définie (§7) |
| Une collision désactive la requête livrée dont le skill dépend | agy-d, codex-d | le livré l'emporte, l'autre est `rejected` (§7) |
| Noms de profil en répertoire : `..`, `_generic`, doublons de casse ; et `/` des groupes SSMS | claude | `profiles/<nom>` validé segment par segment (§7) |
| `-summary` avec `/*` ou `*/` : fichier sauvé vide et « vérifié », ou SQL injecté | codex-d, claude | refus, ligne `Parameters:` générée, relecture du fichier écrit (§12) |
| `-summary` absent de la commande d'exemple alors qu'obligatoire | codex-n | exemple corrigé (§4) |
| Marqueur mal placé, mal orthographié, ou derrière un BOM : script absent sans explication | agy-d, codex-d, claude | toute tentative de marqueur est acceptée ou `rejected` ; BOM retiré (§8) |
| Licence ou URL prise pour le résumé | codex-d, claude | résumé = ligne la plus proche au-dessus du marqueur, URL exclues (§8) |
| `DECLARE @local INT = @cutoff` : `@cutoff` pris pour une variable locale | agy-n | déclaration définie comme cible du `DECLARE` (§9) |
| Fichiers livrés `No parameter.` rejetés si la ligne `Parameters:` est exigée | codex-n | ligne facultative, contrôlée si présente (§8) |
| Exemples faux : `waits-statistics` a un `GO` et aucune variable, `dm_io_virtual_file_stats` passe et a 33 lignes | codex-n, codex-d, claude | exemples pris dans le corpus et passés au garde-fou (§8, §12) |
| Raisons de `rejected`, résumés, chemins : ce qui part chez le fournisseur à chaque session | agy-d, codex-d | règle « Ce qui est publié » (§12) |
| `"default"` dans `saved.params` indiscernable d'une valeur `default` | claude | `params` et `defaults` séparés (§12) |
| `verified` d'un variant jamais exécuté | codex-n, codex-d | sens défini : la surcharge ne change pas la structure du batch (§10) |
| Scripts sans `TOP (n)` face à la règle de borne d'`AGENTS.md` | codex-n | l'auteur répond de la taille, `-maxrows` toujours passé (§14) |

## Retenu comme limite, sans changement de mécanisme

- Deux runs simultanés peuvent perdre une entrée du registre (agy-d, codex-d, claude). La
  conséquence est une requête affichée comme non vérifiée : prudente. Relecture et fusion
  avant `rename`, pas de verrou (§10, §17).

## Écarté

- « `Heavy: yes` présenté comme repris du 2 septembre » (agy-d) : la v1 le listait parmi les
  nouveautés ; la v2 le dit plus explicitement (§14).
- « `SET @p +=` exécute l'intention de l'auteur » (agy-d, Not a problem) : contredit par
  trois lecteurs ; l'agent rapporterait la valeur passée, pas celle calculée.

## Vérifié par l'auteur de la spec

- `sessions-from-host.sql` passe le garde-fou, déclare `@hostname sysname = N'%';` sur une
  ligne conforme, et pose `READ UNCOMMITTED`.
- `waits-statistics.sql` a bien `GO` en ligne 9.
- 45 lignes `DECLARE` d'une ligne de type simple parmi les 214 (la v1 n'en parlait pas, le
  brouillon de la v2 disait 41).
- `tools/` n'a aucun test d'intégration : la v2 en définit le mécanisme au lieu de renvoyer
  à un mécanisme existant.
