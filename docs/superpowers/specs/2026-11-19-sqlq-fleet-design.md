# sqlq sur un parc : avertissements, connexions instables, plusieurs profils

Spec du 19/11/2026, issue de `docs/spec-drafts/TASKS03.md` (fonctionnalités 1 à 3). Statut :
révisée le même jour après la relecture à cinq lecteurs
(`docs/reviews/2026-11-19-sqlq-fleet-design-panel/`), puis après une relecture adverse de cette
révision (`claude-rev2.md` dans le même dossier).

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
avant `emit`, sur tous les chemins qui suivent l'appel au résolveur, y compris la sortie de
l'échec d'enregistrement de `-save-query`. `Result.MarshalJSON` rend `warnings: []` quand il est
vide, comme `messages`, donc aussi dans la sortie de `fail`.

### 3.3 Rédaction

Les avertissements du résolveur citent des chemins locaux et des noms de profils, jamais un
secret ; ils ne passent pas par `redact`. Un avertissement de réessai (section 4) cite le texte
d'une erreur du pilote : il passe par `redact` comme toute erreur.

## 4. Classement des erreurs et réessai de connexion

### 4.1 Ce que le pilote rend, mesuré

Mesuré par les cinq relecteurs du 19/11 avec `go-mssqldb` v1.11.0 contre de faux serveurs TCP
(`docs/reviews/2026-11-19-sqlq-fleet-design-panel/`) :

- Le contexte passé à `db.Conn` ne borne que la numérotation TCP et la phase de réponse au login.
  La lecture de la réponse PRELOGIN et la poignée de main TLS ne le consultent pas
  (`mssql.go:462`, « using the provided context for dialing only ») : un serveur qui accepte puis
  se tait bloque `db.Conn` indéfiniment, et le binaire actuel avec `-timeout 5` ne rend jamais la
  main.
- Le `connection timeout` d'un profil (`connectTimeoutSec`) est un délai d'inactivité réarmé à
  chaque lecture et chaque écriture pendant toute la session (`net.go:23`), pas un délai de
  connexion. Un serveur qui envoie un octet toutes les 500 ms ne le déclenche jamais, et une
  requête dont le premier paquet tarde plus que lui échoue en plein lot. Aucun profil exporté par
  `registered-servers` ne le renseigne.
- `io.EOF`, `syscall.ECONNRESET`, `syscall.ECONNREFUSED`, `*net.DNSError` et `mssql.Error` se
  reconnaissent par `errors.Is` et `errors.As`. Une même coupure à l'acceptation donne `io.EOF`
  ou `ECONNRESET` selon le moment.
- Une coupure pendant la poignée de main TLS rend une chaîne plate,
  `TLS Handshake failed: cannot read handshake packet: <cause>`, formatée par `%v` : ni
  `errors.Is(io.EOF)` ni `ECONNRESET` ne la voient, et une fermeture propre y apparaît aussi
  comme `connection reset by peer`.
- Une réponse incohérente rend `invalid packet size, …` ou `invalid respones, expected packet type
  4, …`, sans type.
- Un délai dépassé pendant le préambule rend `Invalid TDS stream: did not get cancellation
  confirmation from the server (current response: context deadline exceeded)`, sans type.
- `database/sql` ne refait aucune tentative cachée pendant `db.Conn` dans tous ces cas : une seule
  connexion TCP par appel. En revanche, quand l'envoi d'un lot échoue, le pilote rend un
  `RetryableError` qui vaut `driver.ErrBadConn` (`mssql.go:784`, `:857`), et `database/sql`
  rejoue alors un `db.QueryContext` jusqu'à trois fois. `sqlq` y échappe aujourd'hui parce qu'il
  passe par un `*sql.Conn` réservé, dont `QueryContext` ne rejoue pas.

### 4.2 Une ouverture de connexion bornée

