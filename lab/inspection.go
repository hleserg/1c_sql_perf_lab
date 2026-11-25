package lab

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"
)

type Section struct {
	ID        int
	Title     string
	Columns   []string
	Rows      [][]string
	ElapsedMS int64
	Error     string `json:",omitempty"`
}
type check struct {
	id           int
	title, query string
}

var inspections = []check{
	{3, "Кандидаты на обновление статистики", statsList},
	{4, "Журнал и виртуальные файлы", `SELECT d.recovery_model_desc AS [Модель],d.log_reuse_wait_desc AS [Удержание журнала],
 CAST(space.total_log_size_in_bytes/1048576.0 AS decimal(18,1)) AS [Размер МБ],
 CAST(space.used_log_space_in_percent AS decimal(6,2)) AS [Занято %],
 v.total AS [VLF всего],v.active_count AS [Активных VLF]
 FROM sys.databases d CROSS JOIN sys.dm_db_log_space_usage space
 CROSS JOIN (SELECT COUNT_BIG(*) total,SUM(CONVERT(bigint,vlf_active)) active_count FROM sys.dm_db_log_info(DB_ID())) v
 WHERE d.database_id=DB_ID()`},
	{5, "История копирования выбранной базы на этом экземпляре", `SELECT TOP(20) backup_finish_date AS [Окончание],type AS [D/I/L], is_copy_only AS [COPY_ONLY],has_backup_checksums AS [CHECKSUM],CAST(compressed_backup_size/1048576.0 AS decimal(18,1)) AS [МБ] FROM msdb.dbo.backupset WHERE database_name=DB_NAME() AND backup_finish_date IS NOT NULL ORDER BY backup_finish_date DESC`},
	{8, "Базы, видимые текущей учётной записи", `SELECT name AS [База],state_desc AS [Состояние],recovery_model_desc AS [Модель],SUSER_SNAME(owner_sid) AS [Владелец SQL] FROM sys.databases WHERE database_id>4 ORDER BY name`},
	{17, "Файлы tempdb и шаг роста", `SELECT name AS [Файл],type_desc AS [Тип],CONVERT(decimal(18,1),size/128.0) AS [МБ],
 CASE WHEN is_percent_growth=1 THEN CONCAT(growth,'%') ELSE CONCAT(CONVERT(decimal(18,2),growth/128.0),' MB') END AS [Прирост] FROM tempdb.sys.database_files ORDER BY file_id`},
	{18, "Режим и заполнение Query Store", `SELECT desired_state_desc AS [Задано],actual_state_desc AS [Действует],readonly_reason AS [Причина read-only],current_storage_size_mb AS [Занято МБ],max_storage_size_mb AS [Лимит МБ] FROM sys.database_query_store_options`},
	{23, "Проверка страниц, автозакрытие и автосжатие", `SELECT name AS [База],page_verify_option_desc AS [PAGE_VERIFY],is_auto_close_on AS [AUTO_CLOSE],is_auto_shrink_on AS [AUTO_SHRINK] FROM sys.databases WHERE database_id>4 ORDER BY name`},
	{26, "Размер, предел и шаг роста файлов ERP", `SELECT name AS [Файл],type_desc AS [Тип],CONVERT(decimal(18,1),size/128.0) AS [Размер МБ],
 CASE WHEN is_percent_growth=1 THEN CONCAT(growth,'%') ELSE CONCAT(CONVERT(decimal(18,2),growth/128.0),' MB') END AS [Прирост],
 CASE max_size WHEN -1 THEN N'до предела диска' WHEN 0 THEN N'отключён' ELSE CONCAT(CONVERT(decimal(18,1),max_size/128.0),' MB') END AS [Предел] FROM sys.database_files ORDER BY file_id`},
	{28, "Лимиты памяти SQL и доступная память ОС", `SELECT (SELECT CONVERT(bigint,value_in_use) FROM sys.configurations WHERE name='max server memory (MB)') AS [Макс SQL МБ],total_physical_memory_kb/1024 AS [ОС всего МБ],available_physical_memory_kb/1024 AS [ОС доступно МБ],system_memory_state_desc AS [Состояние] FROM sys.dm_os_sys_memory`},
	{29, "Модель памяти процесса SQL", `SELECT sql_memory_model_desc AS [Модель памяти] FROM sys.dm_os_sys_info`},
	{30, "Быстрая инициализация файлов SQL", `SELECT servicename AS [Служба],instant_file_initialization_enabled AS [IFI] FROM sys.dm_server_services WHERE process_id=CONVERT(int,SERVERPROPERTY('ProcessID'))`},
	{34, "Одноразовые ad hoc планы и объём кэша экземпляра", `SELECT COUNT_BIG(*) AS [Объектов кэша],SUM(CASE WHEN objtype='Adhoc' AND usecounts=1 THEN CONVERT(bigint,1) ELSE 0 END) AS [Одноразовых ad hoc],
 CAST(SUM(CASE WHEN objtype='Adhoc' AND usecounts=1 THEN CONVERT(bigint,size_in_bytes) ELSE 0 END)/1048576.0 AS decimal(18,1)) AS [Одноразовые МБ], CAST(SUM(CONVERT(bigint,size_in_bytes))/1048576.0 AS decimal(18,1)) AS [Всего МБ] FROM sys.dm_exec_cached_plans`},
	{36, "Глобальные флаги трассировки", `DBCC TRACESTATUS(-1) WITH NO_INFOMSGS`},
}

func fetchSection(ctx context.Context, conn *sql.Conn, c check, args ...any) (s Section) {
	s = Section{ID: c.id, Title: c.title}
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
	s = Section{ID: 10, Title: "I/O файлов за интервал", Columns: []string{"Файл", "Чтений", "Записей", "Чтение мс/IO", "Запись мс/IO", "Интервал с"}}
	// Sample the same connection, using file_id to pair counters.
	q := check{10, s.Title, `SELECT f.file_id,f.name,v.num_of_reads,v.num_of_writes,v.io_stall_read_ms,v.io_stall_write_ms FROM sys.dm_io_virtual_file_stats(DB_ID(),NULL) v JOIN sys.database_files f ON f.file_id=v.file_id ORDER BY f.file_id`}
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
	prev := map[string][]string{}
	for _, r := range before.Rows {
		prev[r[0]] = r
	}
	for _, r := range after.Rows {
		b := prev[r[0]]
		if b == nil || r[1] != b[1] {
			continue
		}
		var d [4]int64
		ok := true
		for i := range d {
			av, ea := strconv.ParseInt(r[i+2], 10, 64)
			bv, eb := strconv.ParseInt(b[i+2], 10, 64)
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
		s.Rows = append(s.Rows, []string{r[1], fmt.Sprint(d[0]), fmt.Sprint(d[1]), avg(d[2], d[0]), avg(d[3], d[1]), fmt.Sprintf("%.1f", float64(s.ElapsedMS)/1000)})
	}
	return
}
