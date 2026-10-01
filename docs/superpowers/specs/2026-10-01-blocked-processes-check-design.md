# Design — contrôle de la trace `blocked_process_report` (requête livrée)

Date : 2026-10-01
Statut : révisé après la relecture adversariale Codex
(`2026-10-01-blocked-processes-check-design-codex.md`), en attente de relecture avant plan
d'implémentation. Le traitement de chaque constat est en §9.
Portée : partie A d'une tâche découpée en deux. La partie B (la pose, qui écrit) est une tâche
séparée, à la route complète ; elle réutilisera cette requête comme contrôle de sortie.

## 1. Le problème

Une trace des processus bloqués tient à plusieurs choses, et il suffit qu'une seule manque pour
qu'elle ne produise rien, sans aucune erreur :

1. le paramètre serveur `blocked process threshold (s)` est supérieur à 0 **en vigueur**
   (`value_in_use`), sans quoi l'événement `blocked_process_report` n'est jamais levé ;
2. une session d'événements étendus capture cet événement ;
3. cette session a `STARTUP_STATE = ON`, sans quoi elle ne redémarre pas avec l'instance (sa
   définition reste, la collecte s'arrête) ;
4. cette session tourne ;
5. elle écrit dans une cible qui survit à l'incident.

Posée à la main sur plusieurs instances en production, la trace a manqué une étape à chaque
fois : `STARTUP_STATE` resté à OFF sur deux instances pendant des jours, une session définie
mais jamais démarrée, et un seuil à 0 sur six instances, donc aucun rapport quelle que soit la
session. Le script de pose d'origine écrit lui-même `STARTUP_STATE = OFF`.

Une requête de contrôle passée sur toutes les instances a trouvé les trois oublis en une passe.
C'est cette requête qu'on livre ici, en lecture seule, pour qu'elle soit la même à chaque fois.

## 2. Ce que `OK` veut dire

**`OK` = au moment du contrôle, tout ce qui est nécessaire pour que cette instance écrive les
rapports de processus bloqués dans un fichier est en place et en marche, et le sera encore après
un redémarrage** : seuil en vigueur, session qui capture l'événement sans filtre, démarrée, à
démarrage automatique, avec une cible `event_file` définie et active. C'est une observation
ponctuelle et **non atomique** (les vues sont lues l'une après l'autre ; si des sessions
changent pendant le contrôle, on le relance), locale à l'instance.

`OK` ne dit **pas** :

- qu'un rapport a été produit ou écrit : aucun fichier n'est lu, et l'espace ou les droits du
  répertoire de la cible ne sont pas vérifiés ;
- que tous les blocages seront rapportés. Le seuil fixe une durée minimale ; le moniteur tourne
  toutes les cinq secondes environ et au mieux (documentation de l'option
  `blocked process threshold`). La valeur effective est affichée, la requête ne juge pas si
  elle convient ;
- que le partenaire d'un groupe de disponibilité est tracé (§6) ;
- qu'une session à durée limitée (`MAX_DURATION`, SQL Server 2025 et Managed Instance à jour)
  tournera encore demain. La colonne n'existe pas sur les versions antérieures et la requête
  doit rester un seul batch sans SQL dynamique : ce cas est **exclu de la garantie** et nommé
  dans l'en-tête du fichier.

La cible exigée est `package0.event_file`. Un `ring_buffer` est perdu au redémarrage et ne
conserve qu'une fenêtre ; une cible de comptage ne garde pas le rapport. Ni l'un ni l'autre ne
suffit à l'analyse après incident, qui est l'usage. Une session sans cible peut avoir un
consommateur externe (flux en direct) : la requête ne peut pas le prouver et la classe `NOT_OK`.

## 3. Décisions

