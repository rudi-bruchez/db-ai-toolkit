# Design — contrôle de la trace `blocked_process_report` (requête livrée)

Date : 2026-10-01
Statut : brainstorming terminé, en attente de relecture avant plan d'implémentation.
Portée : partie A d'une tâche découpée en deux. La partie B (la pose, qui écrit) est une tâche
séparée, à la route complète ; elle réutilisera cette requête comme contrôle de sortie.

## 1. Le problème

Une trace des processus bloqués tient à quatre choses, et il suffit qu'une seule manque pour
qu'elle ne produise rien, sans aucune erreur :

1. le paramètre serveur `blocked process threshold (s)` est supérieur à 0 **en vigueur**
   (`value_in_use`), sans quoi l'événement `blocked_process_report` n'est jamais levé ;
2. une session d'événements étendus capture cet événement ;
3. cette session a `STARTUP_STATE = ON`, sans quoi elle disparaît au prochain redémarrage ;
4. cette session tourne.

Posée à la main sur plusieurs instances en production, la trace a manqué une étape à chaque
fois : `STARTUP_STATE` resté à OFF sur deux instances pendant des jours, une session définie
mais jamais démarrée, et un seuil à 0 sur six instances, donc aucun rapport quelle que soit la
session. Le script de pose d'origine écrit lui-même `STARTUP_STATE = OFF`.

Une requête de contrôle passée sur toutes les instances a trouvé les trois oublis en une passe.
C'est cette requête qu'on livre ici, en lecture seule, pour qu'elle soit la même à chaque fois.

## 2. Décisions prises au brainstorming

| Question | Décision |
|---|---|
| Option CLI `-check` ou requête livrée ? | **Requête livrée**, lancée par `sqlq -file`. Aucune surface CLI nouvelle. |
| Repérer la session par son nom ou par l'événement ? | **Par l'événement.** Toute session serveur contenant `sqlserver.blocked_process_report`, quel que soit son nom. Une trace posée sous un autre nom est comptée ; sinon le contrôle la dirait absente et la pose en créerait une deuxième. |
| Qui conclut : l'agent ou la requête ? | **La requête.** Une colonne `status` donne le verdict. Recouper quatre colonnes à la main est exactement ce qui a manqué. |
| Disponibilité de groupe (AG) | La session ne suit pas le groupe. La requête affiche `IsHadrEnabled` ; le skill impose de la passer sur le profil de **chaque** nœud. Une connexion ne peut rien affirmer sur le partenaire. |
| Moteurs couverts | SQL Server et Azure SQL Managed Instance. Pas Azure SQL Database, qui n'a pas de session serveur. |

## 3. La requête

Fichier : `plugins/sqlserver-toolkit/skills/live-query/queries/blocked-processes-check.sql`.
Aucun paramètre. Un seul batch, sans `GO`, sans `EXEC`.

### Lignes

- **Une ligne par session définie** qui contient l'événement.
- **Une seule ligne** avec `session_name` à NULL si aucune session ne le contient, ou si le
  login n'a pas le droit de voir les sessions (voir §4).

Une instance est conforme si **au moins une** ligne porte `status = 'OK'`.

### Colonnes

| colonne | contenu |
|---|---|
| `session_name` | nom de la session, NULL si aucune |
| `status` | `OK`, ou la liste des manques séparés par `; ` (ordre ci-dessous) |
| `threshold_value` | `sys.configurations.value` du seuil |
| `threshold_value_in_use` | `sys.configurations.value_in_use` du seuil |
| `startup_state` | `sys.server_event_sessions.startup_state` (bit) |
| `is_running` | la session figure dans `sys.dm_xe_sessions` |
| `event_in_running_session` | l'événement figure dans `sys.dm_xe_session_events` pour la session en cours |
| `targets` | noms des cibles définies, séparés par `, ` (indicatif, hors verdict) |
| `is_hadr_enabled` | `SERVERPROPERTY('IsHadrEnabled')` |

### Règles du verdict

Chaque règle qui s'applique ajoute son libellé à `status`, dans cet ordre :

| condition | libellé |
|---|---|
| le login ne peut pas voir les sessions (§4) | `UNKNOWN: missing VIEW SERVER STATE` — seule règle évaluée dans ce cas |
| `value_in_use = 0` et `value > 0` | `threshold not in use (RECONFIGURE pending)` |
| `value_in_use = 0` et `value = 0` | `threshold=0` |
| aucune session ne contient l'événement | `no session` |
| `startup_state = 0` | `startup_state=OFF` |
| la session ne tourne pas | `not running` |
| la session tourne sans l'événement (définition modifiée sans redémarrage de la session) | `event not in running session` |