`execute` construit le connecteur par `mssql.NewConnector` (ou celui de `azuread` pour
`entra`) et `sql.OpenDB`, au lieu de `sql.Open` sur la DSN, pour lui donner un `Dialer` à lui.
Ce dialer garde la `net.Conn` TCP qu'il ouvre. Pendant `db.Conn`, un chien de garde
(`time.AfterFunc`) ferme cette connexion à l'échéance de la tentative ; il est arrêté dès que
`db.Conn` rend la main. Fermer la socket interrompt toute lecture en cours, quels que soient les
délais que le pilote pose ensuite, ce que ni le contexte ni `connection timeout` ne font.

- `-connect-timeout S`, défaut 15 s, de 1 à 120 : l'échéance de chaque tentative, de la
  numérotation à la fin du login. `db.Conn` reçoit aussi un contexte de même échéance : c'est lui
  qui borne les phases sans socket TCP ouverte, la numérotation elle-même, la requête UDP au
  navigateur SQL (instance nommée sans port) et l'obtention d'un jeton Entra, que fermer une socket
  ne bornerait pas. Le chien de garde ferme toutes les sockets que le dialer a ouvertes pendant la
  tentative, UDP et redirection de login comprises. Deux limites : une numérotation ne dure pas plus
  que le `dial timeout` du pilote (15 s) même si S est plus grand, et la résolution DNS
  (`net.LookupIP`, sans contexte) n'est pas bornée.
- Quand le chien de garde a fermé la connexion, l'erreur rendue par le pilote est quelconque
  (`use of closed network connection`, EOF) : `execute` sait que le chien de garde a tiré et
  classe `connect_timeout`, sans lire l'erreur.
- `connectTimeoutSec` garde son effet actuel (passé au pilote comme `connection timeout`) et sa
  documentation dit ce qu'il est : un délai d'inactivité par lecture pour toute la session.
- La DSN reçoit `disableretry=true` : un échec d'envoi du lot n'est plus jamais un
  `ErrBadConn`, et le « jamais après l'envoi » ne repose plus seulement sur l'usage d'un
  `*sql.Conn`. Un test l'épingle (section 6).
- Le délai `-timeout` du lot part une fois la connexion ouverte, dans un contexte distinct.

### 4.3 La catégorie

`SQLError` gagne un champ `category`, chaîne, toujours renseigné quand `error` ne vaut pas
`null`. Les codes de sortie ne changent pas : la catégorie précise ce que le code dit.

| `category` | Quand | Code |
|---|---|---|
| `usage` | argument, profil, configuration, secret introuvable | 1 |
| `refused` | garde d'écriture, `USE`, `OPENQUERY` et apparentés | 3 |
| `connect_transient` | ouverture coupée par le transport : `io.EOF`, `io.ErrUnexpectedEOF`, `ECONNRESET`, ou un message qui commence par `TLS Handshake failed: cannot read handshake packet: ` | 4 |
| `connect_timeout` | le chien de garde de la section 4.2 a fermé la connexion, ou une erreur de la chaîne est `os.ErrDeadlineExceeded`, `context.DeadlineExceeded` ou un `net.Error` dont `Timeout()` est vrai, avant la fin du login | 4 |
| `connect_failed` | autre échec d'ouverture : `ECONNREFUSED`, `*net.DNSError`, toute autre erreur TLS ou de certificat, réponse incohérente, erreur inclassable | 4 |
| `login_failed` | une `mssql.Error` 18456 dans la chaîne | 4 |
| `database_unavailable` | une erreur de la table `unavailableDatabase` (976, 978, 942, 952, 922, 927, 4060) dans la chaîne, au login ou pendant le lot | 4 au login, 2 pendant le lot |
| `query_timeout` | après l'ouverture, le contexte du lot a expiré (`ctx.Err() != nil`), quelle que soit l'erreur rendue, préambule compris | 2 |
| `transport` | après l'ouverture, une erreur de transport sans réponse du serveur | 2 |
| `sql_error` | toute autre erreur renvoyée par le serveur pendant le préambule ou le lot | 2 |
| `deadline` | mode multi-profils : l'échéance `-deadline` est passée (section 5.4) | 4 avant l'ouverture, 2 après, 5 pour un profil jamais lancé |