| Question | Décision |
|---|---|
| Option CLI `-check` ou requête livrée ? | **Requête livrée**, lancée par `sqlq -file`. Aucune surface CLI nouvelle. |
| Repérer la session par son nom ou par l'événement ? | **Par l'événement** `sqlserver.blocked_process_report`, quel que soit le nom. Une session trouvée reste une candidate même non conforme : **une candidate non conforme n'est jamais « aucune session »**. |
| Qui conclut ? | **La requête**, par des états structurés (`OK`, `NOT_OK`, `UNKNOWN`) et des raisons séparées. Personne ne parse les raisons pour décider. |
| Moteurs | SQL Server 2012 et suivants, Azure SQL Managed Instance. Azure SQL Database n'a pas de session serveur : la requête y échoue à la compilation (erreur, code 2), ce qui ne peut pas passer pour « aucune session ». Seules les versions effectivement testées sont déclarées testées (§8). |

## 4. La requête

Fichier : `plugins/sqlserver-toolkit/skills/live-query/queries/blocked-processes-check.sql`.
Aucun paramètre. Un seul batch, sans `GO`, sans `EXEC`, sans `STRING_AGG`.

### Forme du résultat

Un seul jeu de résultats. **Une ligne par session candidate**, au plus 20, avec les colonnes
d'instance répétées sur chaque ligne. S'il n'y a aucune candidate, ou si le droit manque, **une
ligne unique** dont les colonnes de session sont NULL.

Le verdict d'instance est calculé **sur toutes les candidates avant le `TOP`** : la borne de
présentation ne peut pas changer la conclusion. Les lignes sont ordonnées `OK`, puis `UNKNOWN`,
puis `NOT_OK`, puis par nom de session.

### Colonnes d'instance

| colonne | contenu |
|---|---|
| `instance_state` | `OK` si une candidate au moins est `OK` ; sinon `UNKNOWN` si une candidate est `UNKNOWN` ou si le droit ou le seuil ne sont pas établis ; sinon `NOT_OK` |
| `server_name` | `@@SERVERNAME` : l'instance réellement atteinte, pour le contrôle de couverture d'un AG (§6) |
| `required_permission` | le droit exigé par cette version : `VIEW SERVER STATE` avant 2022, `VIEW SERVER PERFORMANCE STATE` ensuite |
| `has_permission` | 1 / 0 (§5) |
| `threshold_value`, `threshold_value_in_use` | `sys.configurations`, NULL si la ligne est absente |
| `candidate_count` | nombre total de candidates |
| `details_incomplete` | 1 si `candidate_count` dépasse le nombre de lignes renvoyées |
| `is_hadr_enabled` | `SERVERPROPERTY('IsHadrEnabled')` |
| `ag_replicas` | si HADR est activé : noms de réplicas des AG locaux, triés, séparés par `, ` ; NULL sinon |

### Colonnes de session

| colonne | contenu |
|---|---|
| `session_name` | nom de la session candidate |
| `session_state` | `OK`, `NOT_OK` ou `UNKNOWN` |
| `reasons` | raisons séparées par `; `, dans l'ordre du tableau suivant ; NULL si `OK` |
| `startup_state` | `sys.server_event_sessions.startup_state` |
| `is_running` | la session figure dans `sys.dm_xe_sessions` |
| `event_in_running_session` | l'événement figure dans `sys.dm_xe_session_events` pour cette session en cours |
| `event_predicate` | prédicat défini sur l'événement, NULL si aucun |
| `file_target_defined` | la définition a une cible `event_file` |
| `file_target_running` | la session en cours a une cible `event_file` (`sys.dm_xe_session_object_columns`, jamais `sys.dm_xe_session_targets`, qui force une écriture) |
| `targets` | noms des cibles définies, triés, séparés par `, ` |

### Règles

Seuil, évaluées pour l'instance et reportées dans les `reasons` de chaque ligne (une session ne
peut pas être `OK` si le seuil ne l'est pas) :

