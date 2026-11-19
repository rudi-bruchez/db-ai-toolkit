/*  CPU topology of the instance: logical CPUs, sockets, cores, NUMA nodes,
    schedulers online and offline, and the parallelism settings.

    No parameter. Read-only. Run it with -maxrows 1. One row.

    The columns.
      - cpu_count is the number of logical CPUs the operating system shows,
        which is the vCPU count on a virtual machine. socket_count and
        cores_per_socket are what Windows or Linux reports, and on a virtual
        machine they are the hypervisor's presentation, not the host.
      - schedulers_online and schedulers_offline count the VISIBLE
        schedulers, one per logical CPU. schedulers_offline > 0 means CPUs
        the instance does not use: an affinity mask, or the limit of the
        edition (Standard: the lesser of 4 sockets or 24 cores; Express: 1
        socket or 4 cores). A virtual machine presenting many sockets of one
        core each hits the socket limit first: read the edition next to it.
      - numa_nodes counts the online nodes SQL Server built, soft-NUMA
        included (softnuma_configuration_desc).
      - maxdop is the instance setting in use; database_maxdop is the
        database scoped value of the database the connection opened in
        (0 means "use the instance setting"). Pass -database to read
        another one.
      - cost_threshold is cost threshold for parallelism, in use.
      - affinity_mask is the value in use; 0 means automatic.

    Permissions: VIEW SERVER STATE, or VIEW SERVER PERFORMANCE STATE from
    SQL Server 2022, for sys.dm_os_sys_info and sys.dm_os_schedulers.

    Written for SQL Server 2017 and SQL Server 2016 SP2 and later: the
    socket_count and cores_per_socket columns do not exist before (error
    207). Tested on SQL Server 2022.
*/
SELECT TOP (1)
    CAST(SERVERPROPERTY('Edition') AS nvarchar(128))        AS edition,
    CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(128)) AS product_version,
    si.cpu_count,
    si.socket_count,
    si.cores_per_socket,
    si.hyperthread_ratio,
    (SELECT COUNT(*) FROM sys.dm_os_nodes AS n
      WHERE n.node_state_desc = N'ONLINE')                  AS numa_nodes,
    si.softnuma_configuration_desc,
    si.virtual_machine_type_desc,
    (SELECT COUNT(*) FROM sys.dm_os_schedulers AS s
      WHERE s.status = N'VISIBLE ONLINE')                   AS schedulers_online,
    (SELECT COUNT(*) FROM sys.dm_os_schedulers AS s
      WHERE s.status = N'VISIBLE OFFLINE')                  AS schedulers_offline,
    (SELECT CAST(c.value_in_use AS int) FROM sys.configurations AS c
      WHERE c.name = N'max degree of parallelism')          AS maxdop,
    (SELECT CAST(dsc.value AS int) FROM sys.database_scoped_configurations AS dsc
      WHERE dsc.name = N'MAXDOP')                           AS database_maxdop,
    (SELECT CAST(c.value_in_use AS int) FROM sys.configurations AS c
      WHERE c.name = N'cost threshold for parallelism')     AS cost_threshold,
    (SELECT CAST(c.value_in_use AS bigint) FROM sys.configurations AS c
      WHERE c.name = N'affinity mask')                      AS affinity_mask
FROM sys.dm_os_sys_info AS si;