Aucune règle ne s'applique : `OK`.

`event not in running session` est un recoupement défensif : c'est ce qui tourne
(`sys.dm_xe_session_events`) qui produit les rapports, pas ce qui est défini. Je ne connais pas
de séquence documentée qui sépare les deux pour une session démarrée ; la règle coûte une
jointure et transforme un désaccord éventuel en verdict au lieu d'un `OK` à tort.

Le rapprochement entre définition et session en cours se fait par le nom
(`sys.server_event_sessions.name = sys.dm_xe_sessions.name`), qui est unique pour les sessions
serveur.

Compatibilité : pas de `STRING_AGG` (SQL Server 2017+) ; la liste des cibles et le
`status` se construisent avec `FOR XML PATH` ou des concaténations, comme dans
`proc-source.sql`.

## 4. Permissions et faux négatif

Les vues lues demandent `VIEW SERVER STATE` jusqu'à SQL Server 2019, et
`VIEW SERVER PERFORMANCE STATE` à partir de 2022 (documentation Microsoft de
`sys.server_event_sessions`, `sys.dm_xe_sessions`, `sys.dm_xe_session_events`).

Le danger est silencieux : si une vue catalogue renvoie zéro ligne faute de droits au lieu
d'échouer, la requête conclurait `no session` sur une instance pourtant tracée, et la pose de B
en créerait une seconde. La requête teste donc le droit d'abord, avec `HAS_PERMS_BY_NAME`
sur `VIEW SERVER STATE` **ou** `VIEW SERVER PERFORMANCE STATE`, et rend `UNKNOWN` plutôt
qu'un verdict.

Deux points restent à mesurer sur l'instance de dev, pas à supposer :

- ce que renvoie `HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER PERFORMANCE STATE')` avant 2022,
  où ce droit n'existe pas (NULL attendu, à traiter comme 0) ;
- si les vues catalogue, sans le droit, renvoient zéro ligne ou une erreur. Une erreur est
  sans danger : `sqlq` sort en code 2 avec le numéro.

## 5. Le skill

Dans `plugins/sqlserver-toolkit/skills/live-query/SKILL.md`, une ligne dans l'arbre de
décision :

| The user asks | Do this |
|---|---|
| Is the blocked process trace in place / will blocking be captured | `-file queries/blocked-processes-check.sql`, on **every node's profile** for an AG |

Et trois règles, courtes, dans l'en-tête de la requête plutôt que dans le skill (elles voyagent
avec le fichier) :

- l'instance est conforme si une ligne au moins dit `OK` ;
- `UNKNOWN` n'est ni oui ni non : le dire, et demander le droit manquant ;
- pour un AG, un nœud conforme ne dit rien de l'autre.

## 6. Tests

1. **Garde** : `TestBundledQueriesPassTheReadOnlyGuard` couvre le fichier sans changement.
   Il prouve que la requête passe le garde, rien de plus.
2. **Comportement, sur l'instance de dev** (profil `readwrite`, fourni par l'utilisateur), les
   états étant posés à la main par l'utilisateur ou avec son accord explicite, un par un :

   | état posé | `status` attendu |
   |---|---|
   | seuil 0, pas de session | `threshold=0; no session` |
   | seuil 10, session avec l'événement, `STARTUP_STATE = OFF`, arrêtée | `startup_state=OFF; not running` |
   | même session démarrée | `startup_state=OFF` |
   | `STARTUP_STATE = ON`, démarrée | `OK` |
   | seuil `value` = 10 sans `RECONFIGURE` | `threshold not in use (RECONFIGURE pending)` |
   | deux sessions, une seule conforme | deux lignes, une `OK` |
   | login sans `VIEW SERVER STATE` | `UNKNOWN: missing VIEW SERVER STATE` |

   Les résultats sont consignés dans le commit qui livre la requête. Un état qui n'a pas pu être
   posé est nommé comme tel, pas déclaré couvert.

## 7. Hors portée

- La pose (partie B).
- La lecture des rapports collectés.
- Tout nom de client, d'instance ou de serveur dans le dépôt, qui est public.