| condition | état | raison |
|---|---|---|
| droit absent (§5) | `UNKNOWN` | `missing <required_permission>` — seule règle évaluée, la ligne est unique |
| ligne de configuration absente ou valeur NULL | `UNKNOWN` | `threshold unknown` |
| `value_in_use = 0`, `value = 0` | `NOT_OK` | `threshold=0` |
| `value_in_use < 5`, `value` entre 1 et 4 | `NOT_OK` | `threshold below 5 s (no reports generated)` |
| `value_in_use < 5`, `value >= 5` | `NOT_OK` | `threshold set but not in use (RECONFIGURE pending)` |
| `value_in_use >= 5`, `value = 0` | `NOT_OK` | `threshold disable pending (next RECONFIGURE turns reports off)` |
| `value_in_use >= 5`, `value` entre 1 et 4 | `NOT_OK` | `threshold below 5 s pending (next RECONFIGURE turns reports off)` |
| `value_in_use >= 5`, `value >= 5` | — | aucune (des valeurs différentes ne changent pas l'activation ; les deux sont affichées) |

Un seuil de 1 à 4 secondes est accepté par le moteur (`sys.configurations` : minimum 0) mais ne
produit aucun rapport : « If you configure the threshold to a value from 1 to 4, the system
doesn't generate blocked process reports » (Microsoft, règle de stratégie *Increase or disable
blocked process threshold*). Il est donc traité comme 0. Ajouté après la revue des risques (H1).

`RECONFIGURE` applique **toutes** les options en attente, pas seulement le seuil. La requête ne
les liste pas : avant tout `RECONFIGURE`, relire `sys.configurations WHERE value <> value_in_use`
(revue des risques, H2). L'en-tête de la requête le dit.

Une collecte qui s'arrêtera au prochain `RECONFIGURE` de n'importe qui est classée `NOT_OK` :
elle marche ce jour, mais elle ne durera pas, ce qui est exactement l'échec que la requête existe
pour attraper.

Session, cumulées :

| condition | état | raison |
|---|---|---|
| aucune candidate (ligne unique) | `NOT_OK` | `no session` |
| `startup_state = 0` | `NOT_OK` | `startup_state=OFF` |
| la session ne tourne pas | `NOT_OK` | `not running` |
| la session tourne sans l'événement | `NOT_OK` | `event not in running session` |
| pas de cible `event_file` définie | `NOT_OK` | `no file target` |
| la session tourne sans cible `event_file` active | `NOT_OK` | `file target not running` |
| prédicat sur l'événement | `UNKNOWN` | `event filtered by predicate` |

L'état d'une session est le pire de ses règles (`NOT_OK` > `UNKNOWN` > `OK`). Sur la ligne
sans candidate, les règles de session autres que `no session` ne s'appliquent pas : on n'empile
pas de faux défauts sur une session qui n'existe pas.

Un prédicat n'est pas interprété : il peut limiter l'événement à une base ou à une durée, et
seul un humain sait si cela couvre la question posée. La requête l'affiche et rend `UNKNOWN`.

`event not in running session` est un recoupement défensif : ce qui produit les rapports, c'est
ce qui tourne, pas ce qui est défini. Aucune séquence documentée connue ne sépare les deux pour
une session démarrée ; la règle coûte une jointure.

### Invariants d'implémentation

- **Une candidate par session** : sélection par `EXISTS` sur `sys.server_event_session_events`
  (`package = 'sqlserver'`, `name = 'blocked_process_report'`), jamais par jointure qui
  multiplierait les lignes par événement ou par cible.
- **Côté exécution**, `sys.dm_xe_session_events` est joint par
  `event_session_address = sys.dm_xe_sessions.address`, et l'événement est qualifié par
  `event_name` **et** `event_package_guid` résolu dans `sys.dm_xe_packages` (`name = 'sqlserver'`).
