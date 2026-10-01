# Relecture adversariale Codex — contrôle de `blocked_process_report`

Date : 2026-10-01. Document relu : [design](2026-10-01-blocked-processes-check-design.md).

**Verdict : réviser avant le plan d'implémentation.** La requête livrée et la détection par événement sont de bons choix. En revanche, le contrat actuel autorise des `OK` qui ne prouvent pas que les blocages pertinents seront collectés. Il laisse également la conclusion dépendre d'un résultat potentiellement incomplet et de préconditions de permissions insuffisamment précisées.

Cette revue porte sur le design, le skill existant, les conventions et le code du test de garde et de la limite de lignes de `sqlq`. Les comportements SQL ont été confrontés à la documentation Microsoft. Aucune connexion à une instance, aucune modification de configuration et aucun test SQL réel n'ont été effectués. Les scénarios ci-dessous sont des contre-exemples de conception à vérifier, pas des résultats expérimentaux.

Priorités : **P1** = conclusion trompeuse ou contrat bloquant ; **P2** = défaut de robustesse ou de validation à résoudre avant livraison ; **P3** = précision documentaire.

## 1. P1 — Un événement présent peut exclure tous les rapports utiles

**Localisation : §2, détection par événement ; §3, règles de verdict.**

Contre-exemple : seuil actif à 10, session démarrée, démarrage automatique activé, événement présent, mais prédicat limité à une autre base ou à un contexte qui exclut le workload étudié. Toutes les conditions du design sont satisfaites : la requête rend `OK`, et l'agent répond à « will blocking be captured » par oui. Pourtant, les événements pertinents sont filtrés.

Les catalogues exposent `predicate` et `predicate_xml` ; la DMV expose `event_predicate`. Microsoft précise que les prédicats peuvent empêcher le déclenchement de l'événement dans la session. La simple présence du nom ne constitue donc pas une preuve de couverture. Sources : [événements définis](https://learn.microsoft.com/en-us/sql/relational-databases/system-catalog-views/sys-server-event-session-events-transact-sql?view=sql-server-ver17), [événements actifs](https://learn.microsoft.com/en-us/sql/relational-databases/system-dynamic-management-objects/sys-dm-xe-session-events-transact-sql?view=sql-server-ver17).

**Correction :** distinguer découverte et qualification. Toute session contenant l'événement reste une candidate, ce qui évite de créer une deuxième session ; un prédicat non vide entraîne une réserve explicite, par exemple `UNKNOWN: filtered event coverage`, tant que sa portée n'est pas validée. Exposer au minimum la présence d'un filtre, et documenter le moyen de l'inspecter. Ne pas essayer d'interpréter arbitrairement tous les prédicats en T-SQL.

**Critère d'acceptation :** une session filtrée sur une base sans rapport avec la question ne peut pas établir la conformité globale. Un filtre légitime doit pouvoir être rapporté comme une couverture limitée, sans être traité comme une absence de session.

## 2. P1 — Les cibles sont hors verdict, alors que le besoin est la collecte

**Localisation : §1, « quatre choses » ; §3, `targets` indicatif ; §5, « will blocking be captured ».**

Le design ne vérifie aucun récepteur capable de conserver le rapport. Une session sans cible, ou avec une cible qui ne conserve qu'un comptage, satisfait les règles proposées. Même une cible définie ne prouve pas que son équivalent actif reçoit les événements. Une collecte volatile ne satisfait pas nécessairement le besoin d'analyse après incident.

Une session peut légalement avoir zéro cible. `STARTUP_STATE = ON` demande un démarrage automatique ; il ne garantit pas la réussite d'une collecte future. Source : [CREATE EVENT SESSION](https://learn.microsoft.com/en-us/sql/t-sql/statements/create-event-session-transact-sql?view=sql-server-ver17).

**Correction :** choisir explicitement le contrat avant de coder :

- Si A vérifie seulement les quatre réglages historiques, renommer la conclusion en « prérequis de configuration présents » et retirer la promesse que les blocages seront collectés.
- Si A certifie une collecte exploitable, qualifier la cible définie et active, avec une politique explicite : cible conservant le rapport complet, caractère volatile ou persistant, et limites de rétention. Une session sans cible ne doit pas être `OK` pour ce contrat.

Une session sans cible peut avoir un consommateur externe : ne pas affirmer que ce cas implique toujours zéro collecte. Sans preuve de ce consommateur, la collecte reste indéterminée. Lire les rapports collectés peut rester hors portée ; vérifier l'existence d'un chemin de collecte ne l'est pas si le verdict promet cette collecte.

**Critère d'acceptation :** cible absente, cible de comptage seule et cible volatile ont chacun une conclusion définie. Aucun ne reçoit silencieusement la même garantie qu'une collecte persistante validée.

