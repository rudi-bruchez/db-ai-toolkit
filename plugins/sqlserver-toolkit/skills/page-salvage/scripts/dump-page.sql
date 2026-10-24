-- page-salvage, step 4: decode every record of one page, checksum or not. Read-only.
-- sqlcmd -v DB="MyDb" FILE=1 PAGE=339 -i dump-page.sql -o page-1-339.txt
-- Then: page_records.py page-1-339.txt --types ...
-- Run it while the page is still allocated: after REPAIR_ALLOW_DATA_LOSS the
-- page is deallocated and its records are gone for good.
SET NOCOUNT ON;
DBCC TRACEON (3604);
DBCC PAGE (N'$(DB)', $(FILE), $(PAGE), 3) WITH NO_INFOMSGS;
