# Design — catalogue de requêtes `sqlq` (promotion des requêtes ad-hoc en requêtes stockées)

Date : 2026-09-02
Statut : brainstorming terminé, en attente de relecture avant plan d'implémentation.
Prérequis : le design `2026-09-02-live-query-design.md`, implémenté et relu
(`docs/reviews/2026-09-02-sqlq-harm-review.md`).

## 1. Le problème

`sqlq` sait exécuter une requête. Il ne sait rien retenir. Chaque question posée
deux fois fait réécrire le même SQL par le modèle : même coût, et surtout **pas
la même requête** — donc pas la même réponse d'une semaine sur l'autre.

L'objectif formulé par l'utilisateur : que les requêtes deviennent, au fil des
demandes, « des choses déterministes, stockées, qu'on référence et qu'on appelle
sans plus y réfléchir ».

Ce qui est visé, dans l'ordre d'importance :

1. **La stabilité.** La même question donne la même requête, donc une réponse
   comparable dans le temps.
2. **La qualité cumulée.** Une requête corrigée une fois reste corrigée. Les
   pièges catalogue (compter les lignes sur `index_id IN (0,1)`, le SQL dynamique
   invisible aux dépendances) sont payés une seule fois.
3. **Le coût.** Réel mais secondaire, et honnêtement chiffré en §9.

## 2. Décisions prises au brainstorming

| Question | Décision |
|---|---|
| Où vivent les requêtes promues ? | Deux couches : le dépôt porte le canon générique, versionné et testé ; un répertoire personnel hors dépôt porte ce qui est propre à une base. |
| Qu'est-ce qui déclenche la promotion ? | L'agent propose la sauvegarde après chaque requête ad-hoc qui a réussi ; l'utilisateur accepte ou refuse en un mot. |
| Comment l'agent retrouve une requête ? | Index complet à la demande : `sqlq -list-queries` rend le catalogue en JSON. Pas de recherche par mots-clés en v1. |
| Comment borner la validité ? | La portée vient du répertoire. Rien à déclarer, rien à oublier. |

Contexte ajouté en cours de discussion : **le dépôt GitHub sera privé.** Cela
retire l'argument « des noms d'objets de production partent sur un dépôt public »
du choix de la double couche. La double couche reste retenue pour une raison plus
faible mais suffisante : sauvegarder à chaud sans commit, et séparer le canon
générique livré avec le plugin de ce qui n'a de sens que sur une base donnée.

## 3. Le principe qui porte tout le reste

> **On ne sauvegarde pas une requête. On sauvegarde une exécution réussie.**

```bash
sqlq -profile prod-erp -query "<sql>" -save-query index-fragmentation
```

La requête est exécutée d'abord. Le fichier n'est écrit **que si le run se
termine en code 0**. Conséquences :

- aucun fichier du catalogue ne peut exister sans avoir tourné au moins une fois
  contre une instance réelle ;
- la ligne `Verified:` est écrite par le binaire à partir de ce qu'il vient de
  faire, jamais affirmée par le modèle.

C'est la contre-mesure au risque principal du dispositif : une bibliothèque qui
se remplit de SQL plausible que personne n'a jamais exécuté, et qui donne au
lecteur suivant l'illusion d'un canon éprouvé.

## 4. Répartition des responsabilités

| Ce qui doit être **vrai** → `sqlq` (Go, testé) | Ce qui relève du **jugement** → skill `live-query` |
|---|---|
| lister le catalogue | reconnaître qu'aucune requête stockée ne répond |
| résoudre un nom en fichier | proposer la sauvegarde, et à quel nom |
| valider le nom, les paramètres, la portée | rédiger le résumé et la prose d'avertissement |
| écrire le fichier, refuser une collision | décider si la réponse obtenue valait la peine |
| refuser une requête hors de sa portée | présenter le résultat |

Le modèle propose du texte ; le binaire établit les faits. Aucune règle de
sécurité, de portée ou de nommage ne dépend du respect d'une consigne en langage
naturel.

## 5. Emplacements et portée

```
<plugin>/skills/live-query/queries/*.sql        portée « generic », livré, versionné, testé
~/.config/db-ai-toolkit/queries/<profil>/*.sql  portée « <profil> », personnel, non versionné
~/.config/db-ai-toolkit/queries/_generic/*.sql  portée « generic », personnel
```

- Une requête de portée `<profil>` **n'est exécutable que sur ce profil**.
  Ailleurs : refus, code 1, message nommant le fichier et sa portée.
- Une requête de portée `generic` est exécutable partout. C'est la promesse
  qu'elle ne touche que des vues système.
- **`sqlq` n'écrit jamais dans le dépôt.** `-save-query` écrit toujours dans la
  couche personnelle, sous le répertoire du profil utilisé. Promouvoir une
  requête en canon générique est un acte humain : déplacer le fichier dans
  `_generic/`, ou l'ajouter au dépôt via un commit relu — parce que la copie du
  dépôt engage aussi les autres.
