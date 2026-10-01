# Relecture adversariale du plan — contrôle `blocked_process_report`

Date : 2026-10-01.
Document relu : `2026-10-01-blocked-processes-check.md`, confronté à la spec révisée,
à sa première relecture, aux conventions, au skill `live-query`, au test des requêtes
livrées et aux contrôles de `tools/cmd/sqlq/main.go`.

**Avis : corrections nécessaires avant exécution.** Le verdict calculé avant la limitation
des détails corrige bien le risque de troncature. En revanche, le rapprochement
définition/runtime peut perdre l'identité des sessions, la règle de couverture AG peut
annoncer une couverture complète sur un inventaire partiel, et la validation ne garantit
ni l'isolation des cas ni la restauration de l'état initial.

Cette revue porte sur le SQL proposé et le protocole : aucune connexion SQL, écriture
serveur, modification d'implémentation ou création de commit n'a été effectuée. Les scénarios
dépendant du moteur sont explicitement à reproduire. P1 désigne un risque de faux verdict ou
une validation/restauration défectueuse ; P2 une garantie non démontrée ou une lacune
opérationnelle ; P3 une correction mécanique.

## 1. P1 — `DATABASE_DEFAULT` résout le conflit de collation en changeant l'identité

**Localisation : Review Focus 3, ligne 45 ; tâche 2, jointure ligne 309.**