Ordre d'évaluation, le premier qui s'applique l'emporte : une `mssql.Error` dans la chaîne
(`database_unavailable` avant `login_failed`, comme `unavailableCause` aujourd'hui), qui prouve
que la socket n'est pas en cause même si le chien de garde a tiré pendant que le pilote la rendait ;
le chien de garde a tiré ; le contexte du lot a expiré ; puis les erreurs de transport et
de délai par type ; puis les deux messages listés ; puis `connect_failed` ou `sql_error` selon la
phase. Le texte n'est lu que pour les deux cas que le pilote ne type pas (préfixe TLS ci-dessus,
réponse incohérente classée par défaut), et chacun est épinglé par un test avec le message exact.

Le préfixe TLS couvre aussi deux cas qui ne sont pas des coupures : une instance trop ancienne qui
refuse la poignée de main TLS 1.2 de Go, et le délai par lecture d'un `connectTimeoutSec` expiré
pendant TLS. Le premier coûte un réessai inutile (20 s, aucune authentification en jeu), le second
est un délai classé comme coupure ; les deux sont réessayés, ce qui est acceptable.

`ECONNREFUSED` est `connect_failed` : une connexion refusée en direct est une instance arrêtée ou
un port faux, qui ne change pas en 20 s ; à travers ZPA, une instance injoignable donne `EOF`.

### 4.4 Le réessai

Seules les catégories `connect_transient` et `connect_timeout` sont réessayées, et seulement
pendant `db.Conn`. Jamais après l'envoi du préambule ou du lot, jamais une erreur renvoyée par le
serveur (`mssql.Error`), donc jamais 18456 ni 4060.

- `-connect-retries N`, défaut 1, de 0 à 3. 0 retrouve le comportement actuel.
- Délai avant chaque nouvelle tentative : 20 s. Le cas du 10/11 (une instance qui répond 20 s plus
  tard) le fixe.
- Chaque tentative échouée qui sera réessayée ajoute à `warnings`
  `connect attempt <i> of <n> failed (<category>): <message rédigé>; retrying in 20 s`.
- Le résultat gagne `connect_attempts`, entier : tentatives faites, 0 quand aucune n'a eu lieu.

Le risque qui reste, assumé et documenté : une coupure qui survient après l'envoi du paquet de
login (`Read: EOF`) se classe `connect_transient`, alors que le serveur a pu traiter le login. Si
le mot de passe est faux et que le tunnel perd la réponse 18456, l'appel fait deux tentatives
d'authentification au lieu d'une ; avec la seule relance que la section 4.5 permet à l'agent, qui
se fait avec `-connect-retries 0`, trois au plus. Le pilote ne dit pas si le login est parti, et le chiffrement
TLS empêche de le lire sur la socket. C'est pourquoi le défaut est 1 réessai et le maximum 3, et
non 2 et 5.

Pire durée d'un appel à un profil avec les défauts : 2 × 15 s + 20 s + `-timeout` (30 s) = 80 s.

### 4.5 Ce que l'agent en fait

Le skill `live-query` et `AGENTS.md` disent : sur `connect_transient` ou `connect_timeout`
après les réessais de `sqlq`, ne pas relancer en boucle ; une relance unique quelques minutes
plus tard, avec `-connect-retries 0`, est acceptable, puis rapporter. Sur `login_failed`, la règle actuelle (ne jamais
réessayer) reste. Sur `connect_failed`, ne pas relancer : rapporter.

## 5. Plusieurs profils dans un appel

### 5.1 Sélection

Deux options, exclusives entre elles et avec `-profile` :

- `-profiles-match <motif>` : les profils dont le nom correspond au motif `path.Match`. `*` ne
  franchit pas `/` : `'prd/*'` choisit les profils directement sous `prd/`, pas `prd/fr/x`
  (`'prd/*/*'` les choisit), et `'*'` ne choisit que les noms sans `/`, donc rien sur un parc
  exporté de `registered-servers`, où tous les noms ont un groupe. La correspondance respecte la
  casse. Un motif invalide est une erreur d'usage.
