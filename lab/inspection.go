package lab

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strconv"
	"time"
)

type Section struct {
	ID        int
	Title     string
	Columns   []string
	Rows      [][]string
	Baseline  bool `json:",omitempty"`
	ElapsedMS int64
	Error     string `json:",omitempty"`
}
type check struct {
	id           int
	title, query string
}

var inspections = slices.Concat(coreInspections, monitoringInspections, backupInspections, platformInspections)

// InspectionIDs returns the catalog items with SQL evidence, including the timed I/O sample.
func InspectionIDs() []int {
	ids := []int{10}
	for _, c := range inspections {
		ids = append(ids, c.id)
	}
	slices.Sort(ids)
	return slices.Compact(ids)
}

var coreInspections = []check{
	{3, "Кандидаты на обновление статистики", statsList},
	{4, "Журнал и виртуальные файлы", `SELECT d.recovery_model_desc AS [Модель],d.log_reuse_wait_desc AS [Удержание журнала],
 CAST(space.total_log_size_in_bytes/1048576.0 AS decimal(18,1)) AS [Размер МБ],
 CAST(space.used_log_space_in_percent AS decimal(6,2)) AS [Занято %],
 v.total AS [VLF всего],v.active_count AS [Активных VLF]
 FROM sys.databases d CROSS JOIN sys.dm_db_log_space_usage space
 CROSS JOIN (SELECT COUNT_BIG(*) total,SUM(CONVERT(bigint,vlf_active)) active_count FROM sys.dm_db_log_info(DB_ID())) v
 WHERE d.database_id=DB_ID()`},
	{5, "История msdb стенда по имени выбранной базы", `SELECT TOP(20) backup_finish_date AS [Окончание],expiration_date AS [Срок из заголовка],type AS [Тип], is_copy_only AS [COPY_ONLY],has_backup_checksums AS [CHECKSUM],CAST(compressed_backup_size/1048576.0 AS decimal(18,1)) AS [МБ] FROM msdb.dbo.backupset WHERE database_name=DB_NAME() AND backup_finish_date IS NOT NULL ORDER BY backup_finish_date DESC`},
	{8, "Базы, видимые текущей учётной записи", `SELECT name AS [База],state_desc AS [Состояние],recovery_model_desc AS [Модель],SUSER_SNAME(owner_sid) AS [Владелец SQL] FROM sys.databases ORDER BY name`},
	{17, "Файлы tempdb и шаг роста", `SELECT name AS [Файл],physical_name AS [Расположение],type_desc AS [Тип],CONVERT(decimal(18,1),size/128.0) AS [МБ],
 CASE WHEN is_percent_growth=1 THEN CONCAT(growth,'%') ELSE CONCAT(CONVERT(decimal(18,2),growth/128.0),' MB') END AS [Прирост] FROM tempdb.sys.database_files ORDER BY file_id`},
	{18, "Режим и заполнение Query Store", `SELECT desired_state_desc AS [Задано],actual_state_desc AS [Действует],readonly_reason AS [Причина read-only],query_capture_mode_desc AS [Захват],size_based_cleanup_mode_desc AS [Автоочистка],stale_query_threshold_days AS [Хранение дней],current_storage_size_mb AS [Занято МБ],max_storage_size_mb AS [Лимит МБ] FROM sys.database_query_store_options`},
	{23, "Проверка страниц, автозакрытие и автосжатие", `SELECT name AS [База],page_verify_option_desc AS [PAGE_VERIFY],is_auto_close_on AS [AUTO_CLOSE],is_auto_shrink_on AS [AUTO_SHRINK] FROM sys.databases ORDER BY name`},
	{26, "Размер, предел и шаг роста файлов видимых баз", `WITH files AS (
 SELECT database_id,file_id,name,type_desc,size,max_size,growth,is_percent_growth
 FROM sys.master_files WHERE database_id<>2
 UNION ALL
 SELECT 2,file_id,name,type_desc,size,max_size,growth,is_percent_growth FROM tempdb.sys.database_files
 )
 SELECT d.name AS [База],f.name AS [Файл],f.type_desc AS [Тип],CONVERT(decimal(18,1),f.size/128.0) AS [Размер МБ],
 CASE WHEN f.is_percent_growth=1 THEN CONCAT(f.growth,'%') ELSE CONCAT(CONVERT(decimal(18,2),f.growth/128.0),' MB') END AS [Прирост],
 CASE f.max_size WHEN -1 THEN N'до предела диска' WHEN 0 THEN N'отключён' ELSE CONCAT(CONVERT(decimal(18,1),f.max_size/128.0),' MB') END AS [Предел]
 FROM files f JOIN sys.databases d ON d.database_id=f.database_id ORDER BY d.name,f.file_id`},
	{28, "Лимиты памяти SQL и доступная память ОС", `SELECT (SELECT CONVERT(bigint,value_in_use) FROM sys.configurations WHERE name='max server memory (MB)') AS [Макс SQL МБ],total_physical_memory_kb/1024 AS [ОС всего МБ],available_physical_memory_kb/1024 AS [ОС доступно МБ],system_memory_state_desc AS [Состояние] FROM sys.dm_os_sys_memory`},
	{29, "Модель памяти процесса SQL", `SELECT sql_memory_model_desc AS [Модель памяти] FROM sys.dm_os_sys_info`},
	{30, "Быстрая инициализация файлов SQL", windowsServiceCheck + `SELECT servicename AS [Служба],instant_file_initialization_enabled AS [IFI] FROM sys.dm_server_services WHERE process_id=CONVERT(int,SERVERPROPERTY('ProcessID'))` + outsideWindows},
	{34, "Одноразовые ad hoc планы и объём кэша экземпляра", `SELECT COUNT_BIG(*) AS [Объектов кэша],SUM(CASE WHEN objtype='Adhoc' AND usecounts=1 THEN CONVERT(bigint,1) ELSE 0 END) AS [Одноразовых ad hoc],
 CAST(SUM(CASE WHEN objtype='Adhoc' AND usecounts=1 THEN CONVERT(bigint,size_in_bytes) ELSE 0 END)/1048576.0 AS decimal(18,1)) AS [Одноразовые МБ], CAST(SUM(CONVERT(bigint,size_in_bytes))/1048576.0 AS decimal(18,1)) AS [Всего МБ],
 CAST(100.0*SUM(CASE WHEN objtype='Adhoc' AND usecounts=1 THEN CONVERT(bigint,size_in_bytes) ELSE 0 END)
 /NULLIF(SUM(CONVERT(bigint,size_in_bytes)),0) AS decimal(6,2)) AS [Одноразовые % памяти кэша],
 (SELECT CONVERT(int,value_in_use) FROM sys.configurations WHERE name='optimize for ad hoc workloads') AS [optimize for ad hoc workloads]
 FROM sys.dm_exec_cached_plans`},
	{36, "Глобальные флаги трассировки", `DBCC TRACESTATUS(-1) WITH NO_INFOMSGS`},
}

