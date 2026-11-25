package lab

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// The server projects only typed event metadata. XML graphs, SQL text,
// connection strings and input buffers never enter the application.
const serverStateVisible = `
IF (CASE WHEN CONVERT(int,SERVERPROPERTY('ProductMajorVersion'))>=16
  THEN COALESCE(HAS_PERMS_BY_NAME(NULL,NULL,'VIEW SERVER PERFORMANCE STATE'),0)
  ELSE COALESCE(HAS_PERMS_BY_NAME(NULL,NULL,'VIEW SERVER STATE'),0) END)=0
  THROW 50001,N'Недостаточно прав просмотра состояния экземпляра',1;
`

var blockingCheck = check{22, "Блокировки базы · снимок SQL и история Extended Events", serverStateVisible + `
SELECT TOP (50) CONVERT(varchar(30),SYSUTCDATETIME(),126),r.database_id,r.session_id,r.blocking_session_id,
 CONCAT(N'Ожидание ',r.wait_type,N', мс ',r.wait_time,N'; открытых транзакций ',r.open_transaction_count)
FROM sys.dm_exec_requests AS r WHERE r.database_id=DB_ID() AND r.session_id<>@@SPID
 AND (r.blocking_session_id<>0 OR r.wait_type LIKE 'LCK[_]M[_]%')
ORDER BY r.wait_time DESC,r.session_id`}

const xeSettings = serverStateVisible + `
SELECT CONVERT(varchar(30),SYSUTCDATETIME(),126),DB_ID(),NULL,NULL,
 CONCAT(N'Порог blocked process threshold, с: ',(SELECT CONVERT(int,value_in_use) FROM sys.configurations WHERE name='blocked process threshold (s)'),
 N'. Сеансов XE с видимой настройкой blocked_process_report: ',COUNT_BIG(*),
 N', запущено: ',COUNT(a.name),N', запущено с ring_buffer: ',COUNT(rt.target_name))
FROM sys.server_event_sessions s JOIN sys.server_event_session_events e ON e.event_session_id=s.event_session_id
 LEFT JOIN sys.dm_xe_sessions a ON a.name=s.name LEFT JOIN sys.dm_xe_session_targets rt ON rt.event_session_address=a.address AND rt.target_name='ring_buffer'
WHERE e.name='blocked_process_report'`

const deadlockMetadata = serverStateVisible + `
DECLARE @history xml=(SELECT TRY_CONVERT(xml,t.target_data) FROM sys.dm_xe_sessions s
 JOIN sys.dm_xe_session_targets t ON t.event_session_address=s.address
 WHERE s.name=N'system_health' AND t.target_name=N'ring_buffer');
IF @history IS NULL
 SELECT CONVERT(varchar(30),SYSUTCDATETIME(),126),DB_ID(),NULL,NULL,N'Кольцевой буфер system_health отсутствует или недоступен. Проверь цель event_file в SSMS.'
ELSE ` + deadlockProjection

const deadlockProjection = `SELECT TOP (50)
 e.x.value('(@timestamp)[1]','varchar(40)'),p.x.value('(@currentdb)[1]','int'),
 p.x.value('(@spid)[1]','int'),NULL,
 CONCAT(N'Участник; поток ',p.x.value('(@ecid)[1]','int'),N', режим ',p.x.value('(@lockMode)[1]','nvarchar(32)'),
 N', ожидание мс ',p.x.value('(@waittime)[1]','bigint'))
FROM @history.nodes('/RingBufferTarget/event[@name="xml_deadlock_report"]') e(x)
 CROSS APPLY e.x.nodes('data/value/deadlock/process-list/process') p(x)
WHERE p.x.value('(@currentdb)[1]','int')=DB_ID() ORDER BY 1 DESC,3`

const blockedReportMetadata = serverStateVisible + `
WITH targets AS (
 SELECT TOP (4) TRY_CONVERT(xml,t.target_data) AS doc FROM sys.dm_xe_sessions AS a
 JOIN sys.dm_xe_session_targets AS t ON t.event_session_address=a.address
 JOIN sys.server_event_sessions AS s ON s.name=a.name
 WHERE t.target_name=N'ring_buffer' AND EXISTS
 (SELECT 1 FROM sys.server_event_session_events e WHERE e.event_session_id=s.event_session_id AND e.name='blocked_process_report')
 ORDER BY a.name
)
` + blockedProjection

const blockedProjection = `SELECT TOP (50) e.x.value('(@timestamp)[1]','varchar(40)'),r.x.value('(blocked-process/process/@currentdb)[1]','int'),
 r.x.value('(blocked-process/process/@spid)[1]','int'), r.x.value('(blocking-process/process/@spid)[1]','int'),
 CONCAT(N'Ожидание мс ',r.x.value('(blocked-process/process/@waittime)[1]','bigint'))
FROM targets CROSS APPLY doc.nodes('/RingBufferTarget/event[@name="blocked_process_report"]') e(x)
 CROSS APPLY e.x.nodes('data/value/blocked-process-report') r(x)
WHERE r.x.value('(blocked-process/process/@currentdb)[1]','int')=DB_ID() ORDER BY 1 DESC,3`