- Le défaut est donc toujours le réglage étroit. Élargir est délibéré.
- **Le catalogue ne contient que des requêtes de lecture.** `-save-query` refuse
  toute requête que `FindWrites` signale, y compris sur un profil `readwrite` et
  y compris quand `-allow-write` a été donné. Sans cette règle, le dispositif
  fabriquerait une bibliothèque d'instructions destructrices appelables par un
  nom court — la classe F3 de la revue, en pire, parce que le nom masque
  l'instruction.
- Si le répertoire personnel n'existe pas, le catalogue est simplement plus
  court. Ce n'est pas une erreur.

### Localisation des requêtes livrées

`sqlq` trouve le répertoire du dépôt **relativement à son propre exécutable** :
`<dir(os.Executable())>/../skills/live-query/queries/`. C'est déterministe une
fois le plugin installé, et cela n'exige ni variable d'environnement ni
configuration. `-queries <dir>` force le chemin, pour les tests et le
développement hors plugin.

## 6. Format d'un fichier de requête

Un `.sql` ordinaire, exécutable tel quel dans SSMS ou Azure Data Studio. Le
format est **celui des cinq requêtes déjà livrées** : aucun des fichiers
existants n'a besoin d'être modifié.

```sql
/*  Largest user tables in the current database, by space reserved.

    Parameters: none.
    Verified: 2026-09-02 on SRV01/ERP

    Why this shape rather than the obvious one: ...
*/
SELECT ...
```

Règles d'analyse, dans l'ordre :

1. Le fichier doit ouvrir par un bloc `/* … */`. Sinon : fichier invalide, il
   n'entre pas au catalogue et le test échoue.
2. **La première ligne non vide du bloc est le résumé.** C'est la seule chose que
   le catalogue montre en plus du nom.
3. Le bloc `Parameter:` / `Parameters:` déclare les paramètres, sous la forme
   déjà en usage (`Parameter: @name - the object…`, ou `Parameters: none.`).
4. `Verified: <date> on <serveur>/<base>` est écrit par `sqlq`. **Son absence
   signifie « jamais exécuté »** — pas besoin d'une valeur pour le dire, et un
   oubli penche du côté prudent.
5. Tout le reste est de la prose destinée au lecteur humain, ignorée du
   catalogue.

Il n'y a **pas de clé `name`** : le nom est le nom du fichier. Une seule source
de vérité, impossible à désynchroniser.

### Nommage

`^[a-z][a-z0-9-]{1,48}$`. Cette validation n'est pas cosmétique : `-save-query`
écrit un fichier à un chemin dérivé d'un nom **proposé par un modèle**.
`../../../.ssh/authorized_keys` doit être refusé par une règle, pas par la
politesse. Le nom est validé avant toute construction de chemin.

### Les cinq requêtes livrées sont « jamais vérifiées »

Elles n'ont jamais tourné contre une instance. Elles n'auront donc pas de ligne
`Verified:`, et le catalogue l'exposera. L'agent peut alors dire « je lance
celle-ci, elle n'a jamais été exécutée » au lieu de la présenter comme éprouvée.
C'est un gain de franchise, pas un défaut à masquer.

## 7. Contrat CLI

Trois sources de SQL, mutuellement exclusives : `-query`, `-file`, `-saved`.

| Drapeau | Effet |
|---|---|
| `-list-queries` | imprime le catalogue en JSON et sort. N'exige pas de profil. |
| `-saved <nom>` | exécute la requête stockée de ce nom |
| `-save-query <nom>` | après un run réussi, écrit la requête dans la couche personnelle du profil |
| `-queries <dir>` | force le répertoire des requêtes livrées (tests, développement) |

Catalogue :

```json
{"queries":[
  {"name":"tables-largest","summary":"Largest user tables in the current database, by space reserved.",
   "params":[],"scope":"generic","source":"bundled","verified":null},
  {"name":"orders-late","summary":"Orders past their promised date, by customer.",
   "params":["cutoff"],"scope":"prod-erp","source":"personal","verified":"2026-09-02 on SRV01/ERP"}
]}
```

Codes de sortie : inchangés. `3` reste réservé au garde-fou d'écriture. Les
nouveaux refus — nom inconnu, nom invalide, collision, portée incompatible,
paramètre manquant — sont des erreurs d'usage, donc `1`, avec le JSON habituel
sur stdout.

## 8. Validation des paramètres

Un paramètre est un `@x` **référencé et non déclaré** dans le fichier. La
détection s'appuie sur `Sanitize`, déjà écrit : commentaires et littéraux sont
retirés avant l'analyse, ce qui neutralise les faux positifs du genre
`LIKE '%@@ROWCOUNT%'` dans `triggers-inventory.sql`.

- variables globales (`@@VERSION`, `@@ROWCOUNT`) : exclues, elles commencent par
  `@@` ;
- variables locales : tout `@x` introduit par un `DECLARE` (y compris les listes
  `DECLARE @a INT, @b INT`) ;
- ce qui reste : les paramètres.

Deux bénéfices, tous deux mécaniques :

