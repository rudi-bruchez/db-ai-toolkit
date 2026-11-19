/*  SQL Server Agent jobs with the outcome of their last run.

    No parameter. Read-only. Run it with -maxrows 300. One row per job, at
    most 300, failed last runs first, then by name.

    Reads msdb.dbo.sysjobs, sysjobservers, sysjobschedules, sysjobhistory,
    sysjobactivity and syssessions only. msdb.dbo.syscategories is left out
    on purpose: it is refused to logins that can read the other tables, and
    the category is not needed to know whether a job works.

    The columns.
      - last_outcome: failed, succeeded, retry, canceled, in progress or
        unknown, from sysjobservers. never_run = 1 when there is no last run.
      - last_message is the job outcome line of the last run in
        sysjobhistory (step 0), cut to 400 characters. It is NULL when the
        history was purged, which the Agent does by default past 1,000 rows,
        and 100 rows per job.
      - is_running = 1 when the current Agent session started the job and it
        has not stopped. With the Agent stopped, every job reads 0.
      - next_run comes from sysjobschedules, which the Agent refreshes about
        every 20 minutes: it can lag behind a schedule just changed.
      - owner is NULL when the owning login no longer exists, a frequent
        cause of failure.

    Dates are returned as text without a time zone, in the server's local
    time.

    Permissions: SELECT on those msdb tables, usually through db_datareader
    in msdb. The SQLAgent roles alone do not grant it: error 229.

    Written for SQL Server 2012 and later. Tested on SQL Server 2022.
*/
WITH
last_hist AS (
    SELECT h.job_id, h.message,
           ROW_NUMBER() OVER (PARTITION BY h.job_id
                              ORDER BY h.run_date DESC, h.run_time DESC, h.instance_id DESC) AS rn
    FROM msdb.dbo.sysjobhistory AS h
    WHERE h.step_id = 0
),
next_sched AS (
    SELECT js.job_id, MIN(js.next_run_date * CAST(1000000 AS bigint) + js.next_run_time) AS next_run
    FROM msdb.dbo.sysjobschedules AS js
    WHERE js.next_run_date > 0
    GROUP BY js.job_id
),
running AS (
    SELECT a.job_id
    FROM msdb.dbo.sysjobactivity AS a
    WHERE a.session_id = (SELECT MAX(s.session_id) FROM msdb.dbo.syssessions AS s)
      AND a.start_execution_date IS NOT NULL
      AND a.stop_execution_date IS NULL
)
SELECT TOP (300)
    j.name                                              AS job_name,
    CAST(j.enabled AS bit)                              AS is_enabled,
    SUSER_SNAME(j.owner_sid)                            AS owner,
    CASE js.last_run_outcome
        WHEN 0 THEN N'failed'
        WHEN 1 THEN N'succeeded'
        WHEN 2 THEN N'retry'
        WHEN 3 THEN N'canceled'
        WHEN 4 THEN N'in progress'
        ELSE N'unknown' END                             AS last_outcome,
    CAST(CASE WHEN js.last_run_date IS NULL OR js.last_run_date = 0 THEN 1 ELSE 0 END AS bit) AS never_run,
    CASE WHEN js.last_run_date > 0 THEN
        STUFF(STUFF(CAST(js.last_run_date AS char(8)), 7, 0, '-'), 5, 0, '-') + 'T'
        + STUFF(STUFF(RIGHT('000000' + CAST(js.last_run_time AS varchar(6)), 6), 5, 0, ':'), 3, 0, ':')
    END                                                 AS last_run,
    (js.last_run_duration / 10000) * 3600
      + (js.last_run_duration / 100 % 100) * 60
      + js.last_run_duration % 100                      AS last_duration_s,
    CAST(LEFT(lh.message, 400) AS nvarchar(400))        AS last_message,
    CAST(CASE WHEN r.job_id IS NULL THEN 0 ELSE 1 END AS bit) AS is_running,
    CASE WHEN ns.next_run IS NOT NULL THEN
        STUFF(STUFF(CAST(ns.next_run / 1000000 AS char(8)), 7, 0, '-'), 5, 0, '-') + 'T'
        + STUFF(STUFF(RIGHT('000000' + CAST(ns.next_run % 1000000 AS varchar(6)), 6), 5, 0, ':'), 3, 0, ':')
    END                                                 AS next_run,
    (SELECT COUNT(*) FROM msdb.dbo.sysjobschedules AS c WHERE c.job_id = j.job_id) AS schedule_count
FROM msdb.dbo.sysjobs AS j
LEFT JOIN msdb.dbo.sysjobservers AS js
  ON js.job_id = j.job_id
LEFT JOIN last_hist AS lh
  ON lh.job_id = j.job_id AND lh.rn = 1
LEFT JOIN next_sched AS ns
  ON ns.job_id = j.job_id
LEFT JOIN running AS r
  ON r.job_id = j.job_id
ORDER BY CASE WHEN js.last_run_outcome = 0 THEN 0 ELSE 1 END, j.name;
