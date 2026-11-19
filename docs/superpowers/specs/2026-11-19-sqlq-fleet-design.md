# sqlq sur un parc : avertissements, connexions instables, plusieurs profils

Spec du 19/11/2026, issue de `docs/spec-drafts/TASKS03.md` (fonctionnalités 1 à 3). Statut :
à relire avant le plan d'implémentation.

## 1. Le besoin

Une semaine de travail sur un parc de 31 instances, toutes en `readonly`, joignables par Zscaler
Private Access (ZPA), a montré trois manques dans `sqlq` :

- un avertissement du résolveur de secrets (`RegSrvr17.xml` a changé depuis l'import) part sur
  stderr ; dès que l'appelant redirige stderr vers stdout (`2>&1`), la sortie n'est plus du JSON ;
- une connexion qui échoue sur `EOF` est le plus souvent un incident du tunnel ZPA et non de
  l'instance (une instance répondait 20 s plus tard), mais rien ne distingue cet échec d'un échec
  durable, et `sqlq` ne réessaie jamais ;
- une même question posée à tout le parc (édition, vCPU, checkpoint, purge Ola) passe par une
  boucle shell, qui a rencontré le 18/11 des noms de profils terminés par CR, du JSON mêlé
  d'avertissements, et 4 échecs `EOF` sur 31 connexions ouvertes en même temps (deux de ces
  instances répondaient, relancées une par une).

Ces trois points se tiennent : le mode multi-profils a besoin d'avertissements dans le JSON et
d'un réessai de connexion sûr, sans quoi il reproduit à l'intérieur de `sqlq` les défauts de la
boucle shell.

## 2. Ce qui ne change pas

- Un appel imprime un seul objet JSON sur stdout, en succès comme en échec.
- Les codes de sortie 0 à 4 gardent leur sens pour un appel à un profil.
- Le garde d'écriture, le plafond de lignes, la rédaction des erreurs.
- `sqlq` ne réessaie jamais après l'envoi du lot. Un lot envoyé peut avoir été exécuté, même
  s'il est en lecture : le réessai ne concerne que l'ouverture de la connexion.
- Un échec de login (18456) n'est jamais réessayé, ni par `sqlq` ni par l'agent.

## 3. Les avertissements dans le JSON

### 3.1 Le champ

`Result` gagne un champ `warnings`, tableau de chaînes, toujours présent (`[]` quand il n'y a
rien), comme `messages`.

- `messages` reste ce que le serveur a dit (`PRINT`, `RAISERROR` de faible gravité, erreurs
  suivantes) plus les notes du catalogue et du registre, comme aujourd'hui.
- `warnings` reçoit ce que `sqlq` lui-même a à signaler sur son environnement local :
  aujourd'hui les trois avertissements du résolveur (`internal/sqlq/secret.go`, appels à
  `r.warn`), demain les réessais de connexion (section 4).

Les notes du catalogue et du registre restent dans `messages` : la documentation du skill
`live-query` les y annonce, et les déplacer casserait un contrat publié pour un gain de
rangement.

### 3.2 stderr