La jointure impose la collation de la base ouverte par le profil aux deux noms de session.
Elle supprime un conflit possible, mais peut rendre égaux des noms que le catalogue serveur
distingue. Le résultat d'un contrôle serveur dépend alors de la base du profil ou de
`-database`. Microsoft confirme que `DATABASE_DEFAULT` hérite de la collation de la base
courante : [COLLATE](https://learn.microsoft.com/en-us/sql/t-sql/statements/collations?view=sql-server-ver17).

**Contre-exemple à reproduire :** serveur sensible à la casse, base de connexion insensible
à la casse ; candidate `bpr_test_A` arrêtée et autre session `bpr_test_a` démarrée.
La comparaison peut attribuer le runtime de la seconde à la première. Si la seconde
contient l'événement et la cible attendus, la candidate arrêtée peut devenir `OK`.
Si les deux tournent, la jointure peut multiplier les lignes, gonfler `candidate_count`
et déclencher à tort `details_incomplete`.

**Correction :** comparer l'identité exacte sans dépendre de la base courante, par exemple
avec une collation Unicode BIN2 explicitement choisie et disponible sur les versions
supportées. Vérifier sur moteur le rapprochement exact catalogue/DMV. Conserver
l'identifiant de session dans les étapes intermédiaires et vérifier l'unicité après jointure.

**Validation :** deux noms distincts par casse, puis par accent lorsque le serveur le
permet ; un seul runtime actif ; contrôle depuis deux bases de collations différentes.
V0 sur une instance « non standard » sans ces préconditions ne teste pas ce défaut.

## 2. P1 — Une liste AG non vide ne prouve pas qu'elle est complète

**Localisation : en-tête SQL, lignes 203–208 ; agrégation `ag_replicas` dans `inst`.**

L'en-tête conclut à une couverture complète lorsque tous les noms renvoyés ont été contrôlés
`OK`. Il traite seulement une liste NULL comme inconnue. Microsoft documente pourtant que
`sys.availability_replicas` ne renvoie que les réplicas locaux lorsque l'instance ne peut
plus joindre le cluster WSFC. La vue exige aussi `VIEW ANY DEFINITION`, distinct du droit
XE testé par `perm` :
[sys.availability_replicas](https://learn.microsoft.com/en-us/sql/relational-databases/system-catalog-views/sys-availability-replicas-transact-sql?view=sql-server-ver17).

**Contre-exemple documenté :** AG de deux instances, cluster inaccessible, liste contenant
seulement le réplica local, contrôle local `OK`. La règle annonce une couverture complète
sans avoir contrôlé le deuxième réplica.

**Correction :** décrire `ag_replicas` comme un inventaire observé dont la complétude reste
à établir. Comparer les contrôles à une liste attendue indépendante confirmée par l'utilisateur,
ou à une source dont la complétude est démontrée. Séparer visibilité AG et diagnostic XE :
une visibilité AG inconnue ne doit pas annuler un verdict local valide.

**Validation :** inventaire complet, visibilité AG insuffisante et inventaire local partiel.
Ne pas provoquer une perte de quorum pour cet essai : utiliser un environnement adapté ou
consigner le scénario comme non exécuté avec garantie AG limitée.

## 3. P1 — Les cas se contaminent et le tableau mélange verdict de session et d'instance

**Localisation : tâche 4, V1–V17, lignes 532–548.**

La séquence conserve apparemment les sessions jusqu'au nettoyage final. Après V5,
`bpr_test_a` est conforme. V6 laisse ensuite le seuil configuré à 0. V7/V8 attendent seulement
`no file target`, alors que sans remise à 10/10 une raison de seuil s'ajoute.
Si le seuil est rétabli et `bpr_test_a` conservée, V9 donne bien une
`session_state = UNKNOWN` pour la session filtrée, mais `instance_state = OK` grâce à
`bpr_test_a`. V14 ne compte exactement 22 candidates que si toutes les candidates antérieures
ont disparu. Une instance de dev possédant déjà une trace conforme perturbe aussi V1 et V9.

Vérifier les préconditions permet de détecter le problème ; cela ne décrit pas comment
obtenir un état isolé sans modifier des sessions préexistantes. L'attendu non qualifié
`NOT_OK` ou `UNKNOWN` peut valider la mauvaise colonne.

**Correction :** pour chaque cas, définir le seuil complet, l'ensemble exact des candidates,
leur définition/runtime et les assertions distinctes sur `instance_state`, `session_state`,
`reasons`, cardinalité et ordre. Nettoyer les objets du cas avant le suivant, sauf transitions
explicites V3→V5. Exiger une instance dédiée sans candidate préexistante pour les assertions
globales ; sinon déclarer ces cas non exécutables sans autorisation supplémentaire.
Ne pas filtrer la requête livrée sur les noms de test pour arranger les résultats.

V14 doit préciser que les 21 premières sessions capturent toutes l'événement mais sont
non conformes et que seule `bpr_test_zz` est conforme. Vérifier le total avant mesure,
puis les vingt lignes affichées, leur unicité et la présence de la ligne conforme.

## 4. P1 — Le nettoyage ne restaure pas tout l'état modifié

**Localisation : pose ligne 515 ; nettoyage lignes 554–555.**

La pose active `show advanced options`, mais le nettoyage ne le restaure pas. V0 ne renvoie
même pas cette option. « Remettre le seuil initial » n'explique pas comment préserver une
différence initiale entre `value` et `value_in_use`. Un `RECONFIGURE` de préparation peut
également appliquer d'autres changements serveur déjà en attente.
Supprimer toutes les sessions `bpr_test_*` peut toucher un objet préexistant d'un autre essai.
Les logins et permissions éventuellement créés pour V15–V17 sont absents du nettoyage.

**Correction :** capturer séparément `value` et `value_in_use` du seuil et de
`show advanced options`, vérifier les configurations en attente, inventorier les noms de
test préexistants et refuser les collisions. Tenir un registre des objets réellement créés
plutôt qu'une suppression par préfixe. Préparer les instructions exactes de restauration des
options, sessions, logins et permissions sous les accords exigés par le dépôt.

Si l'état initial contient des changements en attente, résoudre ce cas avec l'utilisateur
avant d'appliquer `RECONFIGURE`. Prévoir le nettoyage après erreur ou refus d'une étape
suivante. Comparer les valeurs et inventaires restaurés, pas seulement le verdict final.
La suppression d'une session ne constitue pas à elle seule un nettoyage de ses fichiers
`.xel` ; documenter leur destination et leur devenir.

## 5. P2 — La sentinelle sans permission n'est pas garantie par une CTE filtrée

**Localisation : Review Focus 1 ; `cand`, lignes 258–265 ; V15/V17.**

Le commentaire « Nothing is read when the permission is missing » dépasse ce que démontre
`WHERE perm.has_permission = 1`. Les vues protégées restent dans l'instruction, notamment
les DMV de `detail`. Un filtre de données ne constitue pas une branche procédurale
garantissant l'absence d'accès ou d'erreur de permission. `inst` référence aussi l'inventaire
AG sans condition sur le droit XE.

La spec §5 admet une erreur SQL bruyante lorsqu'une vue protégée refuse l'accès.
Le Review Focus et V15 promettent une ligne unique `UNKNOWN`. La possibilité de laisser
V15–V17 non exécutés permet de terminer sans arbitrer cette divergence.
**Cette revue n'a pas observé d'erreur sous faible privilège : elle constate une garantie
non démontrée.** Les droits des vues sont documentés par Microsoft :
[vues XE](https://learn.microsoft.com/en-us/sql/relational-databases/extended-events/selects-and-joins-from-system-views-for-extended-events-in-sql-server?view=sql-server-ver17).

**Correction :** choisir un contrat. Si la sentinelle est obligatoire, prévoir une branche
`IF` dédiée dont le SELECT ne référence aucune vue protégée, avec le même schéma de sortie,
puis valider le comportement réel. Un seul batch n'exige pas un seul SELECT textuel,
seulement un seul résultat émis. Si l'erreur reste admise, corriger focus, en-tête et attendus.
Ne jamais convertir une erreur en `no session`.

V17 doit préciser le droit refusé et utiliser un principal non-sysadmin :
`DENY VIEW SERVER STATE` et `DENY VIEW SERVER PERFORMANCE STATE` ne sont pas des scénarios
interchangeables sur 2022+. Un cas non exécuté laisse sa garantie non validée ; le compte
rendu doit le dire.

## 6. P2 — Cible active et preuve de collecte sont confondues

**Localisation : Goal ; définition de `OK` ; `file_target_running`, lignes 295–300.**

Le SQL observe une définition `event_file` et une cible active. Il ne vérifie ni la production
d'un rapport, ni son écriture/lisibilité effective, ni la disponibilité du stockage après
redémarrage. Cela établit des prérequis ; « this instance writes blocked process reports
[...] and will again after a restart » promet davantage. Une panne de stockage avec session
encore présente est un scénario à reproduire, pas un bug moteur observé ici.

La cible runtime n'est également qualifiée que par `target_name`, tandis que la définition
exige `package0`. La DMV expose `target_package_guid` : résoudre le package est possible,
comme pour l'événement. Cette omission ne prouve pas qu'un autre package fournisse
aujourd'hui une cible homonyme.

**Correction :** borner `OK` aux prérequis de configuration/runtime, exclure explicitement
santé du stockage et preuve de capture ; ou étendre la validation par un blocage contrôlé
et lecture du rapport écrit, sous consentement exact. Même cet essai ne garantit pas le
stockage futur. Qualifier la cible runtime par `package0`.
Ne pas ajouter directement `bytes_written` au batch 2012+ : cette colonne n'existe
qu'à partir de 2017 :
[sys.dm_xe_session_targets](https://learn.microsoft.com/en-us/sql/relational-databases/system-dynamic-management-objects/sys-dm-xe-session-targets-transact-sql?view=sql-server-ver17).

## 7. P2 — Les CTE répétées ne garantissent pas une photographie cohérente

**Localisation : `judged`, `inst`, `shown`, `sentinel`, SELECT final.**

`judged` sert à l'agrégation et aux détails ; `inst` à la sentinelle et au SELECT final.
Les CTE ne sont pas matérialisées par contrat :
[WITH common_table_expression](https://learn.microsoft.com/en-us/sql/t-sql/queries/with-common-table-expression-transact-sql).
Les lectures peuvent être répétées et le runtime des DMV change.

**Risque à reproduire :** création, arrêt ou suppression concurrente d'une session ;
comptage, verdict et détails peuvent correspondre à des lectures différentes.
« Observation ponctuelle » ne prouve pas que toutes les colonnes appartiennent au même
instant. L'affirmation que le TOP final ne peut jamais couper repose également sur un
état stable entre évaluations de `shown` et `sentinel`.

**Correction :** annoncer une observation non atomique et refaire le contrôle après
stabilisation lorsque des sessions changent. Étudier des agrégats par fenêtre calculés sur
un même flux avant le TOP pour réduire les lectures indépendantes, sans promettre un
snapshot des DMV. Tester les transitions concurrentes ou déclarer cette cohérence non
couverte. Ne pas ajouter automatiquement des écritures temporaires pour matérialiser.

## 8. P2 — Le SELECT force un flush ; vingt lignes ne bornent pas son coût

**Localisation : architecture « lecture seule » ; DMV des cibles dans `detail`.**

Microsoft indique que lire `sys.dm_xe_session_targets` force une vidange des données
collectées vers le disque :
[remarque sur le flush](https://learn.microsoft.com/en-us/sql/relational-databases/system-dynamic-management-objects/sys-dm-xe-session-targets-transact-sql?view=sql-server-ver17).
Le batch passe légitimement le garde lexical mais n'est pas sans effet sur la collecte.
Les sous-requêtes corrélées sur toutes les candidates et les références répétées peuvent
multiplier le travail. Le TOP borne l'affichage, pas le calcul global.
Le nombre exact de flushes dépend du plan d'exécution et n'a pas été mesuré.

**Correction :** documenter ce coût dans l'en-tête, éviter une recommandation de polling
fréquent, mesurer durée/comportement avec plusieurs sessions, notamment V14.
Réduire les lectures répétées si les mesures le justifient. Ce constat n'exige pas un nouvel
accord pour chaque SELECT ; il exige une description exacte de l'opération.

## 9. P2 — La recette de validation ne couvre pas Managed Instance

**Localisation : Tech Stack ; tâche 4, `filename = N'bpr_test_a'`.**

MI est dans la portée mais la pose utilise un nom de fichier local relatif.
La procédure Microsoft pour MI utilise une cible Azure Storage avec URL de blob et
authentification adaptée :
[event_file dans Azure SQL](https://learn.microsoft.com/en-us/azure/azure-sql/database/xevent-code-event-file?view=azuresql).
Cette recette ne valide donc pas MI telle quelle. Sur SQL Server classique, le chemin
relatif suppose également une destination et des droits de service à vérifier.

**Correction :** distinguer les recettes SQL Server/MI, avec emplacement de test fourni par
l'utilisateur et authentification déjà disponible ; ne pas manipuler des secrets pour
compléter automatiquement la recette. Garder les emplacements réels hors du document public.
Si seul SQL Server est disponible, déclarer MI non testé.

Même discipline pour 2012 : une exécution récente et le garde Go ne prouvent pas la
compatibilité 2012. La spec impose cette distinction ; la tâche de consignation doit
explicitement reprendre la liste des versions/moteurs non testés.

## 10. P3 — Quelques vérifications ne démontrent pas leur propriété annoncée

**Localisation : note sur le type de `predicate` ; tâche 3 Step 2 ; commandes finales.**

Le type `nvarchar(3000)` est documenté :
[sys.server_event_session_events](https://learn.microsoft.com/en-us/sql/relational-databases/system-catalog-views/sys-server-event-session-events-transact-sql?view=sql-server-ver17).
Une différence de type ne sera pas nécessairement révélée par conversion ou troncature :
un NULL typé dans une UNION peut être converti implicitement sans erreur et sans tronquer
l'autre branche. Vérifier les métadonnées de sortie et un prédicat non NULL.

`grep` vérifie la présence d'une ligne, pas le rendu du tableau. Lire les lignes voisines
suffit pour cette modification simple ; utiliser les commandes RTK demandées par AGENTS.md.
Ajouter `rtk go test ./...` au contrôle final : le plan ne lance que le paquet sqlq et le vet,
alors que le dépôt demande le test du module. Le garde ne valide ni syntaxe T-SQL ni vérité
des états, distinction correctement faite ailleurs dans le plan.

## Points vérifiés qui ne demandent pas de correction

- Le glob `*.sql` du test existant couvrira effectivement le nouveau fichier.
- L'ordre writes → context changes → batch separators correspond à `main.go` ; le
  durcissement du test est pertinent.
- Sur un état stable et une jointure unique, l'agrégat précède le TOP de présentation :
  une candidate conforme après vingt noms non conformes influence le verdict.
- Les seuils NULL ne traversent pas le ELSE vers `OK`. Un prédicat produit `UNKNOWN`,
  sauf défaut certain prioritaire.
- `FOR XML PATH`, `TYPE` et `.value` décodent correctement les caractères XML de V12.
- L'exclusion de `MAX_DURATION` est explicite ; cette revue ne demande pas de réintroduire
  cette colonne indisponible sur les anciennes versions.

## Conditions de sortie recommandées

1. Corriger l'identité des sessions et la déduction de complétude AG.
2. Fixer avec la spec le contrat de permission manquante et la portée exacte de `OK`.
3. Isoler chaque cas, qualifier ses assertions par colonne et préparer sa restauration
   complète, y compris après échec.
4. Ajouter les contre-exemples collation/visibilité AG ; annoncer les garanties non validées
   lorsque les logins ou moteurs nécessaires ne sont pas disponibles.
5. Consigner les résultats réellement mesurés, versions non testées et limites de
   cohérence/stockage. Le passage du garde et des cas laissés non exécutés ne constituent
   pas une validation fonctionnelle complète.