- **Définition ↔ exécution** par le nom de session, unique pour les sessions serveur, comparé
  en `Latin1_General_BIN2` des deux côtés : une comparaison exacte qui ne dépend pas de la base
  ouverte par le profil (`DATABASE_DEFAULT` en dépendrait, et pourrait confondre `A` et `a` sur
  un serveur sensible à la casse).
- **Cible active** lue dans `sys.dm_xe_session_object_columns` (`object_type = 'target'`,
  `object_name = 'event_file'`, package résolu à `package0`), **pas** dans
  `sys.dm_xe_session_targets`, dont la lecture force une vidange des données collectées vers le
  disque (remarque de la documentation Microsoft). La requête n'a ainsi aucun effet sur la
  collecte qu'elle contrôle.
- **NULL ne mène jamais à `OK`** : toute comparaison sur une valeur absente aboutit à `UNKNOWN`
  par une branche explicite, pas par le `ELSE`.
- **Agrégations de texte** par `FOR XML PATH('')`, `TYPE).value('.', 'nvarchar(max)')`, pour
  qu'un nom contenant `&`, `<` ou un caractère non ASCII ressorte intact.
- **Types fixes** sur la ligne sans candidate (les `CAST` de NULL portent le type de la colonne).

## 5. Permissions

Documentation Microsoft : les vues lues exigent `VIEW SERVER STATE` jusqu'à SQL Server 2019 et
`VIEW SERVER PERFORMANCE STATE` à partir de 2022 ; et à partir de 2022, `VIEW SERVER STATE`
**implique** `VIEW SERVER PERFORMANCE STATE` (table des permissions implicites de
`GRANT Server Permissions`). `HAS_PERMS_BY_NAME` évalue la permission effective, refus
compris, et renvoie NULL pour un nom de permission inconnu de la version.

D'où le test, sans lecture de version :

- `HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER PERFORMANCE STATE')` d'abord. Là où ce droit
  existe (2022+, Managed Instance), c'est le droit exigé, et il est vrai aussi pour un
  détenteur de `VIEW SERVER STATE`.
- S'il renvoie NULL (droit inconnu de la version, donc avant 2022) :
  `HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER STATE')`.

`required_permission` nomme celui des deux qui a été évalué. Tout résultat autre que 1, NULL
compris, vaut « droit non établi ».

