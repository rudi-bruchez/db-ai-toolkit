/*  Backups, restores, ALTER and DBCC statements running now, with what
    blocks them, what they wait on, and how far they are.

    No parameter. Read-only. Run it with -maxrows 100. One row per request
    whose command starts with BACKUP, RESTORE, ALTER, DBCC, CREATE INDEX or
    is KILLED/ROLLBACK, or that reports a percent_complete; at most 100,
    longest running first.

    The columns.
      - database_name is the database the request runs in, not the target
        of the command: an ALTER DATABASE sent from master reads master.
        The target is in statement, and for a lock wait in wait_resource
        ("DATABASE: 14:..." is database_id 14).
      - percent_complete and estimated_completion_s are filled only by
        commands that report progress (backup, restore, DBCC CHECK*, ALTER
        INDEX REORGANIZE, SHRINK, rollback). 0 elsewhere.
      - wait_type and wait_ms are the wait at the moment of the read.
        LCK_M_* with a blocking_session_id is a lock wait: read the
        blocker_* columns. A backup through VSS shows as a request of
        program "Microsoft SQL Server VSS Writer" or of the backup
        product's own program name.
      - blocker_* describe the session named by blocking_session_id: its
        command and wait when it is running a request, its status (often
        "sleeping") and open_transaction_count when it is not. A sleeping
        blocker with an open transaction is an application that did not
        commit.
      - blocks_count is how many requests wait on this one.
      - statement is the current statement of the request, cut to 300
        characters. It can contain literals: paths, names, values.

    This is a single read. A request that finished a second before is not
    there; run it twice, some seconds apart, to see progress.

    Permissions: VIEW SERVER STATE, or VIEW SERVER PERFORMANCE STATE from
    SQL Server 2022. Without it the view shows only the login's own session,
    so the query raises error 50000 instead of returning an empty result.

    Written for SQL Server 2012 and later. Tested on SQL Server 2022.
*/
IF ISNULL(HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER STATE'), 0) = 0
   AND ISNULL(HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER PERFORMANCE STATE'), 0) = 0
BEGIN
    RAISERROR(N'This login lacks VIEW SERVER STATE: it would see only its own session.', 16, 1);
    RETURN;
END;

SELECT TOP (100)
    r.session_id,
    r.command,
    DB_NAME(r.database_id)                               AS database_name,
    r.status,
    CONVERT(varchar(19), r.start_time, 126)              AS start_time,
    DATEDIFF(second, r.start_time, GETDATE())            AS elapsed_s,
    CAST(r.percent_complete AS decimal(5, 1))            AS percent_complete,
    r.estimated_completion_time / 1000                   AS estimated_completion_s,
    r.wait_type,
    r.wait_time                                          AS wait_ms,
    r.wait_resource,
    r.last_wait_type,
    NULLIF(r.blocking_session_id, 0)                     AS blocking_session_id,
    br.command                                           AS blocker_command,
    bs.status                                            AS blocker_status,
    bs.program_name                                      AS blocker_program,
    br.wait_type                                         AS blocker_wait_type,
    bs.open_transaction_count                            AS blocker_open_transactions,
    (SELECT COUNT(*) FROM sys.dm_exec_requests AS w
      WHERE w.blocking_session_id = r.session_id)        AS blocks_count,
    s.program_name,
    s.login_name,
    CAST(LEFT(SUBSTRING(t.text, r.statement_start_offset / 2 + 1,
         CASE WHEN r.statement_end_offset = -1 THEN 300
              ELSE (r.statement_end_offset - r.statement_start_offset) / 2 + 1 END), 300)
         AS nvarchar(300))                               AS statement
FROM sys.dm_exec_requests AS r
JOIN sys.dm_exec_sessions AS s
  ON s.session_id = r.session_id
LEFT JOIN sys.dm_exec_sessions AS bs
  ON bs.session_id = NULLIF(r.blocking_session_id, 0)
LEFT JOIN sys.dm_exec_requests AS br
  ON br.session_id = NULLIF(r.blocking_session_id, 0)
OUTER APPLY sys.dm_exec_sql_text(r.sql_handle) AS t
WHERE r.session_id <> @@SPID
  AND (r.command LIKE 'BACKUP%'
       OR r.command LIKE 'RESTORE%'
       OR r.command LIKE 'ALTER%'
       OR r.command LIKE 'DBCC%'
       OR r.command LIKE 'CREATE INDEX%'
       OR r.command = 'KILLED/ROLLBACK'
       OR r.percent_complete > 0)
ORDER BY r.start_time;