- `-all` : tous les profils du fichier.

Aucun profil choisi : erreur d'usage, code 1, sans lister les noms (même raison que pour
`-profile` absent : la liste des noms est la carte du parc).

Le piège du 18/11 (noms terminés par CR) venait de l'argument que la boucle shell construisait,
pas du fichier : l'export de `registered-servers` normalise les noms. Une valeur de `-profile` ou
de `-profiles-match` qui contient un caractère de contrôle est donc refusée (code 1) par un
message qui le dit (`contains a control character (CR?): strip it`), au lieu d'un « unknown
profile » ou d'une sélection vide. Dans le fichier, un nom écrit à la main qui contient un
caractère de contrôle ou commence ou finit par une espace est marqué `unusable`, avec ce motif ;
le fichier reste chargé.

### 5.2 L'aperçu et l'empreinte de la sélection

`-list-profiles` accepte `-profiles-match` et `-all` et ne liste alors que la sélection. Sa
sortie gagne `selection` (le motif, ou `*all*`) et `selection_fingerprint` : les 12 premiers
caractères hexadécimaux du SHA-256 de la liste triée des paires `nom` et `environment` des profils
choisis, séparées par des sauts de ligne.

L'exécution multi-profils accepte `-selection-fingerprint <valeur>`. Quand elle est donnée,
`sqlq` recalcule l'empreinte sur la sélection du moment et refuse (code 1, sans connexion) si
elle diffère. Quand la sélection contient un profil `"environment": "prod"`, l'option est
obligatoire : son absence est une erreur d'usage. Un fichier de profils régénéré entre l'aperçu
et l'exécution, qui ferait entrer un nouveau profil `prod` dans le motif ou changerait
l'environnement d'un nom, est ainsi refusé au lieu d'être exécuté sous un oui donné pour autre
chose.

### 5.3 Ce qui est refusé en mode multi-profils

Refusés avec le code 1 avant toute connexion : `-allow-write`, `-save-query`, `-plan`.

Le garde s'applique au texte une fois : un lot qui écrirait est refusé (code 3) même si tous les
profils choisis sont `readwrite`, puisque `-allow-write` est refusé.

`-saved` : seules les entrées `bundled`, `tsql-scripts` et `personal` de portée `_generic`
s'exécutent sur un parc. Le catalogue est chargé une fois sans profil, l'entrée y est résolue,
puis il est chargé pour chaque profil sélectionné : si l'un d'eux résout ce nom en une autre entrée
(une version enregistrée pour lui, puisque `-save-query` écrit toujours dans le dossier du profil)
ou ne le résout pas, l'appel est refusé (code 1) avec la consigne de déplacer l'entrée dans
`_generic` ou de lancer chaque profil séparément, sans nommer les profils. Les notes du catalogue (`messages`) vont dans `messages` au sommet du
résultat, une fois.

### 5.4 Exécution

