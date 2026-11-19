/*  Backup history of one database over the last N days, read from msdb.

    Parameters: @days - how many days back, from 1 to 366; @name - the
                database, as msdb records it.

    Read-only. Run it with -maxrows 500. One row per backup set, newest
    first, at most 500. A name msdb has never recorded and that is not a
    database of this instance raises error 50000 instead of an empty result,
    which would read as "no backup".

    The columns.
      - backup_type: D full, I differential, L log, F file or filegroup,
        G differential file, P partial, Q differential partial.
      - is_snapshot = 1: a snapshot backup taken through VSS or the
        snapshot API (Azure Backup, a hypervisor, a storage array, a backup
        product). Its duration says nothing about the data copied: the
        snapshot is taken outside SQL Server, which only freezes the I/O of
        the database for its duration.
      - is_copy_only = 1: outside the backup chain. A copy-only full is not
        the base of the following differentials.
      - device_type: 2 disk, 5 tape, 7 virtual device (VSS and third-party
        backup software), 9 URL (Azure Blob Storage). physical_device_name
        is the first device of the media set; stripe_count says how many
        there are. For a virtual device it is a GUID, not a path.
      - duration_s is finish minus start, to the second.
      - compressed_mb equals size_mb when the backup was not compressed.

    What this does not say.
      - That the backup can be restored, nor that the file still exists.
      - Anything about a backup taken on another replica of an availability
        group: each replica records its own backups in its own msdb. Run it
        on every replica.
      - Anything older than the history kept: sp_delete_backuphistory and the
        maintenance plans purge msdb, so "nothing before day X" may mean
        "purged", not "no backup".

    Permissions: SELECT on msdb.dbo.backupset and msdb.dbo.backupmediafamily,
    usually through db_datareader in msdb. Without it, error 229.

    Written for SQL Server 2012 and later. Tested on SQL Server 2022.

    Dates are returned as text without a time zone, in the server's local
    time: sqlq renders a datetime with a false Z suffix.
*/
DECLARE @window int = TRY_CONVERT(int, @days);

IF @window IS NULL OR @window NOT BETWEEN 1 AND 366
BEGIN
    RAISERROR(N'days must be a whole number from 1 to 366.', 16, 1);
    RETURN;
END;

IF DB_ID(@name) IS NULL
   AND NOT EXISTS (SELECT 1 FROM msdb.dbo.backupset AS b WHERE b.database_name = @name)
BEGIN
    RAISERROR(N'No database named %s on this instance, and none in the backup history.', 16, 1, @name);
    RETURN;
END;

SELECT TOP (500)
    b.database_name,
    CONVERT(varchar(19), b.backup_start_date, 126)        AS backup_start,
    CONVERT(varchar(19), b.backup_finish_date, 126)       AS backup_finish,
    DATEDIFF(second, b.backup_start_date, b.backup_finish_date) AS duration_s,
    b.type                                                AS backup_type,
    CAST(b.is_snapshot AS bit)                            AS is_snapshot,
    CAST(b.is_copy_only AS bit)                           AS is_copy_only,
    CAST(b.backup_size / 1048576.0 AS decimal(18, 1))     AS size_mb,
    CAST(b.compressed_backup_size / 1048576.0 AS decimal(18, 1)) AS compressed_mb,
    b.recovery_model,
    mf.device_type,
    mf.physical_device_name,
    (SELECT COUNT(*)
       FROM msdb.dbo.backupmediafamily AS f
      WHERE f.media_set_id = b.media_set_id
        AND f.mirror = 0)                                 AS stripe_count,
    b.machine_name,
    b.server_name
FROM msdb.dbo.backupset AS b
LEFT JOIN msdb.dbo.backupmediafamily AS mf
  ON mf.media_set_id = b.media_set_id
 AND mf.family_sequence_number = 1
 AND mf.mirror = 0
WHERE b.database_name = @name
  AND b.backup_start_date >= DATEADD(day, -@window, GETDATE())
ORDER BY b.backup_start_date DESC;
