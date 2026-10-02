# Revue des risques de missing-indexes

Branche `feat/missing-indexes`, après la validation (`ffb3c91`). Le skill
`adversarial-harm-review` n'étant pas installé sur ce poste, la revue a été faite par un
sous-agent Opus, sans accès à aucune base, avec une seule question : où ce résultat, lu en
suivant le protocole, fait-il conclure faux ou agir à tort sur une base de production ? Il a lu
la spec, la requête, le protocole, `SKILL.md`, le document de validation, le tri de la revue de
code et `AGENTS.md`, et vérifié les affirmations sur le moteur dans Microsoft Learn.

Il ne trouve rien qui mène à une écriture ou à une suppression : les interdits de DDL et de
suppression sont cohérents dans l'en-tête, le protocole et `SKILL.md`. Les risques sont des
propositions fausses remises à un DBA. Huit retenus, six écartés par le relecteur lui-même.

## Constats et traitement

| # | constat | nature | traitement |
|---|---|---|---|
| 1 | Sur un secondaire lisible d'un groupe de disponibilité, les écritures arrivent par redo : `user_updates` vaut presque 0 et toute table paraît peu écrite. Le §4 fait alors proposer un index que chaque écriture du primaire entretiendra. `database_in_ag` est vrai aussi sur le secondaire, et le résultat ne dit pas lequel est le primaire. | raisonné ; Learn définit `user_updates` comme « updates by user queries » | §4 : vérifier `is_primary_replica` dans `sys.dm_hadr_database_replica_states` avant de citer un chiffre d'écriture ; sur un secondaire, coût d'écriture inconnu. §1 : le résultat ne dit pas si la réplique est le primaire. Pas de nouvelle colonne : elle changerait le contrat de 38 colonnes et demanderait de rejouer la validation, alors qu'une lecture suffit. |
| 2 | Élargir la clé d'un index non unique repousse vers la droite la clé clustered implicite : une requête qui cherchait ou triait sur `(a, id)` perd sa recherche. | vérifié (guide d'architecture des index, localisateurs de ligne) | §3.2 : le dire ; si les `user_seeks` de l'index ne sont pas négligeables, proposer un nouvel index ou dire que l'élargissement est à vérifier contre ses requêtes. |
| 3 | Le §4 compare écritures et lectures sans règle de décision, et `user_updates` compte des instructions, pas des lignes. | vérifié : Learn, `sys.dm_db_index_usage_stats` : « if you delete 1000 rows in one statement, this count increments by 1 » | §4 réécrit : rapporter les chiffres sans en tirer de verdict ; un chargement en masse compte 1. |
| 4 | Une base fermée par AUTO_CLOSE, restaurée ou détachée a des compteurs bien plus jeunes que l'instance, sans date dans le résultat ; le §4 pèse quand même. | raisonné ; M11 a vu AUTO_CLOSE vider les suggestions | §1 : AUTO_CLOSE vrai, fenêtre inconnue, §4 sans conclusion. §5 : restauration, détachement ou mise hors ligne raccourcissent la fenêtre sans laisser de date. |
| 5 | Le protocole ne lit jamais `used_mb` ; créer ou élargir un index se fait hors ligne hors édition Enterprise, et élargir reconstruit un index en service. | vérifié (tableau des éditions de SQL Server 2022) | §5 : citer `used_mb` et le dire. |
| 6 | Élargir une clé peut dépasser 1 700 octets ou 16 colonnes : l'index se crée avec un avertissement, puis des insertions ou mises à jour échouent. | vérifié (`CREATE INDEX`) | §3.3 : vérifier `max_length` dans `sys.columns` avant d'élargir une clé, sinon passer par `INCLUDE`. |
| 7 | « Remember the clustered key » n'était pas une règle : deux agents traitaient différemment une suggestion qui contient la clé clustered. | vérifié | §3 : ajouter la clé clustered à chaque index non clustered avant de comparer (fin de clé si non unique, `INCLUDE` si unique) ; retirer ses colonnes des `included_columns` suggérées. |
| 8 | L'en-tête dit « a unique index or primary key is widened through INCLUDE only » alors que le protocole interdit d'élargir un index clustered, et la plupart des clés primaires sont clustered. | vérifié | En-tête : « a nonclustered unique index or primary key is widened through INCLUDE only, and a clustered index is never widened ». |

## Écartés par le relecteur, et pourquoi ils restent écartés

- Le §2 interdit de créer un index quand des index ne sont pas montrés, mais pas d'en élargir
  un : l'élargissement porte sur un index montré, donc connu. Le risque de doublon avec un index
  caché reste mineur.
- Littéraux de `filter_definition` et noms d'objets dans la transcription : déjà accepté par
  `AGENTS.md` pour toute sortie de `SELECT`.
- Tables masquées par les droits qui sortent du classement : l'en-tête le dit, et M9 montre
  qu'elles sont comptées.
- Index désactivé qui correspondrait à une suggestion : le protocole dit qu'il ne couvre rien ;
  proposer de le reconstruire plutôt qu'un doublon est une amélioration, pas un risque.
- Alignement des partitions d'un nouvel index : la clause `ON` par défaut aligne.
- Coût de la requête : au plus 600 groupes, 3 tables et 45 lignes d'index ; sans effet notable.

## Vérifications

Aucune correction ne touche le texte exécuté de la requête : seul l'en-tête change. Les cas de
validation n'ont donc pas à être rejoués. `cd tools && go test ./... && go vet ./...` : PASS.
