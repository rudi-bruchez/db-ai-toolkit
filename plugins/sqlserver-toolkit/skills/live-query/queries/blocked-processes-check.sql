/*  Is everything in place for this instance to record blocked process reports?

    No parameter. Read-only. One row per event session that captures
    sqlserver.blocked_process_report (at most 20), or one row with NULL
    session columns when there is none or when the login cannot see them.

    READ instance_state, NOT THE ROWS.
      OK      - at the time of the check: the threshold is in use, a session
                captures the event without a filter, is running, starts with
                the instance, and has an event_file target defined and active.
      NOT_OK  - something on that list is missing. A row with a session_name is
                a session to repair, never a reason to add a second session.
      UNKNOWN - neither yes nor no: a permission is missing, the threshold
                could not be read, or the event carries a predicate that only
                a human can judge. Say so; do not round it to yes or no.

    What OK does NOT say:
      - that a report was ever produced or written. No file is read; the
        target's disk space and folder permissions are not checked.
      - that every block will be reported. The threshold is a minimum duration,
        and the monitor runs about every five seconds, best effort.
      - that a session with MAX_DURATION (SQL Server 2025, Managed Instance)
        will still run tomorrow. Not checked.
      - anything about another instance. In an availability group every
        replica needs its own session. Coverage is measured against the list
        of replicas the user confirms: complete when each of them has come back
        as the server_name of an OK check. ag_replicas helps build that list
        but does not prove it complete - a node that lost the cluster only sees
        itself, and without VIEW ANY DEFINITION the list is empty. Two profiles
        returning the same server_name are one instance.

    The views are read one after the other, not as one snapshot. If sessions
    are being created or stopped during the check, run it again.

    details_incomplete = 1 means more than 20 sessions matched; instance_state
    was still computed over all of them.

    sys.dm_xe_session_targets is deliberately not read: reading it flushes the
    collected data to disk. The active target is found in
    sys.dm_xe_session_object_columns instead.

    Permissions: VIEW SERVER PERFORMANCE STATE where it exists (2022+,
    Managed Instance; VIEW SERVER STATE implies it), VIEW SERVER STATE before.
    Without it, the first branch answers UNKNOWN without touching any
    protected view. Azure SQL Database has no server-scoped sessions: this
    query fails there with an error, which is the intended outcome.

    Runs on SQL Server 2012 and later. FOR XML PATH rather than STRING_AGG.
*/
DECLARE @perf int = HAS_PERMS_BY_NAME(NULL, NULL, N'VIEW SERVER PERFORMANCE STATE');
DECLARE @required_permission nvarchar(40) =
    CASE WHEN @perf IS NULL THEN N'VIEW SERVER STATE'
         ELSE N'VIEW SERVER PERFORMANCE STATE' END;
DECLARE @has_permission int =
    COALESCE(@perf, HAS_PERMS_BY_NAME(NULL, NULL, N'VIEW SERVER STATE'), 0);

IF @has_permission <> 1
    -- No protected view in this branch: the answer cannot depend on a view
    -- that might come back empty for lack of permission.
    SELECT CAST(N'UNKNOWN' AS nvarchar(10))                          AS instance_state,
           @@SERVERNAME                                              AS server_name,
           @required_permission                                      AS required_permission,
           @has_permission                                           AS has_permission,
           CAST(NULL AS int)                                         AS threshold_value,
           CAST(NULL AS int)                                         AS threshold_value_in_use,
           CAST(NULL AS int)                                         AS candidate_count,
           CAST(NULL AS int)                                         AS details_incomplete,
           CAST(SERVERPROPERTY('IsHadrEnabled') AS int)              AS is_hadr_enabled,
           CAST(NULL AS nvarchar(max))                               AS ag_replicas,
           CAST(NULL AS sysname)                                     AS session_name,
           CAST(N'UNKNOWN' AS nvarchar(10))                          AS session_state,
           CAST(N'missing ' + @required_permission AS nvarchar(max)) AS reasons,
           CAST(NULL AS bit)                                         AS startup_state,
           CAST(NULL AS int)                                         AS is_running,
           CAST(NULL AS int)                                         AS event_in_running_session,
           CAST(NULL AS nvarchar(3000))                              AS event_predicate,
           CAST(NULL AS int)                                         AS file_target_defined,
           CAST(NULL AS int)                                         AS file_target_running,
           CAST(NULL AS nvarchar(max))                               AS targets;
