/*  Every DML trigger in the database, with cheap triage hints.

    Parameters: none.

    The flag columns are HINTS FROM TEXT SEARCH, not verdicts. They tell you
    which trigger bodies are worth reading first. Never report one as a defect
    without reading the definition.

      sets_nocount          SET NOCOUNT ON appears. Its absence means every
                            fire sends a rowcount message back to the client,
                            which breaks some data-access layers.
      reads_rowcount        @@ROWCOUNT appears. Often part of the single-row
                            assumption, as in "IF @@ROWCOUNT = 1".
      scalar_from_inserted  A scalar is assigned from inserted/deleted. This is
                            the classic multi-row bug: inserted and deleted are
                            TABLES, and a multi-row statement fires the trigger
                            once with all rows in them. SELECT @id = id FROM
                            inserted silently keeps one arbitrary row.
      has_cursor            A cursor appears. Row-by-row work inside a trigger
                            runs inside the caller's transaction.
      has_rollback          ROLLBACK appears. A rollback inside a trigger aborts
                            the whole outer transaction, not just the trigger.
      has_transaction       An explicit transaction is started inside the
                            trigger, which is already running in one.

    parent_class = 1 restricts this to triggers on tables and views. DDL and
    logon triggers live elsewhere; query sys.server_triggers for those.
*/
SELECT
    OBJECT_SCHEMA_NAME(t.parent_id)  AS [parent_schema],
    OBJECT_NAME(t.parent_id)         AS [parent_object],
    SCHEMA_NAME(o.schema_id)         AS [trigger_schema],
    t.name                           AS [trigger_name],
    t.is_disabled,
    t.is_instead_of_trigger,
    o.modify_date,
    CASE WHEN m.definition LIKE '%NOCOUNT%'   THEN 1 ELSE 0 END AS [sets_nocount],
    CASE WHEN m.definition LIKE '%@@ROWCOUNT%' THEN 1 ELSE 0 END AS [reads_rowcount],
    CASE WHEN m.definition LIKE '%=%(SELECT%inserted%'
           OR m.definition LIKE '%=%(SELECT%deleted%'
           OR m.definition LIKE '%SELECT%@%=%FROM%inserted%'
           OR m.definition LIKE '%SELECT%@%=%FROM%deleted%'
         THEN 1 ELSE 0 END                                       AS [scalar_from_inserted],
    CASE WHEN m.definition LIKE '%CURSOR%'    THEN 1 ELSE 0 END AS [has_cursor],
    CASE WHEN m.definition LIKE '%ROLLBACK%'  THEN 1 ELSE 0 END AS [has_rollback],
    CASE WHEN m.definition LIKE '%BEGIN TRAN%' THEN 1 ELSE 0 END AS [has_transaction],
    LEN(m.definition)                AS [definition_length]
FROM sys.triggers AS t
JOIN sys.objects AS o
  ON o.object_id = t.object_id
JOIN sys.sql_modules AS m
  ON m.object_id = t.object_id
WHERE t.parent_class = 1
  AND o.is_ms_shipped = 0
ORDER BY [parent_schema], [parent_object], [trigger_name];