func fetchSection(ctx context.Context, conn *sql.Conn, c check, args ...any) (s Section) {
	s = Section{ID: c.id, Title: c.title, Baseline: c.id == 35 || c.id == 36}
	start := time.Now()
	defer func() { s.ElapsedMS = time.Since(start).Milliseconds() }()
	rows, e := conn.QueryContext(ctx, c.query, args...)
	if e != nil {
		s.Error = e.Error()
		return
	}
	defer rows.Close()
	s.Columns, e = rows.Columns()
	if e != nil {
		s.Error = e.Error()
		return
	}
	vals := make([]any, len(s.Columns))
	ptrs := make([]any, len(vals))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if e = rows.Scan(ptrs...); e != nil {
			s.Error = e.Error()
			return
		}
		r := make([]string, len(vals))
		for i, x := range vals {
			switch v := x.(type) {
			case nil:
				r[i] = "—"
			case []byte:
				r[i] = string(v)
			case time.Time:
				r[i] = v.Format("2006-01-02 15:04:05")
			default:
				r[i] = fmt.Sprint(v)
			}
		}
		s.Rows = append(s.Rows, r)
	}
	if e = rows.Err(); e != nil {
		s.Error = e.Error()
	}
	return
}

const ioWindow = 5 * time.Second

func (l *Live) ioWindow(ctx context.Context) (s Section) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	s = Section{ID: 10, Title: "I/O файлов выбранной базы и tempdb за интервал", Columns: []string{"База", "Файл", "Чтений", "Записей", "Чтение мс/IO", "Запись мс/IO", "Интервал с"}}
	// File IDs are unique within a database. Use both IDs to pair the snapshots.
	q := check{10, s.Title, `SELECT v.database_id,v.file_id,DB_NAME(v.database_id),f.name COLLATE DATABASE_DEFAULT,v.num_of_reads,v.num_of_writes,v.io_stall_read_ms,v.io_stall_write_ms
 FROM sys.dm_io_virtual_file_stats(DB_ID(),NULL) v JOIN sys.database_files f ON f.file_id=v.file_id
 UNION ALL
 SELECT v.database_id,v.file_id,DB_NAME(v.database_id),f.name COLLATE DATABASE_DEFAULT,v.num_of_reads,v.num_of_writes,v.io_stall_read_ms,v.io_stall_write_ms
 FROM sys.dm_io_virtual_file_stats(2,NULL) v JOIN tempdb.sys.database_files f ON f.file_id=v.file_id
 ORDER BY 1,2`}
	before := fetchSection(ctx, l.conn, q)
	if before.Error != "" {
		s.Error = before.Error
		return
	}
	start := time.Now()
	select {
	case <-ctx.Done():
		s.Error = ctx.Err().Error()
		return
	case <-time.After(ioWindow):
	}
	after := fetchSection(ctx, l.conn, q)
	s.ElapsedMS = time.Since(start).Milliseconds()
	if after.Error != "" {
		s.Error = after.Error
		return
	}
	s.Rows = fileIODeltas(before.Rows, after.Rows, s.ElapsedMS)
	return
}

func fileIODeltas(before, after [][]string, elapsedMS int64) [][]string {
	var rows [][]string
	prev := map[[2]string][]string{}
	for _, r := range before {
		prev[[2]string{r[0], r[1]}] = r
	}
	for _, r := range after {
		b := prev[[2]string{r[0], r[1]}]
		if b == nil || r[2] != b[2] || r[3] != b[3] {
			continue
		}
		var d [4]int64
		ok := true
		for i := range d {
			av, ea := strconv.ParseInt(r[i+4], 10, 64)
			bv, eb := strconv.ParseInt(b[i+4], 10, 64)
			if ea != nil || eb != nil || av < bv {
				ok = false
				break
			}
			d[i] = av - bv
		}
		if !ok {
			continue
		}
		avg := func(ms, n int64) string {
			if n == 0 {
				return "—"
			}
			return fmt.Sprintf("%.2f", float64(ms)/float64(n))
		}
		rows = append(rows, []string{r[2], r[3], fmt.Sprint(d[0]), fmt.Sprint(d[1]), avg(d[2], d[0]), avg(d[3], d[1]), fmt.Sprintf("%.1f", float64(elapsedMS)/1000)})
	}
	return rows
}
