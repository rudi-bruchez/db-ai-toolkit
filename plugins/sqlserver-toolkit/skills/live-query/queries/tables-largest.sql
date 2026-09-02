/*  Largest user tables in the current database, by space reserved.

    Parameters: none.

    Why this shape rather than the obvious one:

    - Row counts come only from index_id 0 (heap) or 1 (clustered index).
      Summing row_count across every index multiplies the count by the number
      of nonclustered indexes, which is the classic wrong answer here.
    - Space is summed across all indexes and all partitions, because that is
      what the table actually costs on disk.
    - LOB and row-overflow pages are reported separately. A table that looks
      small in row data and huge in total is storing blobs, and that changes
      what you do about it.

    reserved_mb is what the table has been given; used_mb is what it has filled.
    A wide gap between them is free space inside the allocated extents.
*/
SELECT TOP (50)
    SCHEMA_NAME(t.schema_id) AS [schema_name],
    t.name                   AS [table_name],
    SUM(CASE WHEN ps.index_id IN (0, 1) THEN ps.row_count ELSE 0 END)
                             AS [row_count],
    CAST(SUM(ps.reserved_page_count) * 8.0 / 1024 AS DECIMAL(18, 2))
                             AS [reserved_mb],
    CAST(SUM(ps.used_page_count) * 8.0 / 1024 AS DECIMAL(18, 2))
                             AS [used_mb],
    CAST(SUM(CASE WHEN ps.index_id IN (0, 1)
                  THEN ps.in_row_data_page_count ELSE 0 END) * 8.0 / 1024
         AS DECIMAL(18, 2))  AS [in_row_mb],
    CAST(SUM(ps.lob_used_page_count) * 8.0 / 1024 AS DECIMAL(18, 2))
                             AS [lob_mb],
    CAST(SUM(ps.row_overflow_used_page_count) * 8.0 / 1024 AS DECIMAL(18, 2))
                             AS [row_overflow_mb],
    COUNT(DISTINCT ps.index_id)         AS [index_count],
    MAX(ps.partition_number)            AS [partition_count]
FROM sys.dm_db_partition_stats AS ps
JOIN sys.tables AS t
  ON t.object_id = ps.object_id
WHERE t.is_ms_shipped = 0
GROUP BY t.schema_id, t.name
ORDER BY SUM(ps.reserved_page_count) DESC;
