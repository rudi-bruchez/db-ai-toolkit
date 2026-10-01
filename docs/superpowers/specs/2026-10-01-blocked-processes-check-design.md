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

**`OK` = au moment du contrôle, cette instance collecte les rapports de processus bloqués dans
un fichier, et continuera après un redémarrage.** C'est une observation ponctuelle, locale à
l'instance.

`OK` ne dit **pas** :

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
| `file_target_running` | la session en cours a une cible `event_file` (`sys.dm_xe_session_targets`) |
| `targets` | noms des cibles définies, triés, séparés par `, ` |

### Règles

Seuil, évaluées pour l'instance et reportées dans les `reasons` de chaque ligne (une session ne
peut pas être `OK` si le seuil ne l'est pas) :

| condition | état | raison |
|---|---|---|
| droit absent (§5) | `UNKNOWN` | `missing <required_permission>` — seule règle évaluée, la ligne est unique |
| ligne de configuration absente ou valeur NULL | `UNKNOWN` | `threshold unknown` |
| `value_in_use = 0`, `value = 0` | `NOT_OK` | `threshold=0` |
| `value_in_use = 0`, `value > 0` | `NOT_OK` | `threshold set but not in use (RECONFIGURE pending)` |
| `value_in_use > 0`, `value = 0` | `NOT_OK` | `threshold disable pending (next RECONFIGURE turns reports off)` |
| `value_in_use > 0`, `value > 0` | — | aucune (des valeurs différentes ne changent pas l'activation ; les deux sont affichées) |

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
- **Définition ↔ exécution** par le nom de session, unique pour les sessions serveur.
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
droit ferait conclure `no session`. Le verdict ne dépend donc jamais des vues protégées quand le
droit manque : la ligne `UNKNOWN` est construite sans elles, et les lignes de candidates sont
filtrées par `has_permission = 1`. Si une vue protégée lève une erreur malgré tout, `sqlq` sort
en code 2 avec le numéro : bruyant, donc sans danger. Ce qui se passe réellement sans le droit
(zéro ligne ou erreur) est mesuré en §8, pas supposé.

## 6. Groupes de disponibilité

La session ne suit pas le groupe ; chaque instance hébergeant un réplica doit avoir la sienne.
Une connexion ne prouve rien sur une autre instance, donc **le verdict de la requête reste
local** et la couverture d'un AG se fait dans le skill :

- la couverture est complète quand **chaque nom de `ag_replicas`** est apparu comme
  `server_name` d'un contrôle `OK` ;
- deux profils qui renvoient le même `server_name` (deux chemins, par exemple un listener et
  une connexion directe) comptent pour **une** instance ;
- un réplica sans profil, inaccessible ou non contrôlé laisse la couverture **incomplète**, et
  c'est ce qui est annoncé ;
- `is_hadr_enabled = 1` avec `ag_replicas` NULL ou vide rend la couverture **inconnue**, jamais
  complète : une liste vide peut venir d'un droit de visibilité manquant sur
  `sys.availability_replicas` (mesuré en §8), pas d'une absence de réplicas ;
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
2. **Comportement, sur l'instance de dev** fournie par l'utilisateur. Chaque état est posé par
   l'utilisateur, ou par l'agent avec son accord explicite sur l'instruction exacte. Les
   préconditions sont vérifiées avant chaque mesure.

   | état posé | attendu |
   |---|---|
   | seuil 0/0, aucune session | `NOT_OK`, `threshold=0; no session` |
   | seuil `value` 10, `value_in_use` **0** (sans `RECONFIGURE`) | `NOT_OK`, `threshold set but not in use…` |
   | seuil 10/10, session avec `event_file`, `STARTUP_STATE = OFF`, arrêtée | `NOT_OK`, `startup_state=OFF; not running` |
   | même session démarrée | `NOT_OK`, `startup_state=OFF` |
   | `STARTUP_STATE = ON`, démarrée | `OK` |
   | seuil `value` 0, `value_in_use` **10** | `NOT_OK`, `threshold disable pending…` |
   | session à cible `ring_buffer` seule, démarrée, ON | `NOT_OK`, `no file target` |
   | session sans cible | `NOT_OK`, `no file target` |
   | prédicat sur l'événement | `UNKNOWN`, `event filtered by predicate` |
   | session à plusieurs cibles | une seule ligne pour la session |
   | nom de session autre que celui du script de pose | détectée |
   | nom de session contenant `&` et un caractère non ASCII | rendu intact |
   | deux sessions, une seule conforme | `instance_state = OK`, ligne `OK` en premier |
   | 21 sessions candidates, la conforme nommée en dernier | `instance_state = OK`, `details_incomplete = 1`, la ligne `OK` présente |
   | login sans le droit requis | ligne unique `UNKNOWN`, `missing <droit>` |
   | login avec `VIEW SERVER PERFORMANCE STATE` seul (2022+) | verdict normal, pas `UNKNOWN` |
   | login avec le droit mais `DENY` explicite | `UNKNOWN` |
   | login de lecture habituel, instance en AG | `ag_replicas` renseigné (sinon : droit de visibilité à documenter) |

   Les sessions de test sont supprimées après mesure, avec le même accord.

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

## 10. Hors portée

- La pose (partie B). Son contrat avec A est déjà fixé ici : `instance_state = NOT_OK` avec une
  candidate signifie « réparer cette session », jamais « en créer une ».
- La lecture des rapports collectés.
- Tout nom de client, d'instance ou de serveur dans le dépôt, qui est public.