## 3. P1 — « Au moins une ligne OK » est incompatible avec une liste tronquée

**Localisation : §3, cardinalité et conformité ; §5, invocation sans `-maxrows`.**

`sqlq` renvoie par défaut au plus 50 lignes (`tools/cmd/sqlq/main.go`). Le design exige une ligne par session sans définir de `TOP`, d'ordre ou de conclusion indépendante de la liste. Sur 51 candidates, la seule session conforme peut être la ligne non renvoyée. Conclure à l'absence de conformité serait alors faux. Augmenter arbitrairement `-maxrows` ne supprime pas le problème ; ajouter seulement `TOP (50)` cache des sessions au serveur sans nécessairement activer `truncated` côté client.

Le dépôt exige un `TOP (n)` explicite. Il faut donc résoudre la contradiction entre exhaustivité et borne, pas simplement oublier l'une des deux.

**Correction préférée :** calculer sur toutes les candidates le verdict de l'instance et le nombre de candidates, puis présenter au plus N lignes de détail avec `TOP (N)` et un ordre déterministe. Répéter le verdict global et un indicateur `details_incomplete` sur les lignes renvoyées, y compris la ligne sentinelle. Ce sont de petites vues système ; la borne de présentation ne doit pas devenir une borne de décision.

Alternative minimale : ordonner les lignes `OK` d'abord avant de les borner, exposer le total et formaliser l'incomplétude. Cela protège l'existence d'un `OK`, mais donne une moins bonne base à B pour inventorier et réparer les sessions. Ne pas émettre plusieurs jeux de résultats : le skill documente que `sqlq` ne restitue que le premier.

**Critère d'acceptation :** plus de 50 candidates, avec la seule conforme en dernière position nominale, produit une conclusion globale correcte et une indication explicite que les détails sont incomplets.

## 4. P2 — Le test des permissions doit porter sur le droit réellement requis

**Localisation : §4 ; §6, test du login sans `VIEW SERVER STATE`.**

La précondition proposée est « `VIEW SERVER STATE` OU `VIEW SERVER PERFORMANCE STATE` ». Or les vues documentent des exigences différentes avant et après SQL Server 2022. Le design n'établit pas que ce OU est équivalent à la permission effective exigée, notamment avec un `DENY` explicite. Le libellé `UNKNOWN: missing VIEW SERVER STATE` est incorrect ou trompeur sur les versions qui exigent le second droit.

