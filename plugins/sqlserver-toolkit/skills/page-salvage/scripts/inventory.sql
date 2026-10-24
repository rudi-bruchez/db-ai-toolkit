-- page-salvage, step 1: what is damaged. Read-only.
-- sqlcmd -v DB="MyDb" -i inventory.sql
-- Keep the whole output: suspect_pages is history that a later repair rewrites.
SET NOCOUNT ON;
PRINT '== msdb.dbo.suspect_pages (copy this before anything else; never delete it)';
SELECT database_id, file_id, page_id, event_type, error_count, last_update_date
FROM msdb.dbo.suspect_pages
WHERE database_id = DB_ID(N'$(DB)')
ORDER BY file_id, page_id, event_type;
PRINT '== DBCC CHECKDB, one row per message: read File and Page of each 8928/8939/824';
DBCC CHECKDB (N'$(DB)') WITH NO_INFOMSGS, ALL_ERRORMSGS, TABLERESULTS;
