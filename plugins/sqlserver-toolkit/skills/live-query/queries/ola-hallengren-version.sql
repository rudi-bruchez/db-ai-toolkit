/*  Version of Ola Hallengren's maintenance solution installed in the current
    database, object by object.

    No parameter. Read-only. Run it with -database <db> -maxrows 20, on each
    database where the solution may live: master by default, often a
    database dedicated to administration. One row per object found
    (CommandExecute, DatabaseBackup, DatabaseIntegrityCheck, IndexOptimize,
    the CommandLog, Queue and QueueDatabase tables), or one row with
    object_name NULL when there is none. A login without VIEW DEFINITION in
    the database gets error 50000: it would see the tables and not the
    procedures, and read a partial installation where there is none.

    The columns.
      - version is read from the "--// Version:" line of the procedure's
        text: the date and time of the release. NULL for a table, and for a
        procedure without that line (an old release, or a procedure of
        the same name that is not Ola Hallengren's).
      - Versions that differ between the procedures of one database mean a
        partial upgrade: CommandExecute is called by the three others, which
        expect a matching version.

    What the empty row means: none of these objects exists in this database.

    Permissions: VIEW DEFINITION in the database.

    Written for SQL Server 2012 and later. Tested on SQL Server 2022.

    Dates are returned as text without a time zone, in the server's local
    time.
*/
IF ISNULL(HAS_PERMS_BY_NAME(DB_NAME(), 'DATABASE', 'VIEW DEFINITION'), 0) = 0
BEGIN
    RAISERROR(N'This login lacks VIEW DEFINITION in this database: it cannot see the procedures nor read their version.', 16, 1);
    RETURN;
END;

WITH found AS (
    SELECT o.object_id, o.name, o.type_desc, o.create_date, o.modify_date,
           SCHEMA_NAME(o.schema_id) AS schema_name,
           OBJECT_DEFINITION(o.object_id) AS def
    FROM sys.objects AS o
    WHERE o.is_ms_shipped = 0
      AND ((o.type = 'P' AND o.name IN (N'CommandExecute', N'DatabaseBackup',
                                       N'DatabaseIntegrityCheck', N'IndexOptimize'))
        OR (o.type = 'U' AND o.name IN (N'CommandLog', N'Queue', N'QueueDatabase')))
),
shown AS (
    SELECT f.schema_name,
           f.name AS object_name,
           f.type_desc,
           CASE WHEN CHARINDEX(N'--// Version: ', f.def) > 0
                THEN CAST(SUBSTRING(f.def, CHARINDEX(N'--// Version: ', f.def) + 14, 19) AS nvarchar(19))
           END AS version,
           CONVERT(varchar(19), f.create_date, 126) AS create_date,
           CONVERT(varchar(19), f.modify_date, 126) AS modify_date
    FROM found AS f
    UNION ALL
    SELECT NULL, NULL, NULL, NULL, NULL, NULL
    WHERE NOT EXISTS (SELECT 1 FROM found)
)
SELECT TOP (20)
    DB_NAME() AS database_name,
    s.schema_name,
    s.object_name,
    s.type_desc,
    s.version,
    s.create_date,
    s.modify_date
FROM shown AS s
ORDER BY s.type_desc, s.object_name;
