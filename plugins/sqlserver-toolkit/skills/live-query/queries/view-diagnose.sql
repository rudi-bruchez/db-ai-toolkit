/*  A view's definition and the session settings it was created under.

    Parameter: @name - the view, schema-qualified ('dbo.vOrders') or bare.

    Start here when a view returns the wrong rows, then work the view checklist
    in references/review-checklists.md against the definition.

    Why the SET options matter: uses_ansi_nulls and uses_quoted_identifier are
    frozen at creation time, not taken from your session. A view created with
    ANSI_NULLS OFF compares NULLs differently from the same text run by hand,
    which is one of the ways "the query works but the view doesn't" happens.

    is_schema_bound tells you whether ALTER TABLE on a source could have left
    the view stale. A non-schema-bound view over a changed table keeps its old
    column list until someone runs sp_refreshview.

    Row-level security is NOT covered here: sys.security_predicates only exists
    from SQL Server 2016, so querying it would break the whole batch on older
    versions. The checklist has the separate query to run.
*/
SELECT
    SCHEMA_NAME(v.schema_id)                       AS [schema_name],
    v.name                                         AS [view_name],
    v.create_date,
    v.modify_date,
    m.is_schema_bound,
    m.uses_ansi_nulls,
    m.uses_quoted_identifier,
    OBJECTPROPERTY(v.object_id, 'IsIndexed')       AS [is_indexed],
    (SELECT COUNT(*)
       FROM sys.sql_expression_dependencies AS d
       JOIN sys.synonyms AS s
         ON s.object_id = d.referenced_id
      WHERE d.referencing_id = v.object_id)        AS [synonyms_referenced],
    (SELECT COUNT(*)
       FROM sys.sql_expression_dependencies AS d
      WHERE d.referencing_id = v.object_id
        AND d.referenced_id IS NULL)               AS [unresolved_references],
    m.definition
FROM sys.views AS v
JOIN sys.sql_modules AS m
  ON m.object_id = v.object_id
WHERE v.object_id = OBJECT_ID(@name);
