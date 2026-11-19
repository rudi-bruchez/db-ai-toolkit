/*  Databases whose checkpoint or file growth settings are not the expected
    ones: indirect checkpoint off, or a file growing by a percentage.

    No parameter. Read-only. Run it with -maxrows 500. One row per finding,
    at most 500, by database. An empty result means no finding, never "not
    checked": a login that cannot see the files gets error 50000.

    The findings.
      - target_recovery_time_0: target_recovery_time_in_seconds = 0, so the
        database uses automatic checkpoints, not indirect checkpoint. Typical
        of a database created before SQL Server 2016 and upgraded, or created
        from a model database that was. The default since 2016 is 60.
        master and msdb can legitimately keep 0; is_system says which rows
        they are.
      - percent_growth: a data or log file grows by a percentage (growth_pct).
        Each growth is larger than the previous one; for a log file it also
        means virtual log files of uneven size. A row for model matters: new
        databases copy its settings. master and msdb are installed with
        10% growth on their files.

    The columns.
      - size_mb is the current size of the file, growth_pct its percentage.
        The tempdb rows show the size configured for the next start.
      - state_desc: an OFFLINE or RESTORING database is still listed, its
        settings read from the catalog.

    Permissions: sys.master_files shows rows only with CREATE DATABASE, ALTER
    ANY DATABASE or VIEW ANY DEFINITION. sys.databases needs VIEW ANY
    DATABASE, which public has by default.

    Written for SQL Server 2012 and later. Tested on SQL Server 2022.
*/
IF NOT EXISTS (SELECT 1 FROM sys.master_files)
BEGIN
    RAISERROR(N'sys.master_files shows no file to this login: grant VIEW ANY DEFINITION, or the growth check cannot be made.', 16, 1);
    RETURN;
END;

SELECT TOP (500)
    d.name                                           AS database_name,
    CAST(CASE WHEN d.database_id <= 4 THEN 1 ELSE 0 END AS bit) AS is_system,
    d.state_desc,
    f.finding,
    f.file_name,
    f.file_type,
    f.size_mb,
    f.growth_pct,
    d.target_recovery_time_in_seconds,
    d.compatibility_level
FROM sys.databases AS d
CROSS APPLY (
    SELECT N'target_recovery_time_0'  AS finding,
           CAST(NULL AS sysname)        AS file_name,
           CAST(NULL AS nvarchar(60))   AS file_type,
           CAST(NULL AS decimal(18, 1)) AS size_mb,
           CAST(NULL AS int)            AS growth_pct
    WHERE d.target_recovery_time_in_seconds = 0
    UNION ALL
    SELECT N'percent_growth',
           mf.name,
           mf.type_desc,
           CAST(mf.size * 8 / 1024.0 AS decimal(18, 1)),
           mf.growth
    FROM sys.master_files AS mf
    WHERE mf.database_id = d.database_id
      AND mf.is_percent_growth = 1
      AND mf.growth > 0
) AS f
ORDER BY d.name, f.finding, f.file_name;