func (l *Live) BlockingEvidence(ctx context.Context) (sec Section) {
	start := time.Now()
	defer func() { sec.ElapsedMS = time.Since(start).Milliseconds() }()
	bounded, stop := context.WithTimeout(ctx, 30*time.Second)
	defer stop()
	sec = Section{ID: 22, Title: blockingCheck.title, Columns: []string{"Источник", "UTC", "ID базы SQL", "SPID", "Блокирующий SPID", "Сеанс 1С", "Сведения"}}
	if l.conn == nil {
		sec.Error = "Нет подключения к выбранной базе"
		return
	}
	queries := []struct{ name, query, empty string }{
		{"Текущая блокировка", blockingCheck.query, "В момент снимка блокируемых запросов выбранной базы не видно."},
		{"Настройки XE", xeSettings, "Нет видимых настроек."},
		{"Взаимоблокировка · system_health", deadlockMetadata, "В доступной части кольцевого буфера участников с текущим ID базы не найдено."},
		{"Длительная блокировка · XE", blockedReportMetadata, "В доступных кольцевых буферах с blocked_process_report нет события для текущего ID базы."},
	}
	var failures []string
	for _, q := range queries {
		part := fetchSection(bounded, l.conn, check{22, q.name, q.query})
		if part.Error != "" {
			failures = append(failures, q.name)
			sec.Rows = append(sec.Rows, blockingNote(q.name, "Источник недоступен. Проверь права на состояние экземпляра и наличие запущенного сеанса с ring_buffer."))
			continue
		}
		if len(part.Rows) == 0 {
			sec.Rows = append(sec.Rows, blockingNote(q.name, q.empty))
		}
		for _, r := range part.Rows {
			sec.Rows = append(sec.Rows, []string{q.name, r[0], r[1], r[2], r[3], "—", r[4]})
		}
	}
	if l.P.MonitorERP {
		l.addRASBlocking(bounded, &sec)
	}
	sec.Rows = append(sec.Rows, blockingNote("Сопоставление", "Для исторического события сопоставь UTC (с учётом часового пояса ТЖ) и SPID с dbpid и SessionID в технологическом журнале 1С. Проверь операцию и интервал жизни соединения. Для полной истории проверь файлы XEL действующих сеансов в SSMS. Буферы перезаписываются. Прочитаны до 50 строк на источник и до 4 сеансов blocked_process_report."))
	if len(failures) > 0 {
		sec.Error = "Не удалось прочитать: " + strings.Join(failures, ", ")
	}
	return
}

func blockingNote(source, details string) []string {
	return []string{source, time.Now().UTC().Format(time.RFC3339Nano), "—", "—", "—", "—", details}
}

func (l *Live) addRASBlocking(ctx context.Context, sec *Section) {
	reg, err := l.verifiedRegistration(ctx)
	if err != nil {
		sec.Rows = append(sec.Rows, blockingNote("Снимок RAS", err.Error()))
		return
	}
	cluster, ib := reg.cluster, reg.infobase
	before := fetchSection(ctx, l.conn, blockingCheck)
	if before.Error != "" {
		sec.Rows = append(sec.Rows, blockingNote("Снимок RAS", "Не удалось получить текущие блокировки SQL для сопоставления."))
		return
	}
	records, err := readRAC(ctx, cluster, "session", "list", "--cluster="+cluster.ClusterID, "--infobase="+ib)
	if err != nil {
		sec.Rows = append(sec.Rows, blockingNote("Снимок RAS", err.Error()))
		return
	}
	// Bracket the RAS read with fresh SQL snapshots. A captured SQL session
	// can be returned to the pool, so the result is a candidate even when
	// both snapshots show the same blocker/waiter pair.
	captured := time.Now().UTC().Format(time.RFC3339Nano)
	after := fetchSection(ctx, l.conn, blockingCheck)
	if after.Error != "" {
		sec.Rows = append(sec.Rows, blockingNote("Снимок RAS", "Не удалось повторно проверить текущие блокировки SQL."))
		return
	}
	liveSPIDs := stableBlockingSPIDs(before, after)
	matches := 0
	for _, r := range records {
		spid, e := strconv.Atoi(r["db-proc-info"])
		session, se := strconv.Atoi(r["session-id"])
		if !strings.EqualFold(r["infobase"], ib) || e != nil || se != nil || spid <= 0 || session <= 0 {
			continue
		}
		db, ok := liveSPIDs[strconv.Itoa(spid)]
		if !ok {
			continue
		}
		sec.Rows = append(sec.Rows, []string{"Снимок RAS", captured, db, strconv.Itoa(spid), "—", strconv.Itoa(session), "Одинаковая пара SPID до и после снимка RAS. Подтверди операцию в ТЖ."})
		matches++
		if matches >= 50 {
			break
		}
	}
	if matches == 0 {
		sec.Rows = append(sec.Rows, blockingNote("Снимок RAS", fmt.Sprintf("Получено сеансов зарегистрированной базы: %d. Текущих совпадений по SPID не обнаружено.", len(records))))
	}
}

func stableBlockingSPIDs(before, after Section) map[string]string {
	type edge struct{ db, waiter, blocker string }
	first := map[edge]bool{}
	for _, r := range before.Rows {
		first[edge{r[1], r[2], r[3]}] = true
	}
	result := map[string]string{}
	for _, r := range after.Rows {
		if first[edge{r[1], r[2], r[3]}] {
			result[r[2]] = r[1]
			result[r[3]] = r[1]
		}
	}
	return result
}
