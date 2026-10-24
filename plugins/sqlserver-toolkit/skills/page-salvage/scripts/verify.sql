-- page-salvage, step 8: the database is clean and the history is still there. Read-only.
-- sqlcmd -v DB="MyDb" -i verify.sql
-- CHECKDB with NO_INFOMSGS prints nothing at all when the database is clean.
SET NOCOUNT ON;
DBCC CHECKDB (N'$(DB)') WITH NO_INFOMSGS, ALL_ERRORMSGS;
SELECT database_id, file_id, page_id, event_type, error_count, last_update_date
FROM msdb.dbo.suspect_pages
WHERE database_id = DB_ID(N'$(DB)')
ORDER BY file_id, page_id, event_type;
