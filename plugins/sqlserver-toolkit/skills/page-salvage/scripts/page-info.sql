-- page-salvage, step 2: which object and index a damaged page belongs to. Read-only.
-- sqlcmd -v DB="MyDb" FILE=1 PAGE=339 -i page-info.sql
-- sys.dm_db_page_info exists from SQL Server 2019 (15.x).
SET NOCOUNT ON;
SELECT pi.file_id, pi.page_id, pi.page_type_desc, pi.slot_count,
       OBJECT_SCHEMA_NAME(pi.object_id, pi.database_id) AS schema_name,
       OBJECT_NAME(pi.object_id, pi.database_id) AS object_name,
       pi.index_id, pi.page_level, pi.is_iam_page
FROM sys.dm_db_page_info(DB_ID(N'$(DB)'), $(FILE), $(PAGE), 'DETAILED') AS pi;