`sqlq` n'écrit plus rien sur stderr, sauf l'échec d'écriture du JSON lui-même
(`emit`, `main.go`), qui n'a pas d'autre canal. La fonction passée à `sqlq.NewResolver` dans
`run` accumule les avertissements dans une tranche au lieu d'appeler `fmt.Fprintln(os.Stderr,
…)`, et `run` les copie dans `result.Warnings`.

Un avertissement émis avant l'échec du résolveur (secret introuvable, code 1) doit figurer dans
l'objet d'échec : `execute` rend son `Result` d'échec, et `run` y ajoute les avertissements
avant `emit`, sur tous les chemins qui suivent l'appel au résolveur.

### 3.3 Rédaction

Les avertissements du résolveur citent des chemins locaux et des noms de profils, jamais un
secret ; ils ne passent pas par `redact`. Un avertissement de réessai (section 4) cite le texte
d'une erreur du pilote : il passe par `redact` comme toute erreur.

## 4. Classement des erreurs et réessai de connexion

### 4.1 La catégorie

`SQLError` gagne un champ `category`, chaîne, toujours renseigné quand `error` ne vaut pas
`null` :

| `category` | Quand | Code de sortie |
|---|---|---|
| `usage` | argument, profil, configuration, secret introuvable | 1 |
| `refused` | garde d'écriture, `USE`, `OPENQUERY` et apparentés | 3 |
| `connect_transient` | ouverture de connexion coupée par le transport : `io.EOF`, `io.ErrUnexpectedEOF`, connexion réinitialisée (`ECONNRESET`), connexion refusée (`ECONNREFUSED`) | 4 |
| `connect_timeout` | délai de connexion dépassé (`connection timeout` du profil, ou le contexte) avant la fin du login | 4 |
| `connect_failed` | autre échec d'ouverture : nom introuvable (DNS), erreur TLS ou de certificat, erreur inclassable | 4 |
| `login_failed` | 18456 | 4 |
| `database_unavailable` | une erreur de la table `unavailableDatabase` (976, 978, 942, 952, 922, 927, 4060), au login ou pendant le lot | 4 au login, 2 pendant le lot |
| `query_timeout` | le délai `-timeout` dépassé après l'envoi du lot | 2 |
| `transport` | connexion perdue après l'envoi du lot | 2 |
| `sql_error` | toute autre erreur renvoyée par le serveur pendant le préambule ou le lot | 2 |

Le classement se fait sur la chaîne d'erreurs entière (`errors.Is`, `errors.As`,
`mssql.Error.All`), pas sur le texte du message, sauf là où le pilote ne donne pas de type : le
cas est alors listé dans un test avec le message exact observé. L'ordre de priorité est celui du
tableau en remontant : `database_unavailable` et `login_failed` l'emportent sur une erreur de
transport de la même chaîne, comme `unavailableCause` l'emporte déjà sur 18456.

Les codes de sortie ne changent pas : la catégorie précise ce que le code dit, elle ne le
remplace pas.

### 4.2 Le réessai

Seules les catégories `connect_transient` et `connect_timeout` sont réessayées, et seulement
pendant `db.Conn` (l'ouverture de la connexion, login compris). Rien d'autre :

- pas après l'envoi du préambule ou du lot ;
- jamais une erreur que le serveur a renvoyée (`mssql.Error`), donc jamais 18456 ni 4060 ;
- jamais `connect_failed` : un nom introuvable ou un certificat refusé ne guérit pas en 20 s.

Paramètres :

- `-connect-retries N`, défaut 2, de 0 à 5. 0 retrouve le comportement actuel.
- Délais entre tentatives : 5 s puis 20 s, puis 20 s pour les suivantes. Le cas du 10/11 (une
  instance qui répond 20 s plus tard) fixe le second.
- Chaque tentative a son propre délai de connexion (celui du profil). Le délai `-timeout` du lot
  ne commence qu'une fois la connexion ouverte : aujourd'hui un seul contexte couvre les deux, il
  est scindé en un contexte de connexion par tentative et un contexte de lot.

Chaque tentative échouée qui sera réessayée ajoute à `warnings` une ligne
`connect attempt <i> of <n> failed (<category>): <message rédigé>; retrying in <s> s`. Le
résultat gagne `connect_attempts`, entier, nombre de tentatives faites (1 sans réessai, 0 quand
aucune tentative n'a eu lieu, par exemple sur une erreur d'usage).

### 4.3 Ce que l'agent en fait

Le skill `live-query` et `AGENTS.md` disent : sur `connect_transient` ou `connect_timeout`
après les réessais de `sqlq`, ne pas relancer en boucle ; une relance unique quelques minutes
plus tard est acceptable, puis rapporter. Sur `login_failed`, la règle actuelle (ne jamais
réessayer) reste.

## 5. Plusieurs profils dans un appel

### 5.1 Sélection

Deux options, exclusives entre elles et avec `-profile` :

- `-profiles-match <motif>` : les profils dont le nom correspond au motif `path.Match` (`*` ne
  franchit pas `/` ; `'prd/*'` choisit un groupe). Un motif invalide est une erreur d'usage.
- `-all` : tous les profils du fichier.

Un profil `unusable` sur cette machine (DPAPI hors Windows) n'est pas tenté : il apparaît dans
le résultat avec `category: usage` et son motif.

Aucun profil choisi : erreur d'usage, code 1, sans lister les noms (même raison que pour
`-profile` absent : la liste des noms est la carte du parc).

`-list-profiles` accepte `-profiles-match` et `-all` pour montrer la sélection avant de la
lancer. C'est ce que l'agent montre à l'utilisateur avant un appel qui touche un profil `prod`.

### 5.2 Ce qui est refusé en mode multi-profils

Refusés avec le code 1 avant toute connexion : `-allow-write`, `-save-query`, `-plan`.
Écrire sur un parc d'un seul geste n'a pas de cas d'usage dans ce dépôt, et un plan par instance
dépasse ce que le contexte d'un agent peut lire.

Le garde s'applique au texte une fois, avec le mode de chaque profil : un lot qui écrirait est
refusé (code 3) même si tous les profils choisis sont `readwrite`, puisque `-allow-write` est
refusé.

`-saved` est accepté pour une entrée `bundled`, `tsql-scripts` ou `personal` de portée
`_generic`. Une entrée enregistrée pour un profil (`profiles/<profil>/`) est refusée : elle
n'existe que pour ce profil. Le catalogue est chargé une fois, sans profil.

### 5.3 Exécution

- Parallélisme borné par `-parallel N`, défaut 4, de 1 à 16. Le 18/11, 31 connexions
  simultanées ont produit 4 `EOF` à travers ZPA, et deux des instances concernées répondaient une
  fois relancées une par une. Le défaut de 4 n'est pas mesuré : c'est une borne prudente, à
  ajuster à l'usage. Chaque profil passe par le même chemin qu'un appel simple (`execute`), réessai
  compris.
- `-timeout`, `-maxrows`, `-database`, `-dirty-reads`, `-statistics`, `-param` et les plafonds
  s'appliquent à chaque profil, pas au total.
- Le résolveur de secrets est créé une fois ; ses avertissements vont dans le `warnings` du
  profil concerné quand il les émet pendant la résolution de ce profil.
- Le registre `verified` d'une entrée `-saved` est mis à jour une fois, avec le premier profil
  dans l'ordre des noms qui a réussi.

### 5.4 Sortie

Un seul objet JSON :

```json
{"mode":"multi","selection":"prd/*","profiles_selected":3,"profiles_ok":2,"profiles_failed":1,
 "elapsed_ms":4210,
 "columns":[{"name":"profile","type":"PROFILE"},{"name":"edition","type":"NVARCHAR"}],
 "rows":[{"profile":"prd/a","edition":"Standard"},{"profile":"prd/b","edition":"Enterprise"}],
 "rowcount":2,
 "profiles":[
   {"profile":"prd/a","server":"…","database":"…","elapsed_ms":120,"connect_attempts":1,
    "rowcount":1,"truncated":false,"incomplete":false,"columns_differ":false,
    "more_results":[],"messages":[],"warnings":[],"error":null,"exit_code":0},
   …],
 "warnings":[],"error":null}
