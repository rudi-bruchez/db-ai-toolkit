-- page-salvage, step 6: deallocate the damaged pages. WRITES, LOSES DATA.
-- sqlcmd -b -v DB="MyDb" -i repair.sql
-- Only after the records of every damaged page are extracted and matched.
-- SINGLE_USER WITH ROLLBACK IMMEDIATE ends every other session on the database.
ALTER DATABASE [$(DB)] SET SINGLE_USER WITH ROLLBACK IMMEDIATE;
DBCC CHECKDB (N'$(DB)', REPAIR_ALLOW_DATA_LOSS) WITH NO_INFOMSGS, ALL_ERRORMSGS;
ALTER DATABASE [$(DB)] SET MULTI_USER;
