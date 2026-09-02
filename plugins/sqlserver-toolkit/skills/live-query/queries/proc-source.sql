/*  A module's source, signature and creation-time settings.

    Parameter: @name - the object, schema-qualified ('dbo.usp_Order') or bare.

    Works for anything with a body: stored procedures, functions, triggers and
    views. Use OBJECT_DEFINITION rather than sp_helptext, which sqlq refuses on
    a read-only profile because it needs EXEC.

    execute_as_principal_id is not null when the module runs under a different
    identity (EXECUTE AS). That changes which rows row-level security shows it,
    and which permissions it has - worth knowing before concluding that a
    module "cannot see" some data.

    The parameter list is built with FOR XML PATH rather than STRING_AGG so
    this works before SQL Server 2017.
*/
SELECT
    SCHEMA_NAME(o.schema_id)      AS [schema_name],
    o.name                        AS [object_name],
    o.type_desc,
    o.create_date,
    o.modify_date,
    m.uses_ansi_nulls,
    m.uses_quoted_identifier,
    m.is_recompiled,
    m.execute_as_principal_id,
    STUFF((SELECT ', ' + p.name
                  + ' ' + TYPE_NAME(p.user_type_id)
                  + CASE WHEN p.is_output = 1 THEN ' OUTPUT' ELSE '' END
             FROM sys.parameters AS p
            WHERE p.object_id = o.object_id
              AND p.parameter_id > 0
            ORDER BY p.parameter_id
              FOR XML PATH(''), TYPE).value('.', 'nvarchar(max)'), 1, 2, '')
                                  AS [parameters],
    m.definition
FROM sys.objects AS o
JOIN sys.sql_modules AS m
  ON m.object_id = o.object_id
WHERE o.object_id = OBJECT_ID(@name);
