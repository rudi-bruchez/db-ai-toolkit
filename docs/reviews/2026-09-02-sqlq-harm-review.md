# Adversarial harm review — `sqlq` and the `live-query` skill

Date : 2026-09-02
Périmètre : `tools/cmd/sqlq`, `tools/internal/sqlq`,
`plugins/sqlserver-toolkit/skills/live-query`.

## Modèle de menace

Un DBA installe le plugin depuis la marketplace, lance `build-tools`, écrit un
fichier de profils pointant sur la production, et laisse un agent poser des
questions. **Il a lu le README, pas le code.** Il fait confiance à ce que le
README appelle des garde-fous.

## Phase 1 — scanners

| Outil | Résultat |
|---|---|
| `gosec ./...` | 8 alertes, dont **une seule** dans `sqlq` : G304 sur `os.ReadFile(file)` (`main.go:319`). Chemin fourni par l'opérateur sur sa propre ligne de commande — **écartée après vérification**. Les 7 autres sont dans `internal/errorlog`, hors périmètre de ce changement. Aucune G201/G202 (injection SQL), aucune G101 (secret en dur). |
| `govulncheck -mode=binary` | **0 vulnérabilité atteignable.** 1 vulnérabilité dans un module requis mais non appelé. |
| `govulncheck` (mode source) | **N'a pas pu s'exécuter** : le binaire publié est compilé en go1.26 et refuse de typer un module `go 1.27`. Contourné par le mode binaire. |
| `semgrep` | non exécuté (non installé, réseau d'entreprise). |

Les scanners n'ont trouvé aucune des trois failles sérieuses ci-dessous. C'était
attendu : ce sont des arguments sémantiques, pas des motifs.

---

## F1 — SEVERE — `OPENQUERY` et `OPENROWSET` traversent le garde-fou lecture seule

**Où** : `tools/internal/sqlq/guard.go:18-27` (`writeKeywords`).

**Ce qui se passe.** `Sanitize` retire les littéraux chaîne *avant* que
`FindWrites` ne cherche des mots-clés d'écriture — c'est ce qui fait que
`SELECT 'DROP TABLE x'` est correctement autorisé. Mais la requête *pass-through*
d'`OPENQUERY` **est** un littéral chaîne. Elle est donc effacée avant l'analyse,
et son contenu n'est jamais examiné :

```sql
SELECT * FROM OPENQUERY(LINKED, 'DELETE FROM dbo.T')
```

Après `Sanitize` : `SELECT * FROM OPENQUERY(LINKED, )` → jetons `SELECT`,
`OPENQUERY`, `LINKED` → aucun mot-clé d'écriture → **autorisé**.

**Vérifié empiriquement** sur le binaire compilé, profil `readonly` :

| Requête | Code de sortie | Interprétation |
|---|---|---|
| `SELECT * FROM OPENQUERY(LINKED, 'DELETE FROM dbo.T')` | `4` | garde-fou franchi, échec seulement à la connexion |
| `SELECT * FROM OPENROWSET(..., 'DROP TABLE dbo.T')` | `4` | garde-fou franchi |
| `DELETE FROM dbo.T` (témoin) | `3` | refusé, comme prévu |

**Pour qui.** Le chemin par défaut. Aucune configuration particulière n'est
nécessaire côté `sqlq`. Il faut un serveur lié configuré — courant en
production — et le mapping de connexion d'un serveur lié pointe souvent vers un
compte plus privilégié que l'appelant, si bien que le `db_datareader` local ne
borne pas ce qui est exécuté à distance.

**Pourquoi c'est grave.** C'est exactement le cas « le garde-fou vérifie la
présence d'une chose, pas son contenu ». Le README promet que les instructions
d'écriture sont refusées ; une ligne les fait passer, en écrivant sur un *autre*
serveur que celui que l'opérateur croit protéger.

**Correctif** : ajouter `OPENQUERY`, `OPENROWSET`, `OPENDATASOURCE` et
`OPENJSON`(non — inoffensif) aux mots-clés refusés. Appliqué.

---

## F2 — SEVERE — `READ UNCOMMITTED` est imposé à toutes les requêtes, et présenté comme une protection

**Où** : `tools/cmd/sqlq/main.go:40-42` (`preamble`), README du plugin,
`SKILL.md`.

**Ce qui se passe.** Chaque lot est préfixé de
`SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED`, sans possibilité de le
désactiver. La documentation le présente uniquement comme un dispositif
anti-blocage : « il ne peut ni bloquer une instance chargée ni être bloqué par
elle ». Le coût en correction n'est écrit nulle part.

**Le problème.** Ce niveau d'isolation autorise les lectures sales, les lignes
manquantes et les lignes dupliquées pendant les fractionnements de page. Or la
question centrale du projet est *« pourquoi cette vue ne retourne pas la donnée
que je veux »* — une question de **correction**. L'outil peut répondre « la ligne
n'est pas là » alors qu'elle y est, ou montrer une ligne annulée depuis.

**L'aggravant.** La checklist livrée par ce même skill
(`references/review-checklists.md`, section procédure) dit au lecteur :

> `NOLOCK` used as a performance setting. It permits dirty reads, missing rows
> and duplicated rows during page splits. Acceptable for approximate reporting,
> not for anything that must reconcile.

L'outil fait donc systématiquement, et en silence, ce que sa propre checklist
signale comme un défaut. La documentation est plus rassurante que la réalité :
la classe la plus dangereuse de divergence.

**Pour qui.** Le chemin par défaut, toujours, sans avertissement.

**Correctif** : retirer le changement d'isolation du préambule par défaut.
`SET LOCK_TIMEOUT` est le véritable garde-fou anti-blocage et reste en place.
Ajouter une option explicite `-dirty-reads` et une clé de profil pour ceux qui
la veulent, en documentant son coût. Appliqué.

---

## F3 — SEVERE — un profil `readwrite` autorise toute destruction sans confirmation

**Où** : `tools/cmd/sqlq/main.go:104-110`, `SKILL.md`.

**Ce qui se passe.** `mode: "readwrite"` désactive entièrement le garde-fou.
Rien d'autre ne s'interpose : un agent peut émettre `DROP TABLE dbo.Clients` et
il partira. Aucune confirmation n'est demandée, ni par le binaire, ni par le
skill.

**Ce que cela contredit.** `docs/writing-style.md` de ce dépôt pose la règle :

> Prefer read-only diagnostics by default. **Flag any statement that writes,
> changes configuration, or restarts a service, and require confirmation.**

Le skill livré ne le fait pas. C'est une violation de la convention documentée
du dépôt, introduite par ce changement.

**Pour qui.** Quiconque écrit `readwrite` dans un profil — probablement pour une
base de développement — puis oublie que le profil existe, ou se trompe de nom de
profil. Le mode est une propriété persistante du fichier, pas de l'intention du
moment.

**Correctif**, en défense en profondeur :
1. le profil dit que l'écriture est *permise* ;
2. un `-allow-write` explicite dit que *cette invocation* l'entend ainsi ;
3. le `SKILL.md` impose de demander confirmation à l'utilisateur avant toute
   écriture.

Appliqué.

---

## F4 — MODERATE — fuite possible du mot de passe dans la sortie JSON

**Où** : `tools/cmd/sqlq/main.go:137-141`.

**Ce qui se passe.** Le mot de passe est intégré à l'URL de connexion. Si
`sql.Open` échoue à l'analyser, l'erreur du driver est recopiée telle quelle
dans `error.message`, imprimée sur stdout, puis versée dans le transcript de
l'agent — et de là, éventuellement, dans un log ou un ticket.

**Statut : non vérifié.** Je n'ai pas établi que `go-mssqldb` inclut la chaîne
de connexion dans ses erreurs d'analyse ; certains parseurs le font, d'autres
non. Je le signale parce que le correctif coûte cinq lignes et vaut d'être
appliqué indépendamment de la réponse.

**Correctif** : ne jamais émettre une erreur brute du chemin `DSN`/`Open` ;
retirer le mot de passe de tout texte d'erreur avant émission. Appliqué.

---

## F5 — MODERATE — `triggers-inventory.sql` déverse le corps de tous les triggers

**Où** : `plugins/.../queries/triggers-inventory.sql`, dernière colonne.

**Ce qui se passe.** L'inventaire renvoie `m.definition` pour chaque trigger.
Avec le plafond de 50 lignes par défaut et des corps de quelques kilo-octets,
c'est de l'ordre de 200 Ko de texte SQL injecté dans le contexte du modèle pour
une question de *triage*.

Cela contredit la règle que le skill énonce lui-même : « Never paste a large
result verbatim ». Les colonnes d'indices et `definition_length` sont là
précisément pour éviter d'avoir à lire les corps ; les renvoyer quand même annule
le bénéfice et coûte de l'argent à l'utilisateur.

**Correctif** : retirer `definition` de l'inventaire. Le corps d'un trigger
donné se récupère avec `proc-source.sql`, qui fonctionne déjà sur les triggers.
Appliqué.

---

## F6 — MINOR — `-maxrows` donne un faux sentiment de borne

`RowSet.Add` continue de compter toutes les lignes pour que `rowcount` reste
vrai. Les lignes au-delà du plafond sont donc **lues depuis le réseau puis
jetées** : `-maxrows` borne le contexte du modèle, pas le travail du serveur ni
le transfert. C'est un compromis délibéré et il est écrit dans le `SKILL.md`,
mais pas dans le README. Ajouté au README.

## F7 — MINOR — motif `LIKE` non échappé dans `object-references.sql`

Un nom d'objet contenant `%`, `_` ou `[` élargit la recherche textuelle au lieu
de la restreindre. Sur-correspondance, jamais sous-correspondance : gênant, pas
dangereux. Corrigé : le motif est construit avec les métacaractères échappés et
la clause `ESCAPE`, plutôt que simplement signalé.

---

## Ce que le logiciel fait bien

- `encrypt=true` et `trustservercertificate=false` par défaut : la connexion
  **échoue fermée** face à un certificat auto-signé, plutôt que de se rabattre
  en clair.
- Aucun mot de passe ne peut être stocké dans le fichier de profils : un
  `password` en ligne est refusé au chargement, pas ignoré en silence.
- `mode` absent vaut `readonly` : le réglage sûr est celui qu'on obtient en
  oubliant.
- `app name` identifie les connexions dans `sys.dm_exec_sessions`.
- Les valeurs passent par des paramètres SQL typés ; aucune substitution
  textuelle n'existe dans le code.
- `TestBundledQueriesPassTheReadOnlyGuard` relit les `.sql` livrés et échoue si
  le garde-fou en refuse un : la classe « requête canonique injouable » est
  mécanisée, pas laissée à l'œil.

## Est-il responsable de livrer en l'état ?

**Avant correction : non.** F1 permettait d'écrire sur un système de production
via le garde-fou censé l'empêcher, et F2 faisait répondre faux, en silence, à la
question même que l'outil sert à poser.

**Après correction : oui, pour un usage interne, avec l'avertissement ci-dessous
dans le README.** Reste non vérifié tout ce qui exige une instance réelle :
exécution, capture de plan, conversion des types, remontée d'erreur structurée,
et les trois modes d'authentification.

## L'avertissement le plus court que le README doit porter

> Le filtre d'écriture de `sqlq` est de la prévention d'accident, pas de la
> sécurité. Il analyse du texte : il refuse ce qu'il reconnaît, et il ne
> reconnaît pas tout. Donnez-lui un login qui ne *peut* pas écrire —
> `db_datareader`, `VIEW DEFINITION`, `VIEW SERVER STATE`, et rien d'autre — et
> pas de serveur lié privilégié. Si le seul obstacle entre l'agent et votre
> production est ce filtre, votre production n'est pas protégée.
