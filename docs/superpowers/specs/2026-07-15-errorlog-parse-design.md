# Design — `errorlog-parse` (préprocesseur ERRORLOG SQL Server)

Date : 2026-07-15
Statut : approuvé (brainstorming), en attente de plan d'implémentation.

## 1. Objectif

Outil Go en ligne de commande qui lit un fichier ERRORLOG SQL Server (souvent
volumineux et bruité) et émet un **digest compact** ne contenant que
l'information utile à un diagnostic. But : économiser massivement les tokens
d'un skill d'analyse (à la manière de `rtk` : tout ce qui est utile, aucun
bruit). Il **remplace** l'ébauche v1 `errorlog-parse` en place (même binaire,
même emplacement `tools/cmd/errorlog-parse`, injecté dans
`plugins/sqlserver-toolkit/bin/`).

### Motivation, ancrée sur un fichier réel (échantillon 83 Mo)

Analyse de l'échantillon `temp/ERRORLOG.zip` (108 713 lignes) :

| Constat | Détail | Conséquence de conception |
|---|---|---|
| Encodage | UTF-16LE + BOM (`0xFFFE`) + CRLF | Décodage/transcodage obligatoire (v1 lisait de l'UTF-8 et cassait) |
| 97 % de bruit | 105 575 lignes `Log was backed up` (12 bases × ~8 728 sauvegardes) | Le gisement d'économie de tokens |
| Localisation FR | « Erreur », « Gravité », « État », « Raison » — **mélangés** avec de l'anglais (« Login failed », « Log was backed up ») | Règles multilingues ; appliquer tous les packs à la fois |
| Espace insécable | Confirmé U+00A0 avant les `:` (« Erreur⍽: 976, Gravité⍽: 14 ») | Regex tolérant le NBSP : `[\s\x{00A0}]*:` |
| Données confidentielles | Noms de bases = clients, comptes `DOMAINE\x` (gMSA), IP, chemins, modèle VM | Rédaction opt-in pertinente |
| Boot riche | Version/CU, édition, OS, RAM, CPU/NUMA, collation, auth, UTC, compte de service | Base du résumé d'instance |
| Vrai signal | Erreur 18456 (Gravité 14), Erreur 18056 (Gravité 20), rafales de Login failed d'une même IP, messages Always On AG, transactions rolled back/forward | Entrées à conserver |

Résultat attendu : ~83 Mo → quelques Ko de signal.

## 2. Décisions (issues du brainstorming)

1. **Remplace v1** en place.
2. **Sortie** : texte compact sur STDOUT par défaut (mini-tokens) ; `--format md`
   optionnel (référence humaine). Une seule logique, deux rendus.
3. **Rédaction** : opt-in `--redact`, **pseudonymisation cohérente** (jetons
   stables `DB_1`, `LOGIN_1`, `IP_1`) avec légende.
4. **Agrégation** : activée par défaut ; collapse des répétitions identiques en
   `[×N] plage horaire : message`. Désactivable `--no-aggregate`.
5. **Matching FR/EN** : numéros indépendants de la langue (kind `severity` avec
   capture) + mots-clés dans des packs multilingues.
6. **Filtrage** : hybride — drop du bruit connu, keep du signal connu, **keep de
   l'inconnu** (fail-safe).
7. **Résumé d'instance** : toujours, compact, en tête (`--no-summary` pour omettre).
8. **Entrées** : auto-détection `.zip` / `.gz` / brut, encodage (UTF-16LE/BE
   BOM, UTF-8), CRLF ; fichier unique / dossier (`ERRORLOG` + `ERRORLOG.1..n`) /
   stdin.
9. **Packs de règles** : format texte simple `.rules`, embarqués via `go:embed`,
   surchargeables via `--rules <dir>` ; **tous les packs s'appliquent** (logs
   mixtes). Contribuer une langue = ajouter un fichier, sans toucher au Go.
10. **Advisories par catégorie** : une catégorie de bruit volumineuse produit un
    conseil actionnable (ex. backups → trace flag 3226), piloté par données.

## 3. Architecture (unités isolées, testables)

Module Go `tools/internal/errorlog/`, binaire `tools/cmd/errorlog-parse` :

