package lab

// SQL Server Express has no SQL Server Agent. Other editions can keep its
// schedules in msdb even while the service is stopped.
const agentCheck = `IF CONVERT(int,SERVERPROPERTY('EngineEdition'))=4
 SELECT N'В SQL Server Express отсутствует SQL Server Agent' AS [Доступность]
 ELSE `

// Keep SQL Agent recurrence fields in their native units so weekly masks,
// relative months and intra-day windows can be reconstructed without round-off.
// https://learn.microsoft.com/sql/relational-databases/system-tables/dbo-sysschedules-transact-sql
const jobSchedules = agentCheck + `SELECT j.name AS [Задание],j.enabled AS [Задание включено],
 j.version_number AS [Версия задания],s.name AS [Расписание],s.enabled AS [Расписание включено],
 s.freq_type AS [Тип частоты],s.freq_interval AS [Дни или маска],s.freq_relative_interval AS [Неделя месяца],
 s.freq_recurrence_factor AS [Период недель или месяцев],s.freq_subday_type AS [Единица внутри дня],
 s.freq_subday_interval AS [Интервал внутри дня],s.active_start_date AS [Начало YYYYMMDD],
 s.active_end_date AS [Конец YYYYMMDD],s.active_start_time AS [С HHMMSS],s.active_end_time AS [До HHMMSS],
 j.notify_level_email AS [Условие письма],j.notify_email_operator_id AS [ID почтового оператора]
 FROM msdb.dbo.sysjobs j
 LEFT JOIN msdb.dbo.sysjobschedules js ON js.job_id=j.job_id
 LEFT JOIN msdb.dbo.sysschedules s ON s.schedule_id=js.schedule_id
 ORDER BY j.name,s.name`

