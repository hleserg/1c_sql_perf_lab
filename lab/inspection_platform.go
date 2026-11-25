package lab

// The Windows service view is platform specific. The fallback is an explicit
// capability result, and permission failures on a supported platform remain errors.
const windowsServiceCheck = `IF EXISTS (SELECT 1 FROM sys.dm_os_host_info WHERE host_platform=N'Windows') `
const outsideWindows = ` ELSE SELECT N'Сведения о службах Windows доступны только на узле SQL под Windows' AS [Доступность]`

var platformInspections = []check{
	{2, "Версия, редакция, уровень обновления и платформа SQL", `SELECT
 CONVERT(nvarchar(128),SERVERPROPERTY('ProductVersion')) AS [Сборка],
 CONVERT(nvarchar(128),SERVERPROPERTY('Edition')) AS [Редакция],
 CONVERT(nvarchar(128),SERVERPROPERTY('ProductLevel')) AS [Уровень],
 CONVERT(nvarchar(128),SERVERPROPERTY('ProductUpdateLevel')) AS [CU],
 CONVERT(nvarchar(128),SERVERPROPERTY('ProductUpdateReference')) AS [Номер KB],
 host_platform AS [Платформа],host_distribution AS [ОС],host_release AS [Версия ОС] FROM sys.dm_os_host_info`},
	{7, "Режим доступа и изоляции видимых пользовательских баз", `SELECT name AS [База],
 user_access_desc AS [Доступ],HAS_DBACCESS(name) AS [Доступна текущему входу],is_read_only AS [Только чтение],
 is_read_committed_snapshot_on AS [READ_COMMITTED_SNAPSHOT],snapshot_isolation_state_desc AS [ALLOW_SNAPSHOT_ISOLATION],
 containment_desc AS [Автономность] FROM sys.databases WHERE database_id>4 ORDER BY name`},
	{9, "Накопленные операции файлов по базам экземпляра", `SELECT d.name AS [База],i.sqlserver_start_time AS [Время запуска экземпляра],
 SUM(v.num_of_reads) AS [Чтений],SUM(v.num_of_writes) AS [Записей],
 CAST(SUM(v.num_of_bytes_read)/1048576.0 AS decimal(20,1)) AS [Прочитано МБ],
 CAST(SUM(v.num_of_bytes_written)/1048576.0 AS decimal(20,1)) AS [Записано МБ]
 FROM sys.dm_io_virtual_file_stats(NULL,NULL) v JOIN sys.databases d ON d.database_id=v.database_id
 CROSS JOIN sys.dm_os_sys_info i GROUP BY d.name,i.sqlserver_start_time ORDER BY d.name`},
	{11, "Пользовательские сеансы и выполняемые запросы по текущему контексту базы", `WITH sessions AS (
 SELECT database_id,COUNT_BIG(*) AS n FROM sys.dm_exec_sessions
 WHERE is_user_process=1 AND session_id<>@@SPID GROUP BY database_id),
 requests AS (SELECT r.database_id,COUNT_BIG(*) AS n FROM sys.dm_exec_requests r
 JOIN sys.dm_exec_sessions s ON s.session_id=r.session_id WHERE s.is_user_process=1 AND r.session_id<>@@SPID GROUP BY r.database_id)
 SELECT d.name AS [База],COALESCE(s.n,0) AS [Сеансов],COALESCE(r.n,0) AS [Запросов сейчас]
 FROM sys.databases d LEFT JOIN sessions s ON s.database_id=d.database_id LEFT JOIN requests r ON r.database_id=d.database_id
 WHERE d.database_id>4 ORDER BY d.name`},
	{17, "Занятость файлов данных tempdb в момент снимка", `SELECT f.name AS [Файл],f.physical_name AS [Расположение],
 CAST(u.unallocated_extent_page_count/128.0 AS decimal(18,1)) AS [Свободно МБ],
 CAST(u.user_object_reserved_page_count/128.0 AS decimal(18,1)) AS [Пользовательские объекты МБ],
 CAST(u.internal_object_reserved_page_count/128.0 AS decimal(18,1)) AS [Внутренние объекты МБ],
 CAST(u.version_store_reserved_page_count/128.0 AS decimal(18,1)) AS [Хранилище версий МБ]
 FROM tempdb.sys.dm_db_file_space_usage u JOIN tempdb.sys.database_files f ON f.file_id=u.file_id ORDER BY f.file_id`},
	{19, "MAXDOP экземпляра и базы, порог стоимости параллелизма", `SELECT
 (SELECT CONVERT(int,value_in_use) FROM sys.configurations WHERE name='max degree of parallelism') AS [MAXDOP экземпляра],
 (SELECT CONVERT(int,value) FROM sys.database_scoped_configurations WHERE name='MAXDOP') AS [MAXDOP базы, 0 наследует],
 (SELECT CONVERT(int,value_in_use) FROM sys.configurations WHERE name='cost threshold for parallelism') AS [Порог стоимости]`},
	{24, "Исправления оптимизатора и совместимость выбранной базы", `SELECT d.compatibility_level AS [Совместимость],
 CONVERT(int,c.value) AS [QUERY_OPTIMIZER_HOTFIXES базы],CONVERT(nvarchar(128),SERVERPROPERTY('ProductVersion')) AS [Сборка]
 FROM sys.databases d CROSS JOIN sys.database_scoped_configurations c
 WHERE d.database_id=DB_ID() AND c.name='QUERY_OPTIMIZER_HOTFIXES'`},
	{25, "Автоматическое создание и обновление статистики базы", `SELECT is_auto_create_stats_on AS [AUTO_CREATE_STATISTICS],
 is_auto_update_stats_on AS [AUTO_UPDATE_STATISTICS],is_auto_update_stats_async_on AS [AUTO_UPDATE_STATISTICS_ASYNC]
 FROM sys.databases WHERE database_id=DB_ID()`},
	{26, "Свободное место на томах, где лежат файлы доступных баз", `SELECT d.name AS [База],f.name AS [Файл],v.volume_mount_point AS [Том],
 CAST(v.total_bytes/1073741824.0 AS decimal(18,2)) AS [Всего ГБ],CAST(v.available_bytes/1073741824.0 AS decimal(18,2)) AS [Свободно ГБ]
 FROM sys.master_files f JOIN sys.databases d ON d.database_id=f.database_id
 OUTER APPLY sys.dm_os_volume_stats(f.database_id,f.file_id) v
 WHERE d.state=0 AND f.state=0 AND HAS_DBACCESS(d.name)=1 ORDER BY d.name,f.file_id`},
	{26, "Накопленное число увеличений журналов по счётчикам экземпляра", `SELECT RTRIM(instance_name) AS [База],
 cntr_value AS [Увеличений журнала] FROM sys.dm_os_performance_counters
 WHERE object_name LIKE '%:Databases' AND counter_name='Log Growths' AND instance_name<>'_Total'
 ORDER BY instance_name`},
	{29, "Память процесса SQL и признаки давления в момент снимка", `SELECT physical_memory_in_use_kb/1024 AS [Физическая память МБ],
 locked_page_allocations_kb/1024 AS [Закреплено МБ],process_physical_memory_low AS [Низкая физическая память],
 process_virtual_memory_low AS [Низкая виртуальная память],page_fault_count AS [Ошибок страниц с запуска процесса]
 FROM sys.dm_os_process_memory`},
	{31, "Транспорт видимых пользовательских соединений в контексте выбранной базы", `SELECT c.net_transport AS [Транспорт],
 c.encrypt_option AS [Шифрование],c.auth_scheme AS [Аутентификация],c.net_packet_size AS [Размер пакета Б],COUNT_BIG(*) AS [Соединений]
 FROM sys.dm_exec_connections c JOIN sys.dm_exec_sessions s ON s.session_id=c.session_id
 WHERE s.is_user_process=1 AND s.database_id=DB_ID() AND s.session_id<>@@SPID
 GROUP BY c.net_transport,c.encrypt_option,c.auth_scheme,c.net_packet_size ORDER BY 1,2,3,4`},
	{31, "Сеть текущего подключения лаборатории и ожидание отправки данных экземпляра", `SELECT c.net_transport AS [Транспорт стенда],
 c.net_packet_size AS [Размер пакета Б],c.encrypt_option AS [Шифрование],w.waiting_tasks_count AS [ASYNC_NETWORK_IO ожиданий],
 w.wait_time_ms AS [ASYNC_NETWORK_IO мс с запуска или сброса] FROM sys.dm_exec_connections c
 LEFT JOIN sys.dm_os_wait_stats w ON w.wait_type='ASYNC_NETWORK_IO' WHERE c.session_id=@@SPID`},
	{32, "До 20 крупных индексов для проверки плотности и фрагментации, по счётчикам SQL", `WITH sizes AS (
 SELECT p.object_id,p.index_id,SUM(p.used_page_count) AS used_pages
 FROM sys.dm_db_partition_stats p GROUP BY p.object_id,p.index_id)
 SELECT TOP (20) sc.name AS [Схема],t.name AS [Таблица],i.name AS [Индекс],p.used_pages AS [Занято страниц],
 u.user_seeks AS [Поисков],u.user_scans AS [Сканирований],u.user_lookups AS [Обращений по ключу],
 u.user_updates AS [Операций изменения],u.last_user_update AS [Последнее изменение]
 FROM sizes p JOIN sys.tables t ON t.object_id=p.object_id JOIN sys.schemas sc ON sc.schema_id=t.schema_id
 JOIN sys.indexes i ON i.object_id=p.object_id AND i.index_id=p.index_id
 LEFT JOIN sys.dm_db_index_usage_stats u ON u.database_id=DB_ID() AND u.object_id=p.object_id AND u.index_id=p.index_id
 WHERE t.is_ms_shipped=0 AND t.is_memory_optimized=0 AND i.type IN (1,2) AND i.is_disabled=0 AND i.is_hypothetical=0 AND p.used_pages>=128
 ORDER BY p.used_pages DESC,sc.name,t.name,i.index_id`},
	{33, "Параллелизм, доступный для проверки реструктуризации", `SELECT i.cpu_count AS [Логических CPU],i.scheduler_count AS [Планировщиков],
 CONVERT(int,c.value) AS [MAXDOP базы, 0 наследует],
 (SELECT CONVERT(int,value_in_use) FROM sys.configurations WHERE name='max degree of parallelism') AS [MAXDOP экземпляра]
 FROM sys.dm_os_sys_info i CROSS JOIN sys.database_scoped_configurations c WHERE c.name='MAXDOP'`},
	{35, "Параметры экземпляра для сверки", `SELECT name AS [Параметр],
 CONVERT(nvarchar(256),value) AS [Задано],CONVERT(nvarchar(256),value_in_use) AS [Действует]
 FROM sys.configurations ORDER BY name`},
	{35, "Параметры видимых баз для сверки", `SELECT name AS [База],compatibility_level AS [Совместимость],
 recovery_model_desc AS [Модель],page_verify_option_desc AS [PAGE_VERIFY],is_auto_close_on AS [AUTO_CLOSE],
 is_auto_shrink_on AS [AUTO_SHRINK],is_read_only AS [Только чтение],user_access_desc AS [Доступ],
 is_auto_create_stats_on AS [AUTO_CREATE_STATISTICS],is_auto_update_stats_on AS [AUTO_UPDATE_STATISTICS],
 is_auto_update_stats_async_on AS [AUTO_UPDATE_STATISTICS_ASYNC],is_read_committed_snapshot_on AS [READ_COMMITTED_SNAPSHOT],
 snapshot_isolation_state_desc AS [ALLOW_SNAPSHOT_ISOLATION],is_query_store_on AS [QUERY_STORE]
 FROM sys.databases ORDER BY name`},
	{35, "Параметры уровня выбранной базы для сверки", `SELECT name AS [Параметр],CONVERT(nvarchar(256),value) AS [Основная реплика],
 CONVERT(nvarchar(256),value_for_secondary) AS [Вторичная реплика] FROM sys.database_scoped_configurations ORDER BY name`},
	{35, "Задания и расписания SQL Agent для сверки", jobSchedules},
	{37, "Службы, известные экземпляру SQL под Windows", windowsServiceCheck + `SELECT servicename AS [Служба],
 startup_type_desc AS [Режим запуска],status_desc AS [Состояние],last_startup_time AS [Последний запуск]
 FROM sys.dm_server_services ORDER BY servicename` + outsideWindows},
	{38, "Текущая совместимость, сортировка и размер выбранной базы перед переносом", `SELECT d.compatibility_level AS [Совместимость],
 d.collation_name AS [Сортировка базы],CONVERT(nvarchar(128),SERVERPROPERTY('Collation')) AS [Сортировка экземпляра],
 d.containment_desc AS [Автономность],d.is_encrypted AS [Шифрование базы],
 (SELECT CAST(SUM(CONVERT(bigint,size))/128.0 AS decimal(20,1)) FROM sys.database_files) AS [Размер файлов МБ]
 FROM sys.databases d WHERE d.database_id=DB_ID()`},
	{38, "Сохранённые в базе возможности, зависящие от редакции SQL", `SELECT feature_name AS [Возможность]
 FROM sys.dm_db_persisted_sku_features ORDER BY feature_name`},
}