Sources : [permissions des événements définis](https://learn.microsoft.com/en-us/sql/relational-databases/system-catalog-views/sys-server-event-session-events-transact-sql?view=sql-server-ver17), [permissions des événements actifs](https://learn.microsoft.com/en-us/sql/relational-databases/system-dynamic-management-objects/sys-dm-xe-session-events-transact-sql?view=sql-server-ver17).

**Correction :** établir version et moteur, sélectionner le droit requis, traiter toute valeur autre que 1 comme une précondition non établie, et nommer ce droit dans la sortie. Une autre stratégie est acceptable si elle est démontrée par des tests de permissions effectives ; le OU seul n'est pas une démonstration.

« Tester d'abord » doit également avoir une traduction SQL réelle. Un `CASE` dans le `SELECT`, ou un filtre sur une CTE, ne garantit pas que les vues protégées ne seront pas évaluées. Définir une branche de contrôle qui ne les consulte pas lorsque la précondition échoue, et vérifier le comportement réel. Si une erreur de permissions demeure possible, elle ne doit jamais devenir `no session`, et la conformité reste inconnue.

Le retour NULL pour une permission invalide est documenté, pas seulement une hypothèse à mesurer. Source : [HAS_PERMS_BY_NAME](https://learn.microsoft.com/en-us/sql/t-sql/functions/has-perms-by-name-transact-sql?view=sql-server-ver17). Le test sur ancienne version reste utile pour vérifier l'intégration.

**Critère d'acceptation :** tester ancien moteur, 2022+, compte sans aucun droit, compte avec le droit minimal requis et cas de refus explicite. Le compte 2022+ sans `VIEW SERVER STATE` mais avec le droit de performance nécessaire ne doit pas être marqué inconnu pour cette seule raison.

## 5. P2 — La règle positive sur le seuil dépasse ce que le seuil garantit

**Localisation : §1, seuil supérieur à 0 ; §3, règles ; §6, valeur 10 sans `RECONFIGURE`.**

Le design accepte toute valeur positive et ne définit pas de seuil attendu. Un seuil de plusieurs heures peut donc être `OK` pour un incident de blocage de quelques dizaines de secondes. Une valeur entre 1 et 4 ne promet pas une détection à cet intervalle. Microsoft décrit une cadence minimale de cinq secondes, un fonctionnement au mieux, sans garantie temps réel, et des catégories de tâches non couvertes. Source : [blocked process threshold](https://learn.microsoft.com/en-us/sql/database-engine/configure-windows/blocked-process-threshold-server-configuration-option?view=sql-server-ver17).

**Correction :** préciser que le seuil non nul établit l'activation, pas l'adéquation à tout incident. Afficher clairement le seuil effectif dans la conclusion ; ne pas inventer de seuil universel « correct ». Définir la réponse aux valeurs inférieures à 5 et aux valeurs inconnues. La question du skill doit porter sur les blocages éligibles dépassant le seuil, avec les limites de détection annoncées.

Le test `value = 10 sans RECONFIGURE` n'a le résultat annoncé que si `value_in_use` vaut encore 0. Si la valeur active précédente est déjà 10, il ne démontre rien ; si elle est 5, les rapports restent activés. Ajouter la précondition au scénario. Le cas inverse `value = 0, value_in_use > 0` reste actuellement `OK` alors qu'une désactivation est en attente. Il peut rester opérationnel maintenant, mais doit signaler le changement pending au lieu de sembler durablement conforme.

**Critère d'acceptation :** couvrir zéro/zéro, positif/zéro, positif identique, positifs différents et zéro/positif ; expliquer la distinction entre activation actuelle et configuration en attente.

## 6. P2 — Le support des moteurs n'a pas de frontière testable

**Localisation : §2, moteurs ; §3, compatibilité.**

« Pas de STRING_AGG » ne définit ni une version minimale ni les combinaisons supportées. Une ancienne version, une MI avec politique de mise à jour différente ou Azure SQL Database ne doit pas être confondue avec une instance sans session. Le skill actuel impose déjà la vérification de version avant de dépendre d'objets spécifiques.

Il existe aussi un contre-exemple récent à la permanence supposée : SQL Server 2025 et certaines MI permettent une durée maximale de session (`MAX_DURATION`). Une session active au moment du contrôle peut donc être configurée pour s'arrêter automatiquement. Source : [CREATE EVENT SESSION, MAX_DURATION](https://learn.microsoft.com/en-us/sql/t-sql/statements/create-event-session-transact-sql?view=sql-server-ver17).

**Correction :** annoncer les versions SQL Server et variantes MI prises en charge, et une sortie « unsupported » pour les autres moteurs. Si le verdict porte sur la durée, prendre en compte les sessions à durée finie sur les versions concernées, ou réduire explicitement `OK` à l'état présent. Ne pas référencer dans un batch ancien des colonnes récentes sans stratégie compatible avec le garde ; `EXEC` n'est pas une échappatoire disponible.

**Critère d'acceptation :** exécution sur la version minimale annoncée et sur 2022+, vérification séparée du support MI ; un scénario 2025/MI à durée finie est couvert ou explicitement exclu de la garantie.

## 7. P2 — AG : HADR activé ne prouve ni l'inventaire ni la couverture

**Localisation : §2, AG ; §5, « every node's profile ».**

`IsHadrEnabled` renseigne une capacité locale. Il ne liste pas les réplicas, ne prouve pas l'appartenance à un AG particulier et ne permet pas de savoir que tous les profils désignent des instances distinctes. Deux profils passant par le même listener peuvent contrôler deux fois le primaire et laisser le secondaire sans contrôle. Inversement, le basculement d'une FCI n'est pas le contrôle de deux instances indépendantes.

**Correction :** parler de chaque instance hébergeant un réplica du périmètre convenu, exiger une liste explicite de profils directs et conserver un état de couverture : vérifiée, non vérifiée, inaccessible. Ne jamais annoncer la conformité d'un AG tant que les membres attendus ne sont pas tous couverts. Séparer cela du verdict local de la requête. L'approbation de production reste requise pour chaque profil concerné conformément au workflow existant.

**Critère d'acceptation :** un réplica inaccessible ou non identifié rend la couverture globale incomplète ; deux profils qui atteignent le même backend ne sont pas comptés comme deux réplicas vérifiés.

## 8. P2 — L'identité et les inconnues doivent être des invariants SQL

**Localisation : §3, rapprochements et règles.**

Le design nomme correctement `sqlserver.blocked_process_report`, mais ne fixe pas tous les invariants nécessaires à l'implémentation :

- Côté définition, filtrer le package et le nom d'événement, puis obtenir une candidate unique par session ; utiliser une logique d'existence plutôt qu'une multiplication par événements et cibles.
- Côté runtime, joindre les événements à la session par `event_session_address = address` et qualifier le package par son GUID résolu. Le nom seul ne porte pas toute l'identité documentée. Source : [relations de sys.dm_xe_session_events](https://learn.microsoft.com/en-us/sql/relational-databases/system-dynamic-management-objects/sys-dm-xe-session-events-transact-sql?view=sql-server-ver17).
- Une configuration absente ou une valeur NULL ne doit pas traverser les comparaisons et atteindre le cas par défaut `OK`. Ajouter une règle explicite d'état inconnu. Même exigence pour les éléments obligatoires dont la visibilité n'est pas établie.
- Sans candidate, `startup_state`, état runtime et présence de l'événement sont non applicables. Ne pas accumuler des faux défauts de session sur la ligne sentinelle.
- Définir les types des branches sentinelles, un ordre déterministe pour les détails et les cibles, et un décodage XML correct si `FOR XML PATH` est utilisé. Tester un nom contenant `&` et un caractère non ASCII.

Ces points sont des risques d'implémentation, pas des bugs observés : aucun SQL n'est encore livré par ce design.

## 9. P2 — Le plan de validation ne teste pas les contre-exemples du verdict

**Localisation : §6.**

Le test `TestBundledQueriesPassTheReadOnlyGuard` parcourt effectivement les fichiers `.sql` du répertoire annoncé. Il couvrira le nouveau fichier sans modification, mais il ne valide ni syntaxe, ni permissions, ni jointures, ni cardinalité, ni vérité du verdict. La distinction faite dans le design est correcte ; les tests manuels proposés restent insuffisants pour combler ces autres obligations.

Ajouter au protocole les cas des constats précédents, ainsi que :

| Cas adversarial | Propriété à vérifier |
|---|---|
| Plusieurs cibles pour une session | Une seule ligne candidate, aucune duplication |
| Session nommée autrement que le script de pose | Détection indépendante du nom |
| Nom de session contenant `&` et Unicode | Nom et agrégations rendus sans corruption |
| Démarrage/arrêt pendant le contrôle | Résultat présenté comme observation ponctuelle, sans promesse de stabilité |
| Événement actif absent | Défaut distinct de `not running`, si cet état est reproductible |
| Erreur SQL ou connexion interrompue | Aucun verdict de conformité fabriqué |
| Une candidate non conforme existe, aucune `OK` | B ne reçoit pas implicitement l'ordre de créer une nouvelle session |

Les tests nécessitant des écritures restent sous consentement exact et environnement dédié. Pour les états non reproductibles, notamment une divergence définition/runtime, documenter la limite et vérifier séparément la logique de classement avec des fixtures en lecture seule. Une fixture ne remplace pas la preuve que cet état est atteignable sur un moteur réel.

Consigner des résultats sanitizés dans un document de validation versionné, avec version du moteur, préconditions, résultat attendu/observé et tests non exécutés. Un récit dans le message de commit seul est moins facile à maintenir et à relire. Ne pas publier les noms réels, profils, bases ou chemins de cibles.

## 10. P3 — Démarrage automatique et sortie textuelle demandent une précision

**Localisation : §1, « elle disparaît » ; §3, `status` ; §5.**

Avec `STARTUP_STATE = OFF`, la définition ne disparaît pas au redémarrage ; la session ne démarre pas automatiquement. Corriger cette phrase pour éviter de confondre disparition de la définition et arrêt de collecte. Source : [STARTUP_STATE](https://learn.microsoft.com/en-us/sql/t-sql/statements/create-event-session-transact-sql?view=sql-server-ver17).

La liste textuelle de manques convient à une lecture humaine, mais B ne doit pas parser des fragments de phrase pour décider de créer, modifier ou démarrer. Ajouter un état structuré minimal (`OK`, `NOT_OK`, `UNKNOWN`, `UNSUPPORTED`) et garder les raisons à part, ou annoncer que B s'appuie sur les colonnes typées. Une candidate non conforme reste une session existante ; ce n'est jamais la même chose qu'aucune session.

## Contrat minimal recommandé avant implémentation

1. Fixer la signification de `OK` : prérequis locaux observés, ou collecte qualifiée. Les cibles, les filtres et la durée suivent ce choix.
2. Séparer existence de candidates, qualification des candidates, verdict global de l'instance et couverture des réplicas.
3. Garantir que la limite de présentation ne change pas le verdict global ; exposer l'incomplétude des détails.
4. Définir version minimale, moteurs supportés, précondition de permission effective et branche UNKNOWN qui n'interroge pas les vues protégées.
5. Rendre les valeurs absentes inconnues, préserver une candidate unique et qualifier l'identité de l'événement aux deux niveaux.
6. Étendre le protocole de validation aux contre-exemples avant de réutiliser la requête comme contrôle de sortie de B.

La partie B peut rester hors portée. En revanche, son interface avec A doit déjà interdire l'équivalence dangereuse « aucune ligne OK = aucune session = créer une session ». C'est précisément le faux négatif que le design cherche à éviter.
