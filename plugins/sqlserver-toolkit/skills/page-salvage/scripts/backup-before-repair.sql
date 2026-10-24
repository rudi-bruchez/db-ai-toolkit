-- page-salvage, step 3: a COPY_ONLY backup, so the repair can be undone.
-- sqlcmd -v DB="MyDb" BAK="/var/opt/mssql/backup/MyDb_before_repair.bak" -i backup-before-repair.sql
-- No WITH CHECKSUM: a backup that verifies checksums stops on the damaged page.
-- COPY_ONLY leaves the differential base and the log chain untouched.
BACKUP DATABASE [$(DB)] TO DISK = N'$(BAK)' WITH COPY_ONLY, INIT, STATS = 25;
RESTORE VERIFYONLY FROM DISK = N'$(BAK)';