- `sqlq -saved object-references` sans `-param name=…` échoue immédiatement avec
  « paramètre `name` requis », au lieu du `Must declare the scalar variable
  "@name"` renvoyé par le serveur à la ligne 12 ;
- l'en-tête ne peut plus mentir sur ses paramètres : un test compare la
  déclaration au SQL.

## 9. Le compte de tokens, honnêtement

Le gain n'est pas l'index. Il est que **le SQL n'entre jamais dans le contexte** :
avec `-saved tables-largest`, le modèle envoie un nom et reçoit des lignes. Les
60 lignes du fichier ne sont lues par personne.

| | Coût réel |
|---|---|
| Catalogue de 20 requêtes, une fois par session | ~600 tokens |
| Réécrire une requête comme `tables-largest` | ~900 tokens de sortie, plus 1 à 3 allers-retours d'erreur : 2000 à 4000 en pratique |
| Rejouer la même par son nom | ~15 tokens |

Rentable dès la première requête réutilisée dans une session. Ce que le
dispositif **ne** réduit pas : les lignes de résultat, qui restent le poste
principal. `TOP (n)` et `-maxrows` gardent toute leur importance.

## 10. Changements au skill `live-query`

- La table de décision disparaît. Elle est remplacée par une règle unique :
  **appeler `-list-queries` avant d'écrire du SQL.** Les descriptions ne vivent
  plus qu'à un seul endroit — les fichiers — donc elles ne peuvent plus diverger
  du réel. C'est la même classe de défaut que celle mécanisée le matin même
  (`TestEveryFlagIsDocumented`).
- Après toute requête ad-hoc réussie et utile : proposer la sauvegarde en une
  ligne, avec un nom proposé. Ne jamais sauvegarder sans accord explicite.
- Ne pas proposer la sauvegarde d'une requête qui a tourné mais dont la réponse
  n'a servi à rien : la bibliothèque doit refléter les questions qui se posent,
  pas les tentatives.
- Quand une requête sans ligne `Verified:` est exécutée, le dire.

## 11. Tests

Chacun tue une classe de défaut, plutôt que de la laisser à la relecture.

| Test | Classe supprimée |
|---|---|
| `TestDeclaredParametersMatchTheSQL` | l'en-tête qui promet un paramètre que le SQL n'a pas, ou l'inverse |
| `TestCatalogListsExactlyTheFilesPresent` | l'entrée fantôme et la requête invisible |
| `TestEveryQueryHeaderParses` | le fichier sauvé sans résumé, donc introuvable |
| `TestBoundQueryRefusedOnAnotherProfile` | la bonne réponse à la mauvaise base |
| `TestQueryNameRejectsTraversalAndCase` | l'écriture hors du répertoire de requêtes |
| `TestNothingIsSavedWhenTheRunFailed` | du SQL jamais exécuté entré dans la bibliothèque |
| `TestSaveRefusesAnExistingName` | la requête qui en masque silencieusement une autre |
| `TestSaveRefusesAWritingQuery` | l'instruction destructrice devenue appelable par un nom court |
| `TestMissingParameterIsRefusedBeforeConnecting` | l'erreur serveur là où une erreur d'usage suffit |

S'y ajoutent les deux tests existants qui restent pertinents :
`TestBundledQueriesPassTheReadOnlyGuard` et `TestEveryFlagIsDocumented` — ce
dernier couvrira automatiquement les quatre nouveaux drapeaux.

## 12. Limites connues de la v1

- **`Verified:` atteste d'une exécution réussie, pas d'une réponse juste.** Une
  requête peut tourner et répondre à côté. Le jugement reste humain ; l'outil ne
  prétend qu'à ce qu'il a constaté.
- La couche personnelle n'est pas synchronisée : un même nom peut désigner deux
  requêtes différentes sur deux machines.
- Pas de versionnage des requêtes personnelles : une sauvegarde du même nom est
  refusée, une modification se fait dans l'éditeur.
- Une requête liée à un profil casse si le profil est renommé. Le message
  d'erreur nomme le fichier et la portée attendue.
- Pas de recherche par mots-clés. L'index complet suffit jusqu'à ~100 requêtes ;
  au-delà, ce sera une décision séparée.
- Trois environnements d'une même application (dev / staging / prod) ont trois
  profils, donc trois portées : une requête sauvée sur dev est refusée sur prod
  tant qu'elle n'est pas déplacée dans `_generic/`. Friction assumée : le défaut
  étroit est le bon défaut, et l'élargissement doit être un geste.

## 13. Points laissés ouverts pour la relecture

1. `-saved <nom>` comme nom de drapeau, à côté de `-query` et `-file` — ou plus
   court, `-q`, au prix d'une confusion possible avec `-query` ?
2. Ré-exécuter une requête stockée doit-il rafraîchir la ligne `Verified:` (elle
   devient « dernière vérification ») ou rester la première ?
3. Faut-il un `-widen` qui déplace une requête d'un profil vers `_generic/`, ou
   le déplacement manuel du fichier suffit-il ?
