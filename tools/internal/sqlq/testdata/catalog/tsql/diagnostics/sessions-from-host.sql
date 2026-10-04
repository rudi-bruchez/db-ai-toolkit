-----
-- Get sessions from a specific host
-- sqlq: name=sessions-from-host params=hostname
--
-- rudi@babaluga.com, go ahead license
-----

SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
DECLARE @hostname sysname = N'%';
SELECT host_name FROM sys.dm_exec_sessions WHERE host_name LIKE @hostname;