- Les secrets de tous les profils choisis sont résolus d'abord, un profil après l'autre, par un
  seul résolveur, avant tout lancement. Chaque résolution reçoit son propre collecteur
  d'avertissements, qui vont dans le `warnings` de ce profil. Le résolveur n'est jamais appelé
  depuis deux goroutines : `Resolver.loadStore` n'est pas protégé, et sous la concurrence trois
  relecteurs ont mesuré de faux « credential store is unavailable » (363 sur 800 résolutions pour
  l'un) et des courses signalées par `-race`.
- Puis les connexions et les lots, en parallèle borné par `-parallel N`, défaut 4, de 1 à 16. Le
  18/11, 31 connexions simultanées ont produit 4 `EOF` à travers ZPA, et deux des instances
  concernées répondaient une fois relancées une par une. Le défaut de 4 n'est pas mesuré : c'est
  une borne prudente, à ajuster à l'usage.
- `-timeout`, `-connect-timeout`, `-connect-retries`, `-maxrows`, `-database`, `-dirty-reads`,
  `-statistics`, `-param` et les plafonds s'appliquent à chaque profil.
- `-deadline S`, défaut 100 s, de 10 à 3600 : durée totale de l'appel multi-profils. À
  l'échéance, les profils pas encore lancés ne le sont pas ; pour ceux en cours, le contexte du lot
  est annulé, ce qui fait envoyer au pilote une attention qui arrête l'instruction sur le serveur,
  et la socket n'est fermée qu'après 5 s de grâce (fermer d'abord laisserait une requête lourde
  tourner sur l'instance avec ses verrous). Le résultat est imprimé avec ces profils en
  `category: deadline`. Quand `-timeout` plus la pire ouverture dépasse `-deadline`, une ligne de
  `warnings` au sommet le dit avant le lancement. Le
  défaut reste sous la limite de 2 minutes d'un appel d'outil de Claude Code : un `sqlq` tué
  n'imprime rien, un `sqlq` arrivé à son échéance imprime ce qu'il sait.
- Le registre `verified` d'une entrée `-saved` est mis à jour une fois, après la fin de tous les
  profils, avec le premier profil dans l'ordre des noms qui a réussi.

### 5.5 Sortie

Un seul objet JSON :

```json
{"mode":"multi","selection":"prd/*","selection_fingerprint":"3f9a1c0b7d2e",
 "profiles_selected":4,"profiles_ok":3,"profiles_failed":1,"profiles_skipped":0,
 "profiles_columns_differ":1,"profiles_no_result_set":0,"deadline_reached":false,
 "elapsed_ms":4210,
 "columns":[{"name":"profile","type":"PROFILE"},{"name":"edition","type":"NVARCHAR"}],
 "rows":[{"profile":"prd/a","edition":"Standard"},{"profile":"prd/b","edition":"Enterprise"}],
 "rowcount":2,"truncated":false,
 "profiles":[
   {"profile":"prd/a","server":"…","database":"…","elapsed_ms":120,"connect_attempts":1,
    "rowcount":1,"truncated":false,"incomplete":false,"in_rows":true,"columns_differ":false,
    "more_results":[],"messages":[],"warnings":[],"error":null,"exit_code":0},
   …],
 "messages":[],"warnings":[],"error":null}
```

- Le schéma de référence est celui du premier jeu de résultats partagé par le plus grand nombre de
  profils réussis (mêmes noms, types et ordre de colonnes), à égalité le premier dans l'ordre des
  noms. Un profil réussi sans aucun jeu de résultats ne compte pas et n'est jamais la référence :
  il est compté dans `profiles_no_result_set`. Un jeu vide (des colonnes, zéro ligne) compte.
- `rows` réunit les lignes des profils réussis dont le premier jeu a le schéma de référence, dans
  l'ordre des noms puis l'ordre rendu par le serveur. Chaque ligne reçoit le nom du profil sous une
  clé ajoutée. L'ordre des clés d'un objet JSON n'est pas garanti (`encoding/json` trie les clés
  d'une `map`) : la spec ne promet aucune position.
- La clé ajoutée est `profile`, sauf si une colonne d'un premier jeu de résultats de n'importe quel
  profil s'appelle `profile` sans tenir compte de la casse : la clé est alors `sqlq_profile` pour
  tout l'appel, et `columns` le dit. Le choix se fait une fois tous les profils terminés, avant
  l'impression. Elle n'écrase jamais une valeur du serveur.
- `profiles_columns_differ` compte les profils réussis dont le premier jeu a un autre schéma :
  leurs lignes sont dans `profiles[].rows` avec leurs `columns`, et `in_rows` vaut `false`.
- `rowcount` du sommet est la somme des `rowcount` réels des profils versés dans `rows` (le total
  envoyé par les serveurs, pas le nombre de lignes gardées) ; `truncated` du sommet est vrai si l'un
  d'eux a `truncated` ou `incomplete` vrai. Même sens que dans un appel simple.
- Un profil qui échoue après avoir rendu des lignes ne verse rien dans `rows` : ses lignes restent
  dans `profiles[].rows` et il compte dans `profiles_failed`.
- `profiles_skipped` compte les profils `unusable` sur cette machine (DPAPI hors Windows, nom
  invalide) : présents dans `profiles[]` avec `category: usage` et `exit_code: 1`, jamais tentés,
  sans effet sur le code de sortie.
- `profiles[]` donne, pour chaque profil sélectionné et dans l'ordre des noms, ce qu'un appel
  simple aurait donné, `rows` et `columns` en moins quand `in_rows` est vrai. `exit_code` est le
  code qu'aurait rendu l'appel simple.
- `error` du sommet n'est renseigné que pour un échec qui empêche tout lancement (usage, refus,
  empreinte) ; il a la forme habituelle, et `profiles` vaut `[]`.

La forme diffère de l'appel simple : `mode: "multi"` le dit. C'est un second format à lire,
assumé : le résultat d'un parc n'a pas la forme du résultat d'une instance, et l'aplatir dans
`Result` ferait perdre l'état de chaque profil.

### 5.6 Codes de sortie

| Code | Mode multi-profils |
|---|---|
| 0 | tous les profils tentés ont réussi (les profils `skipped` ne comptent pas) |
| 1 | usage ou empreinte, avant tout lancement ; ou aucun profil tenté, parce que tous les profils choisis sont `skipped` |
| 3 | refus du garde, avant tout lancement |
| 5 | au moins un profil tenté a échoué, ou l'échéance `-deadline` est atteinte ; le détail est dans `profiles[].exit_code` |

Le code 5 est nouveau et réservé au mode multi-profils.

### 5.7 Ce que l'agent en fait

Dans le skill `live-query` et `AGENTS.md` :

- Avant un appel multi-profils, lancer `-list-profiles` avec la même sélection, juste avant. Si
  elle contient un profil `"environment": "prod"`, montrer la liste à l'utilisateur et attendre un
  oui ; puis lancer l'appel avec `-selection-fingerprint` égal à l'empreinte montrée. Un refus
  d'empreinte veut dire que la sélection a changé : refaire l'aperçu et redemander, jamais retirer
  l'option.
- Pour une entrée `-saved`, les règles d'un appel simple valent pour tout le parc : lire son
  en-tête, annoncer un `verified: null`, et pour une entrée `heavy: true` sur une sélection qui
  contient un profil `prod`, annoncer le coût et attendre un oui distinct.
- Avant de lire les lignes, lire `profiles_failed`, `profiles_columns_differ`,
  `profiles_no_result_set`, `profiles_skipped`, `truncated` et `deadline_reached`. Une
  instance absente de `rows` est dans l'un de ces cas, jamais « zéro ligne » sans le vérifier dans
  `profiles[]`. Une conclusion sur le parc dit combien d'instances elle couvre.
- Ne pas relancer en boucle les profils en échec ; une seule relance ciblée par
  `-profiles-match` ou `-profile`, puis rapporter.

## 6. Tests

Tous dans `tools/cmd/sqlq` ou `tools/internal/sqlq`, lancés par `go test ./...` depuis `tools`.
Chaque faux serveur est un `net.Listen` sur `127.0.0.1:0` démarré par le test et fermé par
`t.Cleanup`. Les délais de réessai et l'échéance par défaut sont des variables du paquet que les
tests réduisent.

- Avertissements : un résolveur qui avertit donne un `warnings` non vide et rien sur stderr, en
  succès, quand la résolution échoue ensuite, et sur la sortie de l'échec d'enregistrement de
  `-save-query` ; `warnings` vaut `[]` sans avertissement, y compris dans la sortie de `fail`.
- Ouverture bornée : un faux serveur qui accepte et se tait, un autre qui répond un PRELOGIN valide
  puis se tait, un troisième qui envoie un octet toutes les 500 ms ; avec `-connect-timeout 2` et
  `-connect-retries 0`, chaque appel rend la main entre min(2, `connectTimeoutSec`) et 4 s avec
  `connect_timeout` ; le serveur au compte-gouttes, que le délai par lecture ne voit jamais, finit à
  2 s même avec `connectTimeoutSec` à 1. Le test se borne lui-même : un appel encore en cours après
  6 s le fait échouer au lieu de bloquer la suite.
- Classement : chaque message de la section 4.1 produit par un faux serveur, pas construit à la
  main, avec sa catégorie attendue ; plus une `mssql.Error` 18456, 4060 suivi de 18456, et un délai
  du lot pendant le préambule (`query_timeout`).
- Réessai : un faux serveur qui ferme la première connexion à l'acceptation puis se tait ; avec
  `-connect-retries 1` et `-connect-timeout 2`, deux tentatives, `connect_attempts: 2`, une ligne
  de réessai dans `warnings`, catégorie finale `connect_timeout`. Avec `-connect-retries 0`, une
  seule tentative.
- Non-réessai : un faux serveur qui rend une erreur 18456 au login reçoit une seule connexion TCP,
  quel que soit `-connect-retries`.
- `disableretry` : la DSN construite contient `disableretry=true`. Pas de test de comportement : à
  travers le `*sql.Conn` de `sqlq`, un faux serveur reçoit une seule connexion avec ou sans le
  paramètre, si bien qu'un tel test passerait même sans lui (mesuré par la relecture de la
  révision).