```text
input.go     acquire   path → flux d'octets : .zip (entrée ERRORLOG), .gz, brut ; fichier / dossier / stdin
decode.go    decode    BOM → UTF-16LE/BE ou UTF-8 ; transcode UTF-8 ; CRLF→LF
parse.go     parse     regroupe en Entry{Timestamp, Source, Message, Lines} ; continuations = lignes TAB/espace
rules.go     classify  charge les packs ; règles → (keep, catégorie) ; filtre plage horaire
summary.go   summary   faits d'instance depuis la séquence de boot
aggregate.go aggregate collapse des répétitions → message + compte + plage horaire
redact.go    redact    pseudonymisation cohérente (opt-in)
advice.go    advice    advisories : (a) volume d'une catégorie de bruit, (b) checks d'état boot
render.go    render    rendu texte compact (défaut) ou Markdown
rules/*.rules          packs embarqués : common.rules, en.rules, fr.rules, advice.rules, checks.rules
cmd/errorlog-parse/main.go   flags (stdlib flag) + câblage
```

Flux : `acquire → decode → parse`, puis `summary` d'un côté et
`classify + time-range → aggregate → redact` de l'autre, puis `render`.

Chaque unité a une entrée/sortie claire et se teste seule.

## 4. Parsing

- `entryStart` = ligne commençant par un timestamp
  `^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{2} `.
- Les lignes suivantes ne commençant pas par un timestamp (souvent préfixées
  TAB) sont des **continuations** rattachées à l'entrée précédente.
- `Entry` conserve les lignes brutes ; `Timestamp` (première ligne), `Source`
  (colonne spid/Server/Logon/Backup), `Message` (reste).

## 5. Moteur de règles

Trois **kinds** :

- `signal` — match ⇒ **garder**.
- `noise` — match **et** aucun signal ⇒ **jeter** (compté par catégorie).
- `severity` — capture un entier via un groupe ; **garder si ≥ seuil**
  (`--severity`, défaut 16). Rend « Gravité : 20 » = « Severity: 20 » sans
  dépendre du mot.

**Précédence de classification** d'une entrée :
1. une règle `severity` capture N ≥ seuil ⇒ garder ;
2. sinon une règle `signal` matche ⇒ garder ;
3. sinon une règle `noise` matche ⇒ jeter (catégorie) ;
4. sinon (inconnu) ⇒ **garder** (fail-safe).

### Format de pack `.rules`

Un fichier par langue plus un `common.rules`. Ligne :

```text
catégorie <TAB> kind <TAB> regexp
```

- `#` en début de ligne = commentaire ; lignes vides ignorées.
- `kind` ∈ `signal` | `noise` | `severity`.
- regexp = syntaxe Go **brute** (pas de double-échappement) ; NBSP via
  `\x{00A0}` ; insensibilité à la casse via `(?i)`.
- une règle `severity` doit comporter un groupe de capture entourant le nombre.

Exemple `rules/fr.rules` :

```text
# catégorie      kind      regexp
severity          severity  (?i)Gravité[\s\x{00A0}]*:[\s\x{00A0}]*(\d{1,2})
error-num         signal    (?i)Erreur[\s\x{00A0}]*:[\s\x{00A0}]*\d+
login-fail        signal    (?i)Login failed|Échec de la connexion
backup            noise     (?i)Log was backed up|journal.*sauvegard
checkdb-ok        noise     (?i)CHECKDB.*0 erreur.*0 erreur
```

Chargement : packs embarqués (`go:embed rules/*.rules`) + fichiers de
`--rules <dir>` (ajout/surcharge). `--lang <codes>` restreint aux packs nommés ;
défaut = tous (nécessaire pour les logs mixtes).

### Advisories (deux sources, data-driven)

Les advisories sont des conseils actionnables émis dans la section `ADVISORIES`
(présente dans **les deux formats**, texte et Markdown — signal à forte valeur /
faible coût). Deux sources, toutes deux pilotées par données.

**(a) Volume d'une catégorie de bruit — `rules/advice.rules`**

```text
catégorie <TAB> seuil_min <TAB> message ({count} interpolé) <TAB> url
```

Quand le nombre d'entrées jetées d'une catégorie ≥ `seuil_min`, un advisory est
émis. Première entrée :

