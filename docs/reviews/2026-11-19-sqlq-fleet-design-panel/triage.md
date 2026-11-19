# Tri de la relecture de la spec sqlq sur un parc

Spec : `docs/superpowers/specs/2026-11-19-sqlq-fleet-design.md`, relue au commit 25139a4 par cinq
lecteurs le 19/11/2026 (Vibe Mistral Large en directif, Vibe GLM en neutre, codex deux fois, un
sous-agent Claude en neutre). Les cinq ont exécuté le vrai pilote `go-mssqldb` v1.11.0 contre de
faux serveurs TCP. Aucun n'avait d'instance SQL Server.

## Retenu et corrigé dans la spec

| Trouvaille | Lecteurs | Correction |
|---|---|---|
| Le contexte ne borne ni le PRELOGIN ni TLS : un serveur muet bloque `db.Conn` sans fin, le binaire actuel compris | les cinq | 4.1, 4.2 : chien de garde qui ferme la socket, `-connect-timeout` (15 s) |
| `connection timeout` est un délai par lecture pour toute la session, déjoué par un serveur qui envoie au compte-gouttes | Claude, Mistral, GLM | 4.1, 4.2 : documenté, plus utilisé comme borne de connexion |
| Un délai de connexion arrive en `os.ErrDeadlineExceeded`, pas `context.DeadlineExceeded` | les cinq | 4.3 : les deux, plus `net.Error.Timeout()` |
| Une coupure pendant TLS rend un texte plat (`%v`), classé à tort `connect_failed` | Claude, Mistral, GLM | 4.3 : préfixe exact en `connect_transient`, épinglé par un test |
| Un délai pendant le préambule rend une erreur sans type | Claude | 4.3 : `ctx.Err()` lu avant le classement |
| `RetryableError` vaut `ErrBadConn` après un envoi raté ; le « jamais après l'envoi » ne tenait que par l'usage de `*sql.Conn` | Mistral, codex directif | 4.2 : `disableretry=true` dans la DSN, et un test |
| Le résolveur partagé a une course (`loadStore`), avec de faux « store unavailable » | Claude, Mistral, GLM, codex directif | 5.4 : résolution séquentielle avant lancement, collecteur par profil |
| `columns_differ` et un premier profil sans jeu de résultats vident `rows` sans signal | les cinq | 5.5 : schéma majoritaire, compteurs au sommet, règle d'agent |
| `rowcount` du sommet change de sens, pas de `truncated` au sommet | Claude, Mistral | 5.5 |
| Pas de durée totale : un `sqlq` tué par la limite de l'outil n'imprime rien | Claude, Mistral, codex directif | 5.4 : `-deadline` (100 s), sortie partielle |
| Le oui donné sur un aperçu ne lie pas l'exécution, le fichier peut changer entre les deux | codex directif, codex neutre, Mistral | 5.2 : empreinte de sélection, obligatoire avec un `prod` |
| Une coupure après l'envoi du login peut répéter une authentification | Claude | 4.4 : risque documenté, défaut ramené à 1 réessai, maximum 3 |
| `-saved` de portée profil invisible au catalogue chargé sans profil : la même entrée exécuterait un autre SQL qu'en appel simple | GLM | 5.3 : catalogue résolu par profil, refus si les variantes diffèrent |
| Noms de profils terminés par CR, cités comme motif puis jamais traités | GLM, codex directif, Mistral | 5.1 : profil `unusable` au chargement |
| Priorité contradictoire entre les catégories | GLM | 4.3 : un ordre d'évaluation unique |
| `profile` « en tête » impossible avec une `map` ; collision sensible à la casse | Claude | 5.5 |
| Profils `unusable` : code 1 ou 5 ambigu | GLM, Mistral | 5.5, 5.6 : `profiles_skipped`, sans effet sur le code |
| Notes du catalogue sans place dans la sortie multi | GLM | 5.3 : `messages` au sommet |
| Règles `heavy` et `verified: null` non reprises pour un `-saved` de parc | codex neutre, Claude | 5.7 |
| `'*'` ne choisit rien sur un parc groupé, `'prd/*'` saute `prd/fr/x` | Mistral, Claude | 5.1 : documenté |
| Registre `verified` écrit « au premier qui a réussi » non déterministe | Mistral | 5.4 : après la fin de tous les profils |
| Un `fail` sans `warnings`, l'échec de `-save-query` oublié | Mistral | 3.2 |
| Le test de réessai de la section 6 bloquait ou ne testait rien | Claude, GLM, Mistral | 6 réécrit |

## Écarté, avec la raison

- `ECONNREFUSED` réessayé (spec initiale) : passé en `connect_failed` plutôt que gardé. À travers
  ZPA une instance injoignable donne `EOF`, et un refus direct ne guérit pas en 20 s (remarque de
  GLM sur le coût d'un refus devenu 25 s).
- Rendre `Resolver` sûr pour la concurrence (mutex) : la résolution séquentielle suffit et règle
  aussi l'attribution des avertissements.
- Détecter si le paquet de login est parti, pour ne réessayer qu'avant : impossible à travers TLS
  sans réécrire le transport du pilote ; le risque est borné et documenté à la place.

## Vérifié par moi

- Le chien de garde : un `db.Conn` vers un serveur muet, avec un `Dialer` qui garde la socket et
  la ferme à 2 s, rend la main en 2,00 s (`use of closed network connection`) ; sans lui il reste
  bloqué. Avec `connection timeout=1`, le délai par lecture tire d'abord (1,00 s, `i/o timeout`).
- `disableretry` est un paramètre de la DSN de la v1.11.0 (`msdsn/conn_str.go:83`).
- `azuread.NewConnector` rend un `*mssql.Connector` : le même `Dialer` vaut pour `entra`.
- `mssql.go:462` dit lui-même que le contexte ne sert qu'à la numérotation.