Le danger est le faux négatif silencieux : une vue catalogue qui renverrait zéro ligne faute de
droit ferait conclure `no session`. **Contrat : sans le droit, la requête rend la ligne unique
`UNKNOWN`, sans erreur.** Un filtre `WHERE` dans une CTE ne le garantit pas (les vues restent
dans l'instruction) ; le batch est donc un `IF` : la branche sans droit est un `SELECT` qui ne
référence **aucune** vue protégée, la branche avec droit est la requête complète. Les deux ont
les mêmes colonnes ; une seule s'exécute, donc un seul jeu de résultats. Le batch reste unique,
sans `EXEC` ni SQL dynamique. Le comportement réel est mesuré en §8 ; une erreur reste bruyante
(`sqlq` code 2) et ne devient jamais `no session`, mais elle serait un écart au contrat, à
corriger.

`ag_replicas` lit `sys.availability_replicas`, qui demande un autre droit (`VIEW ANY
DEFINITION`) : sans lui, la liste revient vide. Cela n'affecte pas le verdict local (§6).

## 6. Groupes de disponibilité

La session ne suit pas le groupe ; chaque instance hébergeant un réplica doit avoir la sienne.
Une connexion ne prouve rien sur une autre instance, donc **le verdict de la requête reste
local** et la couverture d'un AG se fait dans le skill :

- la couverture se mesure contre **la liste des réplicas confirmée par l'utilisateur**.
  `ag_replicas` est un inventaire observé qui aide à la construire, pas une preuve qu'elle est
  complète : quand l'instance ne joint plus le cluster WSFC, la vue ne rend que le réplica local
  (documentation de `sys.availability_replicas`). La couverture est complète quand chaque
  réplica de la liste confirmée est apparu comme `server_name` d'un contrôle `OK`, et qu'aucun
  `ag_replicas` observé ne nomme un réplica absent de cette liste ;
- deux profils qui renvoient le même `server_name` (deux chemins, par exemple un listener et
  une connexion directe) comptent pour **une** instance ;
- un réplica sans profil, inaccessible ou non contrôlé laisse la couverture **incomplète**, et
  c'est ce qui est annoncé ;
- `is_hadr_enabled = 1` avec `ag_replicas` NULL ou vide ne dit rien de la couverture : une liste
  vide peut venir d'un droit de visibilité manquant, pas d'une absence de réplicas. Le verdict
  local reste valide ;
- chaque profil de production garde son approbation explicite avant la première requête.

La correspondance nom de réplica ↔ profil n'est pas visible par l'agent (`-list-profiles` ne
donne pas les serveurs) : il demande à l'utilisateur quels profils couvrent les réplicas
manquants.

## 7. Le skill

Dans `plugins/sqlserver-toolkit/skills/live-query/SKILL.md`, une ligne dans l'arbre de
décision :

| The user asks | Do this |
|---|---|
| Is the blocked process trace in place / will blocking be captured | `-file queries/blocked-processes-check.sql`; read `instance_state`, not the rows. For an AG, see the header of that file. |

Les règles de lecture voyagent avec le fichier, dans son en-tête : la signification de `OK`
(§2) et ce qu'il ne dit pas, `UNKNOWN` qui n'est ni oui ni non, la couverture d'un AG (§6), et
« une candidate `NOT_OK` est une session à réparer, pas une absence de session ».

## 8. Validation

1. **Garde** : `TestBundledQueriesPassTheReadOnlyGuard` couvre le fichier sans changement. Il
   prouve que la requête passe le garde, rien de plus.
2. **Comportement, sur l'instance de dev** fournie par l'utilisateur. Le protocole détaillé, cas
   par cas, est dans le plan (tâche 4). Ses règles :
   - chaque écriture est posée par l'utilisateur, ou par l'agent avec son accord explicite sur
     l'instruction exacte ;
   - l'instance ne porte **aucune** session candidate préexistante ; sinon les cas à assertion
     d'instance ne sont pas exécutables sans nouvel accord ;
   - chaque cas part d'un état défini en entier (seuil, ensemble exact des sessions), et ses
     objets sont supprimés avant le cas suivant, sauf transitions annoncées ;
   - chaque cas affirme séparément `instance_state`, `session_state`, `reasons`, le nombre de
     lignes et leur ordre ;
   - l'état initial (`show advanced options`, seuil, sessions) est relevé avant toute écriture et
     restauré à l'identique, y compris après un échec ; une configuration en attente
     préexistante arrête la validation avant le premier `RECONFIGURE` ;
   - les objets créés sont tenus dans un registre, et c'est ce registre qui est nettoyé, pas un
     préfixe ;
   - la collation (deux noms ne différant que par la casse) n'est testée que sur un serveur
     sensible à la casse ; Managed Instance, SQL Server 2012 et les AG ne sont déclarés testés
     que s'ils l'ont été.

3. **Consignation** : un document versionné `docs/validation/2026-10-01-blocked-processes-check.md`,
   sans nom réel d'instance, de profil, de base ni de chemin : version du moteur, préconditions,
   attendu, observé, et **la liste des cas non exécutés** avec leur raison. Une seule version
   testée est déclarée comme telle ; les autres versions restent « non testées ».

Hors validation, faute de pouvoir les poser : la divergence définition / exécution
(`event not in running session`) et une session à `MAX_DURATION`. Ils sont listés comme non
couverts.

## 9. Traitement de la relecture Codex

| # | constat | traitement |
|---|---|---|
| 1 | un prédicat peut exclure les blocages utiles | **retenu** : `event_predicate` affiché, `UNKNOWN` si présent (§4) |
| 2 | les cibles sont hors verdict | **retenu** : contrat fixé en §2, `event_file` exigé, définie et active |
| 3 | « au moins une ligne OK » contre une liste tronquée | **retenu**, correction préférée : verdict avant `TOP`, `candidate_count`, `details_incomplete` |
| 4 | le OU de permissions n'est pas démontré | **en partie** : le OU était juste (`VIEW SERVER STATE` implique `VIEW SERVER PERFORMANCE STATE`, et `HAS_PERMS_BY_NAME` tient compte des refus), mais le libellé était faux en 2022+. Droit évalué selon ce que la version connaît (`VIEW SERVER PERFORMANCE STATE`, repli sur `VIEW SERVER STATE` si inconnu) et nommé dans la sortie ; verdict construit sans les vues protégées (§5) |
| 5 | seuil positif ≠ adéquation, cas en attente | **retenu** : les deux cas en attente sont `NOT_OK`, le seuil est affiché sans jugement, limites annoncées en §2, préconditions ajoutées aux tests |
| 6 | frontière des moteurs, `MAX_DURATION` | **retenu** : moteurs nommés, Azure SQL Database échoue bruyamment, `MAX_DURATION` exclu de la garantie |
| 7 | HADR activé ne prouve pas la couverture | **retenu** : `server_name` et `ag_replicas`, règle de couverture dans le skill (§6) |
| 8 | invariants SQL | **retenu** en entier (§4, invariants) |
| 9 | protocole de validation | **retenu**, sauf les fixtures de classement hors moteur : il n'existe pas de moteur SQL hors instance dans ce dépôt, et une fixture qui reproduit la logique ne la teste pas. Démarrage / arrêt pendant le contrôle : couvert par la définition « observation ponctuelle » (§2), pas par un test |
| 10 | « disparaît », sortie textuelle | **retenu** : phrase corrigée (§1), états structurés séparés des raisons |

### Relecture du plan (`plans/2026-10-01-blocked-processes-check-codex.md`)

| # | constat | traitement |
|---|---|---|
| 1 | `DATABASE_DEFAULT` change l'identité des sessions | **retenu** : comparaison en `Latin1_General_BIN2` (§4) |
| 2 | une liste AG non vide n'est pas complète | **retenu** : couverture mesurée contre la liste confirmée par l'utilisateur (§6) |
| 3 | les cas de validation se contaminent | **retenu** : état complet par cas, assertions par colonne (§8, plan tâche 4) |
| 4 | le nettoyage ne restaure pas tout | **retenu** : relevé initial, arrêt sur configuration en attente, registre des objets (§8) |
| 5 | la sentinelle sans droit n'est pas garantie par un filtre | **retenu** : branche `IF` sans vue protégée, contrat écrit (§5) |
| 6 | cible active ≠ preuve de collecte | **retenu** : `OK` borné aux prérequis observés (§2), cible active qualifiée par `package0` |
| 7 | lectures non atomiques | **retenu** en documentation : observation non atomique, relancer si les sessions changent (§2). Pas de matérialisation, qui demanderait d'écrire |
| 8 | lire `sys.dm_xe_session_targets` force une vidange | **retenu**, au-delà de la correction proposée : la vue n'est plus lue (§4) |
| 9 | Managed Instance et 2012 non validés | **retenu** : déclarés non testés tant qu'ils ne l'ont pas été (§8) |
| 10 | vérifications insuffisantes | **retenu** : type de sortie vérifié dans `columns` du JSON, `go test ./...` complet, lecture des lignes voisines du skill |

## 10. Hors portée

- La pose (partie B). Son contrat avec A est déjà fixé ici : `instance_state = NOT_OK` avec une
  candidate signifie « réparer cette session », jamais « en créer une ».
- La lecture des rapports collectés.
- Tout nom de client, d'instance ou de serveur dans le dépôt, qui est public.