- Classement : une `mssql.Error` 18456 avec le chien de garde tiré reste `login_failed`.
- Multi-profils, sans serveur : sélection par motif (dont des noms à un et deux `/`), `-all`, motif
  invalide, aucun profil choisi, profils `unusable` (DPAPI, nom avec CR), empreinte calculée et
  refusée après modification du fichier, empreinte exigée en présence d'un `prod`, refus de
  `-allow-write`, `-save-query`, `-plan`, et d'une entrée `-saved` à deux variantes.
- Multi-profils, avec de faux serveurs : quatre profils dont un muet, `-deadline` à 3 s, donnent
  `deadline_reached: true`, le code 5 et une sortie JSON complète ; la résolution des secrets se
  fait une fois par profil, dans l'ordre, sans concurrence (test sous `-race`).
- Multi-profils, avec le labo (tests d'intégration existants, `integration_test.go`, sautés sans
  instance) : trois profils vers la même instance, dont un en échec de login par un mauvais secret,
  donnent `profiles_ok: 2`, `profiles_failed: 1`, le code 5, des lignes marquées `profile` ; un
  lot dont le premier profil par nom ne rend aucun jeu ne vide pas `rows` ; un jeu dont les
  colonnes diffèrent selon `-database` donne `profiles_columns_differ: 1`.

## 7. Documentation à mettre à jour

- `AGENTS.md` : la phrase « there is never a second format to parse » devient vraie pour un
  appel à un profil et nomme la forme multi-profils ; la liste des codes de sortie gagne le 5 ;
  les règles des sections 4.5 et 5.7.
- `plugins/sqlserver-toolkit/skills/live-query/SKILL.md` et `plugins/sqlserver-toolkit/README.md` :
  les nouvelles options, `warnings`, `category`, la forme multi-profils, les mêmes règles.

## 8. Hors périmètre

- Sortie tabulaire (`-format tsv|md`), lectures répétées et différence : fonctionnalités 4 et 5,
  spec suivante.
- Liste blanche de procédures système : fonctionnalité 6, spec séparée.
- Réessai d'un lot après une coupure : jamais.
- Un plafond de lignes global sur le mode multi-profils : `-maxrows` par profil suffit tant que
  le parc tient en quelques dizaines d'instances.
- Rendre `Resolver` sûr pour la concurrence : la résolution séquentielle avant lancement suffit.
- Distinguer une coupure avant et après l'envoi du login : le pilote ne l'expose pas (section 4.4).
