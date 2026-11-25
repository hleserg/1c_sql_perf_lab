package lab

// Keep date/LSN facts from msdb distinct from measurements of this restore.
// restore_date is the start of RESTORE. A backup finish timestamp or last_lsn
// alone cannot establish a usable media chain or a recovery point objective.
// https://learn.microsoft.com/sql/relational-databases/system-tables/restorehistory-transact-sql
var backupInspections = []check{
	{5, "Типы носителей последних 20 наборов в msdb, без путей и адресов", `WITH recent AS (
 SELECT TOP (20) backup_set_id,media_set_id,backup_finish_date,type
 FROM msdb.dbo.backupset WHERE database_name=DB_NAME() AND backup_finish_date IS NOT NULL
 ORDER BY backup_finish_date DESC,backup_set_id DESC)
 SELECT b.backup_set_id AS [ID набора],b.backup_finish_date AS [Окончание],b.type AS [Тип копии],
 m.device_type AS [Тип устройства],CASE
 WHEN m.device_type=2 AND LEFT(m.physical_device_name,2)=N'\\' THEN N'Путь UNC'
 WHEN m.device_type=2 THEN N'Путь устройства DISK'
 WHEN m.device_type=9 THEN N'URL'
 WHEN m.device_type=7 THEN N'Виртуальное устройство'
 WHEN m.device_type=5 THEN N'Лента' ELSE N'Другой тип' END AS [Запись о носителе],
 m.family_sequence_number AS [Номер части],m.mirror AS [Номер зеркала]
 FROM recent b JOIN msdb.dbo.backupmediafamily m ON m.media_set_id=b.media_set_id
 ORDER BY b.backup_finish_date DESC,b.backup_set_id DESC,m.mirror,m.family_sequence_number`},
	{14, "Размер, длительность и CHECKSUM последних 20 полных копий по имени базы в msdb", `SELECT TOP (20)
 backup_finish_date AS [Окончание],DATEDIFF_BIG(second,backup_start_date,backup_finish_date) AS [Копирование с],
 CAST(backup_size/1048576.0 AS decimal(18,1)) AS [Размер до сжатия МБ],
 CAST(compressed_backup_size/1048576.0 AS decimal(18,1)) AS [На носителе МБ],
 has_backup_checksums AS [CHECKSUM],is_copy_only AS [COPY_ONLY],is_damaged AS [Помечена повреждённой]
 FROM msdb.dbo.backupset WHERE database_name=DB_NAME() AND type='D' AND backup_finish_date IS NOT NULL
 ORDER BY backup_finish_date DESC,backup_set_id DESC`},
	{15, "Последние восстановления в эту базу по истории msdb стенда", `SELECT TOP (20)
 h.restore_date AS [Начало RESTORE],h.restore_type AS [Тип],h.recovery AS [WITH RECOVERY],h.stop_at AS [STOPAT],
 b.database_name AS [Исходная база],b.backup_finish_date AS [Окончание копирования],b.has_backup_checksums AS [CHECKSUM],
 CONVERT(varchar(30),b.first_lsn) AS [Первый LSN],CONVERT(varchar(30),b.last_lsn) AS [Следующий LSN]
 FROM msdb.dbo.restorehistory h LEFT JOIN msdb.dbo.backupset b ON b.backup_set_id=h.backup_set_id
 WHERE h.destination_database_name=DB_NAME() AND h.restore_type IN ('D','I','L','F','G')
 ORDER BY h.restore_history_id DESC`},
	{15, "Последняя отметка успешного CHECKDB, сохранённая в базе", `SELECT DB_NAME() AS [База],
 NULLIF(CONVERT(datetime,DATABASEPROPERTYEX(DB_NAME(),'LastGoodCheckDbTime')),CONVERT(datetime,'19000101',112)) AS [Отметка времени CHECKDB]`},
	{16, "Модель восстановления и последние копии текущей ветви по данным msdb", `SELECT d.recovery_model_desc AS [Модель],
 CONVERT(varchar(36),r.recovery_fork_guid) AS [Текущая ветвь],CONVERT(varchar(30),r.last_log_backup_lsn) AS [Начало следующей копии журнала LSN],
 MAX(CASE WHEN b.type='D' THEN b.backup_finish_date END) AS [Полная],
 MAX(CASE WHEN b.type='I' THEN b.backup_finish_date END) AS [Разностная],
 MAX(CASE WHEN b.type='L' THEN b.backup_finish_date END) AS [Журнал]
 FROM sys.databases d JOIN sys.database_recovery_status r ON r.database_id=d.database_id
 LEFT JOIN msdb.dbo.backupset b ON b.database_guid=r.database_guid AND b.last_recovery_fork_guid=r.recovery_fork_guid
 AND b.type IN ('D','I','L') AND b.is_damaged=0
 WHERE d.database_id=DB_ID() GROUP BY d.recovery_model_desc,r.recovery_fork_guid,r.last_log_backup_lsn`},
	{16, "До 20 последних копий журнала текущей ветви и интервалы между их окончаниями", `WITH recent AS (
 SELECT TOP (20) b.backup_set_id,b.backup_finish_date,
 CASE WHEN b.time_zone BETWEEN -48 AND 48 THEN DATEADD(minute,-15*b.time_zone,b.backup_finish_date) END AS finish_utc,
 b.first_lsn,b.last_lsn,b.is_copy_only,b.has_backup_checksums
 FROM msdb.dbo.backupset b JOIN sys.database_recovery_status r ON r.database_guid=b.database_guid
 AND r.recovery_fork_guid=b.last_recovery_fork_guid
 WHERE r.database_id=DB_ID() AND b.type='L' AND b.backup_finish_date IS NOT NULL AND b.is_damaged=0
 ORDER BY b.backup_finish_date DESC,b.backup_set_id DESC)
 SELECT backup_finish_date AS [Окончание у источника],finish_utc AS [Окончание UTC],DATEDIFF_BIG(second,LAG(finish_utc) OVER
 (ORDER BY finish_utc,backup_set_id),finish_utc) AS [Интервал с],
 CONVERT(varchar(30),first_lsn) AS [Первый LSN],CONVERT(varchar(30),last_lsn) AS [Следующий LSN],
 is_copy_only AS [COPY_ONLY],has_backup_checksums AS [CHECKSUM]
 FROM recent ORDER BY backup_finish_date DESC,backup_set_id DESC`},
	{27, "Свойства копии, число таблиц и строк по счётчикам разделов", `SELECT
 d.recovery_model_desc AS [Модель],CONVERT(int,DATABASEPROPERTYEX(DB_NAME(),'IsClone')) AS [Клон метаданных],
 COUNT(DISTINCT t.object_id) AS [Пользовательских таблиц],SUM(p.row_count) AS [Строк по метаданным]
 FROM sys.databases d LEFT JOIN sys.tables t ON t.is_ms_shipped=0
 LEFT JOIN sys.dm_db_partition_stats p ON p.object_id=t.object_id AND p.index_id IN (0,1)
 WHERE d.database_id=DB_ID() GROUP BY d.recovery_model_desc`},
	{27, "Виды сжатия разделов пользовательских таблиц и индексов", `SELECT p.data_compression_desc AS [Сжатие],
 COUNT_BIG(*) AS [Разделов],COUNT(DISTINCT p.object_id) AS [Таблиц]
 FROM sys.partitions p JOIN sys.tables t ON t.object_id=p.object_id WHERE t.is_ms_shipped=0
 GROUP BY p.data_compression_desc ORDER BY p.data_compression_desc`},
}