```text
backup   500   {count} messages de sauvegarde réussie noient ce log. Activez le trace flag 3226 pour cesser de journaliser les sauvegardes réussies.   https://www.mssqltips.com/sqlservertip/1457/stop-logging-all-successful-backups-in-your-sql-server-error-logs/
```

**(b) Checks d'état boot — `rules/checks.rules`**

Vérifient un réglage d'instance sous-optimal détecté (ou absent) dans la séquence
de boot :

```text
check_id <TAB> mode(present|absent) <TAB> detect_regexp <TAB> message <TAB> url
```

- `present` : advisory émis si `detect_regexp` matche dans le boot (ex. IFI
  désactivé, échec d'enregistrement SPN).
- `absent` : advisory émis si `detect_regexp` **ne matche pas** (ex. LPIM : pas
  de ligne « locked pages in the memory manager » ⇒ Lock Pages in Memory inactif).
- `detect_regexp` est multilingue (les **valeurs** de réglage sont localisées :
  `activé`/`enabled`, `désactivé`/`disabled`).

Exemples :

```text
ifi-off   present  (?i)Instant File Initialization[\s\x{00A0}]*:[\s\x{00A0}]*(disabled|désactivé)   IFI désactivé : accordez « Effectuer les tâches de maintenance de volume » au compte de service pour l'initialisation instantanée des fichiers.   https://learn.microsoft.com/sql/relational-databases/databases/database-instant-file-initialization
spn-fail  present  (?i)failed to register.*Service Principal Name|échec.*SPN   Enregistrement du SPN en échec : l'authentification Kerberos échouera. Enregistrez le SPN (setspn) ou vérifiez les droits du compte.   https://learn.microsoft.com/sql/database-engine/configure-windows/register-a-service-principal-name-for-kerberos-connections
lpim-off  absent   (?i)locked pages in the memory manager|pages verrouillées   Lock Pages in Memory inactif : envisagez ce privilège pour éviter le paging du buffer pool (selon contexte/édition).   https://learn.microsoft.com/sql/database-engine/configure-windows/enable-the-lock-pages-in-memory-option-windows
```

## 6. Résumé d'instance

Extrait de la séquence de boot : version + build + CU, édition, OS, RAM, CPU
(sockets/cœurs/logiques), NUMA, collation par défaut, mode d'authentification,
compte de service, ajustement UTC, chemin du log, **fenêtre temporelle**
(premier → dernier timestamp) et heure de démarrage. Compact en texte ; plus
détaillé en Markdown. Omis avec `--no-summary`. Toujours dérivé de la séquence
de boot du fichier, **indépendamment de `--from/--to`** (il décrit l'instance,
pas la fenêtre analysée).

**Champs détaillés (Markdown)** : en plus du bloc compact, le résumé Markdown
ajoute port(s) d'écoute TCP (1433, DAC 1434, endpoints AG), enregistrement du
SPN, Lock Pages in Memory (LPIM), Instant File Initialization (IFI) et son
privilège « Effectuer les tâches de maintenance de volume » (Perform Volume
Maintenance Tasks), listener(s) AG, et toute autre info boot utile.

Les **valeurs** de ces réglages sont localisées (`activé`/`enabled`,
`désactivé`/`disabled`) : l'extraction doit être multilingue, comme les packs.
Quand un de ces réglages est sous-optimal, il déclenche en plus un **advisory**
(voir § 5, checks d'état boot `rules/checks.rules`) visible dans les deux formats.

## 7. Agrégation

S'applique **uniquement aux entrées gardées** (après classification et filtre de
plage horaire). Signature normalisée : chiffres, hex, GUID, IP, timestamps →
placeholders ; `spid###` → `spid`. Clé = message normalisé + source.
Occurrences ≥ 2 ⇒ `[×N] première–dernière heure : message représentatif`. Ordre
= première apparition. Désactivable `--no-aggregate`.

## 8. Rédaction (opt-in `--redact`)

Entités : noms de bases, comptes/logins (dont `DOMAINE\x`, gMSA), IP. Mapping
**cohérent et stable** sur toute la sortie ⇒ `DB_1`, `LOGIN_1`, `IP_1`. Légende
en fin, masquable via `--no-legend`. Le diagnostic reste possible (entités
distinguables) sans exposer les vrais noms. (Chemins/hostnames : hors périmètre
v2, extensible ensuite.)

## 9. CLI & formats de sortie

```text
errorlog-parse [flags] <path|->
  --from <iso>       garder entries >= datetime (ISO 8601, inclusif)
  --to <iso>         garder entries <= datetime (inclusif)
  --severity <n>     plancher de gravité (défaut 16)
  --format text|md   défaut text (STDOUT compact)
  --redact           pseudonymiser bases/logins/IP
  --no-legend        cacher la légende de rédaction
  --no-aggregate     ne pas collapser les répétitions
  --lang <codes>     restreindre les packs (défaut : tous)
  --rules <dir>      packs additionnels / de surcharge
  --no-summary       omettre le résumé d'instance
  -h, --help
```

Parsing via `flag` (stdlib, zéro dépendance).

Plage horaire : comparée au timestamp de l'entrée (première ligne). Bornes
inclusives. Formats acceptés : `2026-06-15` ou `2026-06-15T02:00:00`.

Sortie **texte** (défaut), sections :

```text
=== INSTANCE ===
SQL Server 2022 RTM-CU25 (16.0.4255.1) Standard — Windows Server 2025
RAM 64 Go · 8 cœurs · collation French_CI_AS · auth MIXED · UTC+2:00
Fenêtre : 2026-06-12 15:28 → 2026-06-16 09:12 (démarrage 2026-06-12 15:28)
=== ADVISORIES ===
[backup ×105575] … trace flag 3226 … → https://…
=== COUNTS ===
108713 entrées → 143 gardées, 108570 jetées
jetées : backup=105575, login-success=…, checkdb-ok=372, …
=== EVENTS ===
[×128] 2026-06-16 00:00–02:30  Login failed 'DOMAINE\SQL1$' from 172.16.70.81 (Erreur 18456, Gravité 14)
2026-06-13 00:00:00  Erreur 976, Gravité 14, État 1 : …
…
```

Sortie **Markdown** (`--format md`) : mêmes sections en titres + tableaux
(instance, catégories jetées, événements, légende de rédaction), plus informations additionnelles.

## 10. Stratégie de tests

- Unitaires par unité :
  - `decode` : fixture UTF-16LE avec BOM + CRLF → UTF-8/LF.
  - `parse` : continuations TAB / multi-lignes.
  - `rules` : entrées FR, EN et NBSP ; précédence severity/signal/noise/inconnu.
  - `aggregate` : rafale d'entrées identiques → 1 ligne comptée.
  - `redact` : mapping cohérent et stable.
  - `summary` : extraction des champs de boot (dont valeurs localisées activé/enabled).
  - `advice` : (a) seuil de catégorie + interpolation `{count}` ; (b) checks boot
    `present`/`absent`, y compris détection multilingue (IFI désactivé, SPN en
    échec, LPIM absent).
- **Test golden** : fixture UTF-16LE **synthétique** committée dans `testdata/`
  reproduisant la structure réelle (FR+EN, NBSP, backups, une vraie erreur, une
  rafale de logins) mais avec des **données fictives** — jamais les vrais noms
  de clients du fichier de 83 Mo. Comparaison au digest attendu.

## 11. Hors périmètre (YAGNI pour cette version)

- Sortie JSON (les deux rendus texte/MD suffisent au skill et à l'humain).
- Rédaction des chemins/hostnames.
- Détection automatique de la langue pour ne charger qu'un pack (on applique
  tout ; plus simple et robuste aux logs mixtes).
- Corrélation multi-fichiers avancée au-delà de la lecture ordonnée d'un dossier.

## 12. Règles Go & process qualité

- **Go idiomatique**, KISS : aucune complexité architecturale inutile pour un
  exécutable simple et compact. Zéro dépendance externe (stdlib uniquement ;
  packs de règles en `.rules` texte, pas de YAML).
- **golang-skills** appliqués pendant l'implémentation (go-code-review,
  go-linting, go-naming, go-error-handling, go-testing…) ; `golangci-lint`
  configuré et vert.
- **Code lisible, sans dette technique** ; `go vet` et `go test ./...` verts.
- **Revue obligatoire `/code-review` à effort xhigh (Opus)** en fin
  d'implémentation, avant merge. Les findings sont traités avant de clôturer.