ELSE
BEGIN
    WITH cfg AS (
        -- An aggregate without GROUP BY always returns one row, NULLs if the
        -- setting is missing.
        SELECT MAX(CAST(c.value        AS int)) AS threshold_value,
               MAX(CAST(c.value_in_use AS int)) AS threshold_value_in_use
        FROM sys.configurations AS c
        WHERE c.name = N'blocked process threshold (s)'
    ),
    thr AS (
        SELECT cfg.threshold_value,
               cfg.threshold_value_in_use,
               CASE WHEN cfg.threshold_value IS NULL OR cfg.threshold_value_in_use IS NULL
                         THEN N'UNKNOWN'
                    WHEN cfg.threshold_value_in_use = 0 OR cfg.threshold_value = 0
                         THEN N'NOT_OK'
                    ELSE N'OK' END                                  AS threshold_state,
               CASE WHEN cfg.threshold_value IS NULL OR cfg.threshold_value_in_use IS NULL
                         THEN N'threshold unknown'
                    WHEN cfg.threshold_value_in_use = 0 AND cfg.threshold_value = 0
                         THEN N'threshold=0'
                    WHEN cfg.threshold_value_in_use = 0
                         THEN N'threshold set but not in use (RECONFIGURE pending)'
                    WHEN cfg.threshold_value = 0
                         THEN N'threshold disable pending (next RECONFIGURE turns reports off)'
               END                                                  AS threshold_reason
        FROM cfg
    ),
    cand AS (
        -- One row per session, chosen by EXISTS so that several targets or
        -- events never multiply it.
        SELECT s.event_session_id,
               s.name          AS session_name,
               s.startup_state
        FROM sys.server_event_sessions AS s
        WHERE EXISTS (SELECT 1
                      FROM sys.server_event_session_events AS e
                      WHERE e.event_session_id = s.event_session_id
                        AND e.package = N'sqlserver'
                        AND e.name    = N'blocked_process_report')
    ),
    detail AS (
        SELECT c.session_name,
               c.startup_state,
               CASE WHEN xs.address IS NULL THEN 0 ELSE 1 END       AS is_running,
               CASE WHEN EXISTS (SELECT 1
                                 FROM sys.dm_xe_session_events AS xe
                                 JOIN sys.dm_xe_packages AS p
                                   ON p.guid = xe.event_package_guid
                                 WHERE xe.event_session_address = xs.address
                                   AND xe.event_name = N'blocked_process_report'
                                   AND p.name        = N'sqlserver')
                    THEN 1 ELSE 0 END                               AS event_in_running_session,
               (SELECT TOP (1) e.predicate
                  FROM sys.server_event_session_events AS e
                 WHERE e.event_session_id = c.event_session_id
                   AND e.package = N'sqlserver'
                   AND e.name    = N'blocked_process_report'
                 ORDER BY e.event_id)                               AS event_predicate,
               CASE WHEN EXISTS (SELECT 1
                                 FROM sys.server_event_session_targets AS t
                                 WHERE t.event_session_id = c.event_session_id
                                   AND t.package = N'package0'
                                   AND t.name    = N'event_file')
                    THEN 1 ELSE 0 END                               AS file_target_defined,
               -- Not sys.dm_xe_session_targets: reading it flushes to disk.
               CASE WHEN EXISTS (SELECT 1
                                 FROM sys.dm_xe_session_object_columns AS oc
                                 JOIN sys.dm_xe_packages AS p
                                   ON p.guid = oc.object_package_guid
                                 WHERE oc.event_session_address = xs.address
                                   AND oc.object_type = N'target'
                                   AND oc.object_name = N'event_file'
                                   AND p.name         = N'package0')
                    THEN 1 ELSE 0 END                               AS file_target_running,
               STUFF((SELECT N', ' + t.name
                        FROM sys.server_event_session_targets AS t
                       WHERE t.event_session_id = c.event_session_id
                       ORDER BY t.name
                         FOR XML PATH(''), TYPE).value('.', 'nvarchar(max)'), 1, 2, N'')
                                                                    AS targets
        FROM cand AS c
        -- BIN2 on both sides: an exact match that does not depend on the
        -- database the profile opened.
        LEFT JOIN sys.dm_xe_sessions AS xs
          ON xs.name COLLATE Latin1_General_BIN2 = c.session_name COLLATE Latin1_General_BIN2
    ),
    judged AS (
        SELECT d.*,
               CASE WHEN thr.threshold_state = N'NOT_OK'
                      OR d.startup_state = 0
                      OR d.is_running = 0
                      OR (d.is_running = 1 AND d.event_in_running_session = 0)
                      OR d.file_target_defined = 0
                      OR (d.is_running = 1 AND d.file_target_defined = 1 AND d.file_target_running = 0)
                         THEN N'NOT_OK'
                    -- NULL never reaches OK: every unknown is named here.
                    WHEN thr.threshold_state = N'UNKNOWN'
                      OR d.startup_state IS NULL
                      OR d.event_predicate IS NOT NULL
                         THEN N'UNKNOWN'
                    ELSE N'OK' END                                  AS session_state,
               -- STUFF on an empty string returns NULL: reasons is NULL when OK.
               STUFF(COALESCE(N'; ' + thr.threshold_reason, N'')
                   + CASE WHEN d.startup_state = 0 THEN N'; startup_state=OFF' ELSE N'' END
                   + CASE WHEN d.is_running = 0 THEN N'; not running' ELSE N'' END
                   + CASE WHEN d.is_running = 1 AND d.event_in_running_session = 0
                          THEN N'; event not in running session' ELSE N'' END
                   + CASE WHEN d.file_target_defined = 0 THEN N'; no file target' ELSE N'' END
                   + CASE WHEN d.is_running = 1 AND d.file_target_defined = 1
                               AND d.file_target_running = 0
                          THEN N'; file target not running' ELSE N'' END
                   + CASE WHEN d.event_predicate IS NOT NULL
                          THEN N'; event filtered by predicate' ELSE N'' END,
                     1, 2, N'')                                     AS reasons
        FROM detail AS d
        CROSS JOIN thr
    ),
    inst AS (
        SELECT thr.threshold_value,
               thr.threshold_value_in_use,
               thr.threshold_reason,
               agg.candidate_count,
               CASE WHEN agg.any_ok = 1                     THEN N'OK'
                    WHEN agg.any_unknown = 1
                      OR thr.threshold_state = N'UNKNOWN'   THEN N'UNKNOWN'
                    ELSE N'NOT_OK' END                              AS instance_state,
               CASE WHEN agg.candidate_count > 20 THEN 1 ELSE 0 END AS details_incomplete,
               CAST(SERVERPROPERTY('IsHadrEnabled') AS int)         AS is_hadr_enabled,
               CASE WHEN CAST(SERVERPROPERTY('IsHadrEnabled') AS int) = 1
                    THEN STUFF((SELECT N', ' + ar.replica_server_name
                                  FROM sys.availability_replicas AS ar
                                 GROUP BY ar.replica_server_name
                                 ORDER BY ar.replica_server_name
                                   FOR XML PATH(''), TYPE).value('.', 'nvarchar(max)'), 1, 2, N'')
               END                                                  AS ag_replicas
        FROM thr
        CROSS JOIN (SELECT COUNT(*)                                                     AS candidate_count,
                           MAX(CASE WHEN session_state = N'OK'      THEN 1 ELSE 0 END) AS any_ok,
                           MAX(CASE WHEN session_state = N'UNKNOWN' THEN 1 ELSE 0 END) AS any_unknown
                      FROM judged) AS agg
    ),
    shown AS (
        SELECT TOP (20)
               j.session_name, j.session_state, j.reasons, j.startup_state, j.is_running,
               j.event_in_running_session, j.event_predicate, j.file_target_defined,
               j.file_target_running, j.targets,
               CASE j.session_state WHEN N'OK' THEN 0 WHEN N'UNKNOWN' THEN 1 ELSE 2 END
                                                                    AS sort_state
        FROM judged AS j
        ORDER BY sort_state, j.session_name
    ),
    sentinel AS (
        -- The single row when no session captures the event. Session columns
        -- are not applicable, so only "no session" is reported - no invented
        -- session defects.
        SELECT CAST(NULL AS sysname)                                AS session_name,
               N'NOT_OK'                                            AS session_state,
               COALESCE(inst.threshold_reason + N'; ', N'') + N'no session'
                                                                    AS reasons,
               CAST(NULL AS bit)                                    AS startup_state,
               CAST(NULL AS int)                                    AS is_running,
               CAST(NULL AS int)                                    AS event_in_running_session,
               CAST(NULL AS nvarchar(3000))                         AS event_predicate,
               CAST(NULL AS int)                                    AS file_target_defined,
               CAST(NULL AS int)                                    AS file_target_running,
               CAST(NULL AS nvarchar(max))                          AS targets,
               3                                                    AS sort_state
        FROM inst
        WHERE inst.candidate_count = 0
    )
    SELECT TOP (21)
           CAST(inst.instance_state AS nvarchar(10))                AS instance_state,
           @@SERVERNAME                                             AS server_name,
           @required_permission                                     AS required_permission,
           @has_permission                                          AS has_permission,
           inst.threshold_value,
           inst.threshold_value_in_use,
           inst.candidate_count,
           inst.details_incomplete,
           inst.is_hadr_enabled,
           inst.ag_replicas,
           r.session_name,
           CAST(r.session_state AS nvarchar(10))                    AS session_state,
           CAST(r.reasons AS nvarchar(max))                         AS reasons,
           r.startup_state,
           r.is_running,
           r.event_in_running_session,
           r.event_predicate,
           r.file_target_defined,
           r.file_target_running,
           r.targets
    FROM (SELECT * FROM shown UNION ALL SELECT * FROM sentinel) AS r
    CROSS JOIN inst
    ORDER BY r.sort_state, r.session_name;
END;
