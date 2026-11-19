<!-- task-status: partial; date: 2026-11-19 -->
# sqlq : ce qu'il reste à faire

Relevé le 19/11/2026, sur l'usage de `sqlq` lors d'une mission chez un client du 10 au 19/11
(31 profils, tous `readonly`, instances joignables par Zscaler Private Access). Chaque point cite le moment
où le besoin est apparu.

## Maintenance

- Les tests de `internal/sqlq` échouent sous Windows : `TestBlockDirReadErrorIsAMessage`,
  `TestUnreadableFileIsRejected`, `TestUnreadableSubdirectoryIsAMessage` (un `chmod` ne rend
  rien illisible sous Windows) et `TestSymlinkIsRejectedNotFollowed` (créer un lien symbolique
  demande un privilège). Les sauter sous Windows, ou tester le refus autrement.
- `scripts/build-tools.ps1` ne compile rien dès qu'un test échoue, donc jamais sous Windows (les
  onze tests de `godta` s'ajoutent à ceux-ci). Résultat : le `sqlq.exe` en service le 19/11
  datait du 02/10, sans `-statistics` ni le catalogue. Compiler même quand les tests échouent
  en le signalant, ou séparer les tests dépendant de la plateforme.
- Supprimer `plugins/sqlserver-toolkit/bin/sqlq-2026.10.02.exe.old` une fois le binaire du
  19/11 validé à l'usage.

## Traitement

Le 19/11/2026, la partie maintenance :

- `internal/sqlq/catalog_test.go` : `lockForTest` saute le test sous Windows, et le nouveau
  `symlinkForTest` saute quand Windows refuse de créer le lien. Les quatre tests cités sont
  couverts.
- `cmd/godta/safety_test.go` : `TestSignalsCancel` appelait `syscall.Kill`, absent sous
  Windows, si bien que le paquet de tests ne compilait pas et que `go vet` échouait. Le test
  passe dans `cmd/godta/signals_unix_test.go`, réservé à Unix. `GOOS=windows go vet ./...`
  passe désormais.
- `scripts/build-tools.ps1` et `build-tools.sh` : un test en échec n'empêche plus la
  compilation ; le script compile, avertit et sort avec le code 1. `go vet` et `go build`
  restent bloquants, et le script PowerShell contrôle enfin `$LASTEXITCODE` après chaque
  commande `go`. `build-tools.sh` est rendu exécutable. Vérifié sous Linux (bash et `pwsh`)
  avec un test volontairement en échec.

Reste à faire :

- lancer `go test ./...` sous Windows : la machine Windows n'était pas joignable, donc la
  liste des tests de `godta` qui échouent à l'exécution n'est pas établie. Le défaut de
  compilation trouvé en explique peut-être une partie ;
- supprimer `sqlq-2026.10.02.exe.old`, qui n'existe que sur le poste Windows, après usage du
  nouveau binaire.


Requêtes réécrites à la main plusieurs fois dans la semaine, à faire entrer au catalogue :

- historique `msdb.dbo.backupset` d'une base sur N jours (type, début, durée, `is_snapshot`) ;
- travaux de l'Agent avec leur dernier résultat, sans `syscategories` (refusé à `dba_ro` sur
  une instance de production, 10/11) ;
- bases non conformes : `target_recovery_time_in_seconds = 0` ou fichier en croissance par
  pourcentage (relevé de parc du 18/11) ;
- requêtes de sauvegarde ou d'`ALTER` en cours, avec bloqueur, attente et `percent_complete`
  (`ALTER DATABASE` bloqué derrière Azure Backup et VSS, 18/11) ;
- topologie CPU : vCPU, sockets, cœurs, schedulers hors ligne, MAXDOP (18/11) ;
- version d'Ola Hallengren par base (`--// Version:` dans `sql_modules`, à chercher dans `master`,
  `_DBA` et `OPSDB`, 10/11).

## Fonctionnalités, par ordre d'utilité

1. Plusieurs profils d'une commande : `-profiles-match 'prd/*'` ou `-all`, parallélisme
   borné, une colonne `profile` sur chaque ligne, un JSON agrégé. Le 18/11, la boucle shell sur
   31 profils a rencontré trois pièges : CRLF de Python dans les noms de profils, avertissement
   mêlé au JSON par `2>&1`, `EOF` sur 4 profils à 31 connexions simultanées (deux répondaient
   relancés un par un). Besoin rencontré au moins quatre fois dans la semaine : éditions, vCPU,
   checkpoint et autogrowth, purge Ola sur 19 instances.
2. Réessai et classement des erreurs de connexion. Un `EOF` vient souvent de ZPA, pas de
   l'instance : le 10/11, une instance répondait 20 s plus tard. Réessayer les erreurs
   passagères avec un délai, et rendre une catégorie (`connect_transient`, `connect_timeout`,
   `login_failed`, `sql_error`). Ne jamais réessayer un échec de login.
3. Avertissements dans le JSON. L'avertissement « `RegSrvr17.xml` a changé depuis l'import »
   part sur stderr et casse le parsing dès qu'on redirige stderr vers stdout. Un champ
   `warnings` dans l'objet de sortie.
4. Sortie tabulaire, `-format tsv|md`. Le JSON sur une ligne oblige à sélectionner peu de
   colonnes, et les tableaux des mails (vCPU du 18/11, RPO du 19/11) sont recopiés à la main.
   Déjà évoqué dans `sqlq-server-messages.md`, avec son coût : un second format à maintenir.
5. Deux lectures et leur différence : `-repeat 2 -interval 20`, ou un mode delta. Le 18/11,
   `sys.dm_io_virtual_file_stats` a été lu deux fois à la main pour voir quelles bases
   écrivaient. Même geste pour les attentes, les compteurs de performance et
   `dm_exec_requests` pendant un blocage.
6. Liste blanche fermée de procédures système en lecture seule dans le garde `readonly`, à
   commencer par `xp_readerrorlog`. Le 18/11, un gel de `master` pendant un instantané VSS n'a
   pas pu être confirmé par le journal d'erreurs, faute de pouvoir l'appeler. Point le plus
   délicat : il ouvre une exception dans une règle simple, et chaque entrée de la liste doit
   être testée. La sortie se lirait ensuite avec `errorlog-parse`.
