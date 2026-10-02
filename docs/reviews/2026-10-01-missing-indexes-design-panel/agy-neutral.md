## Verified
1. **Évitement du suffixe `Z` par conversion de date.** Le design convertit les dates en `varchar(19)` via `CONVERT(..., 126)` pour éviter le comportement de `sqlq`. C'est vérifié : dans `tools/cmd/sqlq/main.go`, `sqlq` n'ajoute le suffixe `Z` qu'aux objets `time.Time` (via `time.RFC3339Nano`). En renvoyant une chaîne de caractères, la valeur passe dans le cas `default` de `normalise()` et est émise telle quelle, évitant le faux décalage horaire.
2. **Utilisation de `OBJECTPROPERTY` pour la compatibilité.** L'appel à `OBJECTPROPERTY(object_id, 'TableIsMemoryOptimized')` permet bien d'éviter une erreur de compilation sur SQL Server 2012 (où la fonctionnalité In-Memory n'existait pas) contrairement à l'utilisation directe de `sys.tables.is_memory_optimized`. Sur 2012, la fonction renvoie simplement `NULL`.
3. **Interdiction de `INTO`.** Le design indique que la requête n'utilise pas `INTO`. C'est justifié car dans `tools/internal/sqlq/guard.go`, le dictionnaire `writeKeywords` inclut `"INTO": true`, bloquant tout `SELECT ... INTO` par le garde read-only.
4. **Comportement de `maxrows` et `truncated`.** Le mécanisme de troncature de `sqlq` (dans `collect()`) renverra bien les 150 premières lignes et lèvera `truncated = true` si la limite est atteinte. Le skill prévient l'agent que la dernière table est incomplète, rendant la limitation visible et gérée.

## Concluded by reasoning

**Détection du plafond de suggestions erronée sur les anciennes versions (`collection_capped`)**
Le design fixe la colonne `collection_capped` à 1 si le nombre de groupes sur l'instance atteint 600. Cependant, sur SQL Server 2012 à 2016, la limite interne de `sys.dm_db_missing_index_groups` est de 500 groupes (elle a été augmentée à 600 à partir de 2017). Sur ces versions, le compteur n'atteindra donc jamais 600. Conséquence silencieuse : lorsque la collecte plafonnera à 500, l'agent croira à tort qu'elle n'est pas plafonnée et conclura faussement que toutes les suggestions existantes ont été remontées.

**Faux positif sur le manque de permissions (`suggestions_on_hidden_objects`)**
Le design indique que les suggestions dont l'objet n'est pas visible sont comptées dans `suggestions_on_hidden_objects`, ce que le skill interprète par un manque de droits (`VIEW DEFINITION`). Or, la jointure qui récupère les noms exclut volontairement les objets système (`is_ms_shipped = 0`). Si l'optimiseur suggère un index sur une table système (ce qui peut arriver), la jointure échouera et cette suggestion tombera dans le bucket des objets cachés. L'agent accusera alors l'utilisateur à tort de manquer de permissions. La requête doit distinguer un objet exclu par design d'un objet rendu invisible par les droits.

## Not a problem
- L'omission de `user_updates` dans les lignes `missing` n'est pas bloquante, car l'agent peut lire l'intensité d'écriture de la table via la ligne `existing` de son index clustered ou heap.
- L'utilisation de `database_id = DB_ID()` sur `sys.dm_db_index_usage_stats` restreint correctement les statistiques d'usage aux index de la base courante.
- Le calcul de `used_mb` via `sys.dm_db_partition_stats.used_page_count` inclut bien les pages LOB, cette colonne agrégeant `in_row`, `lob` et `row_overflow`.
- Extraire la date la plus récente parmi `seek`, `scan` et `lookup` reste faisable en T-SQL 2012 via un sous-sélect sur `VALUES(...)`, même sans la fonction `GREATEST()`.
- Un résultat vide dû à un manque de droits sur la DMV est empêché : sans `VIEW SERVER STATE`, SQL Server lève une erreur franche interceptée par `sqlq` (code 2).
- Le tri imposé par `row_kind` (les `missing` avant les `existing`) est facilement implémentable via un `CASE` dans la clause `ORDER BY`.