var monitoringInspections = []check{
	{1, "Extended Events экземпляра и зарегистрированные события блокировок", `SELECT s.name AS [Сеанс XE],
 s.startup_state AS [Автозапуск],CASE WHEN a.name IS NULL THEN 0 ELSE 1 END AS [Запущен],
 COUNT(e.event_id) AS [Событий задано],
 MAX(CASE WHEN e.name='blocked_process_report' THEN 1 ELSE 0 END) AS [blocked_process_report],
 MAX(CASE WHEN e.name='xml_deadlock_report' THEN 1 ELSE 0 END) AS [xml_deadlock_report]
 FROM sys.server_event_sessions s LEFT JOIN sys.dm_xe_sessions a ON a.name COLLATE Latin1_General_100_BIN2=s.name COLLATE Latin1_General_100_BIN2
 LEFT JOIN sys.server_event_session_events e ON e.event_session_id=s.event_session_id
 GROUP BY s.name,s.startup_state,a.name ORDER BY s.name`},
	{1, "Задания Agent с интервалом 15 минут и их последний известный результат", agentCheck + `SELECT j.name AS [Задание],j.enabled AS [Задание включено],
 s.name AS [Расписание],s.enabled AS [Расписание включено],s.active_start_time AS [С HHMMSS],s.active_end_time AS [До HHMMSS],
 h.run_date AS [Последняя дата YYYYMMDD],h.run_time AS [Время HHMMSS],h.run_status AS [Исход 0 ошибка / 1 успех / 2 повтор / 3 отмена / 4 в работе]
 FROM msdb.dbo.sysjobs j JOIN msdb.dbo.sysjobschedules js ON js.job_id=j.job_id
 JOIN msdb.dbo.sysschedules s ON s.schedule_id=js.schedule_id
 OUTER APPLY (SELECT TOP(1) run_date,run_time,run_status FROM msdb.dbo.sysjobhistory h
 WHERE h.job_id=j.job_id AND h.step_id=0 ORDER BY h.instance_id DESC) h
 WHERE s.freq_type IN (4,8,16,32) AND ((s.freq_subday_type=4 AND s.freq_subday_interval=15)
 OR (s.freq_subday_type=2 AND s.freq_subday_interval=900))
 ORDER BY j.name,s.name`},
	{6, "SQL Agent, Database Mail и уведомления заданий об ошибке", agentCheck + `SELECT
 (SELECT CONVERT(int,value_in_use) FROM sys.configurations WHERE name='Agent XPs') AS [Agent XPs],
 (SELECT TOP (1) agent_start_date FROM msdb.dbo.syssessions ORDER BY session_id DESC) AS [Последний старт Agent в msdb],
 (SELECT CONVERT(int,value_in_use) FROM sys.configurations WHERE name='Database Mail XPs') AS [Database Mail XPs],
 (SELECT COUNT_BIG(*) FROM msdb.dbo.sysoperators WHERE enabled=1 AND NULLIF(email_address,'') IS NOT NULL) AS [Включённых операторов с адресом],
 (SELECT COUNT_BIG(*) FROM msdb.dbo.sysjobs j JOIN msdb.dbo.sysoperators o ON o.id=j.notify_email_operator_id
 WHERE j.enabled=1 AND j.notify_level_email IN (2,3) AND o.enabled=1 AND NULLIF(o.email_address,'') IS NOT NULL) AS [Включённых заданий с письмом оператору при сбое],
 (SELECT COUNT_BIG(*) FROM msdb.dbo.sysjobs WHERE enabled=1) AS [Включённых заданий всего]`},
	{6, "Оповещения SQL Agent и почтовые операторы", agentCheck + `SELECT a.name AS [Оповещение], a.enabled AS [Включено],
 a.message_id AS [Номер ошибки],a.severity AS [Уровень],CASE WHEN NULLIF(a.performance_condition,'') IS NULL THEN 0 ELSE 1 END AS [По счётчику],
 COUNT(CASE WHEN o.enabled=1 AND (n.notification_method & 1)=1 AND NULLIF(o.email_address,'') IS NOT NULL THEN 1 END) AS [Включённых получателей по почте]
 FROM msdb.dbo.sysalerts a LEFT JOIN msdb.dbo.sysnotifications n ON n.alert_id=a.id
 LEFT JOIN msdb.dbo.sysoperators o ON o.id=n.operator_id
 GROUP BY a.id,a.name,a.enabled,a.message_id,a.severity,a.performance_condition ORDER BY a.severity,a.message_id,a.name`},
	{6, "Последние 20 видимых сообщений Database Mail, без адресов и содержимого", agentCheck + `SELECT TOP (20)
 send_request_date AS [Поставлено в очередь],sent_status AS [Статус Database Mail],sent_date AS [Отправлено SQL]
 FROM msdb.dbo.sysmail_allitems ORDER BY mailitem_id DESC`},
	{21, "Расписание SQL Agent по каждому заданию", jobSchedules},
	{21, "Последние 50 итогов заданий SQL Agent", agentCheck + `SELECT TOP (50) j.name AS [Задание],
 h.run_date AS [Начало YYYYMMDD],h.run_time AS [Время HHMMSS],
 (CONVERT(bigint,h.run_duration)/10000)*3600+((h.run_duration/100)%100)*60+(h.run_duration%100) AS [Длительность с],
 h.run_status AS [Исход 0 ошибка / 1 успех / 2 повтор / 3 отмена / 4 в работе]
 FROM msdb.dbo.sysjobhistory h JOIN msdb.dbo.sysjobs j ON j.job_id=h.job_id
 WHERE h.step_id=0 ORDER BY h.instance_id DESC`},
	blockingCheck,
	{22, "Накопленные ожидания блокировок, диска и сети экземпляра с запуска или сброса", `SELECT TOP (40)
 wait_type AS [Тип],waiting_tasks_count AS [Ожиданий],wait_time_ms AS [Всего мс],signal_wait_time_ms AS [Сигнальных мс]
 FROM sys.dm_os_wait_stats WHERE waiting_tasks_count>0 AND
 (wait_type LIKE 'LCK[_]M[_]%' OR wait_type LIKE 'PAGEIOLATCH[_]%' OR wait_type LIKE 'PAGELATCH[_]%'
 OR wait_type IN ('WRITELOG','IO_COMPLETION','ASYNC_IO_COMPLETION','ASYNC_NETWORK_IO','CXPACKET','CXCONSUMER','RESOURCE_SEMAPHORE'))
 ORDER BY wait_time_ms DESC,wait_type`},
}
