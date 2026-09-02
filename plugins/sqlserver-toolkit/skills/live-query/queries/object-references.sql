/*  Everything in this database that references an object.

    Parameter: @name  - the object, schema-qualified ('dbo.Orders') or bare.

    READ THIS BEFORE TRUSTING THE RESULT.

    sys.sql_expression_dependencies only knows about references SQL Server
    could resolve when the module was created. It does NOT see:

      - dynamic SQL: EXEC('SELECT ... FROM ' + @table) is invisible to it;
      - references from other databases or linked servers, unless the
        dependency was recorded with a valid referenced_id;
      - anything outside the database: Agent jobs, SSIS packages, application
        code, ad-hoc reports.

    That is why the query returns two kinds of row:

      source = 'declared'   the engine recorded the dependency. Reliable.
      source = 'text_match' the object name appears in the module's text.
                            Catches dynamic SQL, but also catches comments and
                            coincidental substrings. Read the module before
                            concluding.

    An object found only by 'text_match' is the interesting case: it usually
    means dynamic SQL. An object found only by 'declared' with no text match is
    normally a name spelled differently (a synonym, or a different case).

    Neither source covers callers outside the database. Say so when you answer.
*/
DECLARE @bare sysname = PARSENAME(@name, 1);

/*  A LIKE metacharacter in an object name would widen the text search instead
    of narrowing it: [Order_Line] would also match Order9Line. Escape them so
    the pattern matches the name literally.  */
DECLARE @pattern nvarchar(300) = @bare;
SET @pattern = REPLACE(@pattern, N'\', N'\\');
SET @pattern = REPLACE(@pattern, N'%', N'\%');
SET @pattern = REPLACE(@pattern, N'_', N'\_');
SET @pattern = REPLACE(@pattern, N'[', N'\[');

SELECT
    x.[source],
    x.[referencing_schema],
    x.[referencing_name],
    x.[referencing_type]
FROM (
    SELECT
        'declared'               AS [source],
        SCHEMA_NAME(o.schema_id) AS [referencing_schema],
        o.name                   AS [referencing_name],
        o.type_desc              AS [referencing_type]
    FROM sys.sql_expression_dependencies AS d
    JOIN sys.objects AS o
      ON o.object_id = d.referencing_id
    WHERE d.referenced_id = OBJECT_ID(@name)
       OR (d.referenced_id IS NULL AND d.referenced_entity_name = @bare)

    UNION

    SELECT
        'text_match',
        SCHEMA_NAME(o.schema_id),
        o.name,
        o.type_desc
    FROM sys.sql_modules AS m
    JOIN sys.objects AS o
      ON o.object_id = m.object_id
    WHERE o.is_ms_shipped = 0
      AND m.definition LIKE N'%' + @pattern + N'%' ESCAPE N'\'
) AS x
ORDER BY x.[source], x.[referencing_schema], x.[referencing_name];