```

- `rows` réunit les lignes du premier jeu de résultats de chaque profil réussi, chacune avec une
  clé `profile` en tête, dans l'ordre des noms de profils puis l'ordre rendu par le serveur.
- `columns` est `profile` (type `PROFILE`, qui n'est pas un type SQL) suivi des colonnes du
  premier profil réussi dans l'ordre des noms. Un profil dont le premier jeu n'a pas les mêmes noms
  et types de colonnes, dans le même ordre, ne verse pas ses lignes dans `rows` :
  `columns_differ: true`, et ses lignes vont dans `profiles[].rows` avec ses `columns`.
- Un jeu de résultats qui contient déjà une colonne `profile` : la clé ajoutée est
  `sqlq_profile`, et `columns` le dit. Elle n'écrase jamais une valeur du serveur.
- `profiles[]` donne, pour chaque profil sélectionné et dans l'ordre des noms, ce qu'un appel
  simple aurait donné, moins `rows` et `columns` quand elles sont dans `rows`. `exit_code` est
  le code qu'aurait rendu l'appel simple.
- `truncated` et `rowcount` restent par profil ; le `rowcount` du haut compte les lignes de
  `rows`.
- `error` du haut n'est renseigné que pour un échec qui empêche tout le lancement (usage,
  refus) ; il a alors la forme habituelle, et `profiles` vaut `[]`.

La forme diffère de l'appel simple : `mode: "multi"` permet à l'appelant de le savoir sans
deviner. C'est un second format à lire, assumé : le résultat d'un parc n'a pas la forme du
résultat d'une instance, et l'aplatir dans `Result` ferait perdre l'état de chaque profil.

### 5.5 Codes de sortie

| Code | Mode multi-profils |
|---|---|
| 0 | tous les profils ont réussi |
| 1 | usage, avant tout lancement |
| 3 | refus du garde, avant tout lancement |
| 5 | au moins un profil a échoué ; le détail est dans `profiles[].exit_code` |

Le code 5 est nouveau et réservé au mode multi-profils.

### 5.6 Ce que l'agent en fait

Dans le skill `live-query` et `AGENTS.md` :

- Avant un appel multi-profils, lancer `-list-profiles` avec la même sélection. Si elle contient
  un profil `"environment": "prod"`, montrer la liste à l'utilisateur et attendre un oui, comme
  pour un profil seul ; le oui couvre cette sélection, pas une sélection élargie ensuite.
- Lire `profiles_failed` avant les lignes : une instance absente des lignes n'a pas répondu, elle
  n'a pas « zéro ligne ».
- Ne pas relancer en boucle les profils en échec ; une seule relance ciblée par
  `-profiles-match` ou `-profile`, puis rapporter.

## 6. Tests

Tous dans `tools/cmd/sqlq` ou `tools/internal/sqlq`, lancés par `go test ./...` depuis `tools`.

- Avertissements : un résolveur qui avertit donne un `warnings` non vide et rien sur stderr,
  en succès et quand la résolution échoue ensuite ; `warnings` vaut `[]` sans avertissement.
- Classement : une table d'erreurs construites (`io.EOF`, `syscall.ECONNRESET` enveloppé dans
  `*net.OpError`, `context.DeadlineExceeded`, `*net.DNSError`, erreur TLS, `mssql.Error` 18456,
  4060 suivi de 18456, 1205 pendant le lot) et la catégorie attendue de chacune.
- Réessai : un faux serveur TCP local (`net.Listen` sur `127.0.0.1:0`) qui ferme les deux
  premières connexions dès l'acceptation puis ne répond plus ; avec `-connect-retries 2` et des
  délais réduits par une variable du paquet réservée aux tests, `sqlq` fait 3 tentatives, rend
  `connect_attempts: 3`, deux lignes de réessai dans `warnings` et la catégorie de la dernière.
  Avec `-connect-retries 0`, une seule tentative.
- Non-réessai : une `mssql.Error` 18456 au login donne une seule tentative, quel que soit
  `-connect-retries`.
- Multi-profils, sans serveur : sélection par motif (dont un nom avec `/`), `-all`, motif
  invalide, aucun profil choisi, profils `unusable`, refus de `-allow-write`, `-save-query`,
  `-plan`, d'une entrée `-saved` de portée profil.
- Multi-profils, avec le labo (tests d'intégration existants, `integration_test.go`, sautés sans
  instance) : trois profils vers la même instance, dont un en échec de login simulé par un
  mauvais secret, donnent `profiles_ok: 2`, `profiles_failed: 1`, le code 5, et des lignes
  marquées `profile` ; un jeu dont les colonnes diffèrent selon `-database` donne
  `columns_differ`.

## 7. Hors périmètre

- Sortie tabulaire (`-format tsv|md`), lectures répétées et différence : fonctionnalités 4 et 5,
  spec suivante.
- Liste blanche de procédures système : fonctionnalité 6, spec séparée.
- Réessai d'un lot après une coupure : jamais.
- Un plafond de lignes global sur le mode multi-profils : `-maxrows` par profil suffit tant que
  le parc tient en quelques dizaines d'instances.
